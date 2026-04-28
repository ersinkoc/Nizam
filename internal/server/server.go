package server

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/mizanproxy/mizan/internal/api"
	"github.com/mizanproxy/mizan/internal/observe"
	"github.com/mizanproxy/mizan/internal/store"
)

type Config struct {
	Bind         string
	Auth         AuthConfig
	MaxBodyBytes int64
}

const DefaultMaxBodyBytes int64 = 10 << 20

type AuthConfig struct {
	Token         string
	ReadOnlyToken string
	BasicUser     string
	BasicPassword string
}

func (cfg AuthConfig) Enabled() bool {
	return cfg.Token != "" || cfg.ReadOnlyToken != "" || (cfg.BasicUser != "" && cfg.BasicPassword != "")
}

func (cfg AuthConfig) Validate() error {
	if cfg.Token != "" && cfg.ReadOnlyToken != "" && constantTimeEqual(cfg.Token, cfg.ReadOnlyToken) {
		return fmt.Errorf("auth token and read-only token must be different")
	}
	return nil
}

func ParseBasicCredential(value string) (string, string, error) {
	user, password, ok := strings.Cut(value, ":")
	if !ok || user == "" || password == "" {
		return "", "", fmt.Errorf("basic auth credential must use user:password")
	}
	return user, password, nil
}

func RequiresAuth(bind string) bool {
	host, _, err := net.SplitHostPort(bind)
	if err != nil {
		host = bind
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return true
	}
	if strings.EqualFold(host, "localhost") {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return true
	}
	return !ip.IsLoopback()
}

func New(cfg Config, st *store.Store, log *slog.Logger) *http.Server {
	if cfg.Bind == "" {
		cfg.Bind = "127.0.0.1:7890"
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = DefaultMaxBodyBytes
	}
	mux := http.NewServeMux()
	api.Register(mux, st)
	mux.Handle("/", embeddedUI())

	handler := http.Handler(mux)
	if cfg.Auth.Enabled() {
		handler = authenticator(cfg.Auth, handler)
	}
	handler = limitRequestBody(cfg.MaxBodyBytes, handler)
	handler = securityHeaders(handler)
	return &http.Server{
		Addr:              cfg.Bind,
		Handler:           recoverer(logger(log, handler)),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		h.Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		if isDynamicPath(r.URL.Path) {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func isDynamicPath(path string) bool {
	return strings.HasPrefix(path, "/api/") ||
		path == "/healthz" ||
		path == "/readyz" ||
		path == "/metrics" ||
		path == "/version"
}

func limitRequestBody(maxBodyBytes int64, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.ContentLength != 0 {
			if r.ContentLength > maxBodyBytes {
				http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

func authenticator(cfg AuthConfig, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		role, ok := cfg.authorize(r)
		if ok {
			if role == authRoleViewer && !safeMethod(r.Method) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), authRoleContextKey{}, role)))
			return
		}
		if cfg.BasicUser != "" {
			w.Header().Set("WWW-Authenticate", `Basic realm="Mizan"`)
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

func authPublicPath(path string) bool {
	return path == "/healthz" || path == "/readyz"
}

func (cfg AuthConfig) authorized(r *http.Request) bool {
	_, ok := cfg.authorize(r)
	return ok
}

func (cfg AuthConfig) authorize(r *http.Request) (authRole, bool) {
	if cfg.Token != "" {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if ok && constantTimeEqual(token, cfg.Token) {
			return authRoleAdmin, true
		}
	}
	if cfg.ReadOnlyToken != "" {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if ok && constantTimeEqual(token, cfg.ReadOnlyToken) {
			return authRoleViewer, true
		}
	}
	if cfg.BasicUser != "" && cfg.BasicPassword != "" {
		user, password, ok := r.BasicAuth()
		if ok && constantTimeEqual(user, cfg.BasicUser) && constantTimeEqual(password, cfg.BasicPassword) {
			return authRoleAdmin, true
		}
	}
	return "", false
}

type authRole string

const (
	authRoleAdmin  authRole = "admin"
	authRoleViewer authRole = "viewer"
)

type authRoleContextKey struct{}

func safeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(data)
}

func (w *statusRecorder) Flush() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *statusRecorder) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *statusRecorder) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func logger(log *slog.Logger, next http.Handler) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		status := rec.statusCode()
		observe.RecordHTTPRequest(r.Method, routePattern(r), status)
		log.Info("http_request", "method", r.Method, "path", r.URL.Path, "status", status, "duration_ms", time.Since(start).Milliseconds())
	})
}

func routePattern(r *http.Request) string {
	if r.Pattern != "" {
		return r.Pattern
	}
	return "unmatched"
}

func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
