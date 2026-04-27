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
	backendIndex := map[string]int{}
	var stack []nginxContext

	for scanner.Scan() {
		line := strings.TrimPrefix(strings.TrimSpace(scanner.Text()), "\ufeff")
		line = stripInlineComment(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := tokenizeNginxLine(line)
		if len(fields) == 0 {
			continue
		}
		closeCount := 0
		for len(fields) > 0 && fields[0] == "}" {
			closeCount++
			fields = fields[1:]
		}
		for ; closeCount > 0 && len(stack) > 0; closeCount-- {
			stack = stack[:len(stack)-1]
		}
		if len(fields) == 0 {
			continue
		}
		opensBlock := fields[len(fields)-1] == "{"
		if opensBlock {
			fields = fields[:len(fields)-1]
		}
		fields = trimNginxSemicolon(fields)
		if len(fields) == 0 {
			continue
		}
		switch {
		case opensBlock && fields[0] == "upstream" && len(fields) >= 2:
			name := fields[1]
			backendIndex[name] = len(m.Backends)
			m.Backends = append(m.Backends, ir.Backend{ID: name, Name: name, Algorithm: "roundrobin", Servers: []string{}, View: ir.EntityView{X: 420, Y: float64(100 + len(m.Backends)*140)}})
			stack = append(stack, nginxContext{Kind: "upstream", Name: name})
		case opensBlock && fields[0] == "server":
			frontendIndex := len(m.Frontends)
			m.Frontends = append(m.Frontends, ir.Frontend{ID: normalizeID("fe", strconv.Itoa(len(m.Frontends)+1)), Name: "server", Protocol: "http", Rules: []string{}, View: ir.EntityView{X: 80, Y: float64(100 + len(m.Frontends)*140)}})
			stack = append(stack, nginxContext{Kind: "server", Name: "server", FrontendIndex: frontendIndex})
		case opensBlock && fields[0] == "location" && len(fields) >= 2:
			ctx := currentNginxContext(stack)
			stack = append(stack, nginxContext{Kind: "location", Name: fields[1], FrontendIndex: ctx.FrontendIndex})
		case opensBlock:
			stack = append(stack, nginxContext{Kind: fields[0], Name: ""})
		default:
			ctx := currentNginxContext(stack)
			if ctx.Kind == "upstream" {
				idx, ok := backendIndex[ctx.Name]
				if ok {
					parseNginxUpstreamLine(m, &m.Backends[idx], fields)
				}
			}
			if ctx.Kind == "server" && ctx.FrontendIndex >= 0 && ctx.FrontendIndex < len(m.Frontends) {
				parseNginxServerLine(m, &m.Frontends[ctx.FrontendIndex], fields)
			}
			if ctx.Kind == "location" && ctx.FrontendIndex >= 0 && ctx.FrontendIndex < len(m.Frontends) {
				parseNginxLocationLine(m, &m.Frontends[ctx.FrontendIndex], ctx.Name, fields)
			}
		}
	}
	return m, scanner.Err()
}

type nginxContext struct {
	Kind          string
	Name          string
	FrontendIndex int
}

func currentNginxContext(stack []nginxContext) nginxContext {
	if len(stack) == 0 {
		return nginxContext{FrontendIndex: -1}
	}
	return stack[len(stack)-1]
}

func tokenizeNginxLine(line string) []string {
	line = strings.NewReplacer("{", " { ", "}", " } ", ";", " ; ").Replace(line)
	return splitConfigFields(line)
}

func trimNginxSemicolon(fields []string) []string {
	out := fields[:0]
	for _, field := range fields {
		if field != ";" {
			out = append(out, field)
		}
	}
	return out
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
