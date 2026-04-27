package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mizanproxy/mizan/internal/ir"
	"github.com/mizanproxy/mizan/internal/monitor"
	"github.com/mizanproxy/mizan/internal/store"
)

func TestProjectLifecycleEndpoints(t *testing.T) {
	st := store.New(t.TempDir())
	mux := http.NewServeMux()
	Register(mux, st)

	res := doJSON(mux, http.MethodGet, "/healthz", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("health status=%d", res.Code)
	}
	res = doJSON(mux, http.MethodGet, "/readyz", nil)
	if res.Code != http.StatusOK || !bytes.Contains(res.Body.Bytes(), []byte(`"store":"ok"`)) {
		t.Fatalf("ready status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSON(mux, http.MethodGet, "/version", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("version status=%d", res.Code)
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects", map[string]any{"name": "edge", "engines": []string{"haproxy", "nginx"}})
	if res.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", res.Code, res.Body.String())
	}
	var created struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
		IR      map[string]any `json:"ir"`
		Version string         `json:"version"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	id := created.Project.ID
	version := created.Version
	res = doJSON(mux, http.MethodGet, "/metrics", nil)
	if res.Code != http.StatusOK || res.Header().Get("Content-Type") != "text/plain; version=0.0.4; charset=utf-8" || !bytes.Contains(res.Body.Bytes(), []byte("mizan_build_info")) || !bytes.Contains(res.Body.Bytes(), []byte("mizan_projects_total 1")) || !bytes.Contains(res.Body.Bytes(), []byte("mizan_http_requests_total")) {
		t.Fatalf("metrics status=%d type=%q body=%s", res.Code, res.Header().Get("Content-Type"), res.Body.String())
	}

	for _, tc := range []struct {
		method string
		path   string
		status int
	}{
		{http.MethodGet, "/api/v1/projects", http.StatusOK},
		{http.MethodGet, "/api/v1/projects/" + id, http.StatusOK},
		{http.MethodGet, "/api/v1/projects/" + id + "/ir", http.StatusOK},
		{http.MethodGet, "/api/v1/projects/" + id + "/ir/snapshots", http.StatusOK},
		{http.MethodGet, "/api/v1/projects/" + id + "/ir/tags", http.StatusOK},
		{http.MethodGet, "/api/v1/projects/" + id + "/audit", http.StatusOK},
		{http.MethodGet, "/api/v1/projects/" + id + "/monitor/snapshot", http.StatusOK},
		{http.MethodGet, "/api/v1/projects/" + id + "/monitor/stream?limit=1", http.StatusOK},
		{http.MethodGet, "/api/v1/projects/" + id + "/events?limit=1", http.StatusOK},
	} {
		res = doJSON(mux, tc.method, tc.path, nil)
		if res.Code != tc.status {
			t.Fatalf("%s %s status=%d body=%s", tc.method, tc.path, res.Code, res.Body.String())
		}
	}

	model, _, err := st.GetIR(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	model.Frontends = append(model.Frontends, ir.Frontend{ID: "fe_web", Name: "web", Bind: ":80", Protocol: "http"})
	model.Backends = append(model.Backends, ir.Backend{ID: "be_app", Name: "app", Algorithm: "roundrobin", Servers: []string{}})
	res = doJSONWithHeader(mux, http.MethodPatch, "/api/v1/projects/"+id+"/ir", map[string]any{"ir": model}, map[string]string{"If-Match": version})
	if res.Code != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", res.Code, res.Body.String())
	}
	var patched struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &patched); err != nil {
		t.Fatal(err)
	}

	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+id+"/generate", map[string]string{"target": "nginx"})
	if res.Code != http.StatusOK {
		t.Fatalf("generate status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+id+"/validate", map[string]string{"target": "haproxy"})
	if res.Code != http.StatusOK {
		t.Fatalf("validate status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+id+"/targets", map[string]any{"name": "prod-a", "host": "lb1.example.com", "engine": "haproxy"})
	if res.Code != http.StatusOK {
		t.Fatalf("target status=%d body=%s", res.Code, res.Body.String())
	}
	var target store.Target
	if err := json.Unmarshal(res.Body.Bytes(), &target); err != nil {
		t.Fatal(err)
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+id+"/clusters", map[string]any{"name": "prod", "target_ids": []string{target.ID}, "required_approvals": 2})
	if res.Code != http.StatusOK {
		t.Fatalf("cluster status=%d body=%s", res.Code, res.Body.String())
	}
	var cluster store.Cluster
	if err := json.Unmarshal(res.Body.Bytes(), &cluster); err != nil {
		t.Fatal(err)
	}
	res = doJSON(mux, http.MethodGet, "/api/v1/projects/"+id+"/targets", nil)
	if res.Code != http.StatusOK || !bytes.Contains(res.Body.Bytes(), []byte("prod-a")) {
		t.Fatalf("targets status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSON(mux, http.MethodGet, "/api/v1/projects/"+id+"/export", nil)
	if res.Code != http.StatusOK || !bytes.Contains(res.Body.Bytes(), []byte(`"format_version":1`)) || !bytes.Contains(res.Body.Bytes(), []byte("prod-a")) {
		t.Fatalf("export status=%d body=%s", res.Code, res.Body.String())
	}
	if got := res.Header().Get("Content-Disposition"); !strings.Contains(got, id+"-mizan-export.json") {
		t.Fatalf("unexpected export disposition %q", got)
	}
	res = doJSON(mux, http.MethodGet, "/api/v1/projects/"+id+"/monitor/snapshot", nil)
	if res.Code != http.StatusOK || !bytes.Contains(res.Body.Bytes(), []byte(`"unknown":1`)) {
		t.Fatalf("monitor snapshot status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+id+"/deploy", map[string]any{"target_id": target.ID, "dry_run": true})
	if res.Code != http.StatusOK || !bytes.Contains(res.Body.Bytes(), []byte(`"dry_run":true`)) {
		t.Fatalf("deploy target status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+id+"/deploy", map[string]any{"cluster_id": cluster.ID})
	if res.Code != http.StatusOK || !bytes.Contains(res.Body.Bytes(), []byte(`"cluster_id":"`+cluster.ID+`"`)) || !bytes.Contains(res.Body.Bytes(), []byte(`"required_approvals":2`)) {
		t.Fatalf("deploy cluster status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+id+"/deploy", map[string]any{"target_id": target.ID, "cluster_id": cluster.ID, "dry_run": true})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("deploy target+cluster status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+id+"/deploy", map[string]any{"target_id": target.ID, "dry_run": false})
	if res.Code != http.StatusBadRequest || !bytes.Contains(res.Body.Bytes(), []byte("confirm_snapshot_hash")) {
		t.Fatalf("deploy execute without confirm status=%d body=%s", res.Code, res.Body.String())
	}
	events, err := st.ListAudit(t.Context(), id, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Action != "deploy.run" {
		t.Fatalf("unexpected deploy audit event: %+v", events)
	}
	if got := fmt.Sprint(events[0].Metadata["credentials"]); strings.Contains(got, "vault") {
		t.Fatalf("unexpected dry-run credential audit metadata: %v", events[0].Metadata["credentials"])
	}
	res = doJSON(mux, http.MethodDelete, "/api/v1/projects/"+id+"/targets/"+target.ID, nil)
	if res.Code != http.StatusNoContent {
		t.Fatalf("delete target status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSON(mux, http.MethodDelete, "/api/v1/projects/"+id+"/clusters/"+cluster.ID, nil)
	if res.Code != http.StatusNoContent {
		t.Fatalf("delete cluster status=%d body=%s", res.Code, res.Body.String())
	}

	res = doJSON(mux, http.MethodGet, "/api/v1/projects/"+id+"/ir/snapshots", nil)
	var snapshots []string
	if err := json.Unmarshal(res.Body.Bytes(), &snapshots); err != nil {
		t.Fatal(err)
	}
	if len(snapshots) < 2 {
		t.Fatalf("snapshots=%v", snapshots)
	}
	res = doJSON(mux, http.MethodGet, "/api/v1/projects/"+id+"/ir/snapshots/"+snapshots[0], nil)
	if res.Code != http.StatusOK {
		t.Fatalf("get snapshot status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+id+"/ir/tag", map[string]string{"snapshot_ref": snapshots[0], "label": "latest"})
	if res.Code != http.StatusCreated {
		t.Fatalf("tag status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+id+"/ir/diff", map[string]string{"from_hash": snapshots[len(snapshots)-1], "to_hash": snapshots[0]})
	if res.Code != http.StatusOK {
		t.Fatalf("diff status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSONWithHeader(mux, http.MethodPost, "/api/v1/projects/"+id+"/ir/revert", map[string]string{"snapshot_ref": "latest"}, map[string]string{"If-Match": patched.Version})
	if res.Code != http.StatusOK {
		t.Fatalf("revert status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSON(mux, http.MethodDelete, "/api/v1/projects/"+id, nil)
	if res.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestProjectEventsEndpoint(t *testing.T) {
	st := store.New(t.TempDir())
	mux := http.NewServeMux()
	Register(mux, st)
	res := doJSON(mux, http.MethodPost, "/api/v1/projects", map[string]any{"name": "edge", "engines": []string{"haproxy"}})
	if res.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", res.Code, res.Body.String())
	}
	var created struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	res = doJSON(mux, http.MethodGet, "/api/v1/projects/"+created.Project.ID+"/events?limit=4&interval=1ms", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("events status=%d body=%s", res.Code, res.Body.String())
	}
	for _, want := range []string{"event: project", "event: targets", "event: approvals", "event: audit", "project.create"} {
		if !bytes.Contains(res.Body.Bytes(), []byte(want)) {
			t.Fatalf("events missing %q body=%s", want, res.Body.String())
		}
	}
	if ct := res.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type=%q", ct)
	}
}

func TestProjectEventsDetectsProjectAndTargetChanges(t *testing.T) {
	st := store.New(t.TempDir())
	mux := http.NewServeMux()
	Register(mux, st)
	res := doJSON(mux, http.MethodPost, "/api/v1/projects", map[string]any{"name": "edge", "engines": []string{"haproxy"}})
	if res.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", res.Code, res.Body.String())
	}
	var created struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	model, _, err := st.GetIR(t.Context(), created.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	model.Description = "streamed update"
	if _, err := st.SaveIR(t.Context(), created.Project.ID, model, created.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertTarget(t.Context(), created.Project.ID, store.Target{Name: "prod-a", Host: "lb1.example.com", Engine: ir.EngineHAProxy}); err != nil {
		t.Fatal(err)
	}
	res = doJSON(mux, http.MethodGet, "/api/v1/projects/"+created.Project.ID+"/events?limit=2&interval=1ms", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("events status=%d body=%s", res.Code, res.Body.String())
	}
	if !bytes.Contains(res.Body.Bytes(), []byte(`"description":"streamed update"`)) || !bytes.Contains(res.Body.Bytes(), []byte(`"targets":[`)) || !bytes.Contains(res.Body.Bytes(), []byte("prod-a")) {
		t.Fatalf("events body=%s", res.Body.String())
	}
	if ct := res.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type=%q", ct)
	}
}

func TestApprovalEndpointsAndDeployRequest(t *testing.T) {
	st := store.New(t.TempDir())
	mux := http.NewServeMux()
	Register(mux, st)
	res := doJSON(mux, http.MethodPost, "/api/v1/projects", map[string]any{"name": "edge", "engines": []string{"haproxy"}})
	if res.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", res.Code, res.Body.String())
	}
	var created struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	id := created.Project.ID
	res = doJSON(mux, http.MethodGet, "/api/v1/projects/"+id+"/approvals", nil)
	if res.Code != http.StatusOK || res.Body.String() != "[]\n" {
		t.Fatalf("empty approvals status=%d body=%s", res.Code, res.Body.String())
	}
	if res = doJSON(mux, http.MethodGet, "/api/v1/projects/missing/approvals", nil); res.Code != http.StatusNotFound {
		t.Fatalf("missing approvals status=%d body=%s", res.Code, res.Body.String())
	}
	if res = doPossiblyRawJSON(mux, http.MethodPost, "/api/v1/projects/"+id+"/approvals", "{bad", nil); res.Code != http.StatusBadRequest {
		t.Fatalf("bad approval json status=%d body=%s", res.Code, res.Body.String())
	}
	if res = doJSON(mux, http.MethodPost, "/api/v1/projects/missing/approvals", map[string]any{"target_id": "missing"}); res.Code != http.StatusNotFound {
		t.Fatalf("missing project approval status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+id+"/targets", map[string]any{"name": "prod-a", "host": "lb1.example.com", "engine": "haproxy"})
	if res.Code != http.StatusOK {
		t.Fatalf("target status=%d body=%s", res.Code, res.Body.String())
	}
	var target store.Target
	if err := json.Unmarshal(res.Body.Bytes(), &target); err != nil {
		t.Fatal(err)
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+id+"/clusters", map[string]any{"name": "prod", "target_ids": []string{target.ID}, "required_approvals": 2})
	if res.Code != http.StatusOK {
		t.Fatalf("cluster status=%d body=%s", res.Code, res.Body.String())
	}
	var cluster store.Cluster
	if err := json.Unmarshal(res.Body.Bytes(), &cluster); err != nil {
		t.Fatal(err)
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+id+"/approvals", map[string]any{"cluster_id": cluster.ID, "batch": 1})
	if res.Code != http.StatusCreated {
		t.Fatalf("create approval status=%d body=%s", res.Code, res.Body.String())
	}
	var approval store.ApprovalRequest
	if err := json.Unmarshal(res.Body.Bytes(), &approval); err != nil {
		t.Fatal(err)
	}
	if approval.Status != store.ApprovalStatusPending || approval.RequiredApprovals != 2 || approval.SnapshotHash == "" {
		t.Fatalf("unexpected approval request: %+v", approval)
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+id+"/approvals", map[string]any{"target_id": target.ID})
	if res.Code != http.StatusCreated || !bytes.Contains(res.Body.Bytes(), []byte(`"status":"approved"`)) {
		t.Fatalf("target approval status=%d body=%s", res.Code, res.Body.String())
	}
	var targetApproval store.ApprovalRequest
	if err := json.Unmarshal(res.Body.Bytes(), &targetApproval); err != nil {
		t.Fatal(err)
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+id+"/deploy", map[string]any{"approval_request_id": "missing", "dry_run": true})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("missing approval deploy status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+id+"/deploy", map[string]any{"approval_request_id": approval.ID, "dry_run": false})
	if res.Code != http.StatusBadRequest || !bytes.Contains(res.Body.Bytes(), []byte("not fully approved")) {
		t.Fatalf("pending approval execute status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSONWithHeader(mux, http.MethodPost, "/api/v1/projects/"+id+"/approvals/"+approval.ID+"/approve", map[string]string{"actor": "alice"}, map[string]string{"X-Mizan-Actor": "ignored"})
	if res.Code != http.StatusOK || !bytes.Contains(res.Body.Bytes(), []byte(`"status":"pending"`)) {
		t.Fatalf("first approve status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSONWithHeader(mux, http.MethodPost, "/api/v1/projects/"+id+"/approvals/"+approval.ID+"/approve", map[string]string{}, map[string]string{"X-Mizan-Actor": "bob"})
	if res.Code != http.StatusOK || !bytes.Contains(res.Body.Bytes(), []byte(`"status":"approved"`)) {
		t.Fatalf("second approve status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+id+"/deploy", map[string]any{"approval_request_id": approval.ID, "dry_run": true})
	if res.Code != http.StatusOK || !bytes.Contains(res.Body.Bytes(), []byte(`"approved_by":["alice","bob"]`)) || !bytes.Contains(res.Body.Bytes(), []byte(`"batch":1`)) {
		t.Fatalf("approved dry-run deploy status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+id+"/deploy", map[string]any{"approval_request_id": approval.ID, "target_id": target.ID, "dry_run": true})
	if res.Code != http.StatusBadRequest || !bytes.Contains(res.Body.Bytes(), []byte("target_id does not match")) {
		t.Fatalf("mismatched approval deploy status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+id+"/deploy", map[string]any{"approval_request_id": approval.ID, "cluster_id": cluster.ID, "batch": 2, "dry_run": true})
	if res.Code != http.StatusBadRequest || !bytes.Contains(res.Body.Bytes(), []byte("batch does not match")) {
		t.Fatalf("mismatched batch deploy status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+id+"/deploy", map[string]any{"approval_request_id": approval.ID, "cluster_id": cluster.ID, "confirm_snapshot_hash": "bad", "dry_run": true})
	if res.Code != http.StatusBadRequest || !bytes.Contains(res.Body.Bytes(), []byte("snapshot_hash does not match")) {
		t.Fatalf("mismatched snapshot deploy status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+id+"/deploy", map[string]any{"approval_request_id": targetApproval.ID, "cluster_id": cluster.ID, "dry_run": true})
	if res.Code != http.StatusBadRequest || !bytes.Contains(res.Body.Bytes(), []byte("cluster_id does not match")) {
		t.Fatalf("mismatched cluster deploy status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+id+"/approvals", map[string]any{"target_id": "missing"})
	if res.Code != http.StatusBadRequest || !bytes.Contains(res.Body.Bytes(), []byte("target not found")) {
		t.Fatalf("missing target approval status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+id+"/approvals", map[string]any{"cluster_id": "missing"})
	if res.Code != http.StatusBadRequest || !bytes.Contains(res.Body.Bytes(), []byte("cluster not found")) {
		t.Fatalf("missing cluster approval status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+id+"/approvals", map[string]any{"target_id": target.ID, "cluster_id": cluster.ID})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("invalid approval scope status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+id+"/approvals/missing/approve", map[string]string{"actor": "alice"})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("missing approval approve status=%d body=%s", res.Code, res.Body.String())
	}
	if events, err := st.ListAudit(t.Context(), id, 10); err != nil {
		t.Fatal(err)
	} else if len(events) == 0 || events[0].Action != "deploy.run" {
		t.Fatalf("expected deploy audit event after approved dry-run: %+v", events)
	}
}

func TestTargetProbeEndpoint(t *testing.T) {
	probeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fail" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer probeServer.Close()
	st := store.New(t.TempDir())
	mux := http.NewServeMux()
	Register(mux, st)
	res := doJSON(mux, http.MethodPost, "/api/v1/projects", map[string]any{"name": "edge", "engines": []string{"haproxy"}})
	if res.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", res.Code, res.Body.String())
	}
	var created struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+created.Project.ID+"/targets", map[string]any{"name": "edge", "host": "localhost", "engine": "haproxy", "post_reload_probe": probeServer.URL + "/ok"})
	if res.Code != http.StatusOK {
		t.Fatalf("target status=%d body=%s", res.Code, res.Body.String())
	}
	var target store.Target
	if err := json.Unmarshal(res.Body.Bytes(), &target); err != nil {
		t.Fatal(err)
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+created.Project.ID+"/targets/"+target.ID+"/probe", nil)
	if res.Code != http.StatusOK || !bytes.Contains(res.Body.Bytes(), []byte(`"status":"success"`)) {
		t.Fatalf("probe status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+created.Project.ID+"/targets", map[string]any{"name": "edge-fail", "host": "localhost", "engine": "haproxy", "monitor_endpoint": probeServer.URL + "/fail"})
	if res.Code != http.StatusOK {
		t.Fatalf("target fail status=%d body=%s", res.Code, res.Body.String())
	}
	var failTarget store.Target
	if err := json.Unmarshal(res.Body.Bytes(), &failTarget); err != nil {
		t.Fatal(err)
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+created.Project.ID+"/targets/"+failTarget.ID+"/probe", nil)
	if res.Code != http.StatusOK || !bytes.Contains(res.Body.Bytes(), []byte(`"status":"failed"`)) || !bytes.Contains(res.Body.Bytes(), []byte("HTTP 500")) {
		t.Fatalf("failed probe status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+created.Project.ID+"/targets", map[string]any{"name": "edge-empty", "host": "localhost", "engine": "haproxy"})
	if res.Code != http.StatusOK {
		t.Fatalf("target empty status=%d body=%s", res.Code, res.Body.String())
	}
	var emptyTarget store.Target
	if err := json.Unmarshal(res.Body.Bytes(), &emptyTarget); err != nil {
		t.Fatal(err)
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+created.Project.ID+"/targets/"+emptyTarget.ID+"/probe", nil)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("empty probe status=%d body=%s", res.Code, res.Body.String())
	}
	if res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+created.Project.ID+"/targets/missing/probe", nil); res.Code != http.StatusNotFound {
		t.Fatalf("missing target probe status=%d body=%s", res.Code, res.Body.String())
	}
	if res = doJSON(mux, http.MethodPost, "/api/v1/projects/missing/targets/missing/probe", nil); res.Code != http.StatusNotFound {
		t.Fatalf("missing project probe status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestProjectEventsBranches(t *testing.T) {
	st := store.New(t.TempDir())
	mux := http.NewServeMux()
	Register(mux, st)
	if res := doJSON(mux, http.MethodGet, "/api/v1/projects/missing/events", nil); res.Code != http.StatusNotFound {
		t.Fatalf("missing events status=%d body=%s", res.Code, res.Body.String())
	}
	meta, _, _, err := st.CreateProject(t.Context(), "edge", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{store: st}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+meta.ID+"/events", nil)
	req.SetPathValue("id", meta.ID)
	plain := &plainResponse{header: http.Header{}}
	h.projectEvents(plain, req)
	if plain.status != http.StatusInternalServerError || !bytes.Contains(plain.body.Bytes(), []byte("streaming is not supported")) {
		t.Fatalf("plain response status=%d body=%s", plain.status, plain.body.String())
	}

	oldAuditEvents := auditEventsFromStore
	t.Cleanup(func() { auditEventsFromStore = oldAuditEvents })
	auditEventsFromStore = func(*store.Store, *http.Request, string, store.AuditFilter) ([]store.AuditEvent, error) {
		return nil, errors.New("audit failed")
	}
	res := doJSON(mux, http.MethodGet, "/api/v1/projects/"+meta.ID+"/events?limit=4", nil)
	if res.Code != http.StatusOK || !bytes.Contains(res.Body.Bytes(), []byte("event: error")) {
		t.Fatalf("events error status=%d body=%s", res.Code, res.Body.String())
	}

	auditEventsFromStore = func(*store.Store, *http.Request, string, store.AuditFilter) ([]store.AuditEvent, error) {
		return []store.AuditEvent{{EventID: "e_1", ProjectID: meta.ID, Action: "test", Outcome: "success"}}, nil
	}
	firstWriteFailure := &flusherResponse{header: http.Header{}, failAt: 0}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+meta.ID+"/events", nil)
	req.SetPathValue("id", meta.ID)
	h.projectEvents(firstWriteFailure, req)
	if firstWriteFailure.body.Len() != 0 {
		t.Fatalf("expected no stream body after write failure: %s", firstWriteFailure.body.String())
	}

	targetWriteFailure := &flusherResponse{header: http.Header{}, failAt: 1}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+meta.ID+"/events", nil)
	req.SetPathValue("id", meta.ID)
	h.projectEvents(targetWriteFailure, req)
	if !bytes.Contains(targetWriteFailure.body.Bytes(), []byte("event: project")) || bytes.Contains(targetWriteFailure.body.Bytes(), []byte("event: targets")) {
		t.Fatalf("expected target write failure after project event: %s", targetWriteFailure.body.String())
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+meta.ID+"/events?limit=bad&interval=bad", nil).WithContext(ctx)
	req.SetPathValue("id", meta.ID)
	canceled := &flusherResponse{header: http.Header{}, failAt: -1}
	h.projectEvents(canceled, req)
	if !bytes.Contains(canceled.body.Bytes(), []byte("event: audit")) {
		t.Fatalf("expected initial audit before canceled context return: %s", canceled.body.String())
	}

	auditEventsFromStore = func(*store.Store, *http.Request, string, store.AuditFilter) ([]store.AuditEvent, error) {
		return []store.AuditEvent{
			{EventID: "e_dup", ProjectID: meta.ID, Action: "duplicate", Outcome: "success"},
			{EventID: "e_dup", ProjectID: meta.ID, Action: "duplicate", Outcome: "success"},
		}, nil
	}
	ctx, cancel = context.WithCancel(context.Background())
	cancel()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+meta.ID+"/events?limit=bad&interval=bad", nil).WithContext(ctx)
	req.SetPathValue("id", meta.ID)
	duplicate := &flusherResponse{header: http.Header{}, failAt: -1}
	h.projectEvents(duplicate, req)
	if got := strings.Count(duplicate.body.String(), "event: audit"); got != 1 {
		t.Fatalf("expected duplicate audit event to emit once, got %d body=%s", got, duplicate.body.String())
	}

	calls := 0
	auditEventsFromStore = func(*store.Store, *http.Request, string, store.AuditFilter) ([]store.AuditEvent, error) {
		calls++
		if calls == 1 {
			return nil, nil
		}
		return []store.AuditEvent{{EventID: "e_after_tick", ProjectID: meta.ID, Action: "later", Outcome: "success"}}, nil
	}
	res = doJSON(mux, http.MethodGet, "/api/v1/projects/"+meta.ID+"/events?limit=4&interval=1ms", nil)
	if res.Code != http.StatusOK || !bytes.Contains(res.Body.Bytes(), []byte("e_after_tick")) {
		t.Fatalf("events after tick status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestProjectEventsStateErrorBranches(t *testing.T) {
	st := store.New(t.TempDir())
	mux := http.NewServeMux()
	Register(mux, st)
	meta, _, _, err := st.CreateProject(t.Context(), "edge", "", []ir.Engine{ir.EngineHAProxy})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(st.Root(), "projects", meta.ID, "config.json")); err != nil {
		t.Fatal(err)
	}
	res := doJSON(mux, http.MethodGet, "/api/v1/projects/"+meta.ID+"/events?limit=1", nil)
	if res.Code != http.StatusOK || !bytes.Contains(res.Body.Bytes(), []byte("event: error")) {
		t.Fatalf("missing config stream status=%d body=%s", res.Code, res.Body.String())
	}

	meta, _, _, err = st.CreateProject(t.Context(), "edge-targets", "", []ir.Engine{ir.EngineHAProxy})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(st.Root(), "projects", meta.ID, "targets.json"), []byte("{bad json"), 0o600); err != nil {
		t.Fatal(err)
	}
	res = doJSON(mux, http.MethodGet, "/api/v1/projects/"+meta.ID+"/events?limit=2", nil)
	if res.Code != http.StatusOK || !bytes.Contains(res.Body.Bytes(), []byte("event: project")) || !bytes.Contains(res.Body.Bytes(), []byte("event: error")) {
		t.Fatalf("bad targets stream status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestProjectEventsStopsWhenProjectDeletedDuringStream(t *testing.T) {
	st := store.New(t.TempDir())
	meta, _, _, err := st.CreateProject(t.Context(), "edge", "", []ir.Engine{ir.EngineHAProxy})
	if err != nil {
		t.Fatal(err)
	}
	oldAuditEvents := auditEventsFromStore
	t.Cleanup(func() { auditEventsFromStore = oldAuditEvents })
	deleted := false
	auditEventsFromStore = func(*store.Store, *http.Request, string, store.AuditFilter) ([]store.AuditEvent, error) {
		if !deleted {
			deleted = true
			if err := st.DeleteProject(t.Context(), meta.ID); err != nil {
				t.Fatal(err)
			}
		}
		return nil, nil
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+meta.ID+"/events?interval=1ms", nil)
	req.SetPathValue("id", meta.ID)
	res := &flusherResponse{header: http.Header{}, failAt: -1}
	(&Handler{store: st}).projectEvents(res, req)
	if !bytes.Contains(res.body.Bytes(), []byte("event: project")) || !bytes.Contains(res.body.Bytes(), []byte("event: error")) {
		t.Fatalf("expected project event followed by deletion error: %s", res.body.String())
	}
}

func TestMonitorStreamEndpoint(t *testing.T) {
	st := store.New(t.TempDir())
	mux := http.NewServeMux()
	Register(mux, st)
	res := doJSON(mux, http.MethodPost, "/api/v1/projects", map[string]any{"name": "edge", "engines": []string{"haproxy"}})
	if res.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", res.Code, res.Body.String())
	}
	var created struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/projects/" + created.Project.ID + "/monitor/stream?limit=2&interval=1ms"
	res = doJSON(mux, http.MethodGet, path, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("stream status=%d body=%s", res.Code, res.Body.String())
	}
	if got := strings.Count(res.Body.String(), "event: snapshot"); got != 2 {
		t.Fatalf("expected 2 snapshot events, got %d body=%s", got, res.Body.String())
	}
	if ct := res.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type=%q", ct)
	}
}

func TestMonitorStreamBranches(t *testing.T) {
	st := store.New(t.TempDir())
	mux := http.NewServeMux()
	Register(mux, st)
	if res := doJSON(mux, http.MethodGet, "/api/v1/projects/missing/monitor/stream", nil); res.Code != http.StatusNotFound {
		t.Fatalf("missing stream status=%d body=%s", res.Code, res.Body.String())
	}
	res := doJSON(mux, http.MethodPost, "/api/v1/projects", map[string]any{"name": "edge", "engines": []string{"haproxy"}})
	if res.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", res.Code, res.Body.String())
	}
	var created struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+created.Project.ID+"/monitor/stream", nil)
	req.SetPathValue("id", created.Project.ID)
	h := &Handler{store: st}
	plain := &plainResponse{header: http.Header{}}
	h.monitorStream(plain, req)
	if plain.status != http.StatusInternalServerError || !bytes.Contains(plain.body.Bytes(), []byte("streaming is not supported")) {
		t.Fatalf("plain response status=%d body=%s", plain.status, plain.body.String())
	}

	oldMonitorSnapshot := monitorSnapshotFromStore
	t.Cleanup(func() { monitorSnapshotFromStore = oldMonitorSnapshot })
	calls := 0
	monitorSnapshotFromStore = func(*store.Store, *http.Request, string) (monitor.Snapshot, error) {
		calls++
		if calls == 2 {
			return monitor.Snapshot{}, errors.New("stream failed")
		}
		return monitor.Snapshot{ProjectID: created.Project.ID}, nil
	}
	res = doJSON(mux, http.MethodGet, "/api/v1/projects/"+created.Project.ID+"/monitor/stream?limit=3&interval=1ms", nil)
	if res.Code != http.StatusOK || !bytes.Contains(res.Body.Bytes(), []byte("event: error")) {
		t.Fatalf("stream error status=%d body=%s", res.Code, res.Body.String())
	}

	monitorSnapshotFromStore = func(*store.Store, *http.Request, string) (monitor.Snapshot, error) {
		return monitor.Snapshot{ProjectID: created.Project.ID}, nil
	}
	firstWriteFailure := &flusherResponse{header: http.Header{}, failAt: 0}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+created.Project.ID+"/monitor/stream", nil)
	req.SetPathValue("id", created.Project.ID)
	h.monitorStream(firstWriteFailure, req)
	if firstWriteFailure.body.Len() != 0 {
		t.Fatalf("expected no stream body after first write failure: %s", firstWriteFailure.body.String())
	}

	secondWriteFailure := &flusherResponse{header: http.Header{}, failAt: 1}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+created.Project.ID+"/monitor/stream?limit=2&interval=1ms", nil)
	req.SetPathValue("id", created.Project.ID)
	h.monitorStream(secondWriteFailure, req)
	if strings.Count(secondWriteFailure.body.String(), "event: snapshot") != 1 {
		t.Fatalf("expected one written event before second failure: %s", secondWriteFailure.body.String())
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+created.Project.ID+"/monitor/stream?limit=bad&interval=bad", nil).WithContext(ctx)
	req.SetPathValue("id", created.Project.ID)
	canceled := &flusherResponse{header: http.Header{}, failAt: -1}
	h.monitorStream(canceled, req)
	if !strings.Contains(canceled.body.String(), "event: snapshot") {
		t.Fatalf("expected initial snapshot before canceled context return: %s", canceled.body.String())
	}
}

func TestImportAndGenerate(t *testing.T) {
	st := store.New(t.TempDir())
	mux := http.NewServeMux()
	Register(mux, st)
	body := map[string]string{
		"name":     "imported",
		"filename": "haproxy.cfg",
		"config": `
frontend web
  bind :80
  default_backend be_app
backend be_app
  server app1 127.0.0.1:8080
`,
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects/import", bytes.NewReader(raw))
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var created struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	genBody := []byte(`{"target":"haproxy"}`)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+created.Project.ID+"/generate", bytes.NewReader(genBody))
	res = httptest.NewRecorder()
	mux.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	if !bytes.Contains(res.Body.Bytes(), []byte("backend be_app")) {
		t.Fatalf("generated config missing backend: %s", res.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+created.Project.ID+"/audit", nil)
	res = httptest.NewRecorder()
	mux.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	if !bytes.Contains(res.Body.Bytes(), []byte("project.import")) || !bytes.Contains(res.Body.Bytes(), []byte("config.generate")) {
		t.Fatalf("audit missing expected actions: %s", res.Body.String())
	}
}

func TestAuditFilterEndpoint(t *testing.T) {
	st := store.New(t.TempDir())
	mux := http.NewServeMux()
	Register(mux, st)
	meta, _, _, err := st.CreateProject(t.Context(), "edge", "", []ir.Engine{ir.EngineHAProxy, ir.EngineNginx})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC)
	for _, event := range []store.AuditEvent{
		{ProjectID: meta.ID, Actor: "alice", Action: "config.generate", Outcome: "success", TargetEngine: ir.EngineHAProxy, Timestamp: base},
		{ProjectID: meta.ID, Actor: "bob", Action: "target.probe", Outcome: "failed", TargetEngine: ir.EngineNginx, Timestamp: base.Add(time.Hour), Metadata: map[string]any{"target_id": "t_nginx", "batch": 2, "dry_run": true}},
		{ProjectID: meta.ID, Actor: "alice", Action: "approval.request", Outcome: "success", TargetEngine: ir.EngineNginx, Timestamp: base.Add(2 * time.Hour), Metadata: map[string]any{"cluster_id": "c_edge", "approval_request_id": "a_edge"}},
		{ProjectID: meta.ID, Actor: "alice", Action: "deploy.run", Outcome: "success", TargetEngine: ir.EngineNginx, Timestamp: base.Add(3 * time.Hour), Metadata: map[string]any{"rollback": map[string]any{"failed": 1}}},
	} {
		if err := st.AppendAudit(t.Context(), event); err != nil {
			t.Fatal(err)
		}
	}
	path := "/api/v1/projects/" + meta.ID + "/audit?limit=1&from=" + base.Add(30*time.Minute).Format(time.RFC3339) + "&to=" + base.Add(90*time.Minute).Format(time.RFC3339) + "&actor=bob&action=target.probe&outcome=failed&target_engine=nginx&target_id=t_nginx&batch=2&dry_run=true&incident=true"
	res := doJSON(mux, http.MethodGet, path, nil)
	if res.Code != http.StatusOK || !bytes.Contains(res.Body.Bytes(), []byte("target.probe")) || bytes.Contains(res.Body.Bytes(), []byte("config.generate")) {
		t.Fatalf("filtered audit status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSON(mux, http.MethodGet, "/api/v1/projects/"+meta.ID+"/audit?action_prefix=approval.&cluster_id=c_edge&approval_request_id=a_edge", nil)
	if res.Code != http.StatusOK || !bytes.Contains(res.Body.Bytes(), []byte("approval.request")) || bytes.Contains(res.Body.Bytes(), []byte("deploy.run")) {
		t.Fatalf("metadata audit status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSON(mux, http.MethodGet, "/api/v1/projects/"+meta.ID+"/audit?rollback_failed=true", nil)
	if res.Code != http.StatusOK || !bytes.Contains(res.Body.Bytes(), []byte("deploy.run")) || bytes.Contains(res.Body.Bytes(), []byte("target.probe")) {
		t.Fatalf("rollback audit status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSON(mux, http.MethodGet, strings.Replace(path, "/audit?", "/audit.csv?", 1), nil)
	if res.Code != http.StatusOK || res.Header().Get("Content-Type") != "text/csv; charset=utf-8" || !bytes.Contains(res.Body.Bytes(), []byte("event_id,timestamp,actor,action,outcome,target_engine")) || !bytes.Contains(res.Body.Bytes(), []byte("target.probe")) {
		t.Fatalf("audit csv status=%d type=%q body=%s", res.Code, res.Header().Get("Content-Type"), res.Body.String())
	}
	if got := res.Header().Get("Content-Disposition"); !strings.Contains(got, meta.ID+"-audit.csv") {
		t.Fatalf("unexpected audit csv disposition %q", got)
	}
	for _, path := range []string{
		"/api/v1/projects/" + meta.ID + "/audit?limit=bad",
		"/api/v1/projects/" + meta.ID + "/audit?limit=0",
		"/api/v1/projects/" + meta.ID + "/audit?from=bad",
		"/api/v1/projects/" + meta.ID + "/audit?to=bad",
		"/api/v1/projects/" + meta.ID + "/audit?target_engine=bad",
		"/api/v1/projects/" + meta.ID + "/audit?batch=bad",
		"/api/v1/projects/" + meta.ID + "/audit?batch=0",
		"/api/v1/projects/" + meta.ID + "/audit?dry_run=bad",
		"/api/v1/projects/" + meta.ID + "/audit?incident=bad",
		"/api/v1/projects/" + meta.ID + "/audit?rollback_failed=bad",
		"/api/v1/projects/" + meta.ID + "/audit.csv?target_engine=bad",
	} {
		if res := doJSON(mux, http.MethodGet, path, nil); res.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d body=%s", path, res.Code, res.Body.String())
		}
	}
}

func TestProjectExportErrorBranches(t *testing.T) {
	st := store.New(t.TempDir())
	mux := http.NewServeMux()
	Register(mux, st)
	meta, _, _, err := st.CreateProject(t.Context(), "edge", "", []ir.Engine{ir.EngineHAProxy})
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(st.Root(), "projects", meta.ID, "config.json")
	if err := os.Remove(configPath); err != nil {
		t.Fatal(err)
	}
	res := doJSON(mux, http.MethodGet, "/api/v1/projects/"+meta.ID+"/export", nil)
	if res.Code != http.StatusNotFound {
		t.Fatalf("missing config export status=%d body=%s", res.Code, res.Body.String())
	}
	if _, err := st.SaveIR(t.Context(), meta.ID, ir.EmptyModel(meta.ID, "edge", "", []ir.Engine{ir.EngineHAProxy}), ""); err != nil {
		t.Fatal(err)
	}
	targetsPath := filepath.Join(st.Root(), "projects", meta.ID, "targets.json")
	if err := os.WriteFile(targetsPath, []byte("{bad json"), 0o600); err != nil {
		t.Fatal(err)
	}
	res = doJSON(mux, http.MethodGet, "/api/v1/projects/"+meta.ID+"/export", nil)
	if res.Code != http.StatusInternalServerError {
		t.Fatalf("bad targets export status=%d body=%s", res.Code, res.Body.String())
	}
	if err := os.WriteFile(targetsPath, []byte(`{"targets":[],"clusters":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(st.Root(), "projects", meta.ID, "approvals.json"), []byte("{bad json"), 0o600); err != nil {
		t.Fatal(err)
	}
	res = doJSON(mux, http.MethodGet, "/api/v1/projects/"+meta.ID+"/export", nil)
	if res.Code != http.StatusInternalServerError {
		t.Fatalf("bad approvals export status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestAPIErrorBranches(t *testing.T) {
	st := store.New(t.TempDir())
	mux := http.NewServeMux()
	Register(mux, st)

	for _, tc := range []struct {
		method string
		path   string
		body   any
		status int
	}{
		{http.MethodPost, "/api/v1/projects", "{bad", http.StatusBadRequest},
		{http.MethodPost, "/api/v1/projects", map[string]any{"name": ""}, http.StatusBadRequest},
		{http.MethodPost, "/api/v1/projects/import", map[string]any{"filename": "x.cfg"}, http.StatusBadRequest},
		{http.MethodPost, "/api/v1/projects/import", map[string]any{"filename": "x.txt", "config": "not config"}, http.StatusBadRequest},
		{http.MethodGet, "/api/v1/projects/missing", nil, http.StatusNotFound},
		{http.MethodGet, "/api/v1/projects/missing/export", nil, http.StatusNotFound},
		{http.MethodGet, "/api/v1/projects/missing/ir", nil, http.StatusNotFound},
		{http.MethodGet, "/api/v1/projects/missing/ir/snapshots", nil, http.StatusOK},
		{http.MethodGet, "/api/v1/projects/missing/ir/snapshots/nope", nil, http.StatusNotFound},
		{http.MethodGet, "/api/v1/projects/missing/monitor/snapshot", nil, http.StatusNotFound},
		{http.MethodPost, "/api/v1/projects/missing/generate", map[string]string{"target": "bad"}, http.StatusNotFound},
		{http.MethodPost, "/api/v1/projects/missing/validate", map[string]string{"target": "bad"}, http.StatusNotFound},
	} {
		res := doPossiblyRawJSON(mux, tc.method, tc.path, tc.body, nil)
		if res.Code != tc.status {
			t.Fatalf("%s %s status=%d want=%d body=%s", tc.method, tc.path, res.Code, tc.status, res.Body.String())
		}
	}

	res := doJSON(mux, http.MethodPost, "/api/v1/projects", map[string]any{"name": "edge", "engines": []string{"haproxy"}})
	if res.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", res.Code, res.Body.String())
	}
	var created struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	id := created.Project.ID
	for _, tc := range []struct {
		path   string
		body   any
		status int
	}{
		{"/api/v1/projects/" + id + "/ir", "{bad", http.StatusBadRequest},
		{"/api/v1/projects/" + id + "/ir", map[string]any{}, http.StatusBadRequest},
		{"/api/v1/projects/" + id + "/ir/revert", map[string]any{}, http.StatusBadRequest},
		{"/api/v1/projects/" + id + "/ir/diff", map[string]any{"from_hash": "missing", "to_hash": "missing"}, http.StatusNotFound},
		{"/api/v1/projects/" + id + "/ir/tag", map[string]any{"snapshot_ref": "missing", "label": "bad"}, http.StatusBadRequest},
		{"/api/v1/projects/" + id + "/generate", map[string]string{"target": "bad"}, http.StatusBadRequest},
		{"/api/v1/projects/" + id + "/validate", map[string]string{"target": "bad"}, http.StatusBadRequest},
		{"/api/v1/projects/" + id + "/deploy", map[string]string{}, http.StatusBadRequest},
		{"/api/v1/projects/" + id + "/targets", map[string]string{"name": ""}, http.StatusBadRequest},
		{"/api/v1/projects/" + id + "/clusters", map[string]string{"name": ""}, http.StatusBadRequest},
	} {
		res := doPossiblyRawJSON(mux, http.MethodPost, tc.path, tc.body, nil)
		if strings.HasSuffix(tc.path, "/ir") {
			res = doPossiblyRawJSON(mux, http.MethodPatch, tc.path, tc.body, map[string]string{"If-Match": created.Version})
		}
		if res.Code != tc.status {
			t.Fatalf("%s status=%d want=%d body=%s", tc.path, res.Code, tc.status, res.Body.String())
		}
	}
}

func TestAPIMoreBranches(t *testing.T) {
	st := store.New(t.TempDir())
	mux := http.NewServeMux()
	Register(mux, st)

	if res := doPossiblyRawJSON(mux, http.MethodPost, "/api/v1/projects/import", "{bad", nil); res.Code != http.StatusBadRequest {
		t.Fatalf("bad import json status=%d body=%s", res.Code, res.Body.String())
	}
	res := doJSON(mux, http.MethodPost, "/api/v1/projects/import", map[string]string{"config": "frontend imported\n  bind :80\n"})
	if res.Code != http.StatusCreated {
		t.Fatalf("default filename import status=%d body=%s", res.Code, res.Body.String())
	}

	res = doJSON(mux, http.MethodPost, "/api/v1/projects", map[string]any{"name": "edge", "engines": []string{"haproxy"}})
	if res.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", res.Code, res.Body.String())
	}
	var created struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	id := created.Project.ID
	version := created.Version

	if res := doJSON(mux, http.MethodPatch, "/api/v1/projects/missing/ir", map[string]any{"ir": map[string]any{}}); res.Code != http.StatusNotFound {
		t.Fatalf("missing patch status=%d body=%s", res.Code, res.Body.String())
	}
	mutation := map[string]any{
		"mutation": map[string]any{
			"type": "frontend.create",
			"data": map[string]any{"id": "fe_web", "name": "web", "bind": ":80", "protocol": "http"},
		},
	}
	res = doJSONWithHeader(mux, http.MethodPatch, "/api/v1/projects/"+id+"/ir", mutation, map[string]string{"If-Match": version})
	if res.Code != http.StatusOK {
		t.Fatalf("mutation patch status=%d body=%s", res.Code, res.Body.String())
	}
	var patched struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &patched); err != nil {
		t.Fatal(err)
	}
	currentModel, _, err := st.GetIR(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	currentModel.Description = "conflict candidate"
	res = doJSONWithHeader(mux, http.MethodPatch, "/api/v1/projects/"+id+"/ir", map[string]any{"ir": currentModel}, map[string]string{"If-Match": version})
	if res.Code != http.StatusConflict {
		t.Fatalf("conflict patch status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSONWithHeader(mux, http.MethodPatch, "/api/v1/projects/"+id+"/ir", map[string]any{
		"mutation": map[string]any{"type": "frontend.create"},
	}, map[string]string{"If-Match": patched.Version})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("bad mutation status=%d body=%s", res.Code, res.Body.String())
	}
	badIR := ir.EmptyModel(id, "bad", "", []ir.Engine{ir.EngineHAProxy})
	badIR.Frontends = []ir.Frontend{{ID: "fe_bad", Bind: ":443"}}
	res = doJSONWithHeader(mux, http.MethodPatch, "/api/v1/projects/"+id+"/ir", map[string]any{"ir": badIR}, map[string]string{"If-Match": patched.Version})
	if res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("lint patch status=%d body=%s", res.Code, res.Body.String())
	}

	errorProject := doJSON(mux, http.MethodPost, "/api/v1/projects", map[string]any{"name": "snapshot-error", "engines": []string{"haproxy"}})
	if errorProject.Code != http.StatusCreated {
		t.Fatalf("snapshot-error create status=%d body=%s", errorProject.Code, errorProject.Body.String())
	}
	var errorCreated struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(errorProject.Body.Bytes(), &errorCreated); err != nil {
		t.Fatal(err)
	}
	errorModel, _, err := st.GetIR(t.Context(), errorCreated.Project.ID)
	if err != nil {
		t.Fatal(err)
	}
	errorModel.Description = "force snapshot write error"
	snapshotPath := filepath.Join(st.Root(), "projects", errorCreated.Project.ID, "snapshots")
	if err := os.RemoveAll(snapshotPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshotPath, []byte("file blocks snapshot dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	res = doJSONWithHeader(mux, http.MethodPatch, "/api/v1/projects/"+errorCreated.Project.ID+"/ir", map[string]any{"ir": errorModel}, map[string]string{"If-Match": errorCreated.Version})
	if res.Code != http.StatusInternalServerError {
		t.Fatalf("snapshot write patch status=%d body=%s", res.Code, res.Body.String())
	}

	for _, tc := range []struct {
		path   string
		body   any
		status int
	}{
		{"/api/v1/projects/" + id + "/ir/revert", "{bad", http.StatusBadRequest},
		{"/api/v1/projects/" + id + "/ir/revert", map[string]string{"snapshot_ref": "missing"}, http.StatusInternalServerError},
		{"/api/v1/projects/" + id + "/ir/diff", "{bad", http.StatusBadRequest},
		{"/api/v1/projects/" + id + "/ir/tag", "{bad", http.StatusBadRequest},
		{"/api/v1/projects/" + id + "/deploy", "{bad", http.StatusBadRequest},
		{"/api/v1/projects/" + id + "/targets", "{bad", http.StatusBadRequest},
		{"/api/v1/projects/" + id + "/clusters", "{bad", http.StatusBadRequest},
	} {
		res := doPossiblyRawJSON(mux, http.MethodPost, tc.path, tc.body, nil)
		if res.Code != tc.status {
			t.Fatalf("%s status=%d want=%d body=%s", tc.path, res.Code, tc.status, res.Body.String())
		}
	}

	res = doJSON(mux, http.MethodGet, "/api/v1/projects/"+id+"/ir/snapshots", nil)
	var snapshots []string
	if err := json.Unmarshal(res.Body.Bytes(), &snapshots); err != nil {
		t.Fatal(err)
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+id+"/ir/diff", map[string]string{"from_hash": snapshots[0], "to_hash": "missing"})
	if res.Code != http.StatusNotFound {
		t.Fatalf("diff missing to status=%d body=%s", res.Code, res.Body.String())
	}

	if err := os.WriteFile(filepath.Join(st.Root(), "projects", id, "snapshot-tags.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	res = doJSON(mux, http.MethodGet, "/api/v1/projects/"+id+"/ir/tags", nil)
	if res.Code != http.StatusInternalServerError {
		t.Fatalf("tags corrupt status=%d body=%s", res.Code, res.Body.String())
	}
	if err := os.WriteFile(filepath.Join(st.Root(), "projects", id, "audit.jsonl"), []byte(strings.Repeat("x", 70_000)), 0o600); err != nil {
		t.Fatal(err)
	}
	res = doJSON(mux, http.MethodGet, "/api/v1/projects/"+id+"/audit?limit=1", nil)
	if res.Code != http.StatusInternalServerError {
		t.Fatalf("audit corrupt status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSON(mux, http.MethodGet, "/api/v1/projects/"+id+"/audit.csv?limit=1", nil)
	if res.Code != http.StatusInternalServerError {
		t.Fatalf("audit csv corrupt status=%d body=%s", res.Code, res.Body.String())
	}
	if res = doJSON(mux, http.MethodGet, "/api/v1/projects/missing/targets", nil); res.Code != http.StatusNotFound {
		t.Fatalf("missing targets status=%d body=%s", res.Code, res.Body.String())
	}
	if res = doJSON(mux, http.MethodDelete, "/api/v1/projects/"+id+"/targets/missing", nil); res.Code != http.StatusNotFound {
		t.Fatalf("missing delete target status=%d body=%s", res.Code, res.Body.String())
	}
	if res = doJSON(mux, http.MethodDelete, "/api/v1/projects/"+id+"/clusters/missing", nil); res.Code != http.StatusNotFound {
		t.Fatalf("missing delete cluster status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestAPIStoreAndNativeFailureBranches(t *testing.T) {
	rootFile := filepath.Join(t.TempDir(), "root-file")
	if err := os.WriteFile(rootFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	badStoreMux := http.NewServeMux()
	Register(badStoreMux, store.New(rootFile))
	for _, tc := range []struct {
		method string
		path   string
		body   any
		status int
	}{
		{http.MethodGet, "/readyz", nil, http.StatusServiceUnavailable},
		{http.MethodGet, "/metrics", nil, http.StatusInternalServerError},
		{http.MethodGet, "/api/v1/projects", nil, http.StatusInternalServerError},
		{http.MethodPost, "/api/v1/projects", map[string]string{"name": "bad"}, http.StatusInternalServerError},
		{http.MethodPost, "/api/v1/projects/import", map[string]string{"filename": "haproxy.cfg", "config": "frontend web\n  bind :80\n"}, http.StatusInternalServerError},
	} {
		res := doPossiblyRawJSON(badStoreMux, tc.method, tc.path, tc.body, nil)
		if res.Code != tc.status {
			t.Fatalf("%s %s status=%d want=%d body=%s", tc.method, tc.path, res.Code, tc.status, res.Body.String())
		}
	}

	dir := t.TempDir()
	oldPath := os.Getenv("PATH")
	t.Cleanup(func() { _ = os.Setenv("PATH", oldPath) })
	if err := os.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatal(err)
	}
	writeFakeNativeBinary(t, dir, "haproxy", "native failed", 3)
	st := store.New(t.TempDir())
	mux := http.NewServeMux()
	Register(mux, st)
	res := doJSON(mux, http.MethodPost, "/api/v1/projects", map[string]any{"name": "edge", "engines": []string{"haproxy"}})
	if res.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", res.Code, res.Body.String())
	}
	var created struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+created.Project.ID+"/generate", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("default generate status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+created.Project.ID+"/validate", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("default validate status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+created.Project.ID+"/validate", map[string]string{"target": "haproxy"})
	if res.Code != http.StatusOK || !bytes.Contains(res.Body.Bytes(), []byte("native failed")) {
		t.Fatalf("failed native validate status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestAPIInjectedStoreFailureBranches(t *testing.T) {
	oldDeleteProject := deleteProjectFromStore
	oldListSnapshots := listSnapshotsFromStore
	t.Cleanup(func() {
		deleteProjectFromStore = oldDeleteProject
		listSnapshotsFromStore = oldListSnapshots
	})
	st := store.New(t.TempDir())
	mux := http.NewServeMux()
	Register(mux, st)

	deleteProjectFromStore = func(*store.Store, *http.Request, string) error {
		return errors.New("delete failed")
	}
	res := doJSON(mux, http.MethodDelete, "/api/v1/projects/p_1", nil)
	if res.Code != http.StatusInternalServerError {
		t.Fatalf("delete injected status=%d body=%s", res.Code, res.Body.String())
	}

	listSnapshotsFromStore = func(*store.Store, *http.Request, string) ([]string, error) {
		return nil, errors.New("list snapshots failed")
	}
	res = doJSON(mux, http.MethodGet, "/api/v1/projects/p_1/ir/snapshots", nil)
	if res.Code != http.StatusInternalServerError {
		t.Fatalf("list snapshots injected status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestAPIHelpers(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if actorFromRequest(req) != "local" {
		t.Fatal("expected local actor")
	}
	req.Header.Set("X-Mizan-Actor", "alice")
	if actorFromRequest(req) != "alice" {
		t.Fatal("expected header actor")
	}
	if truncate("abc", 10) != "abc" || truncate("abcdef", 3) != "abc" {
		t.Fatal("truncate unexpected")
	}
	if hasErrors([]ir.Issue{{Severity: ir.SeverityWarning}}) {
		t.Fatal("warnings should not be errors")
	}
	if !hasErrors([]ir.Issue{{Severity: ir.SeverityError}}) {
		t.Fatal("errors should be detected")
	}
	h := &Handler{}
	h.audit(req, "", "noop", "", "", "success", "", nil)
	if writeSSE(errorWriter{}, "snapshot", map[string]string{"ok": "true"}) {
		t.Fatal("expected writeSSE writer failure")
	}
	if writeSSE(httptest.NewRecorder(), "bad", func() {}) {
		t.Fatal("expected writeSSE marshal failure")
	}
}

func doJSON(mux http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	return doJSONWithHeader(mux, method, path, body, nil)
}

func doJSONWithHeader(mux http.Handler, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	return doPossiblyRawJSON(mux, method, path, body, headers)
}

func doPossiblyRawJSON(mux http.Handler, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else if raw, ok := body.(string); ok {
		reader = bytes.NewReader([]byte(raw))
	} else {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res := httptest.NewRecorder()
	mux.ServeHTTP(res, req)
	return res
}

func writeFakeNativeBinary(t *testing.T, dir, name, output string, exitCode int) {
	t.Helper()
	path := filepath.Join(dir, name)
	content := "#!/bin/sh\necho " + output + "\nexit " + strconv.Itoa(exitCode) + "\n"
	if runtime.GOOS == "windows" {
		path += ".bat"
		content = "@echo off\r\necho " + output + "\r\nexit /b " + strconv.Itoa(exitCode) + "\r\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

type plainResponse struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (r *plainResponse) Header() http.Header {
	return r.header
}

func (r *plainResponse) Write(data []byte) (int, error) {
	return r.body.Write(data)
}

func (r *plainResponse) WriteHeader(status int) {
	r.status = status
}

type errorWriter struct{}

func (errorWriter) Header() http.Header {
	return http.Header{}
}

func (errorWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func (errorWriter) WriteHeader(int) {}

type flusherResponse struct {
	header  http.Header
	body    bytes.Buffer
	status  int
	failAt  int
	writes  int
	flushed bool
}

func (r *flusherResponse) Header() http.Header {
	return r.header
}

func (r *flusherResponse) Write(data []byte) (int, error) {
	if r.failAt >= 0 && r.writes == r.failAt {
		r.writes++
		return 0, errors.New("write failed")
	}
	r.writes++
	return r.body.Write(data)
}

func (r *flusherResponse) WriteHeader(status int) {
	r.status = status
}

func (r *flusherResponse) Flush() {
	r.flushed = true
}
