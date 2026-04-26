package main

import (
	"bytes"
	"os"
	"testing"
)

func TestMainFunctionSuccess(t *testing.T) {
	oldArgs := os.Args
	oldExit := exit
	t.Cleanup(func() {
		os.Args = oldArgs
		exit = oldExit
	})
	gotCode := -1
	exit = func(code int) { gotCode = code }
	os.Args = []string{"mizan", "version"}
	main()
	if gotCode != 0 {
		t.Fatalf("exit code=%d", gotCode)
	}
}

func TestRunMainFailure(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runMain([]string{"nope"}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit code=%d", code)
	}
}
