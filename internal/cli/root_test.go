package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mizanproxy/mizan/internal/secrets"
	"github.com/mizanproxy/mizan/internal/store"
)

func TestRunVersionAndUnknown(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), []string{"version"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("mizan")) {
		t.Fatalf("unexpected version output: %s", stdout.String())
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"version", "--json"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var metadata map[string]string
	if err := json.Unmarshal(stdout.Bytes(), &metadata); err != nil {
		t.Fatalf("version --json output is not JSON: %v\n%s", err, stdout.String())
	}
	if metadata["version"] == "" || metadata["commit"] == "" || metadata["date"] == "" {
		t.Fatalf("missing version metadata: %#v", metadata)
	}
	if err := Run(context.Background(), []string{"nope"}, &stdout, &stderr); err == nil {
		t.Fatal("expected unknown command error")
	}
}

func TestServeInvalidBindReturnsError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), []string{"serve", "--home", t.TempDir(), "--bind", "bad address", "--auth-token", "secret"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected serve error for invalid bind")
	}
}

func TestRunDefaultServeStopsCleanly(t *testing.T) {
	oldListenAndServe := listenAndServe
	t.Cleanup(func() { listenAndServe = oldListenAndServe })
	listenAndServe = func(*http.Server) error {
		return http.ErrServerClosed
	}
	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), nil, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("Mizan serving")) {
		t.Fatalf("unexpected serve output: %s", stdout.String())
	}
}

func TestServeAuthConfiguration(t *testing.T) {
	oldListenAndServe := listenAndServe
	t.Cleanup(func() { listenAndServe = oldListenAndServe })
	listenAndServe = func(*http.Server) error {
		return http.ErrServerClosed
	}
	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), []string{"serve", "--home", t.TempDir(), "--bind", "0.0.0.0:7890"}, &stdout, &stderr); err == nil {
		t.Fatal("expected external bind auth error")
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"serve", "--home", t.TempDir(), "--bind", "0.0.0.0:7890", "--auth-basic", "operator:pass"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIZAN_AUTH_TOKEN", "from-env")
	stdout.Reset()
	if err := Run(context.Background(), []string{"serve", "--home", t.TempDir(), "--bind", "0.0.0.0:7891"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIZAN_AUTH_TOKEN", "")
	t.Setenv("MIZAN_READ_ONLY_TOKEN", "viewer-env")
	stdout.Reset()
	if err := Run(context.Background(), []string{"serve", "--home", t.TempDir(), "--bind", "0.0.0.0:7892"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"serve", "--home", t.TempDir(), "--auth-token", "same", "--read-only-token", "same"}, &stdout, &stderr); err == nil {
		t.Fatal("expected duplicate auth token error")
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"serve", "--home", t.TempDir(), "--max-body-bytes", "1024", "--shutdown-timeout", "1s"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"serve", "--home", t.TempDir(), "--auth-basic", "bad"}, &stdout, &stderr); err == nil {
		t.Fatal("expected bad basic credential error")
	}
}

func TestProjectGenerateValidateAndSnapshotCommands(t *testing.T) {
	home := t.TempDir()
	var stdout, stderr bytes.Buffer
	statusServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("Active connections: 1\nserver accepts handled requests\n1 1 2\nReading: 0 Writing: 1 Waiting: 0\n"))
	}))
	defer statusServer.Close()
	if err := Run(context.Background(), []string{"project", "new", "--home", home, "--name", "edge", "--engines", "haproxy,nginx"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var created struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Project.ID == "" {
		t.Fatal("missing project id")
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"project", "list", "--home", home}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("edge")) {
		t.Fatalf("project list missing edge: %s", stdout.String())
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"generate", "--home", home, "--project", created.Project.ID, "--target", "haproxy"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("Generated by Mizan")) {
		t.Fatalf("generate output unexpected: %s", stdout.String())
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"validate", "--home", home, "--project", created.Project.ID, "--target", "haproxy"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"target":"haproxy"`)) {
		t.Fatalf("validate output unexpected: %s", stdout.String())
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"target", "add", "--home", home, "--project", created.Project.ID, "--name", "edge-01", "--host", "lb1.example.com", "--engine", "haproxy", "--sudo", "--post-reload-probe", "https://edge.example.com/healthz", "--monitor-endpoint", "https://edge.example.com/haproxy?stats;csv"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var target store.Target
	if err := json.Unmarshal(stdout.Bytes(), &target); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"target", "add", "--home", home, "--project", created.Project.ID, "--id", target.ID, "--name", "edge-01b", "--host", "lb1.example.com", "--engine", "nginx", "--config-path", "/etc/nginx/nginx.conf", "--reload-command", "systemctl reload nginx", "--rollback-command", "cp /etc/nginx/nginx.conf.bak /etc/nginx/nginx.conf && systemctl reload nginx", "--monitor-endpoint", statusServer.URL}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("edge-01b")) || !bytes.Contains(stdout.Bytes(), []byte("rollback_command")) {
		t.Fatalf("target update output unexpected: %s", stdout.String())
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"target", "list", "--home", home, "--project", created.Project.ID}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("edge-01b")) {
		t.Fatalf("target list output unexpected: %s", stdout.String())
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"project", "export", "--home", home, created.Project.ID}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"format_version": 1`)) || !bytes.Contains(stdout.Bytes(), []byte("edge-01b")) {
		t.Fatalf("project export output unexpected: %s", stdout.String())
	}
	exportPath := filepath.Join(t.TempDir(), "mizan-export.json")
	stdout.Reset()
	if err := Run(context.Background(), []string{"project", "export", "--home", home, "--out", exportPath, created.Project.ID}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(exportPath); err != nil || !bytes.Contains(data, []byte(`"targets"`)) {
		t.Fatalf("project export file unexpected data=%q err=%v", string(data), err)
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"cluster", "add", "--home", home, "--project", created.Project.ID, "--name", "prod", "--target-ids", target.ID, "--parallelism", "2", "--required-approvals", "2"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var cluster store.Cluster
	if err := json.Unmarshal(stdout.Bytes(), &cluster); err != nil {
		t.Fatal(err)
	}
	if cluster.RequiredApprovals != 2 {
		t.Fatalf("expected required approvals in cluster: %+v", cluster)
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"approval", "request", "--home", home, "--project", created.Project.ID, "--cluster-id", cluster.ID, "--batch", "1"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var approval store.ApprovalRequest
	if err := json.Unmarshal(stdout.Bytes(), &approval); err != nil {
		t.Fatal(err)
	}
	if approval.Status != store.ApprovalStatusPending || approval.RequiredApprovals != 2 || approval.ClusterID != cluster.ID || approval.Batch != 1 {
		t.Fatalf("approval request output unexpected: %+v", approval)
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"approval", "list", "--home", home, "--project", created.Project.ID}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte(approval.ID)) {
		t.Fatalf("approval list output unexpected: %s", stdout.String())
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"approval", "approve", "--home", home, "--project", created.Project.ID, "--actor", "alice", approval.ID}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"status":"pending"`)) {
		t.Fatalf("first approval output unexpected: %s", stdout.String())
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"approval", "approve", "--home", home, "--project", created.Project.ID, "--actor", "bob", approval.ID}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"status":"approved"`)) || !bytes.Contains(stdout.Bytes(), []byte("bob")) {
		t.Fatalf("second approval output unexpected: %s", stdout.String())
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"project", "export", "--home", home, created.Project.ID}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"approvals"`)) || !bytes.Contains(stdout.Bytes(), []byte(approval.ID)) {
		t.Fatalf("project export approval output unexpected: %s", stdout.String())
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"cluster", "add", "--home", home, "--project", created.Project.ID, "--id", cluster.ID, "--name", "prod-b", "--target-ids", target.ID, "--gate-on-failure=false"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("prod-b")) {
		t.Fatalf("cluster update output unexpected: %s", stdout.String())
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"cluster", "list", "--home", home, "--project", created.Project.ID}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("prod-b")) {
		t.Fatalf("cluster list output unexpected: %s", stdout.String())
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"deploy", "--home", home, "--project", created.Project.ID, "--target-id", target.ID}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"dry_run":true`)) || !bytes.Contains(stdout.Bytes(), []byte(`"target_id":"`+target.ID+`"`)) {
		t.Fatalf("deploy target output unexpected: %s", stdout.String())
	}
	var deployPreview struct {
		SnapshotHash string `json:"snapshot_hash"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &deployPreview); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"deploy", "--home", home, "--project", created.Project.ID, "--cluster-id", cluster.ID}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"cluster_id":"`+cluster.ID+`"`)) {
		t.Fatalf("deploy cluster output unexpected: %s", stdout.String())
	}
	sshDir := t.TempDir()
	oldPath := os.Getenv("PATH")
	t.Cleanup(func() { _ = os.Setenv("PATH", oldPath) })
	script := filepath.Join(sshDir, "ssh")
	body := "#!/bin/sh\ncat >/dev/null\necho fake-ssh \"$@\"\n"
	if runtime.GOOS == "windows" {
		script += ".bat"
		body = "@echo off\r\nmore > nul\r\necho fake-ssh %*\r\n"
	}
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Setenv("PATH", sshDir+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"secret", "set", "--home", home, "--id", target.ID, "--username", "vault-user", "--private-key", "PRIVATE KEY", "--vault-passphrase", "vault-pass"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"deploy", "--home", home, "--project", created.Project.ID, "--target-id", target.ID, "--execute", "--confirm-snapshot", deployPreview.SnapshotHash, "--vault-passphrase", "vault-pass"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"dry_run":false`)) || !bytes.Contains(stdout.Bytes(), []byte(`"status":"success"`)) {
		t.Fatalf("deploy execute output unexpected: %s", stdout.String())
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"deploy", "--home", home, "--project", created.Project.ID, "--approval-request-id", approval.ID, "--execute", "--vault-passphrase", "vault-pass"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"cluster_id":"`+cluster.ID+`"`)) || !bytes.Contains(stdout.Bytes(), []byte(`"dry_run":false`)) || !bytes.Contains(stdout.Bytes(), []byte(`"approved_by":["alice","bob"]`)) {
		t.Fatalf("deploy approval execute output unexpected: %s", stdout.String())
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"deploy", "drill"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"status": "success"`)) || !bytes.Contains(stdout.Bytes(), []byte(`"cleanup_failure_is_incident_signal"`)) {
		t.Fatalf("deploy drill output unexpected: %s", stdout.String())
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"deploy", "drill", "--summary"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"totals"`)) || bytes.Contains(stdout.Bytes(), []byte(`"commands"`)) {
		t.Fatalf("deploy drill summary output unexpected: %s", stdout.String())
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"deploy", "drill", "--format", "text"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("Mizan deploy drill: success")) || !bytes.Contains(stdout.Bytes(), []byte("cleanup attempted=4")) {
		t.Fatalf("deploy drill text output unexpected: %s", stdout.String())
	}
	stdout.Reset()
	drillSummaryPath := filepath.Join(t.TempDir(), "drill-summary.json")
	if err := Run(context.Background(), []string{"deploy", "drill", "--summary", "--out", drillSummaryPath}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("expected deploy drill --out to leave stdout empty, got %s", stdout.String())
	}
	drillSummary, err := os.ReadFile(drillSummaryPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(drillSummary, []byte(`"totals"`)) || bytes.Contains(drillSummary, []byte(`"commands"`)) {
		t.Fatalf("deploy drill summary file unexpected: %s", string(drillSummary))
	}
	drillTextPath := filepath.Join(t.TempDir(), "drill.txt")
	if err := Run(context.Background(), []string{"deploy", "drill", "--format", "text", "--out", drillTextPath}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	drillText, err := os.ReadFile(drillTextPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(drillText, []byte("Mizan deploy drill: success")) || !bytes.Contains(drillText, []byte("rollback attempted=2")) {
		t.Fatalf("deploy drill text file unexpected: %s", string(drillText))
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"deploy", "drill", "verify", "--file", drillSummaryPath}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("Drill evidence verified: status=success scenarios=4")) {
		t.Fatalf("deploy drill verify output unexpected: %s", stdout.String())
	}
	stdout.Reset()
	tamperedDrillPath := filepath.Join(t.TempDir(), "drill-tampered.json")
	if err := os.WriteFile(tamperedDrillPath, bytes.Replace(drillSummary, []byte(`"cleanup_failed": 1`), []byte(`"cleanup_failed": 0`), 1), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), []string{"deploy", "drill", "verify", "--file", tamperedDrillPath}, &stdout, &stderr); err == nil {
		t.Fatal("expected deploy drill verify to reject tampered evidence")
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"monitor", "snapshot", "--home", home, "--project", created.Project.ID}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"healthy":1`)) || !bytes.Contains(stdout.Bytes(), []byte("edge-01b")) {
		t.Fatalf("monitor output unexpected: %s", stdout.String())
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"monitor", "stream", "--home", home, "--project", created.Project.ID, "--limit", "2", "--interval", "1ms"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if count := bytes.Count(stdout.Bytes(), []byte(`"project_id"`)); count != 2 {
		t.Fatalf("monitor stream output count=%d body=%s", count, stdout.String())
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"doctor", "--home", home, "--json"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"status"`)) || !bytes.Contains(stdout.Bytes(), []byte(`"project_count"`)) {
		t.Fatalf("doctor json output unexpected: %s", stdout.String())
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"doctor", "--home", home}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("Mizan doctor:")) {
		t.Fatalf("doctor text output unexpected: %s", stdout.String())
	}
	st := store.New(home)
	events, err := st.ListAudit(context.Background(), created.Project.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 7 || events[0].Action != "deploy.run" || events[0].Actor != "cli" {
		t.Fatalf("unexpected deploy audit events: %+v", events)
	}
	if got := fmt.Sprint(events[0].Metadata["credentials"]); !strings.Contains(got, "vault") || strings.Contains(got, "PRIVATE KEY") || strings.Contains(got, "vault-user") {
		t.Fatalf("unexpected deploy credential audit metadata: %v", events[0].Metadata["credentials"])
	}
	if cleanup, ok := events[0].Metadata["cleanup"].(map[string]any); !ok || cleanup["succeeded"] == nil {
		t.Fatalf("unexpected deploy cleanup audit metadata: %v", events[0].Metadata["cleanup"])
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"audit", "show", "--home", home, "--project", created.Project.ID, "--action", "deploy.run", "--actor", "cli", "--outcome", "success", "--from", "2000-01-01T00:00:00Z", "--to", "2100-01-01T00:00:00Z", "--dry-run", "true", "--incident", "false", "--limit", "1"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("deploy.run")) {
		t.Fatalf("audit show output unexpected: %s", stdout.String())
	}
	auditPath := filepath.Join(t.TempDir(), "audit.csv")
	stdout.Reset()
	if err := Run(context.Background(), []string{"audit", "show", "--home", home, "--project", created.Project.ID, "--csv", "--out", auditPath}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(auditPath); err != nil || !bytes.Contains(data, []byte("event_id,timestamp,actor,action,outcome")) {
		t.Fatalf("audit csv file unexpected data=%q err=%v", string(data), err)
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"snapshot", "list", "--home", home, "--project", created.Project.ID}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var snapshots []string
	if err := json.Unmarshal(stdout.Bytes(), &snapshots); err != nil {
		t.Fatal(err)
	}
	if len(snapshots) == 0 {
		t.Fatal("expected snapshots")
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"snapshot", "tag", "--home", home, "--project", created.Project.ID, "--label", "release", snapshots[0]}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"snapshot", "get", "--home", home, "--project", created.Project.ID, "release"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"snapshot", "revert", "--home", home, "--project", created.Project.ID, "release"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"snapshot", "tags", "--home", home, "--project", created.Project.ID}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("release")) {
		t.Fatalf("tags output unexpected: %s", stdout.String())
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"cluster", "delete", "--home", home, "--project", created.Project.ID, cluster.ID}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"target", "delete", "--home", home, "--project", created.Project.ID, target.ID}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
}

func TestSecretCommands(t *testing.T) {
	home := t.TempDir()
	keyPath := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyPath, []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIZAN_VAULT_PASSPHRASE", "vault-pass")
	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), []string{"secret", "set", "--home", home, "--id", "target_1", "--username", "root", "--password", "ssh-pass", "--private-key-file", keyPath, "--passphrase", "key-pass", "--token", "api-token"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"status":"stored"`)) {
		t.Fatalf("secret set output unexpected: %s", stdout.String())
	}
	secretPath := filepath.Join(home, "secrets", "target_1.json")
	if data, err := os.ReadFile(secretPath); err != nil || bytes.Contains(data, []byte("ssh-pass")) || bytes.Contains(data, []byte("PRIVATE KEY")) {
		t.Fatalf("secret file leaked plaintext data=%q err=%v", string(data), err)
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"secret", "list", "--home", home}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("target_1")) {
		t.Fatalf("secret list output unexpected: %s", stdout.String())
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"secret", "get", "--home", home, "--id", "target_1"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"has_password":true`)) || bytes.Contains(stdout.Bytes(), []byte("ssh-pass")) {
		t.Fatalf("secret get redacted output unexpected: %s", stdout.String())
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"secret", "get", "--home", home, "--id", "target_1", "--vault-passphrase", "vault-pass", "--reveal"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("ssh-pass")) || !bytes.Contains(stdout.Bytes(), []byte("PRIVATE KEY")) {
		t.Fatalf("secret reveal output unexpected: %s", stdout.String())
	}
	provider := deployCredentialProvider(home, []byte("vault-pass"))
	secret, err := provider(context.Background(), store.Target{ID: "target_1"})
	if err != nil || secret.Username != "root" || secret.Password != "ssh-pass" {
		t.Fatalf("credential provider secret=%+v err=%v", secret, err)
	}
	secret, err = provider(context.Background(), store.Target{ID: "missing"})
	if err != nil || secret != (secrets.Secret{}) {
		t.Fatalf("missing credential provider secret=%+v err=%v", secret, err)
	}
	if _, err := deployCredentialProvider(home, []byte("bad-pass"))(context.Background(), store.Target{ID: "target_1"}); err == nil {
		t.Fatal("expected credential provider decrypt error")
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"secret", "delete", "--home", home, "--id", "target_1"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"secret", "list", "--home", home}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout.String(), "target_1") {
		t.Fatalf("secret list after delete unexpected: %s", stdout.String())
	}
}

func TestBackupCommands(t *testing.T) {
	home := t.TempDir()
	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), []string{"project", "new", "--home", home, "--name", "edge"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"secret", "set", "--home", home, "--id", "target_1", "--username", "root", "--vault-passphrase", "pass"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(t.TempDir(), "mizan-backup.zip")
	stdout.Reset()
	if err := Run(context.Background(), []string{"backup", "create", "--home", home, "--out", backupPath}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"format_version":2`)) || !bytes.Contains(stdout.Bytes(), []byte("projects/")) || !bytes.Contains(stdout.Bytes(), []byte("sha256")) {
		t.Fatalf("backup create output unexpected: %s", stdout.String())
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"backup", "inspect", "--in", backupPath}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"format_version": 2`)) || !bytes.Contains(stdout.Bytes(), []byte("secrets/target_1.json")) || !bytes.Contains(stdout.Bytes(), []byte(`"size"`)) {
		t.Fatalf("backup inspect output unexpected: %s", stdout.String())
	}
	restoreHome := filepath.Join(t.TempDir(), "restored")
	stdout.Reset()
	if err := Run(context.Background(), []string{"backup", "restore", "--home", restoreHome, "--in", backupPath}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"restored":true`)) {
		t.Fatalf("backup restore output unexpected: %s", stdout.String())
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"project", "list", "--home", restoreHome}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("edge")) {
		t.Fatalf("restored projects unexpected: %s", stdout.String())
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"secret", "list", "--home", restoreHome}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("target_1")) {
		t.Fatalf("restored secrets unexpected: %s", stdout.String())
	}

	stdout.Reset()
	if err := Run(context.Background(), []string{"backup", "restore", "--home", restoreHome, "--in", backupPath}, &stdout, &stderr); err == nil {
		t.Fatal("expected restore into non-empty home to fail")
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"backup", "restore", "--home", restoreHome, "--in", backupPath, "--force"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
}

func TestBackupRestoreRejectsUnsafeArchivePaths(t *testing.T) {
	restoreHome := filepath.Join(t.TempDir(), "restore")
	backupPath := filepath.Join(t.TempDir(), "unsafe.zip")
	writeTestBackup(t, backupPath, backupManifest{
		FormatVersion: backupManifestVersion,
		CreatedAt:     time.Now().UTC(),
		Files: []backupFile{{
			Path:   "../outside.txt",
			Size:   int64(len("bad")),
			SHA256: testSHA256("bad"),
		}},
	}, map[string]string{"../outside.txt": "bad"})
	if _, err := restoreBackup(context.Background(), restoreHome, backupPath, false); err == nil {
		t.Fatal("expected unsafe path restore error")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(restoreHome), "outside.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsafe restore wrote outside target err=%v", err)
	}
}

func TestBackupRestoreRejectsIntegrityMismatch(t *testing.T) {
	restoreHome := filepath.Join(t.TempDir(), "restore")
	backupPath := filepath.Join(t.TempDir(), "tampered.zip")
	writeTestBackup(t, backupPath, backupManifest{
		FormatVersion: backupManifestVersion,
		CreatedAt:     time.Now().UTC(),
		Files: []backupFile{{
			Path:   "projects/p/config.json",
			Size:   int64(len("expected")),
			SHA256: testSHA256("expected"),
		}},
	}, map[string]string{"projects/p/config.json": "tampered"})

	if _, err := restoreBackup(context.Background(), restoreHome, backupPath, false); err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("expected integrity restore error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(restoreHome, "projects", "p", "config.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tampered restore left a file behind err=%v", err)
	}
}

func TestProjectImportAndDelete(t *testing.T) {
	home := t.TempDir()
	cfgPath := filepath.Join(t.TempDir(), "haproxy.cfg")
	if err := os.WriteFile(cfgPath, []byte("frontend web\n  bind :80\n  default_backend be_app\nbackend be_app\n  server app 127.0.0.1:8080\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := Run(context.Background(), []string{"project", "import", "--home", home, "--name", "imported", cfgPath}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var created struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), []string{"project", "delete", "--home", home, created.Project.ID}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
}

func TestCLIErrorBranches(t *testing.T) {
	home := t.TempDir()
	var stdout, stderr bytes.Buffer
	expectErr := func(args ...string) {
		t.Helper()
		stdout.Reset()
		stderr.Reset()
		if err := Run(context.Background(), args, &stdout, &stderr); err == nil {
			t.Fatalf("expected error for args %v", args)
		}
	}

	rootFile := filepath.Join(t.TempDir(), "root-file")
	if err := os.WriteFile(rootFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	badCfg := filepath.Join(t.TempDir(), "bad.txt")
	if err := os.WriteFile(badCfg, []byte("not a config"), 0o600); err != nil {
		t.Fatal(err)
	}
	goodCfg := filepath.Join(t.TempDir(), "haproxy.cfg")
	if err := os.WriteFile(goodCfg, []byte("frontend web\n  bind :80\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	secretRootHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(secretRootHome, "secrets"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	expectErr("serve", "--bad")
	expectErr("serve", "--home", rootFile)
	expectErr("serve", "--home", home, "--max-body-bytes", "0")
	expectErr("serve", "--home", home, "--shutdown-timeout", "0s")
	expectErr("project")
	expectErr("project", "new", "--bad")
	expectErr("project", "new", "--home", home)
	expectErr("project", "new", "--home", rootFile, "--name", "bad")
	expectErr("project", "list", "--bad")
	expectErr("project", "list", "--home", rootFile)
	expectErr("project", "delete", "--bad")
	expectErr("project", "delete", "--home", home)
	expectErr("project", "import", "--bad")
	expectErr("project", "import", "--home", home)
	expectErr("project", "import", "--home", home, filepath.Join(t.TempDir(), "missing.cfg"))
	expectErr("project", "import", "--home", home, badCfg)
	expectErr("project", "import", "--home", rootFile, goodCfg)
	expectErr("project", "export", "--bad")
	expectErr("project", "export", "--home", home)
	expectErr("project", "export", "--home", home, "missing")
	expectErr("project", "unknown")

	expectErr("snapshot")
	expectErr("snapshot", "list", "--bad")
	expectErr("snapshot", "list", "--home", home)
	expectErr("snapshot", "get", "--bad")
	expectErr("snapshot", "get", "--home", home)
	expectErr("snapshot", "get", "--home", home, "--project", "p_1", "missing")
	expectErr("snapshot", "revert", "--bad")
	expectErr("snapshot", "revert", "--home", home)
	expectErr("snapshot", "revert", "--home", home, "--project", "p_1", "missing")
	expectErr("snapshot", "tag", "--bad")
	expectErr("snapshot", "tag", "--home", home)
	expectErr("snapshot", "tag", "--home", home, "--project", "p_1", "--label", "release", "missing")
	expectErr("snapshot", "tags", "--bad")
	expectErr("snapshot", "tags", "--home", home)
	expectErr("snapshot", "unknown")

	expectErr("target")
	expectErr("target", "list", "--bad")
	expectErr("target", "list", "--home", home)
	expectErr("target", "list", "--home", rootFile, "--project", "p_1")
	expectErr("target", "add", "--bad")
	expectErr("target", "add", "--home", home)
	expectErr("target", "add", "--home", rootFile, "--project", "p_1", "--name", "edge", "--host", "host")
	expectErr("target", "delete", "--bad")
	expectErr("target", "delete", "--home", home)
	expectErr("target", "delete", "--home", home, "--project", "p_1", "missing")
	expectErr("target", "unknown")

	expectErr("cluster")
	expectErr("cluster", "list", "--bad")
	expectErr("cluster", "list", "--home", home)
	expectErr("cluster", "list", "--home", rootFile, "--project", "p_1")
	expectErr("cluster", "add", "--bad")
	expectErr("cluster", "add", "--home", home)
	expectErr("cluster", "add", "--home", rootFile, "--project", "p_1", "--name", "prod")
	expectErr("cluster", "delete", "--bad")
	expectErr("cluster", "delete", "--home", home)
	expectErr("cluster", "delete", "--home", home, "--project", "p_1", "missing")
	expectErr("cluster", "unknown")

	expectErr("approval")
	expectErr("approval", "list", "--bad")
	expectErr("approval", "list", "--home", home)
	expectErr("approval", "list", "--home", rootFile, "--project", "p_1")
	expectErr("approval", "request", "--bad")
	expectErr("approval", "request", "--home", home)
	expectErr("approval", "request", "--home", home, "--project", "missing", "--target-id", "t_1")
	expectErr("approval", "request", "--home", home, "--project", "missing", "--target-id", "t_1", "--cluster-id", "c_1")
	expectErr("approval", "request", "--home", home, "--project", "missing", "--cluster-id", "c_1", "--batch", "-1")
	expectErr("approval", "approve", "--bad")
	expectErr("approval", "approve", "--home", home)
	expectErr("approval", "approve", "--home", home, "--project", "missing", "approval")
	expectErr("approval", "unknown")

	expectErr("generate", "--bad")
	expectErr("generate", "--home", home)
	expectErr("generate", "--home", home, "--project", "missing")
	expectErr("validate", "--bad")
	expectErr("validate", "--home", home)
	expectErr("validate", "--home", home, "--project", "missing")
	expectErr("deploy", "--bad")
	expectErr("deploy", "--home", home)
	expectErr("deploy", "--home", home, "--project", "missing")
	expectErr("deploy", "--home", home, "--project", "missing", "--target-id", "t_1")
	expectErr("deploy", "--home", home, "--project", "missing", "--target-id", "t_1", "--execute")
	expectErr("deploy", "--home", home, "--project", "missing", "--target-id", "t_1", "--batch", "-1")
	expectErr("deploy", "drill", "extra")
	expectErr("deploy", "drill", "--format", "xml")
	expectErr("deploy", "drill", "--summary", "--format", "text")
	expectErr("audit")
	expectErr("audit", "show", "--bad")
	expectErr("audit", "show", "--home", home)
	expectErr("audit", "show", "--home", home, "--project", "p_1", "--limit", "0")
	expectErr("audit", "show", "--home", home, "--project", "p_1", "--from", "bad")
	expectErr("audit", "show", "--home", home, "--project", "p_1", "--to", "bad")
	expectErr("audit", "show", "--home", home, "--project", "p_1", "--target-engine", "bad")
	expectErr("audit", "show", "--home", home, "--project", "p_1", "--batch", "-1")
	expectErr("audit", "show", "--home", home, "--project", "p_1", "--dry-run", "bad")
	expectErr("audit", "show", "--home", home, "--project", "p_1", "--incident", "bad")
	expectErr("audit", "show", "--home", home, "--project", "p_1", "--rollback-failed", "bad")
	expectErr("audit", "show", "--home", home, "--project", "p_1", "--cleanup-failed", "bad")
	expectErr("audit", "show", "--home", home, "--project", "p_1", "--out", filepath.Join(t.TempDir(), "missing", "audit.json"))
	expectErr("audit", "unknown")
	expectErr("secret")
	expectErr("secret", "set", "--bad")
	expectErr("secret", "set", "--home", home)
	expectErr("secret", "set", "--home", home, "--id", "target_1")
	expectErr("secret", "set", "--home", home, "--id", "target_1", "--username", "root")
	expectErr("secret", "set", "--home", home, "--id", "target_1", "--username", "root", "--vault-passphrase", "pass", "--private-key", "key", "--private-key-file", filepath.Join(t.TempDir(), "missing"))
	expectErr("secret", "set", "--home", home, "--id", "target_1", "--username", "root", "--vault-passphrase", "pass", "--private-key-file", filepath.Join(t.TempDir(), "missing"))
	expectErr("secret", "get", "--bad")
	expectErr("secret", "get", "--home", home)
	expectErr("secret", "get", "--home", home, "--id", "missing")
	expectErr("secret", "list", "--bad")
	expectErr("secret", "list", "--home", secretRootHome)
	expectErr("secret", "delete", "--bad")
	expectErr("secret", "delete", "--home", home)
	expectErr("secret", "delete", "--home", home, "--id", "../x")
	expectErr("secret", "unknown")
	expectErr("backup")
	expectErr("backup", "create", "--bad")
	expectErr("backup", "create", "--home", rootFile, "--out", filepath.Join(t.TempDir(), "backup.zip"))
	expectErr("backup", "create", "--home", home)
	expectErr("backup", "create", "--home", home, "--out", filepath.Join(home, "backup.zip"))
	expectErr("backup", "create", "--home", home, "--out", filepath.Join(rootFile, "backup.zip"))
	expectErr("backup", "inspect", "--bad")
	expectErr("backup", "inspect")
	expectErr("backup", "inspect", "--in", filepath.Join(t.TempDir(), "missing.zip"))
	expectErr("backup", "restore", "--bad")
	expectErr("backup", "restore")
	expectErr("backup", "restore", "--in", filepath.Join(t.TempDir(), "missing.zip"), "--home", filepath.Join(t.TempDir(), "restore"))
	expectErr("backup", "unknown")
	expectErr("doctor", "--bad")
	expectErr("doctor", "--home", rootFile)
	expectErr("monitor")
	expectErr("monitor", "snapshot", "--bad")
	expectErr("monitor", "snapshot", "--home", home)
	expectErr("monitor", "snapshot", "--home", home, "--project", "missing")
	expectErr("monitor", "stream", "--bad")
	expectErr("monitor", "stream", "--home", home)
	expectErr("monitor", "stream", "--home", home, "--project", "p_1", "--limit", "-1")
	expectErr("monitor", "stream", "--home", home, "--project", "p_1", "--interval", "0s")
	expectErr("monitor", "stream", "--home", home, "--project", "missing", "--limit", "1")
	expectErr("monitor", "unknown")

	if got := firstNonEmpty("", "fallback", "later"); got != "fallback" {
		t.Fatalf("firstNonEmpty=%q", got)
	}
	if got := firstNonEmpty("", ""); got != "" {
		t.Fatalf("firstNonEmpty empty=%q", got)
	}
	if got := deployCredentialProvider(home, nil); got != nil {
		t.Fatal("expected nil credential provider without passphrase")
	}
	if got := secretsRoot("home"); got != filepath.Join("home", "secrets") {
		t.Fatalf("secretsRoot=%q", got)
	}
	t.Setenv("MIZAN_VAULT_PASSPHRASE", "env-pass")
	if got := string(vaultPassphraseBytes("flag-pass")); got != "flag-pass" {
		t.Fatalf("vault pass flag=%q", got)
	}
	if got := string(vaultPassphraseBytes("")); got != "env-pass" {
		t.Fatalf("vault pass env=%q", got)
	}
	if got := redactSecret(secrets.Secret{Username: "u", Password: "p", PrivateKey: "k", Passphrase: "pp", Token: "t"}); !got.HasPassword || !got.HasPrivateKey || !got.HasPassphrase || !got.HasToken || got.Username != "u" {
		t.Fatalf("redacted=%+v", got)
	}

	stdout.Reset()
	if err := Run(context.Background(), []string{"project", "new", "--home", home, "positional"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var created struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	missingDirExport := filepath.Join(t.TempDir(), "missing", "mizan-export.json")
	expectErr("project", "export", "--home", home, "--out", missingDirExport, created.Project.ID)
	stdout.Reset()
	if err := Run(context.Background(), []string{"project", "export", "--home", home, created.Project.ID}, errWriter{}, &stderr); err == nil {
		t.Fatal("expected export writer error")
	}
	configPath := filepath.Join(home, "projects", created.Project.ID, "config.json")
	originalConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(configPath); err != nil {
		t.Fatal(err)
	}
	expectErr("project", "export", "--home", home, created.Project.ID)
	if err := os.WriteFile(configPath, originalConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	targetsPath := filepath.Join(home, "projects", created.Project.ID, "targets.json")
	if err := os.WriteFile(targetsPath, []byte("{bad json"), 0o600); err != nil {
		t.Fatal(err)
	}
	expectErr("project", "export", "--home", home, created.Project.ID)
	if err := os.Remove(targetsPath); err != nil {
		t.Fatal(err)
	}
	if err := store.New(home).AppendAudit(context.Background(), store.AuditEvent{
		ProjectID:    created.Project.ID,
		Actor:        "cli",
		Action:       "target.probe",
		Outcome:      "failed",
		TargetEngine: "nginx",
		ErrorMessage: "probe failed",
		Metadata:     map[string]any{"target_id": "t_probe", "cluster_id": "c_probe", "approval_request_id": "a_probe", "batch": 2, "dry_run": true, "rollback": map[string]any{"failed": 1}, "cleanup": map[string]any{"failed": 1}},
	}); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"audit", "show", "--home", home, "--project", created.Project.ID, "--target-engine", "nginx", "--action-prefix", "target.", "--target-id", "t_probe", "--cluster-id", "c_probe", "--approval-request-id", "a_probe", "--batch", "2", "--dry-run", "true", "--incident", "true", "--rollback-failed", "true", "--cleanup-failed", "true", "--csv"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("target.probe")) || !bytes.Contains(stdout.Bytes(), []byte("probe failed")) {
		t.Fatalf("audit csv stdout unexpected: %s", stdout.String())
	}
	stdout.Reset()
	if err := Run(context.Background(), []string{"audit", "show", "--home", home, "--project", created.Project.ID}, errWriter{}, &stderr); err == nil {
		t.Fatal("expected audit json writer error")
	}
	if err := Run(context.Background(), []string{"audit", "show", "--home", home, "--project", created.Project.ID, "--csv"}, errWriter{}, &stderr); err == nil {
		t.Fatal("expected audit csv writer error")
	}
	if err := os.WriteFile(filepath.Join(home, "projects", created.Project.ID, "audit.jsonl"), []byte(strings.Repeat("x", 70_000)), 0o600); err != nil {
		t.Fatal(err)
	}
	expectErr("audit", "show", "--home", home, "--project", created.Project.ID)
	expectErr("generate", "--home", home, "--project", created.Project.ID, "--target", "bad")
	expectErr("validate", "--home", home, "--project", created.Project.ID, "--target", "bad")
	expectErr("deploy", "--home", home, "--project", created.Project.ID, "--target-id", "t_1", "--cluster-id", "c_1")
	expectErr("deploy", "--home", home, "--project", created.Project.ID, "--target-id", "missing")

	outPath := filepath.Join(t.TempDir(), "haproxy.cfg")
	if err := Run(context.Background(), []string{"generate", "--home", home, "--project", created.Project.ID, "--out", outPath}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(outPath); err != nil || !bytes.Contains(data, []byte("Generated by Mizan")) {
		t.Fatalf("unexpected generated file data=%q err=%v", string(data), err)
	}
	expectErr("generate", "--home", home, "--project", created.Project.ID, "--out", filepath.Join(t.TempDir(), "missing", "x.cfg"))
}

func TestStreamSnapshotsBranches(t *testing.T) {
	st := store.New(t.TempDir())
	meta, _, _, err := st.CreateProject(t.Context(), "edge", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := streamSnapshots(ctx, st, meta.ID, 0, time.Millisecond, &bytes.Buffer{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled before first snapshot, got %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	go func() {
		time.Sleep(time.Millisecond)
		cancel()
	}()
	if err := streamSnapshots(ctx, st, meta.ID, 0, time.Hour, &bytes.Buffer{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled while waiting, got %v", err)
	}
	if err := streamSnapshots(context.Background(), st, meta.ID, 1, time.Millisecond, errWriter{}); err == nil {
		t.Fatal("expected writer error")
	}
}

func TestSnapshotInjectedStoreErrors(t *testing.T) {
	oldListSnapshots := listSnapshotsFromStore
	oldListSnapshotTags := listSnapshotTagsFromStore
	t.Cleanup(func() {
		listSnapshotsFromStore = oldListSnapshots
		listSnapshotTagsFromStore = oldListSnapshotTags
	})
	var stdout, stderr bytes.Buffer
	listSnapshotsFromStore = func(*store.Store, context.Context, string) ([]string, error) {
		return nil, errors.New("list snapshots failed")
	}
	if err := Run(context.Background(), []string{"snapshot", "list", "--home", t.TempDir(), "--project", "p_1"}, &stdout, &stderr); err == nil {
		t.Fatal("expected injected snapshot list error")
	}
	listSnapshotsFromStore = oldListSnapshots

	listSnapshotTagsFromStore = func(*store.Store, context.Context, string) ([]store.SnapshotTag, error) {
		return nil, errors.New("list tags failed")
	}
	if err := Run(context.Background(), []string{"snapshot", "tags", "--home", t.TempDir(), "--project", "p_1"}, &stdout, &stderr); err == nil {
		t.Fatal("expected injected snapshot tags error")
	}
}

func TestParseEngines(t *testing.T) {
	engines := parseEngines("haproxy,nginx,bad")
	if len(engines) != 2 {
		t.Fatalf("engines=%v", engines)
	}
	if items := splitCSV(" a, ,b "); len(items) != 2 || items[0] != "a" || items[1] != "b" {
		t.Fatalf("items=%v", items)
	}
}

func TestApprovalCLIHelpers(t *testing.T) {
	request := store.ApprovalRequest{
		TargetID:     "target-a",
		SnapshotHash: "hash-a",
		Status:       store.ApprovalStatusPending,
		Approvals:    []store.Approval{{Actor: "alice"}},
	}
	if _, _, _, _, _, err := applyCLIApprovalRequest("other", "", 0, "", false, request); err == nil || !strings.Contains(err.Error(), "target_id") {
		t.Fatalf("expected target mismatch error, got %v", err)
	}
	if _, _, _, _, _, err := applyCLIApprovalRequest("", "cluster-a", 0, "", false, request); err == nil || !strings.Contains(err.Error(), "cluster_id") {
		t.Fatalf("expected cluster mismatch error, got %v", err)
	}
	if _, _, _, _, _, err := applyCLIApprovalRequest("", "", 2, "", false, request); err == nil || !strings.Contains(err.Error(), "batch") {
		t.Fatalf("expected batch mismatch error, got %v", err)
	}
	if _, _, _, _, _, err := applyCLIApprovalRequest("", "", 0, "hash-b", false, request); err == nil || !strings.Contains(err.Error(), "snapshot_hash") {
		t.Fatalf("expected snapshot mismatch error, got %v", err)
	}
	if _, _, _, _, _, err := applyCLIApprovalRequest("", "", 0, "", true, request); err == nil || !strings.Contains(err.Error(), "not fully approved") {
		t.Fatalf("expected pending approval execute error, got %v", err)
	}
	request.Status = store.ApprovalStatusApproved
	targetID, clusterID, batch, snapshot, actors, err := applyCLIApprovalRequest("", "", 0, "", true, request)
	if err != nil || targetID != "target-a" || clusterID != "" || batch != 0 || snapshot != "hash-a" || len(actors) != 1 || actors[0] != "alice" {
		t.Fatalf("unexpected approved helper result target=%q cluster=%q batch=%d snapshot=%q actors=%v err=%v", targetID, clusterID, batch, snapshot, actors, err)
	}

	targets := store.TargetsFile{
		Targets: []store.Target{{ID: "target-a"}},
		Clusters: []store.Cluster{
			{ID: "cluster-a", RequiredApprovals: 2},
			{ID: "cluster-b", RequiredApprovals: -1},
		},
	}
	if got, err := approvalPolicyFromTargets(targets, "target-a", ""); err != nil || got != 0 {
		t.Fatalf("target approval policy got=%d err=%v", got, err)
	}
	if got, err := approvalPolicyFromTargets(targets, "", "cluster-a"); err != nil || got != 2 {
		t.Fatalf("cluster approval policy got=%d err=%v", got, err)
	}
	for _, tc := range []struct {
		targetID  string
		clusterID string
		want      string
	}{
		{"", "", "target_id or cluster_id"},
		{"target-a", "cluster-a", "exactly one"},
		{"missing", "", "target not found"},
		{"", "cluster-b", "non-negative"},
		{"", "missing", "cluster not found"},
	} {
		if _, err := approvalPolicyFromTargets(targets, tc.targetID, tc.clusterID); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("approval policy target=%q cluster=%q expected %q, got %v", tc.targetID, tc.clusterID, tc.want, err)
		}
	}
}

func writeTestBackup(t *testing.T, path string, manifest backupManifest, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if manifest.FormatVersion != 0 {
		data, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		w, err := zw.Create(backupManifestPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func testSHA256(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}
