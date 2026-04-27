# Mizan

Mizan is a local-first visual configuration architect for HAProxy and Nginx. It runs as a single Go binary, serves an embedded React/Vite WebUI, stores projects as inspectable JSON under `~/.mizan`, and translates one Universal IR into target-specific HAProxy or Nginx configuration.

The current codebase is release-ready for the documented v0.1 scope with backend statement coverage at 96.7%, browser E2E coverage for the main operator workflow, clean Go/npm vulnerability scans, and a high/critical container CVE gate.

## What Works Today

- Single-binary Go CLI and HTTP server: `mizan serve`
- Optional Bearer token or Basic auth for HTTP access, required when binding outside localhost
- Hardened HTTP defaults with security headers, request body limits, server timeouts, and graceful shutdown
- Encrypted local secrets vault foundation using Argon2id-derived AES-GCM envelopes
- Health, storage-backed readiness, version, and Prometheus-style `/metrics` endpoints with build, project, and HTTP request counters
- Embedded React WebUI with project creation, import, IR editing, generation, validation, snapshots, diff, audit, topology, deployment-target, and approval-request panels
- Project CRUD and filesystem persistence under `~/.mizan/projects`
- Project export endpoint, CLI command, and WebUI download for portable JSON backups
- Full home backup, inspection, and restore commands with SHA-256 manifest verification for projects, snapshots, targets, audit logs, and encrypted vault envelopes
- Dockerfile, systemd unit, and Nginx TLS reverse proxy deployment examples
- `mizan doctor` preflight checks for data root integrity, projects, targets, secrets, SSH, and native validators
- Universal IR with structural linting, deterministic hashes, mutations, canonical JSON, and structural diffs
- HAProxy and Nginx import for the supported v0 directive subset
- HAProxy and Nginx generation from the shared IR
- Validation pipeline with IR linting plus optional native `haproxy -c` / `nginx -t` checks when binaries exist on `PATH`
- Snapshots, tags, snapshot retrieval, revert, and diff
- Append-only project audit log in `audit.jsonl`
- Filterable audit history through API, CLI, and WebUI
- Deployment targets and clusters persisted in `targets.json`
- CLI secret management for encrypted target credentials
- Deployment dry-run planning for single targets or clusters via generated rollout steps
- CLI deployment execution through the local `ssh` command with vault-backed username/private-key support and snapshot confirmation
- Deployment results and audit metadata include rollback summary counts
- Persisted API, CLI, and WebUI approval requests in `approvals.json` for snapshot-bound cluster or target rollouts
- Target probe checks for configured post-reload or monitor endpoints
- Monitor snapshots for registered targets through API, CLI, and WebUI
- Monitor SSE stream endpoint for repeated target health snapshots
- WebUI monitor panel consumes the live SSE stream while a project is open
- Project metadata, target registry updates, approval requests, and audit events can stream to WebUI over SSE
- Filterable audit history by actor, action/action prefix, outcome, target engine, time range, target/cluster/approval ids, batch, dry-run, incident, and rollback-failure state
- Filtered audit CSV export from the API, CLI, and WebUI
- HAProxy monitor endpoint support for `show stat` CSV health summaries
- Nginx monitor endpoint support for OSS `stub_status` connection summaries
- Topology canvas with drag/connect gestures that update the IR

## Run

```sh
go run ./cmd/mizan serve --bind 127.0.0.1:7890
```

Open `http://127.0.0.1:7890`.

When binding outside localhost, Mizan requires HTTP auth:

```sh
go run ./cmd/mizan serve --bind 0.0.0.0:7890 --auth-token "$MIZAN_AUTH_TOKEN"
go run ./cmd/mizan serve --bind 0.0.0.0:7890 --auth-basic operator:change-me
```

Runtime hardening controls:

```sh
go run ./cmd/mizan serve --bind 127.0.0.1:7890 --max-body-bytes 10485760 --shutdown-timeout 10s
```

If you already built the embedded binary on Windows:

```powershell
dist\mizan.exe serve --bind 127.0.0.1:7890
```

For frontend development:

```sh
cd webui
npm install
npm run dev
```

## CLI Examples

```sh
go run ./cmd/mizan project new --name edge-prod --engines haproxy,nginx
go run ./cmd/mizan project import ./haproxy.cfg --name imported-edge
go run ./cmd/mizan project export <id> --out mizan-export.json
go run ./cmd/mizan project list
go run ./cmd/mizan backup create --out mizan-backup.zip
go run ./cmd/mizan backup inspect --in mizan-backup.zip
go run ./cmd/mizan backup restore --in mizan-backup.zip --home /tmp/mizan-restore
go run ./cmd/mizan doctor --json
go run ./cmd/mizan snapshot list --project <id>
go run ./cmd/mizan snapshot tag --project <id> --label release-1 <snapshot-ref>
go run ./cmd/mizan target add --project <id> --name edge-01 --host 10.0.0.10 --engine haproxy --monitor-endpoint 'http://10.0.0.10:8404/;csv' --rollback-command 'cp /etc/haproxy/haproxy.cfg.bak /etc/haproxy/haproxy.cfg && systemctl reload haproxy'
go run ./cmd/mizan cluster add --project <id> --name prod --target-ids <target-id> --required-approvals 2
go run ./cmd/mizan generate --project <id> --target haproxy
go run ./cmd/mizan validate --project <id> --target nginx
go run ./cmd/mizan deploy --project <id> --target-id <target-id>
go run ./cmd/mizan deploy --project <id> --cluster-id <cluster-id> --batch 1
go run ./cmd/mizan approval request --project <id> --cluster-id <cluster-id> --batch 1
go run ./cmd/mizan approval approve --project <id> --actor alice <approval-request-id>
go run ./cmd/mizan approval approve --project <id> --actor bob <approval-request-id>
go run ./cmd/mizan deploy --project <id> --approval-request-id <approval-request-id> --execute --vault-passphrase "$MIZAN_VAULT_PASSPHRASE"
go run ./cmd/mizan audit show --project <id> --action deploy.run --dry-run true --csv --out audit.csv
go run ./cmd/mizan audit show --project <id> --incident true --rollback-failed true
go run ./cmd/mizan secret set --id <target-id> --username root --private-key-file ~/.ssh/id_ed25519 --vault-passphrase "$MIZAN_VAULT_PASSPHRASE"
go run ./cmd/mizan deploy --project <id> --cluster-id <cluster-id> --batch 1 --execute --confirm-snapshot <snapshot_hash> --approved-by alice,bob --vault-passphrase "$MIZAN_VAULT_PASSPHRASE"
go run ./cmd/mizan secret list
go run ./cmd/mizan monitor snapshot --project <id>
go run ./cmd/mizan monitor stream --project <id> --limit 10 --interval 5s
```

## Build

With `make`:

```sh
make ui
make binary
```

On Windows without `make`:

```powershell
cd webui
npm install
npm run build
cd ..
Remove-Item -Recurse -Force internal/server/dist
Copy-Item -Recurse webui/dist internal/server/dist
go build -o dist/mizan.exe ./cmd/mizan
```

Container image:

```sh
make ui
docker build --target runtime -t mizan:local .
docker run --rm -p 127.0.0.1:7890:7890 -v mizan-data:/var/lib/mizan -e MIZAN_AUTH_TOKEN=change-me mizan:local
```

The default container target is the minimal UI/API runtime and intentionally excludes `openssh-client`. Build the SSH-capable runtime when the container itself must execute remote deployments:

```sh
docker build --target runtime-ssh -t mizan:ssh-local .
```

Deployment examples live under `deploy/`:

- `deploy/systemd/mizan.service`
- `deploy/nginx/mizan.conf`

## Test and Coverage

Backend:

```sh
go test -coverprofile dist/coverage.out ./...
go tool cover -func dist/coverage.out
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
```

Frontend:

```sh
cd webui
npm run lint
npm run test:coverage
npx playwright install chromium
npm run test:e2e
npm run build
npm audit --audit-level=low
```

Container high/critical gate:

```sh
make container-scan
```

Full release gate:

```sh
make release-check
```

Current verified gates:

| Area | Status |
|---|---:|
| Go test pass rate | 100% |
| Go total statement coverage | 96.7% |
| Frontend core statement coverage | 100% |
| Frontend core function coverage | 100% |
| Frontend core line coverage | 100% |
| Frontend core branch coverage | 95.89% |
| Browser E2E workflow | Playwright Chromium pass: import, edit, validate, batch approval, rollback dry-run, audit, monitor |
| Full npm audit | 0 vulnerabilities |
| Go vulnerability scan | govulncheck pass: 0 vulnerabilities |
| Container high/critical scan | Anchore/Grype CI gate pass; Docker Scout local gate pass |

Frontend coverage is scoped to `webui/src/lib/**/*.ts` in `webui/vitest.config.ts`; backend coverage is measured across `./...`.

## Architecture

See [ARCHITECTURE.md](ARCHITECTURE.md) for the current architecture, request flows, storage layout, coverage status, and Mermaid diagrams.

For production hardening, release gates, and supported scope boundaries, see [docs/PRODUCTION.md](docs/PRODUCTION.md).
