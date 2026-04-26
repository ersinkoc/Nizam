package store

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mizanproxy/mizan/internal/ir"
)

type Store struct {
	root string
	mu   sync.Mutex
}

type ProjectMeta struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Engines     []ir.Engine `json:"engines"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
	Template    bool        `json:"template,omitempty"`
}

type SnapshotTag struct {
	Label     string    `json:"label"`
	Ref       string    `json:"ref"`
	CreatedAt time.Time `json:"created_at"`
}

type AuditEvent struct {
	EventID        string         `json:"event_id"`
	ProjectID      string         `json:"project_id"`
	Timestamp      time.Time      `json:"timestamp"`
	Actor          string         `json:"actor"`
	Action         string         `json:"action"`
	IRSnapshotHash string         `json:"ir_snapshot_hash,omitempty"`
	TargetEngine   ir.Engine      `json:"target_engine,omitempty"`
	Outcome        string         `json:"outcome"`
	ErrorMessage   string         `json:"error_message,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

var (
	userHomeDir = os.UserHomeDir
	mkdirAll    = os.MkdirAll
	createTemp  = os.CreateTemp
	renameFile  = os.Rename
	randRead    = rand.Read
)

func DefaultRoot() string {
	if v := os.Getenv("MIZAN_HOME"); v != "" {
		return v
	}
	home, err := userHomeDir()
	if err != nil {
		return ".mizan"
	}
	return filepath.Join(home, ".mizan")
}

func New(root string) *Store {
	if root == "" {
		root = DefaultRoot()
	}
	return &Store{root: root}
}

func (s *Store) Root() string { return s.root }

func (s *Store) Bootstrap(ctx context.Context) error {
	return mkdirAll(filepath.Join(s.root, "projects"), 0o755)
}

func (s *Store) ListProjects(ctx context.Context) ([]ProjectMeta, error) {
	if err := s.Bootstrap(ctx); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "projects"))
	if err != nil {
		return nil, err
	}
	projects := make([]ProjectMeta, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		meta, err := s.GetProject(ctx, entry.Name())
		if err == nil {
			projects = append(projects, meta)
		}
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].UpdatedAt.After(projects[j].UpdatedAt) })
	return projects, nil
}

func (s *Store) CreateProject(ctx context.Context, name, description string, engines []ir.Engine) (ProjectMeta, *ir.Model, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.Bootstrap(ctx); err != nil {
		return ProjectMeta{}, nil, "", err
	}
	id := newID()
	now := time.Now().UTC()
	meta := ProjectMeta{ID: id, Name: name, Description: description, Engines: engines, CreatedAt: now, UpdatedAt: now}
	model := ir.EmptyModel(id, name, description, engines)
	if err := mkdirAll(s.projectDir(id), 0o755); err != nil {
		return ProjectMeta{}, nil, "", err
	}
	if err := writeJSON(s.metaPath(id), meta); err != nil {
		return ProjectMeta{}, nil, "", err
	}
	version, err := s.saveIRLocked(id, model, "")
	if err != nil {
		return ProjectMeta{}, nil, "", err
	}
	return meta, model, version, nil
}

func (s *Store) ImportProject(ctx context.Context, name, description string, model *ir.Model) (ProjectMeta, *ir.Model, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.Bootstrap(ctx); err != nil {
		return ProjectMeta{}, nil, "", err
	}
	id := newID()
	now := time.Now().UTC()
	model.ID = id
	if name != "" {
		model.Name = name
	}
	if description != "" {
		model.Description = description
	}
	meta := ProjectMeta{ID: id, Name: model.Name, Description: model.Description, Engines: model.Engines, CreatedAt: now, UpdatedAt: now}
	if err := mkdirAll(s.projectDir(id), 0o755); err != nil {
		return ProjectMeta{}, nil, "", err
	}
	if err := writeJSON(s.metaPath(id), meta); err != nil {
		return ProjectMeta{}, nil, "", err
	}
	version, err := s.saveIRLocked(id, model, "")
	if err != nil {
		return ProjectMeta{}, nil, "", err
	}
	return meta, model, version, nil
}

func (s *Store) GetProject(ctx context.Context, id string) (ProjectMeta, error) {
	var meta ProjectMeta
	return meta, readJSON(s.metaPath(id), &meta)
}

func (s *Store) DeleteProject(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return os.RemoveAll(s.projectDir(id))
}

func (s *Store) GetIR(ctx context.Context, id string) (*ir.Model, string, error) {
	var model ir.Model
	if err := readJSON(s.configPath(id), &model); err != nil {
		return nil, "", err
	}
	version, err := ir.Hash(&model)
	return &model, version, err
}

func (s *Store) SaveIR(ctx context.Context, id string, model *ir.Model, ifMatch string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveIRLocked(id, model, ifMatch)
}

func (s *Store) saveIRLocked(id string, model *ir.Model, ifMatch string) (string, error) {
	if _, err := os.Stat(s.projectDir(id)); err != nil {
		return "", err
	}
	current, currentVersion, err := s.GetIR(context.Background(), id)
	if err == nil && ifMatch != "" && currentVersion != ifMatch {
		_ = current
		return currentVersion, ErrVersionConflict
	}
	model.ID = id
	version, err := ir.Hash(model)
	if err != nil {
		return "", err
	}
	if err := writeCanonical(s.configPath(id), model); err != nil {
		return "", err
	}
	if err := s.writeSnapshot(id, version, model); err != nil {
		return "", err
	}
	var meta ProjectMeta
	if err := readJSON(s.metaPath(id), &meta); err == nil {
		meta.Name = model.Name
		meta.Description = model.Description
		meta.Engines = model.Engines
		meta.UpdatedAt = time.Now().UTC()
		_ = writeJSON(s.metaPath(id), meta)
	}
	return version, nil
}

func (s *Store) ListSnapshots(ctx context.Context, id string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.projectDir(id), "snapshots"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			out = append(out, entry.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out, nil
}

func (s *Store) GetSnapshot(ctx context.Context, id, ref string) (*ir.Model, string, error) {
	if tagRef, ok := s.resolveTag(id, ref); ok {
		ref = tagRef
	}
	dir := filepath.Join(s.projectDir(id), "snapshots")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, "", err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == ref || strings.Contains(name, ref) {
			var model ir.Model
			if err := readJSON(filepath.Join(dir, name), &model); err != nil {
				return nil, "", err
			}
			version, err := ir.Hash(&model)
			return &model, version, err
		}
	}
	return nil, "", os.ErrNotExist
}

func (s *Store) ListSnapshotTags(ctx context.Context, id string) ([]SnapshotTag, error) {
	tags, err := s.readTags(id)
	if err != nil {
		return nil, err
	}
	sort.Slice(tags, func(i, j int) bool { return tags[i].CreatedAt.After(tags[j].CreatedAt) })
	return tags, nil
}

func (s *Store) TagSnapshot(ctx context.Context, id, ref, label string) (SnapshotTag, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if label == "" {
		return SnapshotTag{}, errors.New("label is required")
	}
	if _, _, err := s.GetSnapshot(ctx, id, ref); err != nil {
		return SnapshotTag{}, err
	}
	tags, err := s.readTags(id)
	if err != nil {
		return SnapshotTag{}, err
	}
	tag := SnapshotTag{Label: label, Ref: ref, CreatedAt: time.Now().UTC()}
	replaced := false
	for i := range tags {
		if tags[i].Label == label {
			tags[i] = tag
			replaced = true
		}
	}
	if !replaced {
		tags = append(tags, tag)
	}
	return tag, writeJSON(s.tagsPath(id), tags)
}

func (s *Store) RevertSnapshot(ctx context.Context, id, ref, ifMatch string) (*ir.Model, string, error) {
	model, _, err := s.GetSnapshot(ctx, id, ref)
	if err != nil {
		return nil, "", err
	}
	version, err := s.SaveIR(ctx, id, model, ifMatch)
	if err != nil {
		return nil, "", err
	}
	return model, version, nil
}

func (s *Store) AppendAudit(ctx context.Context, event AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if event.ProjectID == "" {
		return errors.New("project id is required")
	}
	if event.EventID == "" {
		event.EventID = newID()
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	if event.Actor == "" {
		event.Actor = "local"
	}
	if event.Outcome == "" {
		event.Outcome = "success"
	}
	if err := mkdirAll(s.projectDir(event.ProjectID), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(event)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.auditPath(event.ProjectID), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

func (s *Store) ListAudit(ctx context.Context, id string, limit int) ([]AuditEvent, error) {
	f, err := os.Open(s.auditPath(id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []AuditEvent{}, nil
		}
		return nil, err
	}
	defer f.Close()
	var events []AuditEvent
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var event AuditEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err == nil {
			events = append(events, event)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Timestamp.After(events[j].Timestamp) })
	if limit > 0 && len(events) > limit {
		events = events[:limit]
	}
	return events, nil
}

func (s *Store) writeSnapshot(id, hash string, model *ir.Model) error {
	dir := filepath.Join(s.projectDir(id), "snapshots")
	if err := mkdirAll(dir, 0o755); err != nil {
		return err
	}
	name := time.Now().UTC().Format("20060102T150405") + "-" + hash[:12] + ".json"
	return writeCanonical(filepath.Join(dir, name), model)
}

func (s *Store) projectDir(id string) string { return filepath.Join(s.root, "projects", id) }
func (s *Store) metaPath(id string) string   { return filepath.Join(s.projectDir(id), "project.json") }
func (s *Store) configPath(id string) string { return filepath.Join(s.projectDir(id), "config.json") }
func (s *Store) tagsPath(id string) string {
	return filepath.Join(s.projectDir(id), "snapshot-tags.json")
}
func (s *Store) auditPath(id string) string { return filepath.Join(s.projectDir(id), "audit.jsonl") }

var ErrVersionConflict = errors.New("version conflict")

func newID() string {
	var b [8]byte
	if _, err := randRead(b[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().Format("150405.000000")))
	}
	return "p_" + hex.EncodeToString(b[:])
}

func readJSON(path string, out any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(b, '\n'))
}

func writeCanonical(path string, model *ir.Model) error {
	b, err := ir.CanonicalJSON(model)
	if err != nil {
		return err
	}
	return atomicWrite(path, append(b, '\n'))
}

func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := mkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := createTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return renameFile(tmpName, path)
}

func (s *Store) readTags(id string) ([]SnapshotTag, error) {
	var tags []SnapshotTag
	if err := readJSON(s.tagsPath(id), &tags); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []SnapshotTag{}, nil
		}
		return nil, err
	}
	return tags, nil
}

func (s *Store) resolveTag(id, label string) (string, bool) {
	tags, err := s.readTags(id)
	if err != nil {
		return "", false
	}
	for _, tag := range tags {
		if tag.Label == label {
			return tag.Ref, true
		}
	}
	return "", false
}
