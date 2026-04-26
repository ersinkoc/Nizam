package parser

import (
	"strings"
	"testing"
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
  bind :80
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

	nginxCfg := `
http {
  upstream be_app {
    server 10.0.0.1:8080;
  }
  server {
    listen 80;
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
