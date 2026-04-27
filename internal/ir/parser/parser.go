package parser

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/mizanproxy/mizan/internal/ir"
)

func ParseFile(name string, data []byte) (*ir.Model, error) {
	ext := strings.ToLower(filepath.Ext(name))
	text := string(data)
	switch {
	case ext == ".cfg" && strings.Contains(text, "frontend "):
		return ParseHAProxy(text)
	case ext == ".conf", strings.Contains(text, "http {"), strings.Contains(text, "upstream "):
		return ParseNginx(text)
	case strings.Contains(text, "frontend ") || strings.Contains(text, "backend "):
		return ParseHAProxy(text)
	default:
		return nil, errors.New("could not detect config format")
	}
}

func normalizeID(prefix, name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	replacer := strings.NewReplacer(" ", "_", "-", "_", ".", "_", ":", "_", "/", "_")
	name = replacer.Replace(name)
	name = strings.Trim(name, "_")
	if name == "" {
		name = "default"
	}
	return prefix + "_" + name
}

func stripInlineComment(line string) string {
	var quote rune
	escaped := false
	for idx, r := range line {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' && quote != 0 {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			continue
		}
		if r == '"' || r == '\'' {
			quote = r
			continue
		}
		if r == '#' && (idx == 0 || line[idx-1] == ' ' || line[idx-1] == '\t') {
			return strings.TrimSpace(line[:idx])
		}
	}
	return strings.TrimSpace(line)
}

func splitConfigFields(line string) []string {
	var fields []string
	var current strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if current.Len() == 0 {
			return
		}
		fields = append(fields, current.String())
		current.Reset()
	}
	for _, r := range line {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && quote != 0 {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			current.WriteRune(r)
			continue
		}
		if r == '"' || r == '\'' {
			quote = r
			continue
		}
		if r == ' ' || r == '\t' || r == '\r' || r == '\n' {
			flush()
			continue
		}
		current.WriteRune(r)
	}
	flush()
	return fields
}
