package deploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/mizanproxy/mizan/internal/ir"
	"github.com/mizanproxy/mizan/internal/store"
	"github.com/mizanproxy/mizan/internal/validate"
)

type Request struct {
	ProjectID string
	TargetID  string
	ClusterID string
	DryRun    bool
}

type Result struct {
	ProjectID    string `json:"project_id"`
	TargetID     string `json:"target_id,omitempty"`
	ClusterID    string `json:"cluster_id,omitempty"`
	SnapshotHash string `json:"snapshot_hash"`
	DryRun       bool   `json:"dry_run"`
	Status       string `json:"status"`
	StartedAt    string `json:"started_at"`
	FinishedAt   string `json:"finished_at"`
	Steps        []Step `json:"steps"`
}

type Step struct {
	TargetID   string    `json:"target_id"`
	TargetName string    `json:"target_name"`
	Engine     ir.Engine `json:"engine"`
	Stage      string    `json:"stage"`
	Status     string    `json:"status"`
	Command    string    `json:"command,omitempty"`
	Message    string    `json:"message,omitempty"`
	Batch      int       `json:"batch"`
}

type Runner func(context.Context, store.Target, string, string) (string, error)
type Prober func(context.Context, string) error

type Deployer struct {
	Runner Runner
	Prober Prober
	Now    func() time.Time
}

func New() Deployer {
	return Deployer{Runner: sshRunner, Prober: httpProbe, Now: func() time.Time { return time.Now().UTC() }}
}

func (d Deployer) Run(ctx context.Context, st *store.Store, req Request) (Result, error) {
	if req.ProjectID == "" {
		return Result{}, errors.New("project_id is required")
	}
	if req.TargetID == "" && req.ClusterID == "" {
		return Result{}, errors.New("target_id or cluster_id is required")
	}
	model, snapshot, err := st.GetIR(ctx, req.ProjectID)
	if err != nil {
		return Result{}, err
	}
	targetsFile, err := st.ListTargets(ctx, req.ProjectID)
	if err != nil {
		return Result{}, err
	}
	selected, parallelism, gate, err := selectTargets(targetsFile, req)
	if err != nil {
		return Result{}, err
	}
	if d.Runner == nil {
		d.Runner = sshRunner
	}
	if d.Prober == nil {
		d.Prober = httpProbe
	}
	if d.Now == nil {
		d.Now = func() time.Time { return time.Now().UTC() }
	}

	started := d.Now()
	result := Result{
		ProjectID:    req.ProjectID,
		TargetID:     req.TargetID,
		ClusterID:    req.ClusterID,
		SnapshotHash: snapshot,
		DryRun:       req.DryRun,
		Status:       "success",
		StartedAt:    started.Format(time.RFC3339),
	}
	for index, target := range selected {
		batch := index/parallelism + 1
		steps := d.runTarget(ctx, model, req.ProjectID, target, batch, req.DryRun)
		result.Steps = append(result.Steps, steps...)
		if hasFailed(steps) {
			result.Status = "failed"
			if gate {
				break
			}
		}
	}
	result.FinishedAt = d.Now().Format(time.RFC3339)
	return result, nil
}

func (d Deployer) runTarget(ctx context.Context, model *ir.Model, projectID string, target store.Target, batch int, dryRun bool) []Step {
	generated, err := validate.Generate(model, target.Engine)
	steps := []Step{{
		TargetID:   target.ID,
		TargetName: target.Name,
		Engine:     target.Engine,
		Stage:      "generate",
		Status:     stepStatus(err, dryRun),
		Message:    errString(err),
		Batch:      batch,
	}}
	if err != nil {
		return steps
	}
	remoteTmp := fmt.Sprintf("/tmp/mizan-%s-%s.cfg", projectID, target.ID)
	commands := []struct {
		stage string
		cmd   string
		input string
	}{
		{"upload", uploadCommand(target, remoteTmp), generated.Config},
		{"remote_validate", remoteValidateCommand(target, remoteTmp), ""},
		{"install", installCommand(target, remoteTmp), ""},
		{"reload", reloadCommand(target), ""},
	}
	for _, item := range commands {
		step := Step{TargetID: target.ID, TargetName: target.Name, Engine: target.Engine, Stage: item.stage, Command: item.cmd, Batch: batch}
		if dryRun {
			step.Status = "skipped"
			step.Message = "dry run"
		} else if output, runErr := d.Runner(ctx, target, item.cmd, item.input); runErr != nil {
			step.Status = "failed"
			step.Message = strings.TrimSpace(output + "\n" + runErr.Error())
			steps = append(steps, step)
			return steps
		} else {
			step.Status = "success"
			step.Message = strings.TrimSpace(output)
		}
		steps = append(steps, step)
	}
	if target.PostReloadProbe != "" {
		step := Step{TargetID: target.ID, TargetName: target.Name, Engine: target.Engine, Stage: "probe", Command: target.PostReloadProbe, Batch: batch}
		if dryRun {
			step.Status = "skipped"
			step.Message = "dry run"
		} else if err := d.Prober(ctx, target.PostReloadProbe); err != nil {
			step.Status = "failed"
			step.Message = err.Error()
			steps = append(steps, step)
			return steps
		} else {
			step.Status = "success"
		}
		steps = append(steps, step)
	}
	return steps
}

func selectTargets(file store.TargetsFile, req Request) ([]store.Target, int, bool, error) {
	if req.TargetID != "" {
		for _, target := range file.Targets {
			if target.ID == req.TargetID {
				return []store.Target{target}, 1, true, nil
			}
		}
		return nil, 0, false, errors.New("target not found")
	}
	for _, cluster := range file.Clusters {
		if cluster.ID == req.ClusterID {
			byID := map[string]store.Target{}
			for _, target := range file.Targets {
				byID[target.ID] = target
			}
			selected := make([]store.Target, 0, len(cluster.TargetIDs))
			for _, id := range cluster.TargetIDs {
				target, ok := byID[id]
				if !ok {
					return nil, 0, false, errors.New("cluster references a missing target")
				}
				selected = append(selected, target)
			}
			if len(selected) == 0 {
				return nil, 0, false, errors.New("cluster has no targets")
			}
			if cluster.Parallelism <= 0 {
				cluster.Parallelism = 1
			}
			return selected, cluster.Parallelism, cluster.GateOnFailure, nil
		}
	}
	return nil, 0, false, errors.New("cluster not found")
}

func uploadCommand(target store.Target, remoteTmp string) string {
	return fmt.Sprintf("ssh -p %d %s@%s 'cat > %s'", target.Port, target.User, target.Host, shellQuote(remoteTmp))
}

func remoteValidateCommand(target store.Target, remoteTmp string) string {
	if target.Engine == ir.EngineNginx {
		return "nginx -t -c " + shellQuote(remoteTmp)
	}
	return "haproxy -c -f " + shellQuote(remoteTmp)
}

func installCommand(target store.Target, remoteTmp string) string {
	cmd := fmt.Sprintf("install -m 0644 %s %s", shellQuote(remoteTmp), shellQuote(target.ConfigPath))
	return sudoCommand(target, cmd)
}

func reloadCommand(target store.Target) string {
	return sudoCommand(target, target.ReloadCommand)
}

func sudoCommand(target store.Target, command string) string {
	if !target.Sudo {
		return command
	}
	return "sudo sh -lc " + shellQuote(command)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func sshRunner(ctx context.Context, target store.Target, command string, input string) (string, error) {
	args := []string{"-p", fmt.Sprint(target.Port), target.User + "@" + target.Host, command}
	cmd := exec.CommandContext(ctx, "ssh", args...)
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	return output.String(), err
}

func httpProbe(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 400 {
		return fmt.Errorf("probe returned HTTP %d", res.StatusCode)
	}
	return nil
}

func stepStatus(err error, dryRun bool) string {
	if err != nil {
		return "failed"
	}
	if dryRun {
		return "success"
	}
	return "success"
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func hasFailed(steps []Step) bool {
	for _, step := range steps {
		if step.Status == "failed" {
			return true
		}
	}
	return false
}
