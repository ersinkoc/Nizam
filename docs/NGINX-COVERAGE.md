# Nginx Coverage

Implemented in v0:

- `events` and `http` contexts
- `upstream` blocks with weighted servers and `max_conns`
- `server` blocks with port-only or address-qualified `listen`, TLS certificate directives, and basic HTTP/2
- `location` blocks with `proxy_pass`
- `proxy_cache_path` for cache policies
- Full-line and whitespace-prefixed inline comments in imported configs
- Core parser/generator/parser round-trip coverage for upstreams, TLS cert/key paths, HTTP/2, routing, weighted servers, max connections, and default backends

Not yet complete:

- Full parser round-trip for advanced directives
- `map`, advanced `limit_req`, and include resolution
- Source-map driven native error mapping
