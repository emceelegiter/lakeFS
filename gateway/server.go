package gateway

import (
	"fmt"
	"net/http"
	"net/http/pprof"
	"strings"
	"treeverse-lake/auth"
	"treeverse-lake/auth/sig"
	"treeverse-lake/block"
	"treeverse-lake/db"
	"treeverse-lake/gateway/errors"
	ghttp "treeverse-lake/gateway/http"
	"treeverse-lake/gateway/operations"
	"treeverse-lake/gateway/path"
	"treeverse-lake/index"

	"golang.org/x/xerrors"

	"github.com/gorilla/mux"
	log "github.com/sirupsen/logrus"
)

func getRepo(req *http.Request) string {
	vars := mux.Vars(req)
	return vars["repo"]
}

func getKey(req *http.Request) string {
	vars := mux.Vars(req)
	return vars["path"]
}

func getBranch(req *http.Request) string {
	vars := mux.Vars(req)
	return vars["refspec"]
}

type ServerContext struct {
	region           string
	bareDomain       string
	meta             index.Index
	multipartManager index.MultipartManager
	blockStore       block.Adapter
	authService      auth.Service
}

type Server struct {
	ctx        *ServerContext
	server     *http.Server
	bareDomain string
}

func NewServer(
	region string,
	meta index.Index,
	blockStore block.Adapter,
	authService auth.Service,
	multipartManager index.MultipartManager,
	listenAddr, bareDomain string,
) *Server {

	ctx := &ServerContext{
		meta:             meta,
		region:           region,
		bareDomain:       bareDomain,
		blockStore:       blockStore,
		authService:      authService,
		multipartManager: multipartManager,
	}

	// setup routes
	router := mux.NewRouter()
	attachDebug(router)
	attachRoutes(bareDomain, router, ctx)
	// also attach routes to a host string minus the port, if the host contains them
	if strings.Contains(bareDomain, ":") {
		bareDomainWithoutPort := strings.Split(bareDomain, ":")[0]
		attachRoutes(bareDomainWithoutPort, router, ctx)
	}

	router.Use(ghttp.LoggingMiddleWare)

	// assemble server
	return &Server{
		ctx:        ctx,
		bareDomain: bareDomain,
		server: &http.Server{
			Handler: router,
			Addr:    listenAddr,
		},
	}
}

//type h struct {
//	router *mux.Router
//}
//
//func (x h) ServeHTTP(w http.ResponseWriter, r *http.Request) {
//	parts := strings.Split(r.Host, ":")
//	r.Host = parts[0]
//	x.router.ServeHTTP(w, r)
//}

func (s *Server) Listen() error {
	return s.server.ListenAndServe()
}

func attachDebug(router *mux.Router) {
	// TODO: configurable host and prefix
	r := router.Host("localhost:8000").Subrouter()
	r.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	r.HandleFunc("/debug/pprof/profile", pprof.Profile)
	r.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	r.HandleFunc("/debug/pprof/trace", pprof.Trace)
	r.HandleFunc("/debug/pprof/", pprof.Index)
	r.Handle("/debug/pprof/block", pprof.Handler("block"))
	r.Handle("/debug/pprof/goroutine", pprof.Handler("goroutine"))
	r.Handle("/debug/pprof/heap", pprof.Handler("heap"))
	r.Handle("/debug/pprof/threadcreate", pprof.Handler("threadcreate"))
}

func attachRoutes(bareDomain string, router *mux.Router, ctx *ServerContext) {
	// path based routing
	// non-bucket-specific endpoints
	serviceEndpoint := router.Host(bareDomain).Subrouter()
	// repo-specific actions that relate to a key
	pathBasedRepo := serviceEndpoint.PathPrefix(fmt.Sprintf("/%s", path.RepoMatch)).Subrouter()
	pathBasedRepoWithKey := pathBasedRepo.PathPrefix(fmt.Sprintf("/%s/%s", path.RefspecMatch, path.PathMatch)).Subrouter()
	pathBasedRepoWithKey.Methods(http.MethodDelete).HandlerFunc(PathOperationHandler(ctx, &operations.DeleteObject{}))
	pathBasedRepoWithKey.Methods(http.MethodPost).HandlerFunc(PathOperationHandler(ctx, &operations.PostObject{}))
	pathBasedRepoWithKey.Methods(http.MethodGet).HandlerFunc(PathOperationHandler(ctx, &operations.GetObject{}))
	pathBasedRepoWithKey.Methods(http.MethodHead).HandlerFunc(PathOperationHandler(ctx, &operations.HeadObject{}))
	pathBasedRepoWithKey.Methods(http.MethodPut).HandlerFunc(PathOperationHandler(ctx, &operations.PutObject{}))
	// bucket-specific actions that don't relate to a specific key
	pathBasedRepo.Methods(http.MethodPut).HandlerFunc(RepoOperationHandler(ctx, &operations.CreateBucket{}))
	pathBasedRepo.
		Methods(http.MethodGet).
		//Queries("prefix", "{prefix}", "Prefix", "{prefix}", "Delimiter", "{delimiter}", "delimiter", "{delimiter}").
		HandlerFunc(RepoOperationHandler(ctx, &operations.ListObjects{}))
	pathBasedRepo.Methods(http.MethodDelete).HandlerFunc(RepoOperationHandler(ctx, &operations.DeleteBucket{}))
	pathBasedRepo.Methods(http.MethodHead).HandlerFunc(RepoOperationHandler(ctx, &operations.HeadBucket{}))
	pathBasedRepo.Methods(http.MethodPost).HandlerFunc(RepoOperationHandler(ctx, &operations.DeleteObjects{}))
	// global level
	serviceEndpoint.PathPrefix("/").Methods(http.MethodGet).HandlerFunc(OperationHandler(ctx, &operations.ListBuckets{}))

	// sub-domain based routing
	subDomainBasedRepo := router.Host(strings.Join([]string{path.RepoMatch, bareDomain}, ".")).Subrouter()
	// repo-specific actions that relate to a key
	subDomainBasedRepoWithKey := subDomainBasedRepo.PathPrefix(fmt.Sprintf("/%s/%s", path.RefspecMatch, path.PathMatch)).Subrouter()
	subDomainBasedRepoWithKey.Methods(http.MethodDelete).HandlerFunc(PathOperationHandler(ctx, &operations.DeleteObject{}))
	subDomainBasedRepoWithKey.Methods(http.MethodPost).HandlerFunc(PathOperationHandler(ctx, &operations.PostObject{}))
	subDomainBasedRepoWithKey.Methods(http.MethodGet).HandlerFunc(PathOperationHandler(ctx, &operations.GetObject{}))
	subDomainBasedRepoWithKey.Methods(http.MethodHead).HandlerFunc(PathOperationHandler(ctx, &operations.HeadObject{}))
	subDomainBasedRepoWithKey.Methods(http.MethodPut).HandlerFunc(PathOperationHandler(ctx, &operations.PutObject{}))
	// bucket-specific actions that don't relate to a specific key
	subDomainBasedRepo.
		Methods(http.MethodGet).
		//Queries("prefix", "{prefix}", "Prefix", "{prefix}", "Delimiter", "{delimiter}", "delimiter", "{delimiter}").
		HandlerFunc(RepoOperationHandler(ctx, &operations.ListObjects{}))
	subDomainBasedRepo.Path("/").Methods(http.MethodDelete).HandlerFunc(RepoOperationHandler(ctx, &operations.DeleteBucket{}))
	subDomainBasedRepo.Path("/").Methods(http.MethodHead).HandlerFunc(RepoOperationHandler(ctx, &operations.HeadBucket{}))
	subDomainBasedRepo.Path("/").Methods(http.MethodPost).HandlerFunc(RepoOperationHandler(ctx, &operations.DeleteObjects{}))
	subDomainBasedRepo.Path("/").Methods(http.MethodPut).HandlerFunc(RepoOperationHandler(ctx, &operations.CreateBucket{}))
}

func OperationHandler(ctx *ServerContext, handler operations.AuthenticatedOperationHandler) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		// structure operation
		authOp := authenticateOperation(ctx, writer, request, handler.GetPermission(), handler.GetArn())
		if authOp == nil {
			return
		}
		// run callback
		handler.Handle(authOp)
	}
}

func RepoOperationHandler(ctx *ServerContext, handler operations.RepoOperationHandler) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		// structure operation
		authOp := authenticateOperation(ctx, writer, request, handler.GetPermission(), handler.GetArn())
		if authOp == nil {
			return
		}
		// run callback
		handler.Handle(&operations.RepoOperation{
			AuthenticatedOperation: authOp,
			Repo:                   getRepo(request),
		})
	}
}

func PathOperationHandler(ctx *ServerContext, handler operations.PathOperationHandler) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		// structure operation
		authOp := authenticateOperation(ctx, writer, request, handler.GetPermission(), handler.GetArn())
		if authOp == nil {
			return
		}
		// run callback
		handler.Handle(&operations.PathOperation{
			BranchOperation: &operations.BranchOperation{
				RepoOperation: &operations.RepoOperation{
					AuthenticatedOperation: authOp,
					Repo:                   getRepo(request),
				},
				Branch: getBranch(request),
			},
			Path: getKey(request),
		})
	}
}

func authenticateOperation(s *ServerContext, writer http.ResponseWriter, request *http.Request, permission, arn string) *operations.AuthenticatedOperation {
	o := &operations.Operation{
		Request:        request,
		ResponseWriter: writer,
		Region:         s.region,
		FQDN:           s.bareDomain,

		Index:            s.meta,
		MultipartManager: s.multipartManager,
		BlockStore:       s.blockStore,
		Auth:             s.authService,
	}
	// authenticate
	authenticator := sig.NewV4Authenticatior(request)
	authContext, err := authenticator.Parse()
	if err != nil {
		// fallback to authv2
		authenticator = sig.NewV2SigAuthenticator(request)
		authContext, err = authenticator.Parse()

		if err != nil {
			o.EncodeError(errors.Codes.ToAPIErr(errors.ErrAccessDenied))
			o.Log().WithError(err).Warn("could not parse v4 or v2 auth context")
			return nil
		}
	}

	creds, err := s.authService.GetAPICredentials(authContext.GetAccessKeyId())
	if err != nil {
		if !xerrors.Is(err, db.ErrNotFound) {
			o.Log().WithError(err).WithField("key", authContext.GetAccessKeyId()).Warn("error getting access key")
			o.EncodeError(errors.Codes.ToAPIErr(errors.ErrInternalError))
		} else {
			o.Log().WithError(err).WithField("key", authContext.GetAccessKeyId()).Warn("could not find access key")
			o.EncodeError(errors.Codes.ToAPIErr(errors.ErrAccessDenied))
		}
		return nil
	}

	err = authenticator.Verify(creds)
	if err != nil {
		o.Log().WithError(err).WithFields(log.Fields{
			"key":           authContext.GetAccessKeyId(),
			"authenticator": authenticator,
		}).Warn("error verifying credentials for key")
		o.EncodeError(errors.Codes.ToAPIErr(errors.ErrAccessDenied))
		return nil
	}

	// we are verified!
	op := &operations.AuthenticatedOperation{
		Operation:   o,
		SubjectId:   creds.GetEntityId(),
		SubjectType: creds.GetCredentialType(),
	}

	// interpolate arn string
	arn = auth.ResolveARN(arn, mux.Vars(request))

	// authorize
	authResp, err := s.authService.Authorize(&auth.AuthorizationRequest{
		UserID:     op.SubjectId,
		Permission: permission,
		SubjectARN: arn,
	})
	if err != nil {
		o.Log().WithError(err).Error("failed to authorize")
		o.EncodeError(errors.Codes.ToAPIErr(errors.ErrInternalError))
		return nil
	}

	if authResp.Error != nil || !authResp.Allowed {
		o.Log().WithError(authResp.Error).WithField("key", authContext.GetAccessKeyId()).Warn("no permission")
		o.EncodeError(errors.Codes.ToAPIErr(errors.ErrAccessDenied))
		return nil
	}

	// authentication and authorization complete!
	return op
}
