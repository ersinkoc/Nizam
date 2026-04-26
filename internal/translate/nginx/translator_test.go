package nginx

import (
	"strings"
	"testing"

	"github.com/mizanproxy/mizan/internal/ir"
)

func TestGenerateNginx(t *testing.T) {
	model := sampleModel()
	result := Generate(model)
	for _, want := range []string{
		"events {}",
		"proxy_cache_path /var/cache/nginx keys_zone=edge_cache:10m max_size=2g",
		"upstream be_app",
		"least_conn;",
		"server 10.0.0.1:8080 weight=100 max_conns=500;",
		"listen 443 ssl http2;",
		"ssl_certificate /etc/ssl/edge.pem;",
		"location /api/",
		"proxy_pass http://be_api;",
	} {
		if !strings.Contains(result.Config, want) {
			t.Fatalf("generated config missing %q:\n%s", want, result.Config)
		}
	}
	if len(result.Warnings) != 1 {
		t.Fatalf("expected rate limit warning, got %+v", result.Warnings)
	}
}

func TestHelpers(t *testing.T) {
	if defaultString("", "x") != "x" || defaultString("a", "x") != "a" {
		t.Fatal("defaultString unexpected")
	}
	if listenPort(":443") != "443" || listenPort("8080") != "8080" {
		t.Fatal("listenPort unexpected")
	}
}

func TestGenerateNginxMissingOptionalReferences(t *testing.T) {
	model := sampleModel()
	model.Frontends[0].TLSID = "missing_tls"
	model.Frontends[0].Rules = []string{"missing_rule"}
	model.Backends[0].Servers = []string{"missing_server"}
	model.Caches[0].MaxSize = ""
	result := Generate(model)
	if !strings.Contains(result.Config, "max_size=1g") || !strings.Contains(result.Config, "server {") {
		t.Fatalf("unexpected config with missing refs:\n%s", result.Config)
	}
}

func TestGenerateNginxServerDefaults(t *testing.T) {
	model := sampleModel()
	model.Backends[0].Servers = []string{"s_zero"}
	model.Servers = []ir.Server{{ID: "s_zero", Address: "10.0.0.3", Port: 8081}}
	result := Generate(model)
	if !strings.Contains(result.Config, "server 10.0.0.3:8081 weight=100;") {
		t.Fatalf("expected default weight without max_conns:\n%s", result.Config)
	}
}

func sampleModel() *ir.Model {
	return &ir.Model{
		Version: 1,
		ID:      "p_1",
		Name:    "edge",
		Engines: []ir.Engine{ir.EngineNginx},
		Frontends: []ir.Frontend{{
			ID:             "fe_web",
			Name:           "web",
			Bind:           ":443",
			Protocol:       "http2",
			TLSID:          "tls_default",
			Rules:          []string{"r_api"},
			DefaultBackend: "be_app",
		}},
		Backends: []ir.Backend{
			{ID: "be_app", Name: "app", Algorithm: "leastconn", Servers: []string{"s_app"}},
			{ID: "be_api", Name: "api", Algorithm: "roundrobin", Servers: []string{}},
		},
		Servers: []ir.Server{{ID: "s_app", Address: "10.0.0.1", Port: 8080, Weight: 100, MaxConn: 500}},
		Rules: []ir.Rule{{
			ID:        "r_api",
			Predicate: ir.Predicate{Type: "path_prefix", Value: "/api/"},
			Action:    ir.RuleAction{Type: "use_backend", BackendID: "be_api"},
		}},
		TLSProfiles: []ir.TLSProfile{{ID: "tls_default", CertPath: "/etc/ssl/edge.pem", KeyPath: "/etc/ssl/edge.key"}},
		Caches:      []ir.CachePolicy{{ID: "cache", Zone: "edge_cache", Path: "/var/cache/nginx", MaxSize: "2g"}},
		RateLimits:  []ir.RateLimit{{ID: "rl", Key: "ip", PeriodMS: 1000, Requests: 100}},
	}
}
