package apiserver

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Server struct {
	client client.Client
	log    logr.Logger
	router *chi.Mux
}

func New(c client.Client, log logr.Logger) *Server {
	s := &Server{client: c, log: log}
	s.router = s.buildRouter()
	return s
}

func (s *Server) Run(addr string) error {
	return http.ListenAndServe(addr, s.router)
}

func (s *Server) buildRouter() *chi.Mux {
	r := chi.NewRouter()
	r.Use(middleware.RequestLogger(&middleware.DefaultLogFormatter{Logger: stdLogger{s.log}, NoColor: true}))
	r.Use(middleware.Recoverer)
	r.Use(corsMiddleware)

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/appdefinitions", s.listAppDefinitions)
		r.Route("/namespaces/{namespace}/appdefinitions", func(r chi.Router) {
			r.Get("/", s.listAppDefinitionsInNamespace)
			r.Post("/", s.createAppDefinition)
			r.Get("/{name}", s.getAppDefinition)
			r.Put("/{name}", s.updateAppDefinition)
			r.Delete("/{name}", s.deleteAppDefinition)
		})
	})

	return r
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// stdLogger bridges chi's middleware.LogFormatter to logr.Logger.
type stdLogger struct{ log logr.Logger }

func (l stdLogger) Print(v ...interface{}) { l.log.Info("", "msg", v) }
