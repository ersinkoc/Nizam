package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/mizanproxy/mizan/internal/deploy"
	"github.com/mizanproxy/mizan/internal/ir"
	"github.com/mizanproxy/mizan/internal/ir/parser"
	"github.com/mizanproxy/mizan/internal/store"
	"github.com/mizanproxy/mizan/internal/validate"
	"github.com/mizanproxy/mizan/internal/version"
)

type Handler struct {
	store *store.Store
}

var (
	deleteProjectFromStore = func(st *store.Store, r *http.Request, id string) error {
		return st.DeleteProject(r.Context(), id)
	}
	listSnapshotsFromStore = func(st *store.Store, r *http.Request, id string) ([]string, error) {
		return st.ListSnapshots(r.Context(), id)
	}
)

func Register(mux *http.ServeMux, st *store.Store) {
	h := &Handler{store: st}
	mux.HandleFunc("GET /healthz", h.health)
	mux.HandleFunc("GET /readyz", h.health)
	mux.HandleFunc("GET /version", h.version)
	mux.HandleFunc("GET /api/v1/projects", h.listProjects)
	mux.HandleFunc("POST /api/v1/projects", h.createProject)
	mux.HandleFunc("POST /api/v1/projects/import", h.importProject)
	mux.HandleFunc("GET /api/v1/projects/{id}", h.getProject)
	mux.HandleFunc("DELETE /api/v1/projects/{id}", h.deleteProject)
	mux.HandleFunc("GET /api/v1/projects/{id}/ir", h.getIR)
	mux.HandleFunc("PATCH /api/v1/projects/{id}/ir", h.patchIR)
	mux.HandleFunc("GET /api/v1/projects/{id}/ir/snapshots", h.listSnapshots)
	mux.HandleFunc("GET /api/v1/projects/{id}/ir/snapshots/{ref}", h.getSnapshot)
	mux.HandleFunc("POST /api/v1/projects/{id}/ir/revert", h.revertSnapshot)
	mux.HandleFunc("POST /api/v1/projects/{id}/ir/diff", h.diffSnapshots)
	mux.HandleFunc("GET /api/v1/projects/{id}/ir/tags", h.listSnapshotTags)
	mux.HandleFunc("POST /api/v1/projects/{id}/ir/tag", h.tagSnapshot)
	mux.HandleFunc("POST /api/v1/projects/{id}/generate", h.generate)
	mux.HandleFunc("POST /api/v1/projects/{id}/validate", h.validate)
	mux.HandleFunc("POST /api/v1/projects/{id}/deploy", h.deploy)
	mux.HandleFunc("GET /api/v1/projects/{id}/audit", h.listAudit)
	mux.HandleFunc("GET /api/v1/projects/{id}/targets", h.listTargets)
	mux.HandleFunc("POST /api/v1/projects/{id}/targets", h.upsertTarget)
	mux.HandleFunc("DELETE /api/v1/projects/{id}/targets/{targetID}", h.deleteTarget)
	mux.HandleFunc("POST /api/v1/projects/{id}/clusters", h.upsertCluster)
	mux.HandleFunc("DELETE /api/v1/projects/{id}/clusters/{clusterID}", h.deleteCluster)
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) version(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": version.Version, "commit": version.Commit, "date": version.Date})
}

func (h *Handler) listProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := h.store.ListProjects(r.Context())
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, projects)
}

type createProjectRequest struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Engines     []ir.Engine `json:"engines"`
}

func (h *Handler) createProject(w http.ResponseWriter, r *http.Request) {
	var req createProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, http.StatusBadRequest, err)
		return
	}
	if req.Name == "" {
		writeProblem(w, http.StatusBadRequest, errors.New("name is required"))
		return
	}
	meta, model, etag, err := h.store.CreateProject(r.Context(), req.Name, req.Description, req.Engines)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("ETag", etag)
	h.audit(r, meta.ID, "project.create", etag, "", "success", "", map[string]any{"name": meta.Name})
	writeJSON(w, http.StatusCreated, map[string]any{"project": meta, "ir": model, "version": etag})
}

type importProjectRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Filename    string `json:"filename"`
	Config      string `json:"config"`
}

func (h *Handler) importProject(w http.ResponseWriter, r *http.Request) {
	var req importProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, http.StatusBadRequest, err)
		return
	}
	if req.Config == "" {
		writeProblem(w, http.StatusBadRequest, errors.New("config is required"))
		return
	}
	if req.Filename == "" {
		req.Filename = "import.cfg"
	}
	model, err := parser.ParseFile(req.Filename, []byte(req.Config))
	if err != nil {
		writeProblem(w, http.StatusBadRequest, err)
		return
	}
	meta, model, version, err := h.store.ImportProject(r.Context(), req.Name, req.Description, model)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("ETag", version)
	h.audit(r, meta.ID, "project.import", version, "", "success", "", map[string]any{"filename": req.Filename, "name": meta.Name})
	writeJSON(w, http.StatusCreated, map[string]any{"project": meta, "ir": model, "version": version, "issues": ir.Lint(model)})
}

func (h *Handler) getProject(w http.ResponseWriter, r *http.Request) {
	meta, err := h.store.GetProject(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, meta)
}

func (h *Handler) deleteProject(w http.ResponseWriter, r *http.Request) {
	if err := deleteProjectFromStore(h.store, r, r.PathValue("id")); err != nil {
		writeProblem(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) getIR(w http.ResponseWriter, r *http.Request) {
	model, version, err := h.store.GetIR(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, http.StatusNotFound, err)
		return
	}
	w.Header().Set("ETag", version)
	writeJSON(w, http.StatusOK, map[string]any{"ir": model, "version": version, "issues": ir.Lint(model)})
}

type patchIRRequest struct {
	Mutation *ir.Mutation `json:"mutation,omitempty"`
	IR       *ir.Model    `json:"ir,omitempty"`
}

func (h *Handler) patchIR(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	current, _, err := h.store.GetIR(r.Context(), id)
	if err != nil {
		writeProblem(w, http.StatusNotFound, err)
		return
	}
	var req patchIRRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, http.StatusBadRequest, err)
		return
	}
	next := req.IR
	if next == nil && req.Mutation != nil {
		next, err = ir.Apply(current, *req.Mutation)
		if err != nil {
			writeProblem(w, http.StatusBadRequest, err)
			return
		}
	}
	if next == nil {
		writeProblem(w, http.StatusBadRequest, errors.New("mutation or ir is required"))
		return
	}
	if issues := ir.Lint(next); hasErrors(issues) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"issues": issues})
		return
	}
	version, err := h.store.SaveIR(r.Context(), id, next, r.Header.Get("If-Match"))
	if err != nil {
		if errors.Is(err, store.ErrVersionConflict) {
			writeProblem(w, http.StatusConflict, err)
			return
		}
		writeProblem(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("ETag", version)
	h.audit(r, id, "ir.patch", version, "", "success", "", map[string]any{"mutation": req.Mutation != nil})
	writeJSON(w, http.StatusOK, map[string]any{"ir": next, "version": version, "issues": ir.Lint(next)})
}

func (h *Handler) listSnapshots(w http.ResponseWriter, r *http.Request) {
	snapshots, err := listSnapshotsFromStore(h.store, r, r.PathValue("id"))
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, snapshots)
}

func (h *Handler) getSnapshot(w http.ResponseWriter, r *http.Request) {
	model, version, err := h.store.GetSnapshot(r.Context(), r.PathValue("id"), r.PathValue("ref"))
	if err != nil {
		writeProblem(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ir": model, "version": version, "issues": ir.Lint(model)})
}

type revertRequest struct {
	SnapshotHash string `json:"snapshot_hash"`
	SnapshotRef  string `json:"snapshot_ref"`
}

func (h *Handler) revertSnapshot(w http.ResponseWriter, r *http.Request) {
	var req revertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, http.StatusBadRequest, err)
		return
	}
	ref := req.SnapshotHash
	if ref == "" {
		ref = req.SnapshotRef
	}
	if ref == "" {
		writeProblem(w, http.StatusBadRequest, errors.New("snapshot_hash is required"))
		return
	}
	model, version, err := h.store.RevertSnapshot(r.Context(), r.PathValue("id"), ref, r.Header.Get("If-Match"))
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, err)
		return
	}
	h.audit(r, r.PathValue("id"), "snapshot.revert", version, "", "success", "", map[string]any{"ref": ref})
	writeJSON(w, http.StatusOK, map[string]any{"ir": model, "version": version, "issues": ir.Lint(model)})
}

type diffRequest struct {
	FromHash string `json:"from_hash"`
	ToHash   string `json:"to_hash"`
}

func (h *Handler) diffSnapshots(w http.ResponseWriter, r *http.Request) {
	var req diffRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, http.StatusBadRequest, err)
		return
	}
	from, _, err := h.store.GetSnapshot(r.Context(), r.PathValue("id"), req.FromHash)
	if err != nil {
		writeProblem(w, http.StatusNotFound, err)
		return
	}
	to, _, err := h.store.GetSnapshot(r.Context(), r.PathValue("id"), req.ToHash)
	if err != nil {
		writeProblem(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"changes": ir.Diff(from, to)})
}

func (h *Handler) listSnapshotTags(w http.ResponseWriter, r *http.Request) {
	tags, err := h.store.ListSnapshotTags(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, tags)
}

type tagRequest struct {
	SnapshotHash string `json:"snapshot_hash"`
	SnapshotRef  string `json:"snapshot_ref"`
	Label        string `json:"label"`
}

func (h *Handler) tagSnapshot(w http.ResponseWriter, r *http.Request) {
	var req tagRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, http.StatusBadRequest, err)
		return
	}
	ref := req.SnapshotHash
	if ref == "" {
		ref = req.SnapshotRef
	}
	tag, err := h.store.TagSnapshot(r.Context(), r.PathValue("id"), ref, req.Label)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, err)
		return
	}
	h.audit(r, r.PathValue("id"), "snapshot.tag", "", "", "success", "", map[string]any{"ref": ref, "label": req.Label})
	writeJSON(w, http.StatusCreated, tag)
}

type targetRequest struct {
	Target ir.Engine `json:"target"`
}

func (h *Handler) generate(w http.ResponseWriter, r *http.Request) {
	model, _, err := h.store.GetIR(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, http.StatusNotFound, err)
		return
	}
	var req targetRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Target == "" {
		req.Target = ir.EngineHAProxy
	}
	result, err := validate.Generate(model, req.Target)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, err)
		return
	}
	h.audit(r, r.PathValue("id"), "config.generate", "", req.Target, "success", "", map[string]any{"warnings": len(result.Warnings)})
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) validate(w http.ResponseWriter, r *http.Request) {
	model, _, err := h.store.GetIR(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, http.StatusNotFound, err)
		return
	}
	var req targetRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Target == "" {
		req.Target = ir.EngineHAProxy
	}
	result, err := validate.Validate(r.Context(), model, req.Target)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, err)
		return
	}
	outcome := "success"
	errMsg := ""
	if result.Native.Skipped {
		outcome = "skipped"
		errMsg = result.Native.Error
	} else if result.Native.ExitCode != 0 {
		outcome = "failed"
		errMsg = result.Native.Stderr
	}
	h.audit(r, r.PathValue("id"), "config.validate", "", req.Target, outcome, errMsg, map[string]any{"issues": len(result.Issues), "native_available": result.Native.Available})
	writeJSON(w, http.StatusOK, result)
}

type deployRequest struct {
	TargetID  string `json:"target_id"`
	ClusterID string `json:"cluster_id"`
	DryRun    *bool  `json:"dry_run"`
}

func (h *Handler) deploy(w http.ResponseWriter, r *http.Request) {
	var req deployRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeProblem(w, http.StatusBadRequest, err)
		return
	}
	dryRun := true
	if req.DryRun != nil {
		dryRun = *req.DryRun
	}
	result, err := deploy.New().Run(r.Context(), h.store, deploy.Request{
		ProjectID: r.PathValue("id"),
		TargetID:  req.TargetID,
		ClusterID: req.ClusterID,
		DryRun:    dryRun,
	})
	if err != nil {
		writeProblem(w, http.StatusBadRequest, err)
		return
	}
	h.audit(r, r.PathValue("id"), "deploy.run", result.SnapshotHash, "", result.Status, "", map[string]any{
		"dry_run":    result.DryRun,
		"target_id":  result.TargetID,
		"cluster_id": result.ClusterID,
		"steps":      len(result.Steps),
	})
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) listAudit(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	events, err := h.store.ListAudit(r.Context(), r.PathValue("id"), limit)
	if err != nil {
		writeProblem(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (h *Handler) listTargets(w http.ResponseWriter, r *http.Request) {
	targets, err := h.store.ListTargets(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProblem(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, targets)
}

func (h *Handler) upsertTarget(w http.ResponseWriter, r *http.Request) {
	var target store.Target
	if err := json.NewDecoder(r.Body).Decode(&target); err != nil {
		writeProblem(w, http.StatusBadRequest, err)
		return
	}
	target, err := h.store.UpsertTarget(r.Context(), r.PathValue("id"), target)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, err)
		return
	}
	h.audit(r, r.PathValue("id"), "target.upsert", "", target.Engine, "success", "", map[string]any{"target_id": target.ID, "host": target.Host})
	writeJSON(w, http.StatusOK, target)
}

func (h *Handler) deleteTarget(w http.ResponseWriter, r *http.Request) {
	if err := h.store.DeleteTarget(r.Context(), r.PathValue("id"), r.PathValue("targetID")); err != nil {
		writeProblem(w, http.StatusNotFound, err)
		return
	}
	h.audit(r, r.PathValue("id"), "target.delete", "", "", "success", "", map[string]any{"target_id": r.PathValue("targetID")})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) upsertCluster(w http.ResponseWriter, r *http.Request) {
	var cluster store.Cluster
	if err := json.NewDecoder(r.Body).Decode(&cluster); err != nil {
		writeProblem(w, http.StatusBadRequest, err)
		return
	}
	cluster, err := h.store.UpsertCluster(r.Context(), r.PathValue("id"), cluster)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, err)
		return
	}
	h.audit(r, r.PathValue("id"), "cluster.upsert", "", "", "success", "", map[string]any{"cluster_id": cluster.ID, "targets": len(cluster.TargetIDs)})
	writeJSON(w, http.StatusOK, cluster)
}

func (h *Handler) deleteCluster(w http.ResponseWriter, r *http.Request) {
	if err := h.store.DeleteCluster(r.Context(), r.PathValue("id"), r.PathValue("clusterID")); err != nil {
		writeProblem(w, http.StatusNotFound, err)
		return
	}
	h.audit(r, r.PathValue("id"), "cluster.delete", "", "", "success", "", map[string]any{"cluster_id": r.PathValue("clusterID")})
	w.WriteHeader(http.StatusNoContent)
}

func hasErrors(issues []ir.Issue) bool {
	for _, issue := range issues {
		if issue.Severity == ir.SeverityError {
			return true
		}
	}
	return false
}

func (h *Handler) audit(r *http.Request, projectID, action, snapshot string, target ir.Engine, outcome, errMsg string, metadata map[string]any) {
	if projectID == "" {
		return
	}
	_ = h.store.AppendAudit(r.Context(), store.AuditEvent{
		ProjectID:      projectID,
		Actor:          actorFromRequest(r),
		Action:         action,
		IRSnapshotHash: snapshot,
		TargetEngine:   target,
		Outcome:        outcome,
		ErrorMessage:   truncate(errMsg, 500),
		Metadata:       metadata,
	})
}

func actorFromRequest(r *http.Request) string {
	if v := r.Header.Get("X-Mizan-Actor"); v != "" {
		return v
	}
	return "local"
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeProblem(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{
		"type":   "about:blank",
		"title":  http.StatusText(status),
		"status": status,
		"detail": err.Error(),
	})
}
