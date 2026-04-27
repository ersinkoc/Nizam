package parser

import (
	"bufio"
	"strconv"
	"strings"

	"github.com/mizanproxy/mizan/internal/ir"
)

func ParseNginx(config string) (*ir.Model, error) {
	m := ir.EmptyModel("", "imported-nginx", "Imported from nginx.conf", []ir.Engine{ir.EngineNginx})
	scanner := bufio.NewScanner(strings.NewReader(config))
	var block, name string
	backendIndex := map[string]int{}

	for scanner.Scan() {
		line := strings.TrimPrefix(strings.TrimSpace(scanner.Text()), "\ufeff")
		line = stripInlineComment(line)
		line = strings.TrimSuffix(line, ";")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		switch {
		case fields[0] == "upstream" && len(fields) >= 2:
			block, name = "upstream", strings.TrimSuffix(fields[1], "{")
			backendIndex[name] = len(m.Backends)
			m.Backends = append(m.Backends, ir.Backend{ID: name, Name: name, Algorithm: "roundrobin", Servers: []string{}, View: ir.EntityView{X: 420, Y: float64(100 + len(m.Backends)*140)}})
		case fields[0] == "server" && strings.HasSuffix(line, "{"):
			block, name = "server", "server"
			m.Frontends = append(m.Frontends, ir.Frontend{ID: normalizeID("fe", strconv.Itoa(len(m.Frontends)+1)), Name: "server", Protocol: "http", Rules: []string{}, View: ir.EntityView{X: 80, Y: float64(100 + len(m.Frontends)*140)}})
		case fields[0] == "location" && len(fields) >= 2:
			block, name = "location", fields[1]
		case fields[0] == "}":
			block, name = "", ""
		default:
			if block == "upstream" {
				idx, ok := backendIndex[name]
				if ok {
					parseNginxUpstreamLine(m, &m.Backends[idx], fields)
				}
			}
			if block == "server" && len(m.Frontends) > 0 {
				parseNginxServerLine(m, &m.Frontends[len(m.Frontends)-1], fields)
			}
			if block == "location" && len(m.Frontends) > 0 {
				parseNginxLocationLine(m, &m.Frontends[len(m.Frontends)-1], name, fields)
			}
		}
	}
	return m, scanner.Err()
}

func parseNginxUpstreamLine(m *ir.Model, be *ir.Backend, fields []string) {
	if fields[0] == "least_conn" {
		be.Algorithm = "leastconn"
		return
	}
	if fields[0] != "server" || len(fields) < 2 {
		return
	}
	host, port := splitHostPort(fields[1])
	srv := ir.Server{ID: normalizeID("s", host+"_"+strconv.Itoa(port)), Address: host, Port: port, Weight: 100}
	for _, field := range fields[2:] {
		if strings.HasPrefix(field, "weight=") {
			if n, err := strconv.Atoi(strings.TrimPrefix(field, "weight=")); err == nil {
				srv.Weight = n
			}
		}
		if strings.HasPrefix(field, "max_conns=") {
			if n, err := strconv.Atoi(strings.TrimPrefix(field, "max_conns=")); err == nil {
				srv.MaxConn = n
			}
		}
	}
	m.Servers = append(m.Servers, srv)
	be.Servers = appendUnique(be.Servers, srv.ID)
}

func parseNginxServerLine(m *ir.Model, fe *ir.Frontend, fields []string) {
	switch fields[0] {
	case "listen":
		if len(fields) > 1 {
			fe.Bind = normalizeNginxListenBind(fields[1])
		}
		if len(fields) < 3 {
			return
		}
		for _, field := range fields[2:] {
			if field == "ssl" {
				tlsID := normalizeID("tls", fe.ID)
				fe.TLSID = tlsID
				m.TLSProfiles = append(m.TLSProfiles, ir.TLSProfile{ID: tlsID, Name: fe.Name + " TLS"})
			}
			if field == "http2" {
				fe.Protocol = "http2"
			}
		}
	case "ssl_certificate":
		tls := ensureTLS(m, fe)
		if len(fields) > 1 {
			tls.CertPath = fields[1]
		}
	case "ssl_certificate_key":
		tls := ensureTLS(m, fe)
		if len(fields) > 1 {
			tls.KeyPath = fields[1]
		}
	}
}

func normalizeNginxListenBind(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.Contains(value, ":") {
		return value
	}
	return ":" + value
}

func parseNginxLocationLine(m *ir.Model, fe *ir.Frontend, location string, fields []string) {
	if fields[0] != "proxy_pass" || len(fields) < 2 {
		return
	}
	backendID := strings.TrimPrefix(fields[1], "http://")
	ruleID := normalizeID("r", fe.ID+"_"+location)
	if location == "/" {
		fe.DefaultBackend = backendID
		return
	}
	m.Rules = append(m.Rules, ir.Rule{
		ID:        ruleID,
		Predicate: ir.Predicate{Type: "path_prefix", Value: location},
		Action:    ir.RuleAction{Type: "use_backend", BackendID: backendID},
		View:      ir.EntityView{X: 250, Y: fe.View.Y},
	})
	fe.Rules = appendUnique(fe.Rules, ruleID)
}

func ensureTLS(m *ir.Model, fe *ir.Frontend) *ir.TLSProfile {
	if fe.TLSID == "" {
		fe.TLSID = normalizeID("tls", fe.ID)
	}
	for i := range m.TLSProfiles {
		if m.TLSProfiles[i].ID == fe.TLSID {
			return &m.TLSProfiles[i]
		}
	}
	m.TLSProfiles = append(m.TLSProfiles, ir.TLSProfile{ID: fe.TLSID, Name: fe.Name + " TLS"})
	return &m.TLSProfiles[len(m.TLSProfiles)-1]
}
