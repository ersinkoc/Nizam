package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mizanproxy/mizan/internal/store"
)

func TestServerRoutesAndSPA(t *testing.T) {
	st := store.New(t.TempDir())
	srv := New(Config{}, st, slog.Default())
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	res := httptest.NewRecorder()
	srv.Handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("health status=%d", res.Code)
	}
	if res.Header().Get("X-Content-Type-Options") != "nosniff" || res.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("missing security headers: %+v", res.Header())
	}
	if res.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("dynamic cache-control=%q", res.Header().Get("Cache-Control"))
	}
	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	res = httptest.NewRecorder()
	srv.Handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("metrics status=%d", res.Code)
	}
	if body := res.Body.String(); !strings.Contains(body, `mizan_http_requests_total{method="GET",route="GET /healthz",status="200"} 1`) {
		t.Fatalf("metrics missing request count:\n%s", body)
	}
	req = httptest.NewRequest(http.MethodGet, "/some/spa/path", nil)
	res = httptest.NewRecorder()
	srv.Handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("spa status=%d", res.Code)
	}
	if res.Header().Get("Cache-Control") == "no-store" {
		t.Fatal("static UI should not be marked no-store")
	}
	req = httptest.NewRequest(http.MethodGet, "/api/nope", nil)
	res = httptest.NewRecorder()
	srv.Handler.ServeHTTP(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("api fallback status=%d", res.Code)
	}
}

func TestServerHardeningDefaultsAndBodyLimit(t *testing.T) {
	st := store.New(t.TempDir())
	srv := New(Config{MaxBodyBytes: 8}, st, slog.Default())
	if srv.ReadHeaderTimeout != 5*time.Second || srv.ReadTimeout != 30*time.Second || srv.IdleTimeout != 120*time.Second || srv.MaxHeaderBytes != 1<<20 {
		t.Fatalf("unexpected timeout/header defaults: %+v", srv)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader(`{"name":"too-large"}`))
	res := httptest.NewRecorder()
	srv.Handler.ServeHTTP(res, req)
	if res.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large body status=%d body=%s", res.Code, res.Body.String())
	}
	if res.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("large body missing security headers: %+v", res.Header())
	}

	srv = New(Config{MaxBodyBytes: 64}, st, slog.Default())
	req = httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader(`{"name":"ok"}`))
	res = httptest.NewRecorder()
	srv.Handler.ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("small body status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestServerAuth(t *testing.T) {
	st := store.New(t.TempDir())
	srv := New(Config{Auth: AuthConfig{Token: "secret", ReadOnlyToken: "read-only", BasicUser: "operator", BasicPassword: "pass"}}, st, slog.Default())

	for _, path := range []string{"/healthz", "/readyz"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		res := httptest.NewRecorder()
		srv.Handler.ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("public path %s status=%d", path, res.Code)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	res := httptest.NewRecorder()
	srv.Handler.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized || res.Header().Get("WWW-Authenticate") != `Basic realm="Mizan"` {
		t.Fatalf("unauthorized status=%d authenticate=%q", res.Code, res.Header().Get("WWW-Authenticate"))
	}

	req = httptest.NewRequest(http.MethodGet, "/version", nil)
	req.Header.Set("Authorization", "Bearer secret")
	res = httptest.NewRecorder()
	srv.Handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("bearer status=%d", res.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/version", nil)
	req.Header.Set("Authorization", "Bearer read-only")
	res = httptest.NewRecorder()
	srv.Handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("read-only bearer get status=%d", res.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader(`{"name":"blocked"}`))
	req.Header.Set("Authorization", "Bearer read-only")
	res = httptest.NewRecorder()
	srv.Handler.ServeHTTP(res, req)
	if res.Code != http.StatusForbidden {
		t.Fatalf("read-only bearer post status=%d body=%s", res.Code, res.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader(`{"name":"allowed"}`))
	req.Header.Set("Authorization", "Bearer secret")
	res = httptest.NewRecorder()
	srv.Handler.ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("admin bearer post status=%d body=%s", res.Code, res.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/version", nil)
	req.SetBasicAuth("operator", "pass")
	res = httptest.NewRecorder()
	srv.Handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("basic status=%d", res.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/version", nil)
	req.SetBasicAuth("operator", "wrong")
	res = httptest.NewRecorder()
	srv.Handler.ServeHTTP(res, req)
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("bad basic status=%d", res.Code)
	}

	if (AuthConfig{}).authorized(httptest.NewRequest(http.MethodGet, "/version", nil)) {
		t.Fatal("empty auth config should not authorize")
	}
	if err := (AuthConfig{Token: "same", ReadOnlyToken: "same"}).Validate(); err == nil {
		t.Fatal("expected duplicate token validation error")
	}
}

func TestAuthHelpers(t *testing.T) {
	user, password, err := ParseBasicCredential("operator:pass:with:colon")
	if err != nil || user != "operator" || password != "pass:with:colon" {
		t.Fatalf("basic user=%q password=%q err=%v", user, password, err)
	}
	for _, bad := range []string{"", "operator", ":pass", "operator:"} {
		if _, _, err := ParseBasicCredential(bad); err == nil {
			t.Fatalf("expected basic credential error for %q", bad)
		}
	}
	for bind, want := range map[string]bool{
		"127.0.0.1:7890":     false,
		"localhost:7890":     false,
		"[::1]:7890":         false,
		"0.0.0.0:7890":       true,
		":7890":              true,
		"192.168.1.10:7890":  true,
		"mizan.example:7890": true,
		"bad bind":           true,
	} {
		if got := RequiresAuth(bind); got != want {
			t.Fatalf("RequiresAuth(%q)=%v want %v", bind, got, want)
		}
	}
	if !constantTimeEqual("same", "same") || constantTimeEqual("same", "different") {
		t.Fatal("constant time compare mismatch")
	}
	if !authPublicPath("/healthz") || !authPublicPath("/readyz") || authPublicPath("/version") {
		t.Fatal("unexpected public path classification")
	}
}

func TestRecoverer(t *testing.T) {
	handler := recoverer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/", nil))
	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d", res.Code)
	}
}

func TestStatusRecorder(t *testing.T) {
	blank := &statusRecorder{ResponseWriter: httptest.NewRecorder()}
	if blank.statusCode() != http.StatusOK {
		t.Fatalf("blank status=%d", blank.statusCode())
	}
	blank.Flush()
	if blank.statusCode() != http.StatusOK {
		t.Fatalf("flushed blank status=%d", blank.statusCode())
	}

	res := httptest.NewRecorder()
	rec := &statusRecorder{ResponseWriter: res}
	rec.WriteHeader(http.StatusAccepted)
	rec.WriteHeader(http.StatusTeapot)
	if rec.statusCode() != http.StatusAccepted || res.Code != http.StatusAccepted {
		t.Fatalf("status=%d recorder=%d", rec.statusCode(), res.Code)
	}
	if rec.Unwrap() != res {
		t.Fatal("unexpected wrapped response writer")
	}
	rec.Flush()
	if !res.Flushed {
		t.Fatal("response was not flushed")
	}
}

func TestStatusRecorderWriteAndRoutePattern(t *testing.T) {
	res := httptest.NewRecorder()
	rec := &statusRecorder{ResponseWriter: res}
	if _, err := rec.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	if rec.statusCode() != http.StatusOK {
		t.Fatalf("status=%d", rec.statusCode())
	}
	req := httptest.NewRequest(http.MethodGet, "/unmatched", nil)
	if got := routePattern(req); got != "unmatched" {
		t.Fatalf("route=%q", got)
	}
	req.Pattern = "GET /known"
	if got := routePattern(req); got != "GET /known" {
		t.Fatalf("route=%q", got)
	}
}

func TestEmbeddedUIRootAndDefaultLogger(t *testing.T) {
	handler := logger(nil, embeddedUI())
	for _, path := range []string{"/", ""} {
		req := httptest.NewRequest(http.MethodGet, "http://example.com"+path, nil)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("root path %q status=%d", path, res.Code)
		}
		_, _ = io.Copy(io.Discard, res.Body)
	}
}

func TestEmbeddedUIMissingDist(t *testing.T) {
	handler := embeddedUIFromSub(nil, errEmbeddedUIMissing)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/", nil))
	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d", res.Code)
	}
}
