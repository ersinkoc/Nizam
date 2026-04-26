package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/mizanproxy/mizan/internal/ir"
)

type Target struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Host            string    `json:"host"`
	Port            int       `json:"port"`
	User            string    `json:"user"`
	Engine          ir.Engine `json:"engine"`
	ConfigPath      string    `json:"config_path"`
	ReloadCommand   string    `json:"reload_command"`
	Sudo            bool      `json:"sudo"`
	PostReloadProbe string    `json:"post_reload_probe,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type Cluster struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	TargetIDs     []string  `json:"target_ids"`
	Parallelism   int       `json:"parallelism"`
	GateOnFailure bool      `json:"gate_on_failure"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type TargetsFile struct {
	Targets  []Target  `json:"targets"`
	Clusters []Cluster `json:"clusters"`
}

func (s *Store) ListTargets(ctx context.Context, projectID string) (TargetsFile, error) {
	targets, err := s.readTargets(projectID)
	if err != nil {
		return TargetsFile{}, err
	}
	sort.Slice(targets.Targets, func(i, j int) bool { return targets.Targets[i].Name < targets.Targets[j].Name })
	sort.Slice(targets.Clusters, func(i, j int) bool { return targets.Clusters[i].Name < targets.Clusters[j].Name })
	return targets, nil
}

func (s *Store) UpsertTarget(ctx context.Context, projectID string, target Target) (Target, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if target.Name == "" {
		return Target{}, errors.New("target name is required")
	}
	if target.Host == "" {
		return Target{}, errors.New("target host is required")
	}
	if target.Engine == "" {
		target.Engine = ir.EngineHAProxy
	}
	if target.Port == 0 {
		target.Port = 22
	}
	if target.User == "" {
		target.User = "root"
	}
	if target.ConfigPath == "" {
		target.ConfigPath = defaultConfigPath(target.Engine)
	}
	if target.ReloadCommand == "" {
		target.ReloadCommand = defaultReloadCommand(target.Engine)
	}
	targets, err := s.readTargets(projectID)
	if err != nil {
		return Target{}, err
	}
	now := time.Now().UTC()
	if target.ID == "" {
		target.ID = newID()
		target.CreatedAt = now
	}
	target.UpdatedAt = now
	replaced := false
	for i := range targets.Targets {
		if targets.Targets[i].ID == target.ID {
			if target.CreatedAt.IsZero() {
				target.CreatedAt = targets.Targets[i].CreatedAt
			}
			targets.Targets[i] = target
			replaced = true
			break
		}
	}
	if !replaced {
		targets.Targets = append(targets.Targets, target)
	}
	if err := writeJSON(s.targetsPath(projectID), targets); err != nil {
		return Target{}, err
	}
	return target, nil
}

func (s *Store) DeleteTarget(ctx context.Context, projectID, targetID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	targets, err := s.readTargets(projectID)
	if err != nil {
		return err
	}
	next := targets.Targets[:0]
	found := false
	for _, target := range targets.Targets {
		if target.ID == targetID {
			found = true
			continue
		}
		next = append(next, target)
	}
	if !found {
		return os.ErrNotExist
	}
	targets.Targets = next
	for i := range targets.Clusters {
		targets.Clusters[i].TargetIDs = removeString(targets.Clusters[i].TargetIDs, targetID)
		targets.Clusters[i].UpdatedAt = time.Now().UTC()
	}
	return writeJSON(s.targetsPath(projectID), targets)
}

func (s *Store) UpsertCluster(ctx context.Context, projectID string, cluster Cluster) (Cluster, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cluster.Name == "" {
		return Cluster{}, errors.New("cluster name is required")
	}
	if cluster.Parallelism <= 0 {
		cluster.Parallelism = 1
	}
	targets, err := s.readTargets(projectID)
	if err != nil {
		return Cluster{}, err
	}
	targetIDs := map[string]bool{}
	for _, target := range targets.Targets {
		targetIDs[target.ID] = true
	}
	for _, id := range cluster.TargetIDs {
		if !targetIDs[id] {
			return Cluster{}, errors.New("cluster references a missing target")
		}
	}
	now := time.Now().UTC()
	if cluster.ID == "" {
		cluster.ID = newID()
		cluster.CreatedAt = now
	}
	cluster.UpdatedAt = now
	replaced := false
	for i := range targets.Clusters {
		if targets.Clusters[i].ID == cluster.ID {
			if cluster.CreatedAt.IsZero() {
				cluster.CreatedAt = targets.Clusters[i].CreatedAt
			}
			targets.Clusters[i] = cluster
			replaced = true
			break
		}
	}
	if !replaced {
		targets.Clusters = append(targets.Clusters, cluster)
	}
	if err := writeJSON(s.targetsPath(projectID), targets); err != nil {
		return Cluster{}, err
	}
	return cluster, nil
}

func (s *Store) DeleteCluster(ctx context.Context, projectID, clusterID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	targets, err := s.readTargets(projectID)
	if err != nil {
		return err
	}
	next := targets.Clusters[:0]
	found := false
	for _, cluster := range targets.Clusters {
		if cluster.ID == clusterID {
			found = true
			continue
		}
		next = append(next, cluster)
	}
	if !found {
		return os.ErrNotExist
	}
	targets.Clusters = next
	return writeJSON(s.targetsPath(projectID), targets)
}

func (s *Store) readTargets(projectID string) (TargetsFile, error) {
	var targets TargetsFile
	if err := readJSON(s.targetsPath(projectID), &targets); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if _, statErr := statPath(s.projectDir(projectID)); statErr != nil {
				return TargetsFile{}, statErr
			}
			return TargetsFile{Targets: []Target{}, Clusters: []Cluster{}}, nil
		}
		return TargetsFile{}, err
	}
	if targets.Targets == nil {
		targets.Targets = []Target{}
	}
	if targets.Clusters == nil {
		targets.Clusters = []Cluster{}
	}
	return targets, nil
}

func (s *Store) targetsPath(id string) string {
	return filepath.Join(s.projectDir(id), "targets.json")
}

func defaultConfigPath(engine ir.Engine) string {
	if engine == ir.EngineNginx {
		return "/etc/nginx/nginx.conf"
	}
	return "/etc/haproxy/haproxy.cfg"
}

func defaultReloadCommand(engine ir.Engine) string {
	if engine == ir.EngineNginx {
		return "systemctl reload nginx"
	}
	return "systemctl reload haproxy"
}

func removeString(items []string, remove string) []string {
	next := items[:0]
	for _, item := range items {
		if item != remove {
			next = append(next, item)
		}
	}
	return next
}
