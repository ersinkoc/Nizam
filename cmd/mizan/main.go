package main

import (
	"context"
	"io"
	"os"

	"github.com/mizanproxy/mizan/internal/cli"
)

var exit = os.Exit

func main() {
	exit(runMain(os.Args[1:], os.Stdout, os.Stderr))
}

func runMain(args []string, stdout, stderr io.Writer) int {
	if err := cli.Run(context.Background(), args, stdout, stderr); err != nil {
		return 1
	}
	return 0
}
