# Mizan Architecture

Mizan is a local-first visual configuration architect for HAProxy and Nginx. It is built as a Go single-binary application with an embedded React/Vite WebUI. The current implementation is a working foundation: projects can be created or imported, persisted as JSON, edited as IR, visualized as topology, generated into HAProxy/Nginx config, validated, snapshotted, tagged, diffed, reverted, audited, associated with deployment targets or clusters, and previewed as a dry-run deployment plan.

## Current Project Status

```mermaid
flowchart LR
  Foundation["Foundation\nGo CLI + HTTP server\nEmbedded React UI"]:::done
  IR["Universal IR\nLint, mutate, hash\nSnapshots"]:::done
  Import["Import\nBasic HAProxy/Nginx parser"]:::done
  Generate["Generate\nHAProxy + Nginx translators"]:::done
  Validate["Validate\nStructural + native binary wrapper"]:::done
  Topology["Topology\nReact Flow graph\nDrag/connect updates IR"]:::done
  Audit["Audit\nAppend-only audit.jsonl\nWebUI panel"]:::done
  Targets["Target Registry\nTargets + clusters\nWebUI panel"]:::done
  DeployPlan["Deploy Plan\nDry-run rollout steps\nAudit event"]:::done
  Deploy["SSH Deploy\nNot implemented yet"]:::todo
  Monitor["Live Monitoring\nNot implemented yet"]:::todo

  Foundation --> IR --> Import --> Generate --> Validate
  IR --> Topology
  IR --> Audit
  Audit --> Targets
  Targets --> DeployPlan
  DeployPlan --> Deploy
  Validate --> Deploy
  Deploy --> Monitor

  classDef done fill:#dff7ea,stroke:#2b8a57,color:#153b27;
  classDef todo fill:#fff3cd,stroke:#a36b00,color:#4a3100;
```

## High-Level Shape

```mermaid
flowchart TB
  User["Operator / Engineer"]
  Browser["Browser\nReact WebUI"]
  CLI["mizan CLI"]
  Server["Go HTTP Server\nnet/http ServeMux"]
  API["REST API Handlers"]
  IR["Universal IR\npure Go model"]
  Store["Filesystem Store\n~/.mizan/projects"]
  Translators["Translators\nHAProxy / Nginx"]
  Validate["Validation Pipeline\nlint + optional native check"]
  Embedded["Embedded UI\ninternal/server/dist"]

  User --> Browser
  User --> CLI
  Browser --> Server
  CLI --> Store
  CLI --> Translators
  CLI --> Validate
  Server --> API
  Server --> Embedded
  API --> IR
  API --> Store
  API --> Translators
  API --> Validate
  Validate --> Translators
  Store --> IR
```

Mizan intentionally avoids a database and a backend web framework. The backend uses Go standard library primitives where practical, with the filesystem as the durable source of truth.

## Repository Layout

```mermaid
flowchart TB
  Root["repo root"]
  Cmd["cmd/mizan\nentrypoint"]
  API["internal/api\nREST handlers"]
  CLI["internal/cli\ncobra-free CLI"]
  IR["internal/ir\nmodel, lint, mutate, parser"]
  Store["internal/store\nJSON files, snapshots, audit"]
  Server["internal/server\nHTTP bootstrap, embedded SPA"]
  Translate["internal/translate\nhaproxy, nginx"]
  Validate["internal/validate\nlint + native checks"]
  Version["internal/version\nbuild metadata"]
  WebUI["webui\nReact + Vite + TypeScript"]
  Docs["docs + .project\nplans and coverage notes"]

  Root --> Cmd
  Root --> API
  Root --> CLI
  Root --> IR
  Root --> Store
  Root --> Server
  Root --> Translate
  Root --> Validate
  Root --> Version
  Root --> WebUI
  Root --> Docs
```

## Backend Dependency Direction

```mermaid
flowchart TD
  Main["cmd/mizan"]
  CLI["internal/cli"]
  Server["internal/server"]
  API["internal/api"]
  Store["internal/store"]
  Validate["internal/validate"]
  Translate["internal/translate/*"]
  IR["internal/ir"]
  Parser["internal/ir/parser"]
  Version["internal/version"]

  Main --> CLI
  CLI --> Server
  CLI --> Store
  CLI --> Validate
  CLI --> Translate
  CLI --> Parser
  Server --> API
  Server --> Store
  API --> Store
  API --> IR
  API --> Parser
  API --> Validate
  API --> Version
  Validate --> IR
  Validate --> Translate
  Translate --> IR
  Parser --> IR
  Store --> IR
```

The IR package is the core domain layer. Translators, validation, storage, API, CLI, and WebUI all orbit around it.

## Universal IR

The IR is the canonical configuration model. The UI, topology, generators, validators, snapshots, and audit trail all refer to this model.

```mermaid
erDiagram
  MODEL ||--o{ FRONTEND : contains
  MODEL ||--o{ BACKEND : contains
  MODEL ||--o{ SERVER : contains
  MODEL ||--o{ RULE : contains
  MODEL ||--o{ TLS_PROFILE : contains
  MODEL ||--o{ HEALTH_CHECK : contains
  MODEL ||--o{ CACHE_POLICY : contains
  MODEL ||--o{ LOGGER : contains
  FRONTEND }o--|| TLS_PROFILE : uses
  FRONTEND }o--o{ RULE : orders
  FRONTEND }o--|| BACKEND : default_backend
  RULE }o--|| BACKEND : use_backend
  BACKEND }o--o{ SERVER : members
  BACKEND }o--|| HEALTH_CHECK : uses

  MODEL {
    int version
    string id
    string name
    string[] engines
  }
  FRONTEND {
    string id
    string bind
    string protocol
    string tls_id
  }
  BACKEND {
    string id
    string algorithm
    string[] servers
  }
  SERVER {
    string id
    string address
    int port
    int weight
  }
  RULE {
    string id
    string predicate
    string action
  }
```

## Request Flow

```mermaid
sequenceDiagram
  participant Browser as React WebUI
  participant API as Go API Handler
  participant Store as Store
  participant IR as IR Logic
  participant Audit as audit.jsonl

  Browser->>API: PATCH /api/v1/projects/{id}/ir
  API->>Store: GetIR(projectID)
  Store-->>API: current model + version
  API->>IR: Apply mutation or accept full IR
  API->>IR: Lint(next model)
  alt lint has errors
    API-->>Browser: 422 issues
  else valid
    API->>Store: SaveIR(projectID, next, If-Match)
    Store->>Store: write config.json atomically
    Store->>Store: write snapshot
    API->>Audit: append ir.patch event
    API-->>Browser: next IR + new version
  end
```

## Import Flow

```mermaid
flowchart LR
  Paste["Pasted config\nor CLI file path"]
  Detect["Format detection\nfilename + content"]
  HAProxyParser["HAProxy parser\nline-oriented sections"]
  NginxParser["Nginx parser\nbrace-ish block scan"]
  IR["Universal IR"]
  Store["Project directory\nconfig.json + snapshot"]
  UI["WebUI\nIR editor + topology"]

  Paste --> Detect
  Detect -->|frontend/backend| HAProxyParser
  Detect -->|http/upstream/conf| NginxParser
  HAProxyParser --> IR
  NginxParser --> IR
  IR --> Store
  Store --> UI
```

Current parser coverage is intentionally basic. It handles the core v0 subset: frontends/listeners, backends/upstreams, servers, TLS certificate paths, default backends, simple ACL/location routing, weights, and basic health checks.

## Generation and Validation

```mermaid
flowchart TD
  IR["Universal IR"]
  Lint["Structural lint\nrefs, ports, TLS, empty pools"]
  Target{"Target"}
  HGen["HAProxy translator"]
  NGen["Nginx translator"]
  Config["Generated config\nwith source map"]
  Native{"Native binary exists?"}
  NativeCheck["haproxy -c / nginx -t"]
  Result["ValidateResult\nissues + config + native status"]

  IR --> Lint
  IR --> Target
  Target -->|haproxy| HGen
  Target -->|nginx| NGen
  HGen --> Config
  NGen --> Config
  Config --> Native
  Native -->|yes| NativeCheck
  Native -->|no| Result
  NativeCheck --> Result
  Lint --> Result
```

The current native validation wrapper is opportunistic: if `haproxy` or `nginx` is not on `PATH`, validation is marked as skipped rather than crashing the workflow.

## Deployment Dry Run

```mermaid
sequenceDiagram
  participant Browser as React WebUI
  participant API as POST /deploy
  participant Deploy as internal/deploy
  participant Store as Store
  participant Translate as Generator
  participant Audit as audit.jsonl

  Browser->>API: target_id or cluster_id, dry_run=true
  API->>Deploy: Run(project, target selection)
  Deploy->>Store: GetIR + ListTargets
  Deploy->>Translate: Generate config per target engine
  Deploy-->>API: rollout steps
  API->>Audit: append deploy.run
  API-->>Browser: dry-run plan
```

The deploy package now computes the concrete rollout steps for one target or a cluster: upload generated config, remote validate, install, reload, and optional post-reload probe. The WebUI currently invokes this as a dry run. The same backend path has command-runner and probe hooks for future real SSH execution.

The same flow is exposed from the CLI:

```sh
mizan deploy --project <id> --target-id <target-id>
mizan deploy --project <id> --cluster-id <cluster-id>
```

Targets and clusters can also be managed from the CLI:

```sh
mizan target add --project <id> --name edge-01 --host 10.0.0.10 --engine haproxy
mizan target list --project <id>
mizan cluster add --project <id> --name prod --target-ids <target-id>
mizan cluster list --project <id>
```

CLI deploy defaults to dry-run planning. Passing `--execute` switches to the real command runner.

## Topology Editing

```mermaid
flowchart TB
  IR["Active IR"]
  Build["buildTopology(model, issues)"]
  Nodes["React Flow nodes"]
  Edges["React Flow edges"]
  Drag["Drag node"]
  Connect["Connect nodes"]
  Mutate["Frontend/backend/rule/server mutation"]
  Save["PATCH /ir"]

  IR --> Build
  Build --> Nodes
  Build --> Edges
  Nodes --> Drag
  Edges --> Connect
  Drag --> Mutate
  Connect --> Mutate
  Mutate --> Save
  Save --> IR
```

Supported topology mutations:

| Gesture | IR effect |
|---|---|
| Drag frontend/backend/rule | Persists `view.x` / `view.y` |
| Connect frontend to backend | Sets `frontend.default_backend` |
| Connect rule to backend | Sets `rule.action.backend_id` |
| Connect frontend to rule | Adds rule ID to frontend rule order |
| Connect backend to server | Adds server ID to backend members |

## Storage Layout

```mermaid
flowchart TB
  Home["~/.mizan"]
  State["state.json\nfuture UI prefs"]
  Projects["projects/"]
  Project["<project-id>/"]
  Meta["project.json"]
  Config["config.json\ncurrent IR"]
  Targets["targets.json\ndeploy targets + clusters"]
  Snapshots["snapshots/"]
  Snapshot["timestamp-hash.json"]
  Tags["snapshot-tags.json"]
  Audit["audit.jsonl"]

  Home --> State
  Home --> Projects
  Projects --> Project
  Project --> Meta
  Project --> Config
  Project --> Targets
  Project --> Snapshots
  Snapshots --> Snapshot
  Project --> Tags
  Project --> Audit
```

Writes use temp-file + fsync + rename for the core JSON files. Audit events are append-only JSON Lines.

## Snapshot and Audit Flow

```mermaid
sequenceDiagram
  participant API
  participant Store
  participant Snapshot as snapshots/
  participant Tags as snapshot-tags.json
  participant Audit as audit.jsonl

  API->>Store: SaveIR()
  Store->>Store: write config.json
  Store->>Snapshot: write timestamp-hash.json
  API->>Audit: append ir.patch

  API->>Store: TagSnapshot(ref, label)
  Store->>Tags: upsert label -> ref
  API->>Audit: append snapshot.tag

  API->>Store: RevertSnapshot(ref)
  Store->>Snapshot: read snapshot
  Store->>Store: save as current IR
  API->>Audit: append snapshot.revert
```

## WebUI State Model

```mermaid
flowchart LR
  APIClient["api/client.ts"]
  App["App.tsx\nlocal state"]
  IRDraft["IR JSON draft"]
  GeneratePanel["Generate / Validate panel"]
  Topology["TopologyCanvas"]
  Snapshots["Snapshots panel"]
  Targets["Targets / clusters panel"]
  Audit["Audit panel"]

  APIClient --> App
  App --> IRDraft
  App --> GeneratePanel
  App --> Topology
  App --> Snapshots
  App --> Targets
  App --> Audit
  Topology -->|move/connect| App
  IRDraft -->|Save| APIClient
  GeneratePanel -->|Generate/Validate| APIClient
```

The frontend currently uses local React state and a small typed API client. A later phase can introduce TanStack Query/Zustand as originally planned, but the current app is intentionally simple and testable.

## Build and Runtime Packaging

```mermaid
flowchart LR
  WebSrc["webui/src"]
  Vite["npm run build"]
  WebDist["webui/dist"]
  EmbedDist["internal/server/dist"]
  GoBuild["go build ./cmd/mizan"]
  Binary["dist/mizan.exe"]
  Serve["mizan serve"]
  Browser["http://127.0.0.1:7890"]

  WebSrc --> Vite --> WebDist
  WebDist --> EmbedDist
  EmbedDist --> GoBuild
  GoBuild --> Binary
  Binary --> Serve --> Browser
```

## Test and Coverage Status

```mermaid
flowchart TB
  Tests["Automated tests"]
  Go["Go test suite\nPASS"]
  FE["Frontend Vitest\nPASS"]
  FECoverage["Frontend core coverage\n100% statements\n100% functions\n100% lines\n95.58% branches"]
  GoCoverage["Go total coverage\n100.0% statements"]

  Tests --> Go
  Tests --> FE
  FE --> FECoverage
  Go --> GoCoverage
```

Current verified commands:

```sh
go test -coverprofile dist/coverage.out ./...
go tool cover -func dist/coverage.out
npm run lint
npm run test:coverage
npm run build
npm audit --omit=dev
```

Current coverage state:

| Area | Status |
|---|---:|
| Go test pass rate | 100% |
| Go total statement coverage | 100.0% |
| Frontend core statement coverage | 100% |
| Frontend core branch coverage | 95.58% |
| Frontend core function coverage | 100% |
| Frontend core line coverage | 100% |
| Production dependency audit | 0 vulnerabilities |

## Implemented Capabilities

```mermaid
mindmap
  root((Mizan v0 Current))
    Projects
      create
      list
      delete
      import
    IR
      lint
      mutate
      hash
      canonical JSON
    Storage
      config.json
      snapshots
      tags
      audit.jsonl
      targets.json
    Deployment
      dry-run plan
      target selection
      cluster batches
      audit event
    Snapshot Diff
      structural tree
      added removed modified
    Generation
      HAProxy
      Nginx
    Validation
      structural lint
      native wrapper
      skipped if binary missing
    WebUI
      IR editor
      generate panel
      validate panel
      topology
      snapshots
      diff
      targets
      clusters
      deploy preview
      audit
    CLI
      serve
      project
      snapshot
      target
      cluster
      generate
      validate
      deploy
```

## Not Implemented Yet

```mermaid
flowchart LR
  SSH["SSH deployment"]
  Rollout["Cluster rollout orchestration"]
  Secrets["Encrypted secrets vault"]
  Monitor["Live HAProxy/Nginx monitoring"]
  SSE["SSE project/monitor streams"]
  FullParser["Full round-trip parsers"]
  Wizard["Full wizard UI"]
  DiffUI["Rich snapshot diff UI"]

  SSH --> Rollout
  Secrets --> SSH
  Monitor --> SSE
  FullParser --> Wizard
  DiffUI --> Wizard
```

The codebase is now a working product foundation, not yet a full v1 implementation. Target and cluster persistence plus dry-run deployment planning exist, but real SSH execution, credential handling, and staged rollout safety gates are still future work. The next largest architectural slices are deployment execution, monitoring, full parser round-trip, richer wizard editing, and deeper diff UI.

## Design Principles

- **Local-first**: user data lives under `~/.mizan`.
- **Single binary**: Go backend embeds the built React UI.
- **IR-centered**: all editing, topology, validation, generation, snapshots, and audit are derived from the same model.
- **No database**: project state is JSON, easy to inspect and Git-version.
- **Pure translators**: target configs are deterministic outputs of the IR.
- **Append-only audit**: project history is observable and never rewritten.
- **Progressive hardening**: backend statement coverage is currently 100%, while frontend core library coverage is gated at 100% statements, functions, and lines.
