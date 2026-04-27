package parser

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/mizanproxy/mizan/internal/ir"
	haproxytranslator "github.com/mizanproxy/mizan/internal/translate/haproxy"
	nginxtranslator "github.com/mizanproxy/mizan/internal/translate/nginx"
)

func TestParseHAProxy(t *testing.T) {
	cfg := "\ufeff" + `
frontend web
  bind :443 ssl crt /etc/ssl/edge.pem alpn h2,http/1.1
  mode http
  acl api path_beg /api/
  use_backend be_api if api
  default_backend be_app

backend be_app
  balance leastconn
  option httpchk GET /healthz
  server app1 10.0.0.1:8080 weight 80 maxconn 100

backend be_api
  server api1 10.0.0.2:9000 check
`
	model, err := ParseHAProxy(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(model.Frontends); got != 1 {
		t.Fatalf("frontends=%d", got)
	}
	fe := model.Frontends[0]
	if fe.Bind != ":443" || fe.TLSID == "" || fe.DefaultBackend != "be_app" {
		t.Fatalf("unexpected frontend: %+v", fe)
	}
	if got := len(model.Backends); got != 2 {
		t.Fatalf("backends=%d", got)
	}
	if got := len(model.Servers); got != 2 {
		t.Fatalf("servers=%d", got)
	}
	if model.Backends[0].Algorithm != "leastconn" || model.Backends[0].HealthCheckID == "" {
		t.Fatalf("unexpected backend: %+v", model.Backends[0])
	}
}

func TestParseNginx(t *testing.T) {
	cfg := `
events {}
http {
  upstream be_app {
    least_conn;
    server 10.0.0.1:8080 weight=90;
  }
  server {
    listen 443 ssl http2;
    ssl_certificate /etc/ssl/edge.pem;
    ssl_certificate_key /etc/ssl/edge.key;
    location / {
      proxy_pass http://be_app;
    }
  }
}
`
	model, err := ParseNginx(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(model.Backends); got != 1 {
		t.Fatalf("backends=%d", got)
	}
	if model.Backends[0].Algorithm != "leastconn" {
		t.Fatalf("algorithm=%q", model.Backends[0].Algorithm)
	}
	if got := len(model.Frontends); got != 1 {
		t.Fatalf("frontends=%d", got)
	}
	if model.Frontends[0].TLSID == "" || model.Frontends[0].DefaultBackend != "be_app" {
		t.Fatalf("unexpected frontend: %+v", model.Frontends[0])
	}
}

func TestHAProxyRoundTripPreservesCoreIR(t *testing.T) {
	cfg := `
frontend web
  bind :443 ssl crt /etc/ssl/edge.pem
  mode http
  acl api path_beg /api/
  use_backend be_api if api
  default_backend be_app

backend be_app
  balance leastconn
  option httpchk GET /healthz
  server app1 10.0.0.1:8080 weight 80 check inter 1500ms rise 4 fall 2 maxconn 100

backend be_api
  balance roundrobin
  server api1 10.0.0.2:9000 weight 100
`
	original, err := ParseHAProxy(cfg)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip, err := ParseHAProxy(haproxytranslator.Generate(original).Config)
	if err != nil {
		t.Fatal(err)
	}
	assertCoreRoundTrip(t, original, roundTrip)
}

func TestNginxRoundTripPreservesCoreIR(t *testing.T) {
	cfg := `
events {}
http {
  upstream be_app {
    least_conn;
    server 10.0.0.1:8080 weight=90 max_conns=250;
  }
  upstream be_api {
    server 10.0.0.2:9000 weight=100;
  }
  server {
    listen 443 ssl http2;
    ssl_certificate /etc/ssl/edge.pem;
    ssl_certificate_key /etc/ssl/edge.key;
    location /api/ {
      proxy_pass http://be_api;
    }
    location / {
      proxy_pass http://be_app;
    }
  }
}
`
	original, err := ParseNginx(cfg)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip, err := ParseNginx(nginxtranslator.Generate(original).Config)
	if err != nil {
		t.Fatal(err)
	}
	assertCoreRoundTrip(t, original, roundTrip)
}

func TestParseFileDetectionAndErrors(t *testing.T) {
	if model, err := ParseFile("nginx.conf", []byte("events {}\nhttp {\nupstream be { server 127.0.0.1:80; }\n}\n")); err != nil || model.Name != "imported-nginx" {
		t.Fatalf("nginx detect failed model=%+v err=%v", model, err)
	}
	if model, err := ParseFile("haproxy.cfg", []byte("frontend web\n  bind :80\n")); err != nil || model.Name != "imported-haproxy" {
		t.Fatalf("haproxy detect failed model=%+v err=%v", model, err)
	}
	if _, err := ParseFile("unknown.txt", []byte("not a config")); err == nil {
		t.Fatal("expected unknown config error")
	}
}

func TestParserEdgeBranches(t *testing.T) {
	haproxyCfg := `
frontend web
  bind :80 # public listener
  acl host_acl hdr(host) -i example.com
  use_backend be_app if host_acl
  use_backend be_other
backend be_app
  server app_without_port 10.0.0.1
backend be_other
  option httpchk GET
  server badport 10.0.0.2:nope
`
	model, err := ParseHAProxy(haproxyCfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(model.Rules) < 2 {
		t.Fatalf("expected rules from ACL/use_backend branches: %+v", model.Rules)
	}
	if model.Servers[0].Port != 80 || model.Servers[1].Port != 80 {
		t.Fatalf("expected fallback ports, got %+v", model.Servers)
	}
	if model.Frontends[0].Bind != ":80" {
		t.Fatalf("expected HAProxy inline comments to be stripped from bind, got %q", model.Frontends[0].Bind)
	}

	nginxCfg := `
http {
  upstream be_app {
    server 10.0.0.1:8080; # primary
  }
  server {
    listen 127.0.0.1:8081;
    location /api/ {
      proxy_pass http://be_app;
    }
  }
}
`
	nginxModel, err := ParseNginx(nginxCfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(nginxModel.Rules) != 1 || nginxModel.Frontends[0].DefaultBackend != "" {
		t.Fatalf("unexpected nginx location parse: %+v", nginxModel)
	}
	if nginxModel.Servers[0].Port != 8080 || nginxModel.Frontends[0].Bind != "127.0.0.1:8081" {
		t.Fatalf("expected Nginx inline comment/listen normalization, got bind=%q servers=%+v", nginxModel.Frontends[0].Bind, nginxModel.Servers)
	}
}

func TestParserDefensiveBranches(t *testing.T) {
	haproxyCfg := `
frontend
listen
backend
defaults
frontend web
  bind
  mode
  acl too_short
  use_backend
backend be_app
  balance
  option httpchk
  server
  server dup 10.0.0.1:80
  server dup 10.0.0.1:80
`
	haproxyModel, err := ParseHAProxy(haproxyCfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(haproxyModel.Frontends) != 1 || len(haproxyModel.Backends) != 1 {
		t.Fatalf("unexpected defensive HAProxy model: %+v", haproxyModel)
	}
	if len(haproxyModel.Backends[0].Servers) != 1 {
		t.Fatalf("expected duplicate server reference to be ignored: %+v", haproxyModel.Backends[0].Servers)
	}

	nginxCfg := `
upstream
upstream be_app {
  keepalive 32;
  server
  server 10.0.0.1:8080 weight=nope;
  server 10.0.0.1:8080;
}
server {
  listen
  ssl_certificate /etc/ssl/edge.pem;
  ssl_certificate_key /etc/ssl/edge.key;
  location /empty/ {
    return 404;
  }
  location /short/ {
    proxy_pass;
  }
}
}
`
	nginxModel, err := ParseNginx(nginxCfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(nginxModel.Backends) != 1 || len(nginxModel.Frontends) != 1 {
		t.Fatalf("unexpected defensive Nginx model: %+v", nginxModel)
	}
	if nginxModel.Frontends[0].TLSID == "" || len(nginxModel.TLSProfiles) != 1 {
		t.Fatalf("expected ensureTLS to create and reuse a profile: %+v", nginxModel)
	}
	if len(nginxModel.Backends[0].Servers) != 1 {
		t.Fatalf("expected duplicate upstream server reference to be ignored: %+v", nginxModel.Backends[0].Servers)
	}

	if model, err := ParseFile("plain.txt", []byte("backend be_app\n  server s1 127.0.0.1:80\n")); err != nil || model.Name != "imported-haproxy" {
		t.Fatalf("content HAProxy detect failed model=%+v err=%v", model, err)
	}
	if got := normalizeID("x", " / "); got != "x_default" {
		t.Fatalf("unexpected normalized default id: %q", got)
	}
	for _, tc := range []struct {
		raw  string
		want int
		ok   bool
	}{
		{"250ms", 250, true},
		{"2s", 2000, true},
		{"1m", 60000, true},
		{"1500", 1500, true},
		{"bad", 0, false},
		{"", 0, false},
	} {
		got, ok := parseDurationMillis(tc.raw)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("parseDurationMillis(%q)=%d,%t want %d,%t", tc.raw, got, ok, tc.want, tc.ok)
		}
	}
}

func TestParserScannerErrors(t *testing.T) {
	longLine := strings.Repeat("x", 70_000)
	if _, err := ParseHAProxy(longLine); err == nil {
		t.Fatal("expected HAProxy scanner error for oversized token")
	}
	if _, err := ParseNginx(longLine); err == nil {
		t.Fatal("expected Nginx scanner error for oversized token")
	}
}

func assertCoreRoundTrip(t *testing.T, original, roundTrip *ir.Model) {
	t.Helper()
	if len(roundTrip.Frontends) != len(original.Frontends) || len(roundTrip.Backends) != len(original.Backends) || len(roundTrip.Servers) != len(original.Servers) {
		t.Fatalf("round-trip counts changed: original f/b/s=%d/%d/%d got=%d/%d/%d", len(original.Frontends), len(original.Backends), len(original.Servers), len(roundTrip.Frontends), len(roundTrip.Backends), len(roundTrip.Servers))
	}
	if got, want := backendSignature(roundTrip), backendSignature(original); got != want {
		t.Fatalf("backend signature changed:\noriginal=%s\ngot=%s", want, got)
	}
	if got, want := serverSignature(roundTrip), serverSignature(original); got != want {
		t.Fatalf("server signature changed:\noriginal=%s\ngot=%s", want, got)
	}
	if got, want := routeSignature(roundTrip), routeSignature(original); got != want {
		t.Fatalf("route signature changed:\noriginal=%s\ngot=%s", want, got)
	}
	if got, want := tlsSignature(roundTrip), tlsSignature(original); got != want {
		t.Fatalf("TLS signature changed:\noriginal=%s\ngot=%s", want, got)
	}
	if got, want := healthSignature(roundTrip), healthSignature(original); got != want {
		t.Fatalf("health signature changed:\noriginal=%s\ngot=%s", want, got)
	}
}

func backendSignature(m *ir.Model) string {
	parts := make([]string, 0, len(m.Backends))
	for _, be := range m.Backends {
		servers := append([]string(nil), be.Servers...)
		sort.Strings(servers)
		parts = append(parts, fmt.Sprintf("%s|%s|%t|%s", be.ID, be.Algorithm, be.HealthCheckID != "", strings.Join(servers, ",")))
	}
	sort.Strings(parts)
	return strings.Join(parts, ";")
}

func serverSignature(m *ir.Model) string {
	parts := make([]string, 0, len(m.Servers))
	for _, srv := range m.Servers {
		parts = append(parts, fmt.Sprintf("%s|%s|%d|%d|%d", srv.ID, srv.Address, srv.Port, srv.Weight, srv.MaxConn))
	}
	sort.Strings(parts)
	return strings.Join(parts, ";")
}

func routeSignature(m *ir.Model) string {
	rules := map[string]ir.Rule{}
	for _, rule := range m.Rules {
		rules[rule.ID] = rule
	}
	parts := make([]string, 0, len(m.Frontends)+len(m.Rules))
	for _, fe := range m.Frontends {
		parts = append(parts, fmt.Sprintf("frontend|%s|%s|%t|%s", fe.Bind, fe.Protocol, fe.TLSID != "", fe.DefaultBackend))
		for _, ruleID := range fe.Rules {
			rule := rules[ruleID]
			parts = append(parts, fmt.Sprintf("rule|%s|%s|%s|%s", rule.Predicate.Type, rule.Predicate.Value, rule.Action.Type, rule.Action.BackendID))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, ";")
}

func tlsSignature(m *ir.Model) string {
	parts := make([]string, 0, len(m.TLSProfiles))
	for _, tls := range m.TLSProfiles {
		parts = append(parts, fmt.Sprintf("%s|%s", tls.CertPath, tls.KeyPath))
	}
	sort.Strings(parts)
	return strings.Join(parts, ";")
}

func healthSignature(m *ir.Model) string {
	parts := make([]string, 0, len(m.HealthChecks))
	for _, hc := range m.HealthChecks {
		parts = append(parts, fmt.Sprintf("%s|%s|%v|%d|%d|%d|%d", hc.ID, hc.Type, hc.ExpectedStatus, hc.IntervalMS, hc.TimeoutMS, hc.Rise, hc.Fall))
	}
	sort.Strings(parts)
	return strings.Join(parts, ";")
}
