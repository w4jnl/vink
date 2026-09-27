// Command vink is the heartbeat and uptime monitor: server, CLI client and
// (later) probe agent in one binary.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

// exitCoder is implemented by errors that carry a process exit code:
// 1 user error, 2 server or network error, 3 something is down.
type exitCoder interface{ ExitCode() int }

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	root := newRootCmd()
	root.SetArgs(args)
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(stderr, "vink:", err)
		var ec exitCoder
		if errors.As(err, &ec) {
			return ec.ExitCode()
		}
		return 1
	}
	return 0
}
