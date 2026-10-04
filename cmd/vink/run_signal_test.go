package main

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestRunForwardsSignals: SIGTERM to `vink run` reaches the command, which
// stops cleanly in its own way, and the finish ping still goes out with
// the command's exit code and output, although SIGTERM cancelled vink's
// context. The test binary runs itself as vink, with main's signal
// handling, so the signal goes to a real process.
func TestRunForwardsSignals(t *testing.T) {
	if os.Getenv("VINK_TEST_AS_MAIN") == "1" {
		args := os.Args
		for i, a := range args {
			if a == "--" {
				args = args[i+1:]
				break
			}
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		code := run(ctx, args, os.Stdin, os.Stdout, os.Stderr)
		stop()
		os.Exit(code)
	}
	if runtime.GOOS == "windows" {
		t.Skip("needs sh and SIGTERM")
	}
	var mu sync.Mutex
	var pings []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		pings = append(pings, r.URL.Path+"?"+r.URL.RawQuery+" "+string(b))
		mu.Unlock()
	}))
	defer srv.Close()

	// the command stops on TERM with its own exit code; its sleep must not
	// hold vink's output pipe open after it
	script := `trap 'kill $! 2>/dev/null; echo stopping cleanly; exit 7' TERM; echo ready; sleep 30 </dev/null >/dev/null 2>&1 & wait`
	cmd := exec.Command(os.Args[0], "-test.run=^TestRunForwardsSignals$", "--", //nolint:gosec // G204: the test binary runs itself as vink
		"run", "job", "--ping-key", "k", "--ping-url", srv.URL+"/ping/", "--", "sh", "-c", script)
	cmd.Env = append(os.Environ(), "VINK_TEST_AS_MAIN=1", "VINK_CONFIG="+t.TempDir()+"/config.toml")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lines := bufio.NewScanner(stdout)
	if !lines.Scan() || lines.Text() != "ready" {
		t.Fatalf("the command did not start: %q", lines.Text())
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	var rest []string
	for lines.Scan() {
		rest = append(rest, lines.Text())
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case err = <-waited:
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("vink run did not end after SIGTERM")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
		t.Fatalf("vink run ended with %v, want the command's exit 7", err)
	}
	if strings.Join(rest, "\n") != "stopping cleanly" {
		t.Errorf("the command did not stop in its own way: %q", rest)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(pings) != 2 || !strings.HasPrefix(pings[0], "/ping/k/job/start?rid=") {
		t.Fatalf("pings %q", pings)
	}
	rid := strings.TrimPrefix(strings.Fields(pings[0])[0], "/ping/k/job/start?")
	if want := "/ping/k/job/7?" + rid + " ready\nstopping cleanly\n"; pings[1] != want {
		t.Errorf("finish ping %q, want %q", pings[1], want)
	}
}
