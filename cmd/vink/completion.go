package main

import (
	"context"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/w4jnl/vink/internal/cli"
)

// Shell completion. cobra's `vink completion bash|zsh|fish|powershell`
// writes the script; what it completes comes from here: contexts from
// the config file, monitor slugs, open incidents and tags from the
// current context's server, and the enumerated flags. A server that does
// not answer within two seconds completes nothing, quietly.

type compFunc = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective)

// completionTimeout bounds one lookup at the server.
const completionTimeout = 2 * time.Second

func addCompletions(root *cobra.Command, g *globals) {
	root.CompletionOptions.HiddenDefaultCmd = false
	monitors := completeFromServer(g, monitorSlugs)
	walk(root, func(c *cobra.Command) {
		if c.Flags().Lookup("context") != nil {
			_ = c.RegisterFlagCompletionFunc("context", completeContexts)
		}
		switch c.Name() {
		case "get", "logs", "pause", "resume", "check", "ping", "run":
			c.ValidArgsFunction = monitors
		case "ack":
			c.ValidArgsFunction = completeFromServer(g, openIncidents)
		case "ls":
			_ = c.RegisterFlagCompletionFunc("state", fixed("up", "late", "down", "paused", "new"))
			_ = c.RegisterFlagCompletionFunc("tag", completeFromServer(g, monitorTags))
		case "use", "rm":
			if c.Parent() != nil && c.Parent().Name() == "ctx" {
				c.ValidArgsFunction = completeContexts
			}
		}
		if c.Flags().Lookup("role") != nil {
			_ = c.RegisterFlagCompletionFunc("role", fixed("owner", "admin", "member", "viewer"))
		}
		if c.Flags().Lookup("access") != nil {
			_ = c.RegisterFlagCompletionFunc("access", fixed("ro", "rw"))
		}
	})
}

func walk(c *cobra.Command, fn func(*cobra.Command)) {
	fn(c)
	for _, sub := range c.Commands() {
		walk(sub, fn)
	}
}

// fixed completes an enumerated flag.
func fixed(values ...string) compFunc {
	return func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return values, cobra.ShellCompDirectiveNoFileComp
	}
}

// completeContexts lists the config file's contexts with their servers.
func completeContexts(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	cfg, err := cli.LoadConfig(cli.ConfigPath())
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, name := range cfg.Names() {
		out = append(out, name+"\t"+cfg.Contexts[name].Server)
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

// completeFromServer resolves the command's context and asks the server;
// only the first positional argument is completed this way, the rest of
// a `run` line is the shell's.
func completeFromServer(g *globals, fn func(context.Context, *cli.Client) ([]string, error)) compFunc {
	return func(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveDefault
		}
		f := &clientFlags{g: g}
		f.context, _ = cmd.Flags().GetString("context")
		c, _, _, _, err := f.connect(cmd)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		c.Debug = false
		parent := cmd.Context()
		if parent == nil {
			parent = context.Background()
		}
		ctx, cancel := context.WithTimeout(parent, completionTimeout)
		defer cancel()
		out, err := fn(ctx, c)
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		return out, cobra.ShellCompDirectiveNoFileComp
	}
}

func monitorSlugs(ctx context.Context, c *cli.Client) ([]string, error) {
	list, _, err := listAll[monitor](ctx, c, "/monitors?limit=200")
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(list))
	for _, m := range list {
		out = append(out, m.Slug+"\t"+m.Name+" · "+m.State)
	}
	return out, nil
}

func monitorTags(ctx context.Context, c *cli.Client) ([]string, error) {
	list, _, err := listAll[monitor](ctx, c, "/monitors?limit=200")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range list {
		for _, t := range m.Tags {
			if !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

type openIncident struct {
	ID          string    `json:"id"`
	Monitor     string    `json:"monitor"`
	MonitorName string    `json:"monitor_name"`
	OpenedAt    time.Time `json:"opened_at"`
}

func openIncidents(ctx context.Context, c *cli.Client) ([]string, error) {
	list, _, err := listAll[openIncident](ctx, c, "/incidents?open=1&limit=200")
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(list))
	for _, inc := range list {
		out = append(out, inc.ID+"\t"+inc.Monitor+" · since "+inc.OpenedAt.UTC().Format("2 Jan 15:04"))
	}
	return out, nil
}
