package store

import (
	"context"
	"errors"
	"io/fs"
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
	base := time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC)
	for _, event := range []AuditEvent{
		{ProjectID: meta.ID, Actor: "Alice", Action: "config.generate", Outcome: "success", TargetEngine: ir.EngineHAProxy, Timestamp: base},
		{ProjectID: meta.ID, Actor: "Bob", Action: "target.probe", Outcome: "failed", TargetEngine: ir.EngineNginx, Timestamp: base.Add(time.Hour)},
		{ProjectID: meta.ID, Actor: "Alice", Action: "target.probe", Outcome: "success", TargetEngine: ir.EngineNginx, Timestamp: base.Add(2 * time.Hour)},
	} {
		if err := st.AppendAudit(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name   string
		filter AuditFilter
		want   string
	}{
		{"from", AuditFilter{From: base.Add(30 * time.Minute)}, "target.probe"},
		{"to", AuditFilter{To: base.Add(30 * time.Minute)}, "config.generate"},
		{"actor", AuditFilter{Actor: "alice", Limit: 1}, "target.probe"},
		{"action", AuditFilter{Action: "config.generate"}, "config.generate"},
		{"outcome", AuditFilter{Outcome: "failed"}, "target.probe"},
		{"target", AuditFilter{TargetEngine: ir.EngineHAProxy}, "config.generate"},
		{"all", AuditFilter{Actor: "bob", Action: "target.probe", Outcome: "failed", TargetEngine: ir.EngineNginx, From: base.Add(30 * time.Minute), To: base.Add(90 * time.Minute)}, "target.probe"},
	} {
		events, err := st.ListAuditFiltered(ctx, meta.ID, tc.filter)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) == 0 || events[0].Action != tc.want {
			t.Fatalf("%s events=%+v", tc.name, events)
		}
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

func TestStoreInjectedOSBranches(t *testing.T) {
	ctx := context.Background()
	st := New(t.TempDir())
	meta, model, version, err := st.CreateProject(ctx, "edge", "", []ir.Engine{ir.EngineHAProxy})
	if err != nil {
		t.Fatal(err)
	}
	originalReadDir := readDir
	originalRemoveAll := removeAll
	originalStatPath := statPath
	originalOpenFile := openFile
	originalOpenPath := openPath
	originalTempWrite := tempWrite
	originalTempSync := tempSync
	originalTempClose := tempClose
	t.Cleanup(func() {
		readDir = originalReadDir
		removeAll = originalRemoveAll
		statPath = originalStatPath
		openFile = originalOpenFile
		openPath = originalOpenPath
		tempWrite = originalTempWrite
		tempSync = originalTempSync
		tempClose = originalTempClose
	})

	readDir = func(string) ([]fs.DirEntry, error) {
		return nil, errors.New("readdir failed")
	}
	if _, err := st.ListProjects(ctx); err == nil {
		t.Fatal("expected list projects readdir error")
	}
	if _, err := st.ListSnapshots(ctx, meta.ID); err == nil {
		t.Fatal("expected list snapshots readdir error")
	}
	if _, _, err := st.GetSnapshot(ctx, meta.ID, "missing"); err == nil {
		t.Fatal("expected get snapshot readdir error")
	}
	readDir = originalReadDir

	removeAll = func(string) error {
		return errors.New("remove failed")
	}
	if err := st.DeleteProject(ctx, meta.ID); err == nil {
		t.Fatal("expected delete project remove error")
	}
	removeAll = originalRemoveAll

	statPath = func(string) (os.FileInfo, error) {
		return nil, errors.New("stat failed")
	}
	if _, err := st.SaveIR(ctx, meta.ID, model, version); err == nil {
		t.Fatal("expected save stat error")
	}
	if _, err := st.ListTargets(ctx, meta.ID); err == nil {
		t.Fatal("expected targets stat error")
	}
	statPath = originalStatPath

	openFile = func(string, int, os.FileMode) (*os.File, error) {
		return nil, errors.New("openfile failed")
	}
	if err := st.AppendAudit(ctx, AuditEvent{ProjectID: meta.ID, Action: "audit"}); err == nil {
		t.Fatal("expected append audit openfile error")
	}
	openFile = originalOpenFile

	openPath = func(string) (*os.File, error) {
		return nil, errors.New("open failed")
	}
	if _, err := st.ListAudit(ctx, meta.ID, 0); err == nil {
		t.Fatal("expected list audit open error")
	}
	openPath = originalOpenPath

	path := filepath.Join(t.TempDir(), "atomic.json")
	tempWrite = func(*os.File, []byte) (int, error) {
		return 0, errors.New("write failed")
	}
	if err := atomicWrite(path, []byte("{}")); err == nil {
		t.Fatal("expected atomic write data error")
	}
	tempWrite = originalTempWrite

	tempSync = func(*os.File) error {
		return errors.New("sync failed")
	}
	if err := atomicWrite(path, []byte("{}")); err == nil {
		t.Fatal("expected atomic write sync error")
	}
	tempSync = originalTempSync

	tempClose = func(tmp *os.File) error {
		_ = tmp.Close()
		return errors.New("close failed")
	}
	if err := atomicWrite(path, []byte("{}")); err == nil {
		t.Fatal("expected atomic write close error")
	}
	tempClose = originalTempClose
}

func TestStoreCreateImportAndSnapshotWriteBranches(t *testing.T) {
	ctx := context.Background()
	st := New(t.TempDir())
	originalMkdirAll := mkdirAll
	originalRenameFile := renameFile
	originalTempSync := tempSync
	t.Cleanup(func() {
		mkdirAll = originalMkdirAll
		renameFile = originalRenameFile
		tempSync = originalTempSync
	})

	mkdirCalls := 0
	mkdirAll = func(path string, mode os.FileMode) error {
		mkdirCalls++
		if mkdirCalls == 2 {
			return errors.New("project mkdir failed")
		}
		return originalMkdirAll(path, mode)
	}
	if _, _, _, err := st.CreateProject(ctx, "edge", "", []ir.Engine{ir.EngineHAProxy}); err == nil {
		t.Fatal("expected create project directory error")
	}
	mkdirCalls = 0
	if _, _, _, err := st.ImportProject(ctx, "", "", ir.EmptyModel("", "edge", "", []ir.Engine{ir.EngineHAProxy})); err == nil {
		t.Fatal("expected import project directory error")
	}
	mkdirAll = originalMkdirAll

	renameFile = func(string, string) error {
		return errors.New("metadata write failed")
	}
	if _, _, _, err := st.CreateProject(ctx, "edge", "", []ir.Engine{ir.EngineHAProxy}); err == nil {
		t.Fatal("expected create metadata write error")
	}
	if _, _, _, err := st.ImportProject(ctx, "", "", ir.EmptyModel("", "edge", "", []ir.Engine{ir.EngineHAProxy})); err == nil {
		t.Fatal("expected import metadata write error")
	}
	renameFile = originalRenameFile

	syncCalls := 0
	tempSync = func(tmp *os.File) error {
		syncCalls++
		if syncCalls == 3 {
			return errors.New("snapshot write failed")
		}
		return originalTempSync(tmp)
	}
	if _, _, _, err := st.CreateProject(ctx, "edge", "", []ir.Engine{ir.EngineHAProxy}); err == nil {
		t.Fatal("expected create snapshot write error")
	}
	syncCalls = 0
	if _, _, _, err := st.ImportProject(ctx, "", "", ir.EmptyModel("", "edge", "", []ir.Engine{ir.EngineHAProxy})); err == nil {
		t.Fatal("expected import snapshot write error")
	}
	tempSync = originalTempSync
}

func TestStoreRemainingBranchCoverage(t *testing.T) {
	ctx := context.Background()
	st := New(t.TempDir())
	meta, model, version, err := st.CreateProject(ctx, "edge", "", []ir.Engine{ir.EngineHAProxy})
	if err != nil {
		t.Fatal(err)
	}
	model.Description = "second"
	version2, err := st.SaveIR(ctx, meta.ID, model, version)
	if err != nil {
		t.Fatal(err)
	}
	snaps, err := st.ListSnapshots(ctx, meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) < 2 {
		t.Fatalf("expected at least two snapshots: %v", snaps)
	}
	firstTag, err := st.TagSnapshot(ctx, meta.ID, version[:12], "older")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	secondTag, err := st.TagSnapshot(ctx, meta.ID, version2[:12], "newer")
	if err != nil {
		t.Fatal(err)
	}
	tags, err := st.ListSnapshotTags(ctx, meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 2 || tags[0].Label != secondTag.Label || tags[1].Label != firstTag.Label {
		t.Fatalf("tags were not sorted newest first: %+v", tags)
	}

	originalRenameFile := renameFile
	renameFile = func(string, string) error {
		return errors.New("tag write failed")
	}
	if _, err := st.TagSnapshot(ctx, meta.ID, version2[:12], "write-error"); err == nil {
		t.Fatal("expected tag write error")
	}
	renameFile = originalRenameFile

	originalMkdirAll := mkdirAll
	mkdirAll = func(string, os.FileMode) error {
		return errors.New("atomic mkdir failed")
	}
	if err := atomicWrite(filepath.Join(t.TempDir(), "atomic.json"), []byte("{}")); err == nil {
		t.Fatal("expected atomic mkdir error")
	}
	mkdirAll = originalMkdirAll

	if _, _, err := st.GetSnapshot(ctx, meta.ID, "definitely-missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected missing snapshot ref error, got %v", err)
	}
	if _, err := st.TagSnapshot(ctx, meta.ID, "definitely-missing", "missing-ref"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected tag missing snapshot error, got %v", err)
	}

	originalOpenFile := openFile
	readOnlyPath := filepath.Join(t.TempDir(), "readonly-audit.jsonl")
	if err := os.WriteFile(readOnlyPath, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	openFile = func(string, int, os.FileMode) (*os.File, error) {
		return os.Open(readOnlyPath)
	}
	if err := st.AppendAudit(ctx, AuditEvent{ProjectID: meta.ID, Action: "audit"}); err == nil {
		t.Fatal("expected append audit write error")
	}
	openFile = originalOpenFile
}
