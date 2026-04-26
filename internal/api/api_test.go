package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mizanproxy/mizan/internal/ir"
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
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+id+"/clusters", map[string]any{"name": "prod", "target_ids": []string{target.ID}})
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
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+id+"/deploy", map[string]any{"target_id": target.ID, "dry_run": true})
	if res.Code != http.StatusOK || !bytes.Contains(res.Body.Bytes(), []byte(`"dry_run":true`)) {
		t.Fatalf("deploy target status=%d body=%s", res.Code, res.Body.String())
	}
	res = doJSON(mux, http.MethodPost, "/api/v1/projects/"+id+"/deploy", map[string]any{"cluster_id": cluster.ID})
	if res.Code != http.StatusOK || !bytes.Contains(res.Body.Bytes(), []byte(`"cluster_id":"`+cluster.ID+`"`)) {
		t.Fatalf("deploy cluster status=%d body=%s", res.Code, res.Body.String())
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
		{http.MethodGet, "/api/v1/projects/missing/ir", nil, http.StatusNotFound},
		{http.MethodGet, "/api/v1/projects/missing/ir/snapshots", nil, http.StatusOK},
		{http.MethodGet, "/api/v1/projects/missing/ir/snapshots/nope", nil, http.StatusNotFound},
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
	if err := os.WriteFile(filepath.Join(dir, "haproxy.bat"), []byte("@echo off\r\necho native failed\r\nexit /b 3\r\n"), 0o755); err != nil {
		t.Fatal(err)
	}
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
