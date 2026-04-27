# Production Readiness

This document tracks what "production ready" means for Mizan and how to operate a hardened instance.

Mizan is local-first and single-binary by design. A production deployment should still be treated as an operator-facing control plane: protect the HTTP surface, back up the data directory, validate generated configs before reloads, and keep an audit trail.

## Release Status

Mizan is release-ready for the documented v0.1 scope:

- Local-first project, IR, snapshot, target, approval, audit, monitor, backup, and secret-vault workflows are implemented.
- WebUI and CLI flows cover create/import, edit, generate, validate, dry-run deploy planning, approvals, monitor snapshots/streams, audit filters, and CSV export.
- CLI `deploy --execute` is implemented through the local `ssh` command with vault-backed username/private-key support and snapshot confirmation.
- The default container is a minimal WebUI/API runtime; the separate `runtime-ssh` image is available when remote deployment execution must happen inside the container.
- CI gates Go tests, frontend lint/coverage/build/E2E, Go/npm vulnerability scans, and high/critical container CVEs for both runtime images.

The boundaries below are intentional v0.1 scope limits, not open blockers for the current release.

## Runtime Baseline

Recommended serve command:

```sh
mizan serve \
  --bind 127.0.0.1:7890 \
  --home /var/lib/mizan \
  --max-body-bytes 10485760 \
  --shutdown-timeout 10s
```

When binding outside localhost, authentication is required:

```sh
MIZAN_AUTH_TOKEN="$(openssl rand -base64 32)" \
mizan serve --bind 0.0.0.0:7890 --home /var/lib/mizan
```

For internet-facing use, put Mizan behind a TLS-terminating reverse proxy and keep the Mizan process bound to localhost or a private interface. The built-in HTTP server is hardened for local/private operation, but it does not terminate TLS.

## Deployment Options

Systemd example:

```sh
sudo useradd --system --create-home --home-dir /var/lib/mizan --shell /usr/sbin/nologin mizan
sudo install -o root -g root -m 0755 dist/mizan /usr/local/bin/mizan
sudo install -o root -g root -m 0644 deploy/systemd/mizan.service /etc/systemd/system/mizan.service
sudo install -d -o root -g root -m 0750 /etc/mizan
sudo install -d -o mizan -g mizan -m 0750 /var/lib/mizan
sudo systemctl daemon-reload
sudo systemctl enable --now mizan
```

Optional `/etc/mizan/mizan.env`:

```sh
MIZAN_BIND=127.0.0.1:7890
MIZAN_HOME=/var/lib/mizan
MIZAN_AUTH_TOKEN=replace-with-secret-manager-value
MIZAN_MAX_BODY_BYTES=10485760
MIZAN_SHUTDOWN_TIMEOUT=10s
```

Container example:

```sh
make ui
docker build --target runtime -t mizan:local .
docker run -d --name mizan \
  -p 127.0.0.1:7890:7890 \
  -v mizan-data:/var/lib/mizan \
  -e MIZAN_AUTH_TOKEN=replace-with-secret-manager-value \
  mizan:local
```

The default `runtime` image is meant for the WebUI/API control plane and does not include `openssh-client`. If operators need the container to run `mizan deploy --execute`, build and run the deploy-capable target instead:

```sh
docker build --target runtime-ssh -t mizan:ssh-local .
```

### Nginx TLS Reverse Proxy

The sample Nginx TLS reverse proxy is `deploy/nginx/mizan.conf`. Keep the Mizan listener private and terminate TLS at Nginx or another edge proxy.

Example install flow on a Debian-style host:

```sh
sudo apt-get update
sudo apt-get install -y nginx certbot python3-certbot-nginx
sudo certbot certonly --nginx -d mizan.example.com
sudo install -o root -g root -m 0644 deploy/nginx/mizan.conf /etc/nginx/sites-available/mizan.conf
sudo ln -sf /etc/nginx/sites-available/mizan.conf /etc/nginx/sites-enabled/mizan.conf
sudo nginx -t
sudo systemctl reload nginx
```

Before enabling it, replace `mizan.example.com` and the certificate paths in the sample config. The config sets HSTS, forwards the original host/protocol headers, keeps upgrade support for streaming endpoints, and disables proxy buffering so SSE monitor/audit streams reach the browser promptly.

## Hardened Defaults

The HTTP server applies:

- Bearer or Basic auth for non-loopback binds
- `/healthz` liveness and `/readyz` readiness; readiness verifies the configured data root is accessible
- Security headers for all responses
- `Cache-Control: no-store` for API, health, metrics, and version responses
- 10 MiB default request body limit, configurable with `--max-body-bytes`
- Read header, read, and idle timeouts
- 1 MiB maximum request headers
- Panic recovery with sanitized 500 responses
- Graceful shutdown on interrupt or SIGTERM

## Data Protection

Back up the configured `--home` directory. It contains:

- `projects/<id>/project.json`
- `projects/<id>/config.json`
- `projects/<id>/targets.json`
- `projects/<id>/snapshots/`
- `projects/<id>/snapshot-tags.json`
- `projects/<id>/audit.jsonl`
- `secrets/` encrypted vault envelopes

Recommended backup policy before production use:

- Snapshot `/var/lib/mizan` before every rollout window
- Store backups on encrypted storage
- Test restore into a separate temporary `--home` directory
- Export important projects with `mizan project export <id> --out <file>`

Full home backup commands:

```sh
mizan backup create --home /var/lib/mizan --out mizan-backup.zip
mizan backup inspect --in mizan-backup.zip
mizan backup restore --in mizan-backup.zip --home /tmp/mizan-restore
```

`backup restore` verifies every file against the archive manifest SHA-256 and size, and refuses to write into a non-empty home directory unless `--force` is provided. Use `--force` only after confirming the destination is disposable or already snapshotted.

Vault files are encrypted, but the vault passphrase is still operationally sensitive. Prefer environment injection from a secret manager over shell history or static scripts.

## Deployment Safety Gates

Use this minimum gate before executing remote deployment:

```sh
mizan doctor --home /var/lib/mizan
mizan generate --project <id> --target haproxy --out candidate.cfg
mizan validate --project <id> --target haproxy
mizan deploy --project <id> --target-id <target-id>
mizan deploy --project <id> --cluster-id <cluster-id> --batch 1
mizan approval request --project <id> --cluster-id <cluster-id> --batch 1
mizan approval approve --project <id> --actor alice <approval-request-id>
mizan approval approve --project <id> --actor bob <approval-request-id>
mizan deploy --project <id> --approval-request-id <approval-request-id> --execute
mizan deploy --project <id> --cluster-id <cluster-id> --batch 1 --execute --confirm-snapshot <snapshot_hash> --approved-by alice,bob
mizan monitor snapshot --project <id>
```

Use the `snapshot_hash` from the dry-run deployment result as the `--confirm-snapshot` value. If the project changes between preview and execution, Mizan rejects the execute request and forces a fresh dry run.

For clusters, start with `--batch 1`, confirm probe and monitor health, then proceed through later batches. Keep `gate_on_failure` enabled for production clusters. Set `--required-approvals 2` or higher on production clusters, then either pass distinct `--approved-by` names when executing or persist a snapshot-bound approval request under `approvals.json` with the API, CLI, or WebUI. Approved requests let deploy execution reference an `approval_request_id` / `--approval-request-id` instead of sending approvers inline.

Recommended rollout loop:

1. Dry-run the exact target or cluster batch and inspect the generated steps.
2. Confirm the dry-run contains the expected rollback step when `rollback_command` is configured.
3. Create an approval request for the same batch and collect the required actors.
4. Execute with `--approval-request-id` or `approval_request_id` only after the request is approved.
5. Run `mizan monitor snapshot --project <id>` after each batch before approving the next batch.
6. Stop the rollout if monitor health regresses, audit entries look unexpected, or a rollback step appears in an executed run.

For incident triage, audit queries can be narrowed to operational metadata such as `target_id`, `cluster_id`, `approval_request_id`, rollout `batch`, `dry_run`, `incident`, and `rollback_failed`. The same filters are available through the API, CSV export, and `mizan audit show`.

For targets that can restore a known-good config, set a `rollback_command`. Mizan runs it after a failed install, reload, or post-reload probe and records the rollback as a deployment step:

```sh
mizan target add \
  --project <id> \
  --name edge-01 \
  --host 10.0.0.10 \
  --engine haproxy \
  --rollback-command 'cp /etc/haproxy/haproxy.cfg.bak /etc/haproxy/haproxy.cfg && systemctl reload haproxy'
```

Deployment results and `deploy.run` audit metadata include a rollback summary:

- `planned`: rollback steps present in the rollout plan, including dry-run-only rollback steps.
- `attempted`: rollback commands actually executed during a non-dry-run deployment.
- `succeeded`: rollback commands that exited successfully.
- `failed`: rollback commands that were attempted but failed.

Treat `rollback.failed > 0` as an incident signal. Inspect the per-step command output, confirm the active HAProxy/Nginx config on the target, and pause later batches until the target is manually recovered.

## CI and Release Gates

Production releases should pass:

```sh
make release-check
```

`make release-check` runs backend coverage, frontend coverage, browser E2E, Go/npm vulnerability scans, the embedded binary build, and high/critical Docker Scout gates for both runtime images. If Docker is unavailable on a local workstation, run the non-container gates directly and rely on CI's Anchore/Grype image scan.

CI fails the container job on critical or high CVEs for both `runtime` and `runtime-ssh`. Medium findings remain visible in the scanner output so operators can track base-image remediation without blocking routine builds.

The release workflow runs when a `v*` tag is pushed, and it can also be started manually from GitHub Actions. Tag-triggered releases build cross-platform binaries, embed the release version/commit/date metadata, upload build artifacts, and publish a GitHub Release containing each binary plus its SHA-256 checksum, keyless Sigstore signature, and signing certificate. Before tagging a release, verify the generated binary embeds the current WebUI and returns the expected `/version` metadata.

## Supported Scope Boundaries

These are deliberate v0.1 boundaries:

- HAProxy/Nginx import targets the documented v0 directive subset. Advanced vendor-specific directives may need manual IR editing after import.
- Approval identity is operator-supplied and audit-recorded. External SSO/RBAC integration is out of scope for v0.1.
- SSH execution delegates password/passphrase behavior to the local SSH environment, agent, or config. Mizan stores vault-backed usernames and private keys, not interactive passwords.
- Browser E2E covers the main operator workflow. Deep fault-injection scenarios, such as real remote rollback command failure, should be validated in an environment-specific staging setup before first production rollout.
