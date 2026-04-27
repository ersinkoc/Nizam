package parser

import (
	"bufio"
	"strconv"
	"strings"

	"github.com/mizanproxy/mizan/internal/ir"
)

func ParseHAProxy(config string) (*ir.Model, error) {
	m := ir.EmptyModel("", "imported-haproxy", "Imported from haproxy.cfg", []ir.Engine{ir.EngineHAProxy})
	scanner := bufio.NewScanner(strings.NewReader(config))
	var section, sectionName string
	backendIndex := map[string]int{}
	ruleByACL := map[string]ir.Rule{}

	for scanner.Scan() {
		raw := scanner.Text()
		line := strings.TrimPrefix(strings.TrimSpace(raw), "\ufeff")
		line = stripInlineComment(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		switch fields[0] {
		case "frontend", "listen":
			if len(fields) < 2 {
				continue
			}
			section, sectionName = "frontend", fields[1]
			m.Frontends = append(m.Frontends, ir.Frontend{
				ID:       normalizeID("fe", sectionName),
				Name:     sectionName,
				Protocol: "http",
				Rules:    []string{},
				View:     ir.EntityView{X: 80, Y: float64(100 + len(m.Frontends)*140)},
			})
		case "backend":
			if len(fields) < 2 {
				continue
			}
			section, sectionName = "backend", fields[1]
			backendIndex[sectionName] = len(m.Backends)
			m.Backends = append(m.Backends, ir.Backend{
				ID:        sectionName,
				Name:      sectionName,
				Algorithm: "roundrobin",
				Servers:   []string{},
				View:      ir.EntityView{X: 420, Y: float64(100 + len(m.Backends)*140)},
			})
		case "global", "defaults":
			section, sectionName = fields[0], fields[0]
		default:
			if section == "frontend" && len(m.Frontends) > 0 {
				fe := &m.Frontends[len(m.Frontends)-1]
				parseHAProxyFrontendLine(m, fe, fields, ruleByACL)
			}
			if section == "backend" {
				idx, ok := backendIndex[sectionName]
				if ok {
					parseHAProxyBackendLine(m, &m.Backends[idx], fields)
				}
			}
		}
	}
	for _, rule := range ruleByACL {
		m.Rules = append(m.Rules, rule)
	}
	return m, scanner.Err()
}

func parseHAProxyFrontendLine(m *ir.Model, fe *ir.Frontend, fields []string, rules map[string]ir.Rule) {
	switch fields[0] {
	case "bind":
		if len(fields) > 1 {
			fe.Bind = fields[1]
		}
		for i, f := range fields {
			if f == "ssl" {
				tlsID := normalizeID("tls", fe.Name)
				fe.TLSID = tlsID
				cert := ""
				if i+2 < len(fields) && fields[i+1] == "crt" {
					cert = fields[i+2]
				}
				m.TLSProfiles = append(m.TLSProfiles, ir.TLSProfile{ID: tlsID, Name: fe.Name + " TLS", CertPath: cert})
			}
		}
	case "mode":
		if len(fields) > 1 {
			fe.Protocol = fields[1]
		}
	case "acl":
		if len(fields) < 4 {
			return
		}
		aclName := fields[1]
		predicate := ir.Predicate{Type: "path_prefix", Value: fields[len(fields)-1]}
		if fields[2] == "hdr(host)" {
			predicate = ir.Predicate{Type: "host", Value: fields[len(fields)-1]}
		}
		rules[aclName] = ir.Rule{ID: normalizeID("r", aclName), Name: aclName, Predicate: predicate, Action: ir.RuleAction{Type: "use_backend"}, View: ir.EntityView{X: 250, Y: fe.View.Y}}
	case "use_backend":
		if len(fields) < 2 {
			return
		}
		backendID := fields[1]
		aclName := ""
		if len(fields) >= 4 && fields[2] == "if" {
			aclName = fields[3]
		}
		rule := rules[aclName]
		if rule.ID == "" {
			rule = ir.Rule{ID: normalizeID("r", backendID), Predicate: ir.Predicate{Type: "path_prefix", Value: "/"}, View: ir.EntityView{X: 250, Y: fe.View.Y}}
		}
		rule.Action = ir.RuleAction{Type: "use_backend", BackendID: backendID}
		rules[aclName] = rule
		fe.Rules = appendUnique(fe.Rules, rule.ID)
	case "default_backend":
		if len(fields) > 1 {
			fe.DefaultBackend = fields[1]
		}
	}
}

func parseHAProxyBackendLine(m *ir.Model, be *ir.Backend, fields []string) {
	switch fields[0] {
	case "balance":
		if len(fields) > 1 {
			be.Algorithm = fields[1]
		}
	case "option":
		if len(fields) >= 3 && fields[1] == "httpchk" {
			hc := ensureBackendHealthCheck(m, be)
			path := "/"
			if len(fields) >= 4 {
				path = fields[3]
			}
			hc.Type = "http"
			hc.Path = path
			hc.ExpectedStatus = []int{200}
		}
	case "server":
		if len(fields) < 3 {
			return
		}
		host, port := splitHostPort(fields[2])
		srv := ir.Server{ID: normalizeID("s", fields[1]), Name: fields[1], Address: host, Port: port, Weight: 100}
		for i := 3; i < len(fields)-1; i++ {
			if fields[i] == "weight" {
				if n, err := strconv.Atoi(fields[i+1]); err == nil {
					srv.Weight = n
				}
			}
			if fields[i] == "maxconn" {
				if n, err := strconv.Atoi(fields[i+1]); err == nil {
					srv.MaxConn = n
				}
			}
		}
		if hasHAProxyCheckOptions(fields) {
			applyHAProxyServerCheckOptions(m, be, fields)
		}
		m.Servers = append(m.Servers, srv)
		be.Servers = appendUnique(be.Servers, srv.ID)
	}
}

func applyHAProxyServerCheckOptions(m *ir.Model, be *ir.Backend, fields []string) {
	hc := ensureBackendHealthCheck(m, be)
	for i := 3; i < len(fields)-1; i++ {
		switch fields[i] {
		case "inter":
			if ms, ok := parseDurationMillis(fields[i+1]); ok {
				hc.IntervalMS = ms
			}
		case "rise":
			if n, err := strconv.Atoi(fields[i+1]); err == nil {
				hc.Rise = n
			}
		case "fall":
			if n, err := strconv.Atoi(fields[i+1]); err == nil {
				hc.Fall = n
			}
		}
	}
}

func ensureBackendHealthCheck(m *ir.Model, be *ir.Backend) *ir.HealthCheck {
	if be.HealthCheckID == "" {
		be.HealthCheckID = normalizeID("hc", be.ID)
	}
	for i := range m.HealthChecks {
		if m.HealthChecks[i].ID == be.HealthCheckID {
			return &m.HealthChecks[i]
		}
	}
	m.HealthChecks = append(m.HealthChecks, ir.HealthCheck{ID: be.HealthCheckID, Type: "tcp", IntervalMS: 2000, TimeoutMS: 1000, Rise: 2, Fall: 3})
	return &m.HealthChecks[len(m.HealthChecks)-1]
}

func parseDurationMillis(value string) (int, bool) {
	if value == "" {
		return 0, false
	}
	multiplier := 1
	raw := value
	switch {
	case strings.HasSuffix(value, "ms"):
		multiplier = 1
		raw = strings.TrimSuffix(value, "ms")
	case strings.HasSuffix(value, "s"):
		multiplier = 1000
		raw = strings.TrimSuffix(value, "s")
	case strings.HasSuffix(value, "m"):
		multiplier = 60 * 1000
		raw = strings.TrimSuffix(value, "m")
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	return n * multiplier, true
}

func hasHAProxyCheckOptions(fields []string) bool {
	for _, field := range fields {
		if field == "inter" || field == "rise" || field == "fall" {
			return true
		}
	}
	return false
}

func splitHostPort(v string) (string, int) {
	idx := strings.LastIndex(v, ":")
	if idx < 0 {
		return v, 80
	}
	port, err := strconv.Atoi(v[idx+1:])
	if err != nil {
		port = 80
	}
	return v[:idx], port
}

func appendUnique(items []string, item string) []string {
	for _, existing := range items {
		if existing == item {
			return items
		}
	}
	return append(items, item)
}
