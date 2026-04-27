package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mizanproxy/mizan/internal/ir"
	"github.com/mizanproxy/mizan/internal/secrets"
	"github.com/mizanproxy/mizan/internal/store"
)

type DrillReport struct {
	Status    string                `json:"status"`
	Scenarios []DrillScenarioResult `json:"scenarios"`
}

type DrillSummary struct {
	Status    string                 `json:"status"`
	Scenarios []DrillScenarioSummary `json:"scenarios"`
	Totals    DrillSummaryTotals     `json:"totals"`
}

type DrillScenarioSummary struct {
	Name             string        `json:"name"`
	Status           string        `json:"status"`
	DeploymentStatus string        `json:"deployment_status"`
	Message          string        `json:"message,omitempty"`
	Rollback         RollbackStats `json:"rollback"`
	Cleanup          CleanupStats  `json:"cleanup"`
	StepCount        int           `json:"step_count"`
}

type DrillSummaryTotals struct {
	Scenarios          int `json:"scenarios"`
	FailedScenarios    int `json:"failed_scenarios"`
	RollbackAttempted  int `json:"rollback_attempted"`
	RollbackSucceeded  int `json:"rollback_succeeded"`
	RollbackFailed     int `json:"rollback_failed"`
	CleanupAttempted   int `json:"cleanup_attempted"`
	CleanupSucceeded   int `json:"cleanup_succeeded"`
	CleanupFailed      int `json:"cleanup_failed"`
	DeploymentFailures int `json:"deployment_failures"`
}

type DrillScenarioResult struct {
	Name             string        `json:"name"`
	Status           string        `json:"status"`
	DeploymentStatus string        `json:"deployment_status"`
	Message          string        `json:"message,omitempty"`
	Rollback         RollbackStats `json:"rollback"`
	Cleanup          CleanupStats  `json:"cleanup"`
	Commands         []string      `json:"commands"`
	Steps            []Step        `json:"steps"`
}

type drillScenario struct {
	name   string
	prober Prober
	runner func(command string) (string, error)
	expect func([]Step, []string) error
}

func RunDrill(ctx context.Context) DrillReport {
	report := DrillReport{Status: "success", Scenarios: []DrillScenarioResult{}}
	for _, scenario := range drillScenarios() {
		var commands []string
		deployer := Deployer{
			Runner: func(_ context.Context, _ store.Target, _ secrets.Secret, command string, _ string) (string, error) {
				commands = append(commands, command)
				return scenario.runner(command)
			},
			Prober: scenario.prober,
		}
		steps := deployer.runTarget(ctx, drillModel(), "drill", drillTarget(), secrets.Secret{}, 1, false)
		deploymentStatus := "success"
		if hasFailed(steps) {
			deploymentStatus = "failed"
		}
		result := DrillScenarioResult{
			Name:             scenario.name,
			Status:           "success",
			DeploymentStatus: deploymentStatus,
			Rollback:         RollbackSummary(steps),
			Cleanup:          CleanupSummary(steps),
			Commands:         commands,
			Steps:            steps,
		}
		if err := scenario.expect(steps, commands); err != nil {
			result.Status = "failed"
			result.Message = err.Error()
			report.Status = "failed"
		}
		report.Scenarios = append(report.Scenarios, result)
	}
	return report
}

func SummarizeDrill(report DrillReport) DrillSummary {
	summary := DrillSummary{Status: report.Status, Scenarios: []DrillScenarioSummary{}}
	for _, scenario := range report.Scenarios {
		if scenario.Status != "success" {
			summary.Totals.FailedScenarios++
		}
		if scenario.DeploymentStatus == "failed" {
			summary.Totals.DeploymentFailures++
		}
		summary.Totals.Scenarios++
		summary.Totals.RollbackAttempted += scenario.Rollback.Attempted
		summary.Totals.RollbackSucceeded += scenario.Rollback.Succeeded
		summary.Totals.RollbackFailed += scenario.Rollback.Failed
		summary.Totals.CleanupAttempted += scenario.Cleanup.Attempted
		summary.Totals.CleanupSucceeded += scenario.Cleanup.Succeeded
		summary.Totals.CleanupFailed += scenario.Cleanup.Failed
		summary.Scenarios = append(summary.Scenarios, DrillScenarioSummary{
			Name:             scenario.Name,
			Status:           scenario.Status,
			DeploymentStatus: scenario.DeploymentStatus,
			Message:          scenario.Message,
			Rollback:         scenario.Rollback,
			Cleanup:          scenario.Cleanup,
			StepCount:        len(scenario.Steps),
		})
	}
	return summary
}

func ParseDrillEvidence(data []byte) (DrillSummary, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return DrillSummary{}, err
	}
	if _, ok := raw["totals"]; ok {
		var summary DrillSummary
		if err := json.Unmarshal(data, &summary); err != nil {
			return DrillSummary{}, err
		}
		return summary, nil
	}
	var report DrillReport
	if err := json.Unmarshal(data, &report); err != nil {
		return DrillSummary{}, err
	}
	return SummarizeDrill(report), nil
}

func ValidateDrillEvidence(summary DrillSummary) error {
	if summary.Status != "success" {
		return fmt.Errorf("drill status is %q", summary.Status)
	}
	expectedScenarios := map[string]bool{
		"remote_validation_failure_cleans_tmp":      false,
		"install_failure_rolls_back_and_cleans_tmp": false,
		"probe_failure_rolls_back_and_cleans_tmp":   false,
		"cleanup_failure_is_incident_signal":        false,
	}
	var totals DrillSummaryTotals
	for _, scenario := range summary.Scenarios {
		if _, ok := expectedScenarios[scenario.Name]; ok {
			expectedScenarios[scenario.Name] = true
		}
		if scenario.Status != "success" {
			return fmt.Errorf("scenario %q status is %q", scenario.Name, scenario.Status)
		}
		if err := validateDrillScenarioEvidence(scenario); err != nil {
			return err
		}
		totals.Scenarios++
		if scenario.DeploymentStatus == "failed" {
			totals.DeploymentFailures++
		}
		totals.RollbackAttempted += scenario.Rollback.Attempted
		totals.RollbackSucceeded += scenario.Rollback.Succeeded
		totals.RollbackFailed += scenario.Rollback.Failed
		totals.CleanupAttempted += scenario.Cleanup.Attempted
		totals.CleanupSucceeded += scenario.Cleanup.Succeeded
		totals.CleanupFailed += scenario.Cleanup.Failed
	}
	for name, present := range expectedScenarios {
		if !present {
			return fmt.Errorf("missing required drill scenario %q", name)
		}
	}
	if totals != summary.Totals {
		return fmt.Errorf("drill totals mismatch: declared=%+v calculated=%+v", summary.Totals, totals)
	}
	if summary.Totals.FailedScenarios != 0 {
		return fmt.Errorf("failed_scenarios=%d", summary.Totals.FailedScenarios)
	}
	if summary.Totals.RollbackAttempted < 2 || summary.Totals.RollbackFailed != 0 {
		return fmt.Errorf("unexpected rollback totals: %+v", summary.Totals)
	}
	if summary.Totals.CleanupAttempted < 4 || summary.Totals.CleanupFailed < 1 {
		return fmt.Errorf("unexpected cleanup totals: %+v", summary.Totals)
	}
	return nil
}

func validateDrillScenarioEvidence(scenario DrillScenarioSummary) error {
	if scenario.DeploymentStatus != "failed" {
		return fmt.Errorf("scenario %q deployment_status is %q", scenario.Name, scenario.DeploymentStatus)
	}
	switch scenario.Name {
	case "remote_validation_failure_cleans_tmp":
		if scenario.Rollback.Planned != 0 || scenario.Rollback.Attempted != 0 || scenario.Cleanup.Succeeded != 1 || scenario.Cleanup.Failed != 0 {
			return fmt.Errorf("unexpected remote validation drill evidence: %+v", scenario)
		}
	case "install_failure_rolls_back_and_cleans_tmp", "probe_failure_rolls_back_and_cleans_tmp":
		if scenario.Rollback.Attempted != 1 || scenario.Rollback.Succeeded != 1 || scenario.Rollback.Failed != 0 || scenario.Cleanup.Succeeded != 1 || scenario.Cleanup.Failed != 0 {
			return fmt.Errorf("unexpected rollback drill evidence for %q: %+v", scenario.Name, scenario)
		}
	case "cleanup_failure_is_incident_signal":
		if scenario.Rollback.Attempted != 0 || scenario.Cleanup.Attempted != 1 || scenario.Cleanup.Failed != 1 {
			return fmt.Errorf("unexpected cleanup failure drill evidence: %+v", scenario)
		}
	default:
		return nil
	}
	return nil
}

func FormatDrillText(report DrillReport) string {
	summary := SummarizeDrill(report)
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "Mizan deploy drill: %s\n", summary.Status)
	_, _ = fmt.Fprintf(&b, "scenarios=%d failed_scenarios=%d deployment_failures=%d\n", summary.Totals.Scenarios, summary.Totals.FailedScenarios, summary.Totals.DeploymentFailures)
	_, _ = fmt.Fprintf(&b, "rollback attempted=%d succeeded=%d failed=%d\n", summary.Totals.RollbackAttempted, summary.Totals.RollbackSucceeded, summary.Totals.RollbackFailed)
	_, _ = fmt.Fprintf(&b, "cleanup attempted=%d succeeded=%d failed=%d\n", summary.Totals.CleanupAttempted, summary.Totals.CleanupSucceeded, summary.Totals.CleanupFailed)
	for _, scenario := range summary.Scenarios {
		_, _ = fmt.Fprintf(
			&b,
			"- %s: check=%s deploy=%s steps=%d rollback=%d/%d/%d cleanup=%d/%d/%d\n",
			scenario.Name,
			scenario.Status,
			scenario.DeploymentStatus,
			scenario.StepCount,
			scenario.Rollback.Attempted,
			scenario.Rollback.Succeeded,
			scenario.Rollback.Failed,
			scenario.Cleanup.Attempted,
			scenario.Cleanup.Succeeded,
			scenario.Cleanup.Failed,
		)
		if scenario.Message != "" {
			_, _ = fmt.Fprintf(&b, "  message: %s\n", scenario.Message)
		}
	}
	return b.String()
}

func drillScenarios() []drillScenario {
	return []drillScenario{
		{
			name:   "remote_validation_failure_cleans_tmp",
			prober: func(context.Context, string) error { return nil },
			runner: func(command string) (string, error) {
				if strings.Contains(command, "haproxy -c") {
					return "invalid config", errors.New("exit 1")
				}
				return "ok", nil
			},
			expect: func(steps []Step, commands []string) error {
				if !hasStageStatus(steps, "remote_validate", "failed") {
					return errors.New("remote validation failure was not recorded")
				}
				if hasCommand(commands, "install -m") || hasCommand(commands, "reload") {
					return errors.New("install/reload ran after failed remote validation")
				}
				if !hasStageStatus(steps, "cleanup", "success") {
					return errors.New("cleanup did not succeed after remote validation failure")
				}
				if RollbackSummary(steps).Planned != 0 {
					return errors.New("rollback should not be planned for remote validation failure")
				}
				return nil
			},
		},
		{
			name:   "install_failure_rolls_back_and_cleans_tmp",
			prober: func(context.Context, string) error { return nil },
			runner: func(command string) (string, error) {
				if strings.Contains(command, "install -m") {
					return "install failed", errors.New("exit 1")
				}
				return "ok", nil
			},
			expect: func(steps []Step, commands []string) error {
				if !hasStageStatus(steps, "install", "failed") {
					return errors.New("install failure was not recorded")
				}
				if stats := RollbackSummary(steps); stats.Attempted != 1 || stats.Succeeded != 1 || stats.Failed != 0 {
					return errors.New("rollback did not succeed after install failure")
				}
				if !hasStageStatus(steps, "cleanup", "success") {
					return errors.New("cleanup did not succeed after install failure")
				}
				return nil
			},
		},
		{
			name:   "probe_failure_rolls_back_and_cleans_tmp",
			prober: func(context.Context, string) error { return errors.New("probe failed") },
			runner: func(command string) (string, error) {
				return "ok", nil
			},
			expect: func(steps []Step, commands []string) error {
				if !hasStageStatus(steps, "probe", "failed") {
					return errors.New("probe failure was not recorded")
				}
				if stats := RollbackSummary(steps); stats.Attempted != 1 || stats.Succeeded != 1 || stats.Failed != 0 {
					return errors.New("rollback did not succeed after probe failure")
				}
				if !hasStageStatus(steps, "cleanup", "success") {
					return errors.New("cleanup did not succeed after probe failure")
				}
				return nil
			},
		},
		{
			name:   "cleanup_failure_is_incident_signal",
			prober: func(context.Context, string) error { return nil },
			runner: func(command string) (string, error) {
				if strings.HasPrefix(command, "rm -f ") {
					return "cleanup failed", errors.New("exit 1")
				}
				return "ok", nil
			},
			expect: func(steps []Step, commands []string) error {
				if !hasStageStatus(steps, "reload", "success") {
					return errors.New("reload did not complete before cleanup failure")
				}
				if stats := CleanupSummary(steps); stats.Attempted != 1 || stats.Failed != 1 {
					return errors.New("cleanup failure was not summarized as an incident signal")
				}
				return nil
			},
		},
	}
}

func drillModel() *ir.Model {
	model := ir.EmptyModel("drill", "drill", "", []ir.Engine{ir.EngineHAProxy})
	model.Frontends = []ir.Frontend{{
		ID:             "fe_web",
		Name:           "web",
		Bind:           ":80",
		Protocol:       "http",
		DefaultBackend: "be_app",
	}}
	model.Backends = []ir.Backend{{
		ID:        "be_app",
		Name:      "be_app",
		Algorithm: "roundrobin",
		Servers:   []string{"s_app"},
	}}
	model.Servers = []ir.Server{{
		ID:      "s_app",
		Name:    "app",
		Address: "127.0.0.1",
		Port:    8080,
		Weight:  100,
	}}
	return model
}

func drillTarget() store.Target {
	return store.Target{
		ID:              "t_drill",
		Name:            "drill-edge",
		Host:            "drill.example.com",
		Port:            22,
		User:            "root",
		Engine:          ir.EngineHAProxy,
		ConfigPath:      "/etc/haproxy/haproxy.cfg",
		ReloadCommand:   "systemctl reload haproxy",
		RollbackCommand: "cp /etc/haproxy/haproxy.cfg.bak /etc/haproxy/haproxy.cfg && systemctl reload haproxy",
		PostReloadProbe: "https://drill.example.com/healthz",
	}
}

func hasStageStatus(steps []Step, stage, status string) bool {
	for _, step := range steps {
		if step.Stage == stage && step.Status == status {
			return true
		}
	}
	return false
}

func hasCommand(commands []string, needle string) bool {
	for _, command := range commands {
		if strings.Contains(command, needle) {
			return true
		}
	}
	return false
}
