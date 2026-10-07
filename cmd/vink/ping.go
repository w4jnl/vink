package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/w4jnl/vink/internal/cli"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/ping"
)

// pingFlags are the client flags plus a ping key and a ping address. With
// a ping key, ping and run send their pings without an API key and never
// call the API, so a job host needs only what a curl line would carry.
type pingFlags struct {
	clientFlags
	key string
	url string
}

func (f *pingFlags) add(cmd *cobra.Command) {
	f.clientFlags.add(cmd, false)
	cmd.Flags().StringVar(&f.key, "ping-key", "", "the project's ping key, to ping without an API key (env VINK_PING_KEY)")
	cmd.Flags().StringVar(&f.url, "ping-url", "", "where pings go: a ping URL up to the key, ending in /ping/ (env VINK_PING_URL; default: the server's)")
}

// pingTarget resolves the ping base (ending in /ping/) and the ping key.
// A ping key from --ping-key or VINK_PING_KEY is used as it is; otherwise
// the API key fetches it from /me once and the context caches it.
// --ping-url or VINK_PING_URL, when set, is where the pings go either way.
// The ping module checks both when the client is made.
func pingTarget(cmd *cobra.Command, f *pingFlags) (string, string, error) {
	base := firstSet(f.url, os.Getenv("VINK_PING_URL"))
	if key := firstSet(f.key, os.Getenv("VINK_PING_KEY")); key != "" {
		if base == "" {
			b, err := f.serverPingBase()
			if err != nil {
				return "", "", err
			}
			base = b
		}
		return base, key, nil
	}
	c, cfg, res, _, err := f.connect(cmd)
	if err != nil {
		return "", "", err
	}
	if !res.FromEnv {
		if ctx := cfg.Contexts[res.Name]; ctx.PingBase != "" && ctx.PingKey != "" {
			return firstSet(base, ctx.PingBase), ctx.PingKey, nil
		}
	}
	var me struct {
		Project *struct {
			PingKey  string `json:"ping_key"`
			PingBase string `json:"ping_base"`
		} `json:"project"`
	}
	if err := c.Do(cmd.Context(), "GET", "/me", nil, &me); err != nil {
		return "", "", err
	}
	if me.Project == nil || me.Project.PingKey == "" {
		return "", "", cli.UserError("this API key cannot see the ping key; a read-only key cannot ping. Set VINK_PING_KEY to ping with the ping key alone")
	}
	if !res.FromEnv {
		ctx := cfg.Contexts[res.Name]
		ctx.PingBase, ctx.PingKey = me.Project.PingBase, me.Project.PingKey
		cfg.Contexts[res.Name] = ctx
		if err := cfg.Save(); err != nil {
			fmt.Fprintln(cmd.ErrOrStderr(), "warning: could not cache the ping key:", err)
		}
	}
	return firstSet(base, me.Project.PingBase), me.Project.PingKey, nil
}

// client makes the ping module's client for these flags; opts tune it
// for one use.
func (f *pingFlags) client(cmd *cobra.Command, base, key string, opts ...ping.Option) (*ping.Client, error) {
	return cli.PingClient(base, key, f.g.debug, cmd.ErrOrStderr(), opts...)
}

// serverPingBase is where pings go when only a ping key is given: the
// named context, else VINK_SERVER, else the current context; a context
// that has pinged before knows the server's own ping address.
func (f *pingFlags) serverPingBase() (string, error) {
	fromContext := func(name string) (string, error) {
		cfg, err := cli.LoadConfig(cli.ConfigPath())
		if err != nil {
			return "", err
		}
		if name == "" {
			name = cfg.Current
		}
		ctx, ok := cfg.Contexts[name]
		switch {
		case ok && ctx.PingBase != "":
			return ctx.PingBase, nil
		case ok && ctx.Server != "":
			return strings.TrimRight(ctx.Server, "/") + "/ping/", nil
		case f.context != "":
			return "", cli.UserError("no context named %q; run vink ctx ls", f.context)
		}
		return "", cli.UserError("a ping key needs an address: set VINK_PING_URL to a ping URL up to the key (https://vink.example.com/ping/), or VINK_SERVER")
	}
	if f.context != "" {
		return fromContext(f.context)
	}
	if s := os.Getenv("VINK_SERVER"); s != "" {
		return strings.TrimRight(s, "/") + "/ping/", nil
	}
	return fromContext("")
}

func firstSet(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// createFlags make the monitor from the ping, set up as the job runs.
// The settings apply on create only: a monitor that exists is left as it is.
type createFlags struct {
	create                                               bool
	name, period, cron, tz, grace, tolerance, maxRuntime string
	tags                                                 []string
	created                                              bool
}

var createSettings = []string{"name", "period", "cron", "tz", "grace", "tolerance", "max-runtime", "tag"}

func (c *createFlags) add(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.BoolVar(&c.create, "create", false, "create the monitor on first ping, set up by the flags below")
	fl.StringVar(&c.name, "name", "", "with --create: the monitor's name (default: the slug)")
	fl.StringVar(&c.period, "period", "", "with --create: a ping expected every period, like 1h or 1d (default 1d)")
	fl.StringVar(&c.cron, "cron", "", `with --create: pings expected on a cron schedule, like "0 3 * * *"`)
	fl.StringVar(&c.tz, "tz", "", "with --create: the schedule's IANA timezone (default: the project's)")
	fl.StringVar(&c.grace, "grace", "", "with --create: how late a ping may be before the monitor is down (default 1h)")
	fl.StringVar(&c.tolerance, "tolerance", "", "with --create: how late a ping still counts as on time")
	fl.StringVar(&c.maxRuntime, "max-runtime", "", "with --create: fail a run whose finish has not come by then")
	fl.StringArrayVar(&c.tags, "tag", nil, "with --create: a tag; repeat for more")
	cmd.MarkFlagsMutuallyExclusive("period", "cron")
}

// option is the Create option for the pings, or nil without --create.
func (c *createFlags) option(cmd *cobra.Command) (ping.PingOption, error) {
	given := false
	for _, n := range createSettings {
		given = given || cmd.Flags().Changed(n)
	}
	if !c.create {
		if given {
			return nil, cli.UserError("--name, --period, --cron, --tz, --grace, --tolerance, --max-runtime and --tag set up a new monitor: add --create")
		}
		return nil, nil
	}
	opts := []ping.CreateOption{ping.WasCreated(&c.created)}
	if c.name != "" {
		opts = append(opts, ping.Name(c.name))
	}
	if c.cron != "" {
		opts = append(opts, ping.Cron(c.cron))
	}
	if c.tz != "" {
		opts = append(opts, ping.Timezone(c.tz))
	}
	for _, d := range []struct {
		flag, value string
		opt         func(time.Duration) ping.CreateOption
	}{{"period", c.period, ping.Period}, {"grace", c.grace, ping.Grace}, {"tolerance", c.tolerance, ping.Tolerance}, {"max-runtime", c.maxRuntime, ping.MaxRuntime}} {
		if d.value == "" {
			continue
		}
		v, err := domain.ParseDuration(d.value)
		if err != nil || v <= 0 {
			return nil, cli.UserError("--%s: %q is not a duration like 90s, 30m, 2h or 1d", d.flag, d.value)
		}
		opts = append(opts, d.opt(v.Std()))
	}
	if len(c.tags) > 0 {
		opts = append(opts, ping.Tags(c.tags...))
	}
	return ping.Create(opts...), nil
}

// noteExisting says, when settings were given, that the monitor was there
// already and kept its own.
func (c *createFlags) noteExisting(cmd *cobra.Command, slug string) {
	if !c.create || c.created {
		return
	}
	for _, n := range createSettings {
		if cmd.Flags().Changed(n) {
			fmt.Fprintf(cmd.ErrOrStderr(), "note: monitor %s exists; the create settings were not applied (change it in vink)\n", slug)
			return
		}
	}
}

func newPingCmd(g *globals) *cobra.Command {
	f := &pingFlags{clientFlags: clientFlags{g: g}}
	cf := &createFlags{}
	var start, fail, logNote bool
	var exitCode int
	var msg, rid string
	var bodyFrom string
	cmd := &cobra.Command{
		Use:   "ping <slug>",
		Short: "Send a heartbeat ping with the project's ping key",
		Long: `vink ping sends one heartbeat ping: a success, or with --start, --fail,
--exit or --log one of those signals. --log adds a progress note to the
monitor's history and changes nothing else; give it --msg, --body - or both.

With --ping-key or VINK_PING_KEY it
needs no API key: the ping goes to --ping-url or VINK_PING_URL (a ping URL
up to the key, ending in /ping/), else to VINK_SERVER or the context's
server. Without a ping key, the context's API key looks the ping key up
once; that needs a read-write key.

--create makes the monitor from this ping when it does not exist yet,
set up by --name, --period or --cron, --tz, --grace, --tolerance,
--max-runtime and --tag. They apply on create only: a monitor that
exists keeps its own settings.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if logNote && msg == "" && bodyFrom != "-" {
				return cli.UserError("--log sends a note: add --msg or --body -")
			}
			create, err := cf.option(cmd)
			if err != nil {
				return err
			}
			base, key, err := pingTarget(cmd, f)
			if err != nil {
				return err
			}
			pc, err := f.client(cmd, base, key)
			if err != nil {
				return err
			}
			var opts []ping.PingOption
			if msg != "" {
				opts = append(opts, ping.Msg(msg))
			}
			if rid != "" {
				opts = append(opts, ping.RunID(rid))
			}
			if create != nil {
				opts = append(opts, create)
			}
			if bodyFrom == "-" {
				// the end of a log says why it failed: keep the last bytes
				tw := &tailWriter{n: ping.DefaultBodyLimit}
				if _, err := io.Copy(tw, cmd.InOrStdin()); err != nil {
					return err
				}
				opts = append(opts, ping.Body(tw.Bytes()))
			}
			m, ctx := pc.Monitor(args[0]), cmd.Context()
			switch {
			case start:
				err = m.Start(ctx, opts...)
			case fail:
				err = m.Fail(ctx, opts...)
			case logNote:
				err = m.Log(ctx, "", opts...)
			case cmd.Flags().Changed("exit"):
				err = m.Exit(ctx, exitCode, opts...)
			default:
				err = m.Success(ctx, opts...)
			}
			if err != nil {
				return cli.PingError(err)
			}
			cf.noteExisting(cmd, args[0])
			if !f.quiet {
				fmt.Fprintln(cmd.OutOrStdout(), "ok")
			}
			return nil
		},
	}
	f.add(cmd)
	cmd.Flags().BoolVar(&start, "start", false, "send a start signal")
	cmd.Flags().BoolVar(&fail, "fail", false, "send a fail signal")
	cmd.Flags().IntVar(&exitCode, "exit", 0, "send an exit code (0 is ok)")
	cmd.Flags().BoolVar(&logNote, "log", false, "send a progress note (--msg or --body -) that changes no state")
	cmd.Flags().StringVar(&msg, "msg", "", "a short message stored with the ping")
	cmd.Flags().StringVar(&rid, "rid", "", "run id pairing a start with its finish")
	cf.add(cmd)
	cmd.Flags().StringVar(&bodyFrom, "body", "", "read the body from stdin with --body -, keeping its last 64 kB")
	cmd.MarkFlagsMutuallyExclusive("start", "fail", "exit", "log")
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
	f := &pingFlags{clientFlags: clientFlags{g: g}}
	cf := &createFlags{}
	var tail int
	cmd := &cobra.Command{
		Use:   "run <slug> -- <command> [args…]",
		Short: "Run a command between a start ping and a finish ping carrying its exit code and output tail",
		Long: `vink run wraps a job: it sends /start, runs the command with stdout and
stderr passed through, then sends /<exit code> with the last bytes of
output as the body, and exits with the command's exit code. When the
server cannot be reached the command still runs. It finds the ping key
and the address as vink ping does: --ping-key or VINK_PING_KEY with
--ping-url or VINK_PING_URL needs no API key. --create and its settings
make the monitor as vink ping does.`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			create, err := cf.option(cmd)
			if err != nil {
				return err
			}
			var withCreate []ping.PingOption
			if create != nil {
				withCreate = append(withCreate, create)
			}
			base, key, err := pingTarget(cmd, f)
			if err != nil {
				return err
			}
			// the start must not hold the job up while vink is away: one
			// short try; the finish gets the module's retries
			starter, err := f.client(cmd, base, key, ping.WithAttempts(1), ping.WithTimeout(5*time.Second))
			if err != nil {
				return err
			}
			pc, err := f.client(cmd, base, key, ping.WithBodyLimit(max(tail, 0)))
			if err != nil {
				return err
			}
			run := pc.Monitor(args[0]).NewRun()
			ctx := cmd.Context()
			// Ctrl-C and SIGTERM cancel ctx; how the command ended is
			// reported all the same
			after := context.WithoutCancel(ctx)
			warn := func(what string, err error) {
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s ping failed: %v\n", what, cli.PingError(err))
				}
			}
			startErr := starter.Monitor(args[0]).Start(ctx, append([]ping.PingOption{ping.RunID(run.ID())}, withCreate...)...)
			warn("start", startErr)
			if startErr == nil {
				cf.noteExisting(cmd, args[0])
			}
			tw := &tailWriter{n: tail}
			// a context that cancellation does not reach: a cancelled ctx
			// would kill the command outright, while the signal passed on
			// below lets it stop in its own way
			child := exec.CommandContext(after, args[1], args[2:]...) //nolint:gosec // running the caller's command is the point
			child.Stdin = cmd.InOrStdin()
			child.Stdout = io.MultiWriter(cmd.OutOrStdout(), tw)
			child.Stderr = io.MultiWriter(cmd.ErrOrStderr(), tw)
			sigs := make(chan os.Signal, 1)
			signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
			defer signal.Stop(sigs)
			if ctx.Err() != nil {
				warn("finish", run.Fail(after, ping.Msg("stopped before "+args[1]+" started")))
				return cli.UserError("stopped before %s started", args[1])
			}
			if err := child.Start(); err != nil {
				warn("finish", run.Fail(after, ping.Msg("could not start: "+err.Error())))
				return cli.UserError("start %s: %v", args[1], err)
			}
			done := make(chan error, 1)
			go func() { done <- child.Wait() }()
			var waitErr error
		wait:
			for {
				select {
				case s := <-sigs:
					_ = child.Process.Signal(s)
				case waitErr = <-done:
					break wait
				}
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
			// with --create the finish makes the monitor too, when the
			// start could not
			warn("finish", run.Exit(after, code, append([]ping.PingOption{ping.Body(tw.Bytes())}, withCreate...)...))
			if code != 0 {
				return &cli.ExitError{Code: code, Err: fmt.Errorf("%s exited with %d", args[1], code)}
			}
			return nil
		},
	}
	f.add(cmd)
	cf.add(cmd)
	cmd.Flags().IntVar(&tail, "tail", 16*1024, "bytes of output to send as the body")
	return cmd
}
