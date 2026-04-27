package deploy

import (
	"context"
	"errors"
	"strings"

	"github.com/mizanproxy/mizan/internal/ir"
	"github.com/mizanproxy/mizan/internal/secrets"
	"github.com/mizanproxy/mizan/internal/store"
)

type DrillReport struct {
	Status    string                `json:"status"`
	Scenarios []DrillScenarioResult `json:"scenarios"`
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
