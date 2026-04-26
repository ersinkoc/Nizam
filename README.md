# Mizan

Mizan is a local-first visual configuration architect for HAProxy and Nginx. It runs as a single Go binary, serves an embedded React/Vite WebUI, stores projects as inspectable JSON under `~/.mizan`, and translates one Universal IR into target-specific HAProxy or Nginx configuration.

The current codebase is a working product foundation with backend statement coverage at 100%.

## What Works Today

- Single-binary Go CLI and HTTP server: `mizan serve`
- Embedded React WebUI with project creation, import, IR editing, generation, validation, snapshots, diff, audit, topology, and deployment-target panels
- Project CRUD and filesystem persistence under `~/.mizan/projects`
- Universal IR with structural linting, deterministic hashes, mutations, canonical JSON, and structural diffs
- HAProxy and Nginx import for the supported v0 directive subset
- HAProxy and Nginx generation from the shared IR
- Validation pipeline with IR linting plus optional native `haproxy -c` / `nginx -t` checks when binaries exist on `PATH`
- Snapshots, tags, snapshot retrieval, revert, and diff
- Append-only project audit log in `audit.jsonl`
- Deployment targets and clusters persisted in `targets.json`
- Deployment dry-run planning for single targets or clusters via generated rollout steps
- Monitor snapshots for registered targets through API, CLI, and WebUI
- Monitor SSE stream endpoint for repeated target health snapshots
- WebUI monitor panel consumes the live SSE stream while a project is open
- Project audit events can stream to WebUI over SSE
- HAProxy monitor endpoint support for `show stat` CSV health summaries
- Nginx monitor endpoint support for OSS `stub_status` connection summaries
- Topology canvas with drag/connect gestures that update the IR

## Run

```sh
go run ./cmd/mizan serve --bind 127.0.0.1:7890
```

Open `http://127.0.0.1:7890`.

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
go run ./cmd/mizan project list
go run ./cmd/mizan snapshot list --project <id>
go run ./cmd/mizan snapshot tag --project <id> --label release-1 <snapshot-ref>
go run ./cmd/mizan target add --project <id> --name edge-01 --host 10.0.0.10 --engine haproxy --monitor-endpoint 'http://10.0.0.10:8404/;csv'
go run ./cmd/mizan cluster add --project <id> --name prod --target-ids <target-id>
go run ./cmd/mizan generate --project <id> --target haproxy
go run ./cmd/mizan validate --project <id> --target nginx
go run ./cmd/mizan deploy --project <id> --target-id <target-id>
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

## Test and Coverage

Backend:

```sh
go test -coverprofile dist/coverage.out ./...
go tool cover -func dist/coverage.out
```

Frontend:

```sh
cd webui
npm run lint
npm run test:coverage
npm run build
npm audit --omit=dev
```

Current verified gates:

| Area | Status |
|---|---:|
| Go test pass rate | 100% |
| Go total statement coverage | 100.0% |
| Frontend core statement coverage | 100% |
| Frontend core function coverage | 100% |
| Frontend core line coverage | 100% |
| Frontend core branch coverage | 95.58% |
| Production dependency audit | 0 vulnerabilities |

Frontend coverage is scoped to `webui/src/lib/**/*.ts` in `webui/vitest.config.ts`; backend coverage is measured across `./...`.

## Architecture

See [ARCHITECTURE.md](ARCHITECTURE.md) for the current architecture, request flows, storage layout, coverage status, and Mermaid diagrams.
