package validate

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"time"

	"github.com/mizanproxy/mizan/internal/ir"
	"github.com/mizanproxy/mizan/internal/translate"
	"github.com/mizanproxy/mizan/internal/translate/haproxy"
	"github.com/mizanproxy/mizan/internal/translate/nginx"
)

type NativeResult struct {
	Available bool   `json:"available"`
	Skipped   bool   `json:"skipped"`
	Command   string `json:"command,omitempty"`
	ExitCode  int    `json:"exit_code"`
	Stdout    string `json:"stdout,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
	Error     string `json:"error,omitempty"`
}

type Result struct {
	Target    ir.Engine        `json:"target"`
	Issues    []ir.Issue       `json:"issues"`
	Generated translate.Result `json:"generated"`
	Native    NativeResult     `json:"native"`
}

var (
	createTemp      = os.CreateTemp
	writeTempConfig = func(tmp *os.File, config string) error {
		_, err := tmp.WriteString(config)
		return err
	}
)

func Generate(m *ir.Model, target ir.Engine) (translate.Result, error) {
	switch target {
	case ir.EngineHAProxy:
		return haproxy.Generate(m), nil
	case ir.EngineNginx:
		return nginx.Generate(m), nil
	default:
		return translate.Result{}, errors.New("unknown generation target")
	}
}

func Validate(ctx context.Context, m *ir.Model, target ir.Engine) (Result, error) {
	generated, err := Generate(m, target)
	if err != nil {
		return Result{}, err
	}
	res := Result{
		Target:    target,
		Issues:    ir.Lint(m),
		Generated: generated,
		Native:    runNative(ctx, target, generated.Config),
	}
	return res, nil
}

func runNative(ctx context.Context, target ir.Engine, config string) NativeResult {
	bin := string(target)
	if target == ir.EngineNginx {
		bin = "nginx"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		return NativeResult{Available: false, Skipped: true, Error: bin + " not found on PATH"}
	}
	tmp, err := createTemp("", "mizan-*")
	if err != nil {
		return NativeResult{Available: true, Skipped: true, Error: err.Error()}
	}
	defer os.Remove(tmp.Name())
	if err := writeTempConfig(tmp, config); err != nil {
		_ = tmp.Close()
		return NativeResult{Available: true, Skipped: true, Error: err.Error()}
	}
	_ = tmp.Close()

	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	switch target {
	case ir.EngineHAProxy:
		cmd = exec.CommandContext(cctx, path, "-c", "-f", tmp.Name())
	case ir.EngineNginx:
		cmd = exec.CommandContext(cctx, path, "-t", "-c", tmp.Name())
	default:
		return NativeResult{Available: false, Skipped: true, Error: "unsupported target"}
	}
	out, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		exitCode = 1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		}
	}
	return NativeResult{
		Available: true,
		Command:   cmd.String(),
		ExitCode:  exitCode,
		Stderr:    string(out),
	}
}
