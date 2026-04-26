package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mizanproxy/mizan/internal/ir"
)

func TestStoreSnapshotsTagsAndRevert(t *testing.T) {
	ctx := context.Background()
	st := New(t.TempDir())
	meta, model, version, err := st.CreateProject(ctx, "edge", "", []ir.Engine{ir.EngineHAProxy})
	if err != nil {
		t.Fatal(err)
	}
	model.Description = "changed"
	version2, err := st.SaveIR(ctx, meta.ID, model, version)
	if err != nil {
		t.Fatal(err)
	}
	snapshots, err := st.ListSnapshots(ctx, meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) < 2 {
		t.Fatalf("expected snapshots, got %v", snapshots)
	}
	tag, err := st.TagSnapshot(ctx, meta.ID, version2[:12], "release")
	if err != nil {
		t.Fatal(err)
	}
	if tag.Label != "release" {
		t.Fatalf("unexpected tag: %+v", tag)
	}
	reverted, _, err := st.RevertSnapshot(ctx, meta.ID, "release", "")
	if err != nil {
		t.Fatal(err)
	}
	if reverted.Description != "changed" {
		t.Fatalf("unexpected reverted model: %+v", reverted)
	}
}

func TestAuditAppendAndList(t *testing.T) {
	ctx := context.Background()
	st := New(t.TempDir())
	meta, _, _, err := st.CreateProject(ctx, "edge", "", []ir.Engine{ir.EngineHAProxy})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendAudit(ctx, AuditEvent{ProjectID: meta.ID, Actor: "test", Action: "ir.patch", IRSnapshotHash: "abc", Outcome: "success"}); err != nil {
		t.Fatal(err)
	}
	events, err := st.ListAudit(ctx, meta.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events=%d", len(events))
	}
	if events[0].Action != "ir.patch" || events[0].Actor != "test" {
		t.Fatalf("unexpected event: %+v", events[0])
	}
}

func TestStoreProjectImportListDeleteAndConflicts(t *testing.T) {
	ctx := context.Background()
	st := New(t.TempDir())
	model := ir.EmptyModel("", "imported", "", []ir.Engine{ir.EngineNginx})
	meta, imported, version, err := st.ImportProject(ctx, "", "", model)
	if err != nil {
		t.Fatal(err)
	}
	if imported.ID != meta.ID || version == "" {
		t.Fatalf("unexpected import result: %+v %+v %q", meta, imported, version)
	}
	projects, err := st.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 {
		t.Fatalf("projects=%d", len(projects))
	}
	if _, err := st.GetProject(ctx, meta.ID); err != nil {
		t.Fatal(err)
	}
	imported.Description = "changed"
	if _, err := st.SaveIR(ctx, meta.ID, imported, "wrong-version"); err != ErrVersionConflict {
		t.Fatalf("expected conflict, got %v", err)
	}
	if _, err := st.ListSnapshotTags(ctx, meta.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.TagSnapshot(ctx, meta.ID, version, ""); err == nil {
		t.Fatal("expected empty tag label error")
	}
	if err := st.DeleteProject(ctx, meta.ID); err != nil {
		t.Fatal(err)
	}
	projects, err = st.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 0 {
		t.Fatalf("projects after delete=%d", len(projects))
	}
}

func TestEmptyCollections(t *testing.T) {
	ctx := context.Background()
	st := New(t.TempDir())
	if got := st.Root(); got == "" {
		t.Fatal("root should not be empty")
	}
	if snapshots, err := st.ListSnapshots(ctx, "missing"); err == nil && len(snapshots) != 0 {
		t.Fatalf("unexpected snapshots: %v", snapshots)
	}
	if audit, err := st.ListAudit(ctx, "missing", 10); err != nil || len(audit) != 0 {
		t.Fatalf("audit=%v err=%v", audit, err)
	}
}

func TestDefaultRootEnvAndStoreErrorBranches(t *testing.T) {
	old := os.Getenv("MIZAN_HOME")
	t.Cleanup(func() { _ = os.Setenv("MIZAN_HOME", old) })
	if err := os.Setenv("MIZAN_HOME", "custom-home"); err != nil {
		t.Fatal(err)
	}
	if DefaultRoot() != "custom-home" {
		t.Fatalf("DefaultRoot did not honor env: %q", DefaultRoot())
	}

	ctx := context.Background()
	st := New(t.TempDir())
	model := ir.EmptyModel("missing", "missing", "", []ir.Engine{ir.EngineHAProxy})
	if _, err := st.SaveIR(ctx, "missing", model, ""); err == nil {
		t.Fatal("expected missing project save error")
	}
	if _, _, err := st.GetSnapshot(ctx, "missing", "nope"); err == nil {
		t.Fatal("expected missing snapshot error")
	}
	if _, _, err := st.RevertSnapshot(ctx, "missing", "nope", ""); err == nil {
		t.Fatal("expected missing revert error")
	}
	if err := st.AppendAudit(ctx, AuditEvent{Action: "bad"}); err == nil {
		t.Fatal("expected audit missing project id error")
	}
}

func TestDefaultRootFallbackAndNew(t *testing.T) {
	old := os.Getenv("MIZAN_HOME")
	t.Cleanup(func() { _ = os.Setenv("MIZAN_HOME", old) })
	if err := os.Unsetenv("MIZAN_HOME"); err != nil {
		t.Fatal(err)
	}
	if got := DefaultRoot(); got == "" || !strings.Contains(got, ".mizan") {
		t.Fatalf("unexpected default root: %q", got)
	}
	if got := New("").Root(); got == "" || !strings.Contains(got, ".mizan") {
		t.Fatalf("New did not use default root: %q", got)
	}
	originalUserHomeDir := userHomeDir
	userHomeDir = func() (string, error) {
		return "", errors.New("no home")
	}
	if got := DefaultRoot(); got != ".mizan" {
		t.Fatalf("expected local fallback root, got %q", got)
	}
	userHomeDir = originalUserHomeDir
}

func TestAuditLimitAndDefaults(t *testing.T) {
	ctx := context.Background()
	st := New(t.TempDir())
	meta, _, _, err := st.CreateProject(ctx, "edge", "", []ir.Engine{ir.EngineHAProxy})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AppendAudit(ctx, AuditEvent{ProjectID: meta.ID, Action: "older", Timestamp: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendAudit(ctx, AuditEvent{ProjectID: meta.ID, Action: "newer"}); err != nil {
		t.Fatal(err)
	}
	events, err := st.ListAudit(ctx, meta.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Action != "newer" || events[0].Actor != "local" || events[0].Outcome != "success" {
		t.Fatalf("unexpected limited audit events: %+v", events)
	}
}

func TestStoreAdditionalBranches(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st := New(root)
	first, firstModel, firstVersion, err := st.CreateProject(ctx, "first", "", []ir.Engine{ir.EngineHAProxy})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	second, _, _, err := st.CreateProject(ctx, "second", "", []ir.Engine{ir.EngineNginx})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "projects", "plain-file"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "projects", "bad-meta"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "projects", "bad-meta", "project.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	projects, err := st.ListProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 2 || projects[0].ID != second.ID {
		t.Fatalf("unexpected sorted projects: %+v", projects)
	}

	importModel := ir.EmptyModel("", "model-name", "model-desc", []ir.Engine{ir.EngineNginx})
	imported, model, _, err := st.ImportProject(ctx, "override-name", "override-desc", importModel)
	if err != nil {
		t.Fatal(err)
	}
	if imported.Name != "override-name" || model.Description != "override-desc" {
		t.Fatalf("import overrides not applied: %+v %+v", imported, model)
	}

	firstModel.Metadata = map[string]any{"bad": func() {}}
	if _, err := st.SaveIR(ctx, first.ID, firstModel, ""); err == nil {
		t.Fatal("expected SaveIR hash error")
	}
	firstModel.Metadata = nil
	firstModel.Description = "current"
	if err := os.WriteFile(st.metaPath(first.ID), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	currentVersion, err := st.SaveIR(ctx, first.ID, firstModel, "")
	if err != nil {
		t.Fatalf("save should ignore corrupt metadata refresh: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(st.projectDir(first.ID), "snapshots"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(st.projectDir(first.ID), "snapshots", "bad-snapshot.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.GetSnapshot(ctx, first.ID, "bad-snapshot"); err == nil {
		t.Fatal("expected corrupt snapshot error")
	}
	if _, err := st.ListSnapshotTags(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.TagSnapshot(ctx, first.ID, currentVersion[:12], "release"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.TagSnapshot(ctx, first.ID, currentVersion[:12], "release"); err != nil {
		t.Fatal(err)
	}
	tags, err := st.ListSnapshotTags(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 1 || tags[0].Label != "release" {
		t.Fatalf("unexpected replaced tags: %+v", tags)
	}
	if _, _, err := st.RevertSnapshot(ctx, first.ID, "release", firstVersion); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("expected revert conflict, got %v", err)
	}
	if err := os.WriteFile(st.tagsPath(first.ID), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ListSnapshotTags(ctx, first.ID); err == nil {
		t.Fatal("expected corrupt tags error")
	}
	if _, err := st.TagSnapshot(ctx, first.ID, currentVersion[:12], "new-release"); err == nil {
		t.Fatal("expected tag snapshot readTags error")
	}
	if ref, ok := st.resolveTag(first.ID, "release"); ok || ref != "" {
		t.Fatalf("expected corrupt tags to be ignored by resolveTag, got %q %v", ref, ok)
	}
}

func TestStoreFilesystemErrorBranches(t *testing.T) {
	ctx := context.Background()
	rootFile := filepath.Join(t.TempDir(), "root-file")
	if err := os.WriteFile(rootFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := New(rootFile)
	if err := st.Bootstrap(ctx); err == nil {
		t.Fatal("expected bootstrap error when root is a file")
	}
	if _, err := st.ListProjects(ctx); err == nil {
		t.Fatal("expected list projects bootstrap error")
	}
	if _, _, _, err := st.CreateProject(ctx, "bad", "", []ir.Engine{ir.EngineHAProxy}); err == nil {
		t.Fatal("expected create project bootstrap error")
	}
	if _, _, _, err := st.ImportProject(ctx, "", "", ir.EmptyModel("", "bad", "", []ir.Engine{ir.EngineHAProxy})); err == nil {
		t.Fatal("expected import project bootstrap error")
	}
}

func TestStoreReadErrors(t *testing.T) {
	ctx := context.Background()
	st := New(t.TempDir())
	meta, model, version, err := st.CreateProject(ctx, "edge", "", []ir.Engine{ir.EngineHAProxy})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SaveIR(ctx, meta.ID, model, version); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.auditPath(meta.ID), []byte("{bad json}\n{}\n"+strings.Repeat("x", 70_000)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ListAudit(ctx, meta.ID, 0); err == nil {
		t.Fatal("expected audit scanner error")
	}
}

func TestStoreInjectedFailureBranches(t *testing.T) {
	ctx := context.Background()
	st := New(t.TempDir())

	originalMkdirAll := mkdirAll
	mkdirAll = func(string, os.FileMode) error {
		return errors.New("mkdir failed")
	}
	if _, _, _, err := st.CreateProject(ctx, "edge", "", []ir.Engine{ir.EngineHAProxy}); err == nil {
		t.Fatal("expected create project mkdir error")
	}
	if _, _, _, err := st.ImportProject(ctx, "", "", ir.EmptyModel("", "edge", "", []ir.Engine{ir.EngineHAProxy})); err == nil {
		t.Fatal("expected import project mkdir error")
	}
	if err := st.AppendAudit(ctx, AuditEvent{ProjectID: "p_1", Action: "audit"}); err == nil {
		t.Fatal("expected append audit mkdir error")
	}
	if err := st.writeSnapshot("p_1", strings.Repeat("a", 64), ir.EmptyModel("p_1", "edge", "", []ir.Engine{ir.EngineHAProxy})); err == nil {
		t.Fatal("expected write snapshot mkdir error")
	}
	mkdirAll = originalMkdirAll

	meta, model, version, err := st.CreateProject(ctx, "edge", "", []ir.Engine{ir.EngineHAProxy})
	if err != nil {
		t.Fatal(err)
	}

	originalRandRead := randRead
	randRead = func([]byte) (int, error) {
		return 0, errors.New("entropy unavailable")
	}
	if got := newID(); got == "" || strings.HasPrefix(got, "p_") {
		t.Fatalf("expected timestamp fallback id, got %q", got)
	}
	randRead = originalRandRead

	originalCreateTemp := createTemp
	createTemp = func(string, string) (*os.File, error) {
		return nil, errors.New("temp failed")
	}
	if err := writeJSON(filepath.Join(t.TempDir(), "x.json"), map[string]string{"x": "y"}); err == nil {
		t.Fatal("expected writeJSON temp error")
	}
	createTemp = originalCreateTemp

	originalRenameFile := renameFile
	renameFile = func(string, string) error {
		return errors.New("rename failed")
	}
	if _, err := st.SaveIR(ctx, meta.ID, model, version); err == nil {
		t.Fatal("expected SaveIR rename error")
	}
	renameFile = originalRenameFile

	if err := writeJSON(filepath.Join(t.TempDir(), "bad.json"), map[string]any{"bad": func() {}}); err == nil {
		t.Fatal("expected writeJSON marshal error")
	}
	badModel := ir.EmptyModel("p_1", "bad", "", []ir.Engine{ir.EngineHAProxy})
	badModel.Metadata = map[string]any{"bad": func() {}}
	if err := writeCanonical(filepath.Join(t.TempDir(), "bad-model.json"), badModel); err == nil {
		t.Fatal("expected writeCanonical marshal error")
	}
	if err := st.AppendAudit(ctx, AuditEvent{ProjectID: meta.ID, Action: "audit", Metadata: map[string]any{"bad": func() {}}}); err == nil {
		t.Fatal("expected append audit marshal error")
	}
}
