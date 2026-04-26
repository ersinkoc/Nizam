package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

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
	req = httptest.NewRequest(http.MethodGet, "/some/spa/path", nil)
	res = httptest.NewRecorder()
	srv.Handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("spa status=%d", res.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/nope", nil)
	res = httptest.NewRecorder()
	srv.Handler.ServeHTTP(res, req)
	if res.Code != http.StatusNotFound {
		t.Fatalf("api fallback status=%d", res.Code)
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
