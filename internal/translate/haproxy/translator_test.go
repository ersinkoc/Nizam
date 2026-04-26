package haproxy

import (
	"strings"
	"testing"

	"github.com/mizanproxy/mizan/internal/ir"
)

func TestGenerateHAProxy(t *testing.T) {
	model := sampleModel()
	result := Generate(model)
	for _, want := range []string{
		"frontend web",
		"bind :443 ssl crt /etc/ssl/edge.pem alpn h2,http/1.1",
		"acl acl_r_api path_beg /api/",
		"use_backend be_api if acl_r_api",
		"backend app",
		"balance leastconn",
		"option httpchk GET /healthz",
		"server app1 10.0.0.1:8080 weight 100 check maxconn 500",
	} {
		if !strings.Contains(result.Config, want) {
			t.Fatalf("generated config missing %q:\n%s", want, result.Config)
		}
	}
	if len(result.SourceMap) == 0 {
		t.Fatal("expected source map entries")
	}
}

func TestHelpers(t *testing.T) {
	if safeName("id", "") != "id" || safeName("id", "my-name") != "my_name" {
		t.Fatal("safeName unexpected")
	}
	if haproxyMode("tcp") != "tcp" || haproxyMode("http") != "http" {
		t.Fatal("haproxyMode unexpected")
	}
	if haproxyAlgorithm("source") != "source" || haproxyAlgorithm("least_conn") != "leastconn" || haproxyAlgorithm("") != "roundrobin" {
		t.Fatal("haproxyAlgorithm unexpected")
	}
	if predicate(ir.Predicate{Type: "host", Value: "example.com"}) != "hdr(host) -i example.com" {
		t.Fatal("host predicate unexpected")
	}
	if predicate(ir.Predicate{Type: "path", Value: "/exact"}) != "path /exact" {
		t.Fatal("path predicate unexpected")
	}
}

func TestGenerateHAProxyMissingOptionalReferences(t *testing.T) {
	model := sampleModel()
	model.Frontends[0].TLSID = "missing_tls"
	model.Frontends[0].Rules = []string{"missing_rule"}
	model.Backends[0].HealthCheckID = "missing_hc"
	model.Backends[0].Servers = []string{"missing_server"}
	result := Generate(model)
	if !strings.Contains(result.Config, "frontend web") || !strings.Contains(result.Config, "backend app") {
		t.Fatalf("unexpected config with missing refs:\n%s", result.Config)
	}
}

func TestGenerateHAProxyDefaults(t *testing.T) {
	model := sampleModel()
	model.Backends[0].HealthCheckID = "hc_empty"
	model.Backends[0].Servers = []string{"s_z", "s_a", ""}
	model.Servers = []ir.Server{
		{ID: "s_z", Address: "10.0.0.3", Port: 8081},
		{ID: "s_a", Address: "10.0.0.4", Port: 8082},
		{ID: "", Address: "10.0.0.5", Port: 8083},
	}
	model.HealthChecks = []ir.HealthCheck{{ID: "hc_empty", Type: "http"}}
	result := Generate(model)
	for _, want := range []string{
		"option httpchk GET /",
		"server s_a 10.0.0.4:8082 weight 100 check",
		"server s_z 10.0.0.3:8081 weight 100 check",
		"server  10.0.0.5:8083 weight 100 check",
	} {
		if !strings.Contains(result.Config, want) {
			t.Fatalf("generated config missing %q:\n%s", want, result.Config)
		}
	}
}

func sampleModel() *ir.Model {
	return &ir.Model{
		Version: 1,
		ID:      "p_1",
		Name:    "edge",
		Engines: []ir.Engine{ir.EngineHAProxy, ir.EngineNginx},
		Frontends: []ir.Frontend{{
			ID:             "fe_web",
			Name:           "web",
			Bind:           ":443",
			Protocol:       "http",
			TLSID:          "tls_default",
			Rules:          []string{"r_api"},
			DefaultBackend: "be_app",
		}},
		Backends: []ir.Backend{
			{ID: "be_app", Name: "app", Algorithm: "leastconn", HealthCheckID: "hc_default", Servers: []string{"s_app"}},
			{ID: "be_api", Name: "api", Algorithm: "roundrobin", Servers: []string{}},
		},
		Servers: []ir.Server{{ID: "s_app", Name: "app1", Address: "10.0.0.1", Port: 8080, Weight: 100, MaxConn: 500}},
		Rules: []ir.Rule{{
			ID:        "r_api",
			Predicate: ir.Predicate{Type: "path_prefix", Value: "/api/"},
			Action:    ir.RuleAction{Type: "use_backend", BackendID: "be_api"},
		}},
		TLSProfiles:  []ir.TLSProfile{{ID: "tls_default", CertPath: "/etc/ssl/edge.pem", ALPN: []string{"h2", "http/1.1"}}},
		HealthChecks: []ir.HealthCheck{{ID: "hc_default", Type: "http", Path: "/healthz"}},
		OpaqueBlocks: []ir.OpaqueBlock{{Section: "backend be_app", Lines: []string{"option redispatch"}}},
	}
}
