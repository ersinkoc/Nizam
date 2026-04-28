package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mizanproxy/mizan/internal/ir"
	"github.com/mizanproxy/mizan/internal/store"
)

type Status string

const (
	StatusPass Status = "pass"
	StatusWarn Status = "warn"
	StatusFail Status = "fail"
)

type Check struct {
	Name    string `json:"name"`
	Status  Status `json:"status"`
	Message string `json:"message"`
}

type Report struct {
	Status       Status  `json:"status"`
	Root         string  `json:"root"`
	Production   bool    `json:"production,omitempty"`
	ProjectCount int     `json:"project_count"`
	TargetCount  int     `json:"target_count"`
	ClusterCount int     `json:"cluster_count"`
	Checks       []Check `json:"checks"`
}

type LookPath func(string) (string, error)

type Options struct {
	Production bool
}

func Run(ctx context.Context, st *store.Store, lookPath LookPath) Report {
	return RunWithOptions(ctx, st, lookPath, Options{})
}

func RunWithOptions(ctx context.Context, st *store.Store, lookPath LookPath, options Options) Report {
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	report := Report{Status: StatusPass, Root: st.Root(), Production: options.Production}
	add := func(name string, status Status, message string) {
		report.Checks = append(report.Checks, Check{Name: name, Status: status, Message: message})
		report.Status = worst(report.Status, status)
	}

	if err := st.Bootstrap(ctx); err != nil {
		add("data_root", StatusFail, err.Error())
		return report
	}
	add("data_root", StatusPass, fmt.Sprintf("%s is accessible", st.Root()))

	projects, err := st.ListProjects(ctx)
	if err != nil {
		add("projects", StatusFail, err.Error())
		return report
	}
	report.ProjectCount = len(projects)
	add("projects", StatusPass, fmt.Sprintf("%d project(s) readable", len(projects)))

	engines := map[ir.Engine]bool{}
	var integrityErrors []string
	targetsByProject := map[string]store.TargetsFile{}
	for _, project := range projects {
		for _, engine := range project.Engines {
			engines[engine] = true
		}
		if _, _, err := st.GetIR(ctx, project.ID); err != nil {
			integrityErrors = append(integrityErrors, project.ID+": "+err.Error())
		}
		targets, err := st.ListTargets(ctx, project.ID)
		if err != nil {
			integrityErrors = append(integrityErrors, project.ID+" targets: "+err.Error())
			continue
		}
		targetsByProject[project.ID] = targets
		report.TargetCount += len(targets.Targets)
		report.ClusterCount += len(targets.Clusters)
	}
	if len(integrityErrors) > 0 {
		add("project_integrity", StatusFail, strings.Join(integrityErrors, "; "))
	} else {
		add("project_integrity", StatusPass, "project config and target files are readable")
	}
	add("targets", StatusPass, fmt.Sprintf("%d target(s), %d cluster(s)", report.TargetCount, report.ClusterCount))

	checkSecrets(st.Root(), add)
	checkTool("ssh", "remote deployment execution", true, lookPath, add)
	checkNativeTool(ir.EngineHAProxy, "haproxy", engines[ir.EngineHAProxy], lookPath, add)
	checkNativeTool(ir.EngineNginx, "nginx", engines[ir.EngineNginx], lookPath, add)
	if options.Production {
		checkProduction(st.Root(), targetsByProject, report.TargetCount, report.ClusterCount, add)
	}
	return report
}

func checkProduction(root string, targetsByProject map[string]store.TargetsFile, targetCount, clusterCount int, add func(string, Status, string)) {
	if targetCount == 0 {
		add("production_targets", StatusWarn, "no deployment targets configured; production rollout cannot be rehearsed")
	} else {
		add("production_targets", StatusPass, fmt.Sprintf("%d deployment target(s) configured", targetCount))
	}
	if clusterCount == 0 {
		add("production_clusters", StatusWarn, "no deployment clusters configured; use clusters for batch rollout and approval policy")
	} else {
		add("production_clusters", StatusPass, fmt.Sprintf("%d deployment cluster(s) configured", clusterCount))
	}

	var ungatedClusters, underApprovedClusters, missingRollback, missingProbe, missingMonitor []string
	for projectID, targets := range targetsByProject {
		for _, cluster := range targets.Clusters {
			name := projectScopedName(projectID, cluster.Name)
			if !cluster.GateOnFailure {
				ungatedClusters = append(ungatedClusters, name)
			}
			if cluster.RequiredApprovals < 2 {
				underApprovedClusters = append(underApprovedClusters, name)
			}
		}
		for _, target := range targets.Targets {
			name := projectScopedName(projectID, target.Name)
			if strings.TrimSpace(target.RollbackCommand) == "" {
				missingRollback = append(missingRollback, name)
			}
			if strings.TrimSpace(target.PostReloadProbe) == "" {
				missingProbe = append(missingProbe, name)
			}
			if strings.TrimSpace(target.MonitorEndpoint) == "" {
				missingMonitor = append(missingMonitor, name)
			}
		}
	}
	addProductionCollectionCheck("production_gate_on_failure", ungatedClusters, "all clusters gate on failure", "cluster(s) have gate_on_failure disabled", add)
	addProductionCollectionCheck("production_required_approvals", underApprovedClusters, "all clusters require at least two approvals", "cluster(s) require fewer than two approvals", add)
	addProductionCollectionCheck("production_rollback", missingRollback, "all targets have rollback commands", "target(s) are missing rollback_command", add)
	addProductionCollectionCheck("production_post_reload_probe", missingProbe, "all targets have post-reload probes", "target(s) are missing post_reload_probe", add)
	addProductionCollectionCheck("production_monitoring", missingMonitor, "all targets expose monitor endpoints", "target(s) are missing monitor_endpoint", add)
	checkProductionSecrets(root, targetsByProject, targetCount, add)
}

func addProductionCollectionCheck(name string, missing []string, passMessage, warnPrefix string, add func(string, Status, string)) {
	if len(missing) == 0 {
		add(name, StatusPass, passMessage)
		return
	}
	add(name, StatusWarn, fmt.Sprintf("%s: %s", warnPrefix, summarizeNames(missing)))
}

func checkProductionSecrets(root string, targetsByProject map[string]store.TargetsFile, targetCount int, add func(string, Status, string)) {
	if targetCount == 0 {
		add("production_target_secrets", StatusWarn, "no targets configured to associate with encrypted credentials")
		return
	}
	secretIDs, err := secretIDs(root)
	if err != nil {
		add("production_target_secrets", StatusWarn, "could not inspect encrypted target credentials: "+err.Error())
		return
	}
	var missing []string
	for projectID, targets := range targetsByProject {
		for _, target := range targets.Targets {
			if !secretIDs[target.ID] {
				missing = append(missing, projectScopedName(projectID, target.Name))
			}
		}
	}
	addProductionCollectionCheck("production_target_secrets", missing, "all targets have encrypted credential envelopes", "target(s) are missing encrypted credentials", add)
}

func secretIDs(root string) (map[string]bool, error) {
	ids := map[string]bool{}
	secretRoot := filepath.Join(root, "secrets")
	entries, err := os.ReadDir(secretRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ids, nil
		}
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		ids[strings.TrimSuffix(entry.Name(), ".json")] = true
	}
	return ids, nil
}

func projectScopedName(projectID, name string) string {
	if name == "" {
		return projectID
	}
	return projectID + "/" + name
}

func summarizeNames(names []string) string {
	const maxNames = 5
	if len(names) <= maxNames {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s, +%d more", strings.Join(names[:maxNames], ", "), len(names)-maxNames)
}

func checkSecrets(root string, add func(string, Status, string)) {
	secretRoot := filepath.Join(root, "secrets")
	info, err := os.Stat(secretRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			add("secrets", StatusPass, "no encrypted secrets stored")
			return
		}
		add("secrets", StatusFail, err.Error())
		return
	}
	if !info.IsDir() {
		add("secrets", StatusFail, fmt.Sprintf("%s is not a directory", secretRoot))
		return
	}
	entries, err := os.ReadDir(secretRoot)
	if err != nil {
		add("secrets", StatusFail, err.Error())
		return
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
			count++
		}
	}
	add("secrets", StatusPass, fmt.Sprintf("%d encrypted secret envelope(s)", count))
}

func checkTool(name, purpose string, important bool, lookPath LookPath, add func(string, Status, string)) {
	path, err := lookPath(name)
	if err != nil {
		status := StatusWarn
		if important {
			status = StatusWarn
		}
		add("tool_"+name, status, fmt.Sprintf("%s not found on PATH; %s will be limited", name, purpose))
		return
	}
	add("tool_"+name, StatusPass, path)
}

func checkNativeTool(engine ir.Engine, tool string, used bool, lookPath LookPath, add func(string, Status, string)) {
	path, err := lookPath(tool)
	if err != nil {
		if used {
			add("native_"+tool, StatusWarn, fmt.Sprintf("%s project(s) exist but %s is not on PATH; native validation will be skipped", engine, tool))
			return
		}
		add("native_"+tool, StatusPass, fmt.Sprintf("%s not on PATH; no %s projects currently require it", tool, engine))
		return
	}
	add("native_"+tool, StatusPass, path)
}

func worst(current, next Status) Status {
	if current == StatusFail || next == StatusFail {
		return StatusFail
	}
	if current == StatusWarn || next == StatusWarn {
		return StatusWarn
	}
	return StatusPass
}
