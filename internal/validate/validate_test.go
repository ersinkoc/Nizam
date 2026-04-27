package validate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/mizanproxy/mizan/internal/ir"
)

func TestGenerateAndValidateUnknownTarget(t *testing.T) {
	model := ir.EmptyModel("p_1", "edge", "", []ir.Engine{ir.EngineHAProxy})
	if result, err := Generate(model, ir.EngineNginx); err != nil || result.Target != ir.EngineNginx {
		t.Fatalf("expected nginx generation, got result=%+v err=%v", result, err)
	}
	if _, err := Generate(model, ir.Engine("caddy")); err == nil {
		t.Fatal("expected unknown generation target error")
	}
	if _, err := Validate(context.Background(), model, ir.Engine("caddy")); err == nil {
		t.Fatal("expected unknown validation target error")
	}
}

func TestValidateSkipsMissingNativeBinary(t *testing.T) {
	model := ir.EmptyModel("p_1", "edge", "", []ir.Engine{ir.EngineHAProxy})
	result, err := Validate(context.Background(), model, ir.EngineHAProxy)
	if err != nil {
		t.Fatal(err)
	}
	if result.Target != ir.EngineHAProxy {
		t.Fatalf("target=%q", result.Target)
	}
	if result.Native.Available && result.Native.Command == "" {
		t.Fatalf("native result missing command: %+v", result.Native)
	}
}

func TestRunNativeSuccessAndFailureWithFakeBinary(t *testing.T) {
	dir := t.TempDir()
	oldPath := os.Getenv("PATH")
	t.Cleanup(func() { _ = os.Setenv("PATH", oldPath) })
	if err := os.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatal(err)
	}
	writeFakeNativeBinary(t, dir, "haproxy", "ok", 0)
	ok := runNative(context.Background(), ir.EngineHAProxy, "global\n")
	if !ok.Available || ok.Skipped || ok.ExitCode != 0 || ok.Stderr == "" {
		t.Fatalf("unexpected success native result: %+v", ok)
	}

	writeFakeNativeBinary(t, dir, "nginx", "bad", 7)
	failed := runNative(context.Background(), ir.EngineNginx, "events {}\n")
	if !failed.Available || failed.Skipped || failed.ExitCode == 0 || failed.Stderr == "" {
		t.Fatalf("unexpected failed native result: %+v", failed)
	}

	writeFakeNativeBinary(t, dir, "custom", "custom", 0)
	unsupported := runNative(context.Background(), ir.Engine("custom"), "config")
	if unsupported.Available || !unsupported.Skipped || !strings.Contains(unsupported.Error, "unsupported target") {
		t.Fatalf("unexpected unsupported native result: %+v", unsupported)
	}
}

func TestRunNativeTempErrors(t *testing.T) {
	dir := t.TempDir()
	oldPath := os.Getenv("PATH")
	t.Cleanup(func() { _ = os.Setenv("PATH", oldPath) })
	if err := os.Setenv("PATH", dir+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatal(err)
	}
	writeFakeNativeBinary(t, dir, "haproxy", "ok", 0)

	originalCreateTemp := createTemp
	createTemp = func(string, string) (*os.File, error) {
		return nil, errors.New("temp unavailable")
	}
	got := runNative(context.Background(), ir.EngineHAProxy, "global\n")
	createTemp = originalCreateTemp
	if !got.Available || !got.Skipped || !strings.Contains(got.Error, "temp unavailable") {
		t.Fatalf("unexpected create temp error result: %+v", got)
	}

	originalWriteTempConfig := writeTempConfig
	writeTempConfig = func(*os.File, string) error {
		return errors.New("write unavailable")
	}
	got = runNative(context.Background(), ir.EngineHAProxy, "global\n")
	writeTempConfig = originalWriteTempConfig
	if !got.Available || !got.Skipped || !strings.Contains(got.Error, "write unavailable") {
		t.Fatalf("unexpected write temp error result: %+v", got)
	}
}

func writeFakeNativeBinary(t *testing.T, dir, name, output string, exitCode int) {
	t.Helper()
	path := filepath.Join(dir, name)
	content := "#!/bin/sh\necho " + output + "\nexit " + strconv.Itoa(exitCode) + "\n"
	if runtime.GOOS == "windows" {
		path += ".bat"
		content = "@echo off\r\necho " + output + "\r\nexit /b " + strconv.Itoa(exitCode) + "\r\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}
