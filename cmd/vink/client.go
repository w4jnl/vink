package main

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/w4jnl/vink/internal/cli"
	"github.com/w4jnl/vink/internal/logging"
)

// clientFlags are shared by every command that talks to a server.
type clientFlags struct {
	g       *globals
	context string
	asJSON  bool
	quiet   bool
}

func (f *clientFlags) add(cmd *cobra.Command, readCommand bool) {
	cmd.Flags().StringVar(&f.context, "context", "", "context name (default: the current one)")
	if readCommand {
		cmd.Flags().BoolVar(&f.asJSON, "json", false, "print the API payload unchanged")
	}
	cmd.Flags().BoolVar(&f.quiet, "quiet", false, "print nothing on success")
}

// connect resolves the context and builds a client and a printer.
func (f *clientFlags) connect(cmd *cobra.Command) (*cli.Client, *cli.Config, cli.Resolved, *cli.Printer, error) {
	cfg, err := cli.LoadConfig(cli.ConfigPath())
	if err != nil {
		return nil, nil, cli.Resolved{}, nil, err
	}
	res, err := cfg.Resolve(f.context, os.Getenv)
	if err != nil {
		return nil, nil, cli.Resolved{}, nil, err
	}
	c := cli.NewClient(res.Server, res.Key)
	c.Debug = f.g.debug
	c.Log = cmd.ErrOrStderr()
	mode, _ := logging.ParseColorMode(f.g.color)
	p := &cli.Printer{Out: cmd.OutOrStdout(), Color: logging.ColorEnabled(mode, cmd.OutOrStdout())}
	return c, cfg, res, p, nil
}

// pageOf is the API's envelope.
type pageOf[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

// listAll follows next_cursor until the end and returns every item plus
// the raw first page for --json.
func listAll[T any](ctx context.Context, c *cli.Client, path string) ([]T, []byte, error) {
	var (
		all    []T
		first  []byte
		cursor string
	)
	for {
		p := path
		if cursor != "" {
			sep := "?"
			if strings.Contains(p, "?") {
				sep = "&"
			}
			p += sep + "cursor=" + cursor
		}
		var page pageOf[T]
		raw, err := c.DoRaw(ctx, "GET", p, nil, &page)
		if err != nil {
			return nil, nil, err
		}
		if first == nil {
			first = raw
		}
		all = append(all, page.Items...)
		if page.NextCursor == nil || *page.NextCursor == "" {
			return all, first, nil
		}
		cursor = *page.NextCursor
	}
}

// monitor is the API's monitor representation, as the CLI reads it.
type monitor struct {
	Slug       string     `json:"slug"`
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	Tags       []string   `json:"tags"`
	State      string     `json:"state"`
	StateSince time.Time  `json:"state_since"`
	LastObsAt  *time.Time `json:"last_obs_at"`
	LastOkAt   *time.Time `json:"last_ok_at"`
	NextDueAt  *time.Time `json:"next_due_at"`
	ExpectedAt *time.Time `json:"expected_at"`
	Paused     bool       `json:"paused"`
	PingURL    string     `json:"ping_url"`
	Schedule   *struct {
		Period string `json:"period"`
		Cron   string `json:"cron"`
	} `json:"schedule"`
	Timezone string `json:"timezone"`
	Grace    string `json:"grace"`
}

func (m monitor) schedule() string {
	if m.Schedule == nil {
		return ""
	}
	if m.Schedule.Cron != "" {
		return "cron " + m.Schedule.Cron
	}
	return "every " + m.Schedule.Period
}

func fmtTime(t *time.Time, now time.Time, f func(time.Time, time.Time) string) string {
	if t == nil {
		return "-"
	}
	return f(*t, now)
}
