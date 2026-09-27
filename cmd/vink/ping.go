package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"sync"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/w4jnl/vink/internal/cli"
	"github.com/w4jnl/vink/internal/domain"
)

// pingTarget resolves the ping base and key for the current context,
// fetching /me once and caching the answer in the contexts file.
func pingTarget(cmd *cobra.Command, f *clientFlags) (*cli.Client, string, string, error) {
	c, cfg, res, _, err := f.connect(cmd)
	if err != nil {
		return nil, "", "", err
	}
	if !res.FromEnv {
		if ctx := cfg.Contexts[res.Name]; ctx.PingBase != "" && ctx.PingKey != "" {
			return c, ctx.PingBase, ctx.PingKey, nil
		}
	}
	var me struct {
		Project *struct {
			PingKey  string `json:"ping_key"`
			PingBase string `json:"ping_base"`
		} `json:"project"`
	}
	if err := c.Do(cmd.Context(), "GET", "/me", nil, &me); err != nil {
		return nil, "", "", err
	}
	if me.Project == nil || me.Project.PingKey == "" {
		return nil, "", "", cli.UserError("this API key cannot see the ping key; a read-only key cannot ping")
	}
	if !res.FromEnv {
		ctx := cfg.Contexts[res.Name]
		ctx.PingBase, ctx.PingKey = me.Project.PingBase, me.Project.PingKey
		cfg.Contexts[res.Name] = ctx
		if err := cfg.Save(); err != nil {
			fmt.Fprintln(cmd.ErrOrStderr(), "warning: could not cache the ping key:", err)
		}
	}
	return c, me.Project.PingBase, me.Project.PingKey, nil
}

func newPingCmd(g *globals) *cobra.Command {
	f := &clientFlags{g: g}
	var start, fail, create bool
	var exitCode int
	var msg, rid string
	var bodyFrom string
	cmd := &cobra.Command{
		Use:   "ping <slug>",
		Short: "Send a heartbeat ping using the context's project ping key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, base, key, err := pingTarget(cmd, f)
			if err != nil {
				return err
			}
			target := base + key + "/" + url.PathEscape(args[0])
			switch {
			case start:
				target += "/start"
			case fail:
				target += "/fail"
			case cmd.Flags().Changed("exit"):
				target += "/" + strconv.Itoa(exitCode)
			}
			q := url.Values{}
			if msg != "" {
				q.Set("msg", msg)
			}
			if rid != "" {
				q.Set("rid", rid)
			}
			if create {
				q.Set("create", "1")
			}
			if len(q) > 0 {
				target += "?" + q.Encode()
			}
			var body []byte
			if bodyFrom == "-" {
				body, err = io.ReadAll(io.LimitReader(cmd.InOrStdin(), 64*1024))
				if err != nil {
					return err
				}
			}
			if err := c.Ping(cmd.Context(), target, body, ""); err != nil {
				return err
			}
			if !f.quiet {
				fmt.Fprintln(cmd.OutOrStdout(), "ok")
			}
			return nil
		},
	}
	f.add(cmd, false)
	cmd.Flags().BoolVar(&start, "start", false, "send a start signal")
	cmd.Flags().BoolVar(&fail, "fail", false, "send a fail signal")
	cmd.Flags().IntVar(&exitCode, "exit", 0, "send an exit code (0 is ok)")
	cmd.Flags().StringVar(&msg, "msg", "", "a short message stored with the ping")
	cmd.Flags().StringVar(&rid, "rid", "", "run id pairing a start with its finish")
	cmd.Flags().BoolVar(&create, "create", false, "create the monitor on first ping")
	cmd.Flags().StringVar(&bodyFrom, "body", "", "read the body from stdin with --body -")
	return cmd
}

// tailWriter keeps the last n bytes written. stdout and stderr share one
// writer from two goroutines, so it locks.
type tailWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
	n   int
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf.Write(p)
	if t.buf.Len() > t.n {
		excess := t.buf.Len() - t.n
		t.buf.Next(excess)
	}
	return len(p), nil
}

// Bytes returns a copy of the tail.
func (t *tailWriter) Bytes() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte(nil), t.buf.Bytes()...)
}

func newRunCmd(g *globals) *cobra.Command {
	f := &clientFlags{g: g}
	var tail int
	cmd := &cobra.Command{
		Use:   "run <slug> -- <command> [args…]",
		Short: "Run a command between a start ping and a finish ping carrying its exit code and output tail",
		Long: `vink run wraps a job: it sends /start, runs the command with stdout and
stderr passed through, then sends /<exit code> with the last bytes of
output as the body, and exits with the command's exit code. When the
server cannot be reached the command still runs.`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, base, key, err := pingTarget(cmd, f)
			if err != nil {
				return err
			}
			target := base + key + "/" + url.PathEscape(args[0])
			rid := domain.NewID()
			if err := c.Ping(cmd.Context(), target+"/start?rid="+rid, nil, ""); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "warning: start ping failed:", err)
			}
			tw := &tailWriter{n: tail}
			child := exec.CommandContext(cmd.Context(), args[1], args[2:]...) //nolint:gosec // running the caller's command is the point
			child.Stdin = cmd.InOrStdin()
			child.Stdout = io.MultiWriter(cmd.OutOrStdout(), tw)
			child.Stderr = io.MultiWriter(cmd.ErrOrStderr(), tw)
			sigs := make(chan os.Signal, 1)
			signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
			defer signal.Stop(sigs)
			if err := child.Start(); err != nil {
				_ = c.Ping(cmd.Context(), target+"/fail?rid="+rid+"&msg="+url.QueryEscape("could not start: "+err.Error()), nil, "")
				return cli.UserError("start %s: %v", args[1], err)
			}
			done := make(chan error, 1)
			go func() { done <- child.Wait() }()
			var waitErr error
			select {
			case s := <-sigs:
				_ = child.Process.Signal(s)
				waitErr = <-done
			case waitErr = <-done:
			}
			code := 0
			var exitErr *exec.ExitError
			if errors.As(waitErr, &exitErr) {
				code = exitErr.ExitCode()
				if code < 0 {
					code = 128
				}
			} else if waitErr != nil {
				code = 1
			}
			if err := c.Ping(cmd.Context(), target+"/"+strconv.Itoa(code)+"?rid="+rid, tw.Bytes(), "text/plain; charset=utf-8"); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "warning: finish ping failed:", err)
			}
			if code != 0 {
				return &cli.ExitError{Code: code, Err: fmt.Errorf("%s exited with %d", args[1], code)}
			}
			return nil
		},
	}
	f.add(cmd, false)
	cmd.Flags().IntVar(&tail, "tail", 16*1024, "bytes of output to send as the body")
	return cmd
}
