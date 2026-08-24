package apiserver

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/time/rate"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Server struct {
	client         client.Client
	log            logr.Logger
	router         *chi.Mux
	accessReviewer AccessReviewer
	allowedOrigins map[string]struct{}
	apiLimiter     *rate.Limiter
}

type Option func(*Server)

func WithAccessReviewer(reviewer AccessReviewer) Option {
	return func(s *Server) { s.accessReviewer = reviewer }
}

func WithAllowedOrigins(origins ...string) Option {
	return func(s *Server) {
		for _, origin := range origins {
			if origin != "" {
				s.allowedOrigins[origin] = struct{}{}
			}
		}
	}
}

func New(c client.Client, log logr.Logger, options ...Option) *Server {
	s := &Server{
		client: c, log: log, allowedOrigins: make(map[string]struct{}),
		apiLimiter: rate.NewLimiter(rate.Limit(50), 100),
	}
	for _, option := range options {
		option(s)
	}
	s.router = s.buildRouter()
	return s
}

func (s *Server) Handler() http.Handler {
	return s.router
}

func (s *Server) Run(ctx context.Context, addr string) error {
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           s.router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			s.log.Error(err, "failed to gracefully shut down API server")
		}
	}()

	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) buildRouter() *chi.Mux {
	r := chi.NewRouter()
	r.Use(middleware.RequestLogger(&middleware.DefaultLogFormatter{Logger: stdLogger{s.log}, NoColor: true}))
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))
	r.Use(metricsMiddleware)
	r.Use(s.corsMiddleware)

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	r.Get("/readyz", s.readiness)
	r.Handle("/metrics", promhttp.Handler())

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(s.rateLimitAPI)
		r.Use(s.authenticate)
		r.With(s.requireAccess("list", false, false)).Get("/appdefinitions", s.listAppDefinitions)
		r.Route("/namespaces/{namespace}/appdefinitions", func(r chi.Router) {
			r.With(s.requireAccess("list", true, false)).Get("/", s.listAppDefinitionsInNamespace)
			r.With(s.requireAccess("create", true, false)).Post("/", s.createAppDefinition)
			r.With(s.requireAccess("get", true, true)).Get("/{name}", s.getAppDefinition)
			r.With(s.requireAccess("update", true, true)).Put("/{name}", s.updateAppDefinition)
			r.With(s.requireAccess("delete", true, true)).Delete("/{name}", s.deleteAppDefinition)
		})
	})

	return r
}

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if _, allowed := s.allowedOrigins[origin]; origin != "" && allowed {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			if origin != "" {
				if _, allowed := s.allowedOrigins[origin]; !allowed {
					http.Error(w, "origin is not allowed", http.StatusForbidden)
					return
				}
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) rateLimitAPI(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.apiLimiter.Allow() {
			w.Header().Set("Retry-After", "1")
			s.writeError(w, http.StatusTooManyRequests, errors.New("API rate limit exceeded"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// stdLogger bridges chi's middleware.LogFormatter to logr.Logger.
type stdLogger struct{ log logr.Logger }

func (l stdLogger) Print(v ...interface{}) { l.log.Info("", "msg", v) }
