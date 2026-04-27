# HAProxy Coverage

Implemented in v0:

- `global` and `defaults` boilerplate
- `frontend` with `bind`, `mode`, ACLs, `use_backend`, and `default_backend`
- TLS bind with certificate path and ALPN
- `backend` with `balance`, `server`, `weight`, `maxconn`, and `check`
- HTTP health checks via `option httpchk`, plus server `check inter`, `rise`, and `fall`
- Bracketed IPv6 bind/server endpoints
- Quoted directive values, including certificate paths and ACL values that contain spaces or `#`
- Full-line and whitespace-prefixed inline comments in imported configs
- Opaque backend lines when present in IR
- Core parser/generator/parser round-trip coverage for frontends, backends, TLS certs, routing, servers, weights, max connections, and health check timing

Not yet complete:

- Full parser round-trip for advanced directives
- Stick tables and advanced rate limiting
- Source-map driven native error mapping
