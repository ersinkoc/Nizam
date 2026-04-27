# Staging Deployment Drills

Use these drills before the first production rollout and after meaningful deploy-path changes. They validate real target behavior that `mizan deploy drill` intentionally simulates locally.

## Preconditions

- Use disposable staging HAProxy/Nginx targets, never production targets.
- Keep `gate_on_failure` enabled on the staging cluster.
- Configure at least one target with `rollback_command`.
- Configure `post_reload_probe` or `monitor_endpoint` for every target.
- Snapshot or back up remote config files before starting.
- Run `mizan deploy drill --summary` locally first and require `status: success`.

Example rollback commands:

```sh
cp /etc/haproxy/haproxy.cfg.bak /etc/haproxy/haproxy.cfg && systemctl reload haproxy
cp /etc/nginx/nginx.conf.bak /etc/nginx/nginx.conf && systemctl reload nginx
```

## Drill 1: Remote Validation Failure

Goal: prove invalid generated or staged config does not install or reload, and remote temp cleanup runs.

1. Point a staging target at a remote validation command that will fail, or temporarily make the candidate config invalid in a disposable project.
2. Run a dry-run deploy and record `snapshot_hash`.
3. Execute the target deploy with `--confirm-snapshot`.
4. Confirm result status is `failed`.
5. Confirm `remote_validate` is `failed`.
6. Confirm `install` and `reload` are absent.
7. Confirm `cleanup.succeeded == 1`.
8. Confirm the active HAProxy/Nginx config is unchanged.

## Drill 2: Install Or Reload Failure

Goal: prove rollback runs after a failed install or reload.

1. Configure a staging target with a valid `rollback_command`.
2. Temporarily set the target reload command to a failing command such as `false`.
3. Dry-run and then execute with the matching snapshot hash.
4. Confirm result status is `failed`.
5. Confirm `reload` or `install` is `failed`.
6. Confirm `rollback.attempted == 1`.
7. Confirm `rollback.succeeded == 1`.
8. Confirm `cleanup.succeeded == 1`.
9. Confirm the service is running the known-good config.

## Drill 3: Post-Reload Probe Failure

Goal: prove rollback runs when reload succeeds but health verification fails.

1. Configure `post_reload_probe` to a staging endpoint that returns a non-2xx/non-3xx response.
2. Dry-run and then execute with the matching snapshot hash.
3. Confirm `reload` is `success`.
4. Confirm `probe` is `failed`.
5. Confirm `rollback.succeeded == 1`.
6. Confirm `cleanup.succeeded == 1`.
7. Restore the valid probe URL and verify `mizan monitor snapshot --project <id>`.

## Drill 4: Cleanup Failure Signal

Goal: prove stale temp cleanup failures are visible and treated as incident signals.

1. Use a disposable target where `/tmp/mizan-*.cfg` cleanup can be made to fail safely.
2. Execute a deploy that reaches cleanup.
3. Confirm result status is `failed`.
4. Confirm `cleanup.failed == 1`.
5. Confirm `mizan audit show --project <id> --action deploy.run --cleanup-failed true` returns the event.
6. Manually remove any leftover temp files.

## Exit Criteria

- Local `mizan deploy drill --summary` passes.
- At least one real HAProxy target and one real Nginx target pass applicable staging drills.
- Failed validation does not install or reload.
- Failed install/reload/probe attempts rollback when configured.
- Cleanup failures are visible in deploy result and audit filters.
- Operators can identify the active config and restore it manually without Mizan.

## Evidence To Keep

- Dry-run output with `snapshot_hash`.
- Execute output JSON.
- `mizan audit show --project <id> --action deploy.run --incident true`.
- `mizan monitor snapshot --project <id>`.
- Remote `haproxy -c` or `nginx -t` output after recovery.
