package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/SujalChoudhari/Ember/internal/ember"
)

const defaultOperatorQuota int64 = 64 << 20

func run(args []string, stdout, stderr io.Writer) int {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	flags := flag.NewFlagSet("ember", flag.ContinueOnError)
	flags.SetOutput(stderr)
	stateDir := flags.String("state-dir", os.Getenv("EMBER_STATE_DIR"), "private Ember state directory")
	quota := flags.Int64("quota", defaultOperatorQuota, "blob quota in bytes")
	listen := flags.String("listen", "127.0.0.1:8080", "loopback address for serve mode")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *stateDir == "" {
		*stateDir = ".ember-state"
	}
	operator, err := ember.NewFileOperator(*stateDir, *quota)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = operator.Close() }()
	if command := flags.Args(); len(command) > 0 && command[0] == "serve" {
		if len(command) != 1 {
			_, _ = fmt.Fprintln(stderr, "serve does not accept positional arguments")
			return 2
		}
		if err := ember.Serve(context.Background(), operator, *listen); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	if err := ember.RunCLI(context.Background(), operator, flags.Args(), stdout); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
