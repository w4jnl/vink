package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/w4jnl/vink/internal/cli"
)

func newCtxCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{Use: "ctx", Short: "Manage contexts: a server URL plus an API key, like kubectl"}
	var server, key string
	add := &cobra.Command{
		Use:   "add <name>",
		Short: "Add or replace a context and make it current when it is the first",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := cli.LoadConfig(cli.ConfigPath())
			if err != nil {
				return err
			}
			if err := cfg.Add(args[0], cli.Context{Server: server, Key: key}); err != nil {
				return err
			}
			// ask once what the key is, so vink ctx ls can say
			c := cfg.Contexts[args[0]]
			info, lookupErr := cli.LookupKey(cmd.Context(), c.Server, c.Key, keyLookupTimeout)
			if lookupErr == nil {
				info.Apply(&c)
				cfg.Contexts[args[0]] = c
			}
			if err := cfg.Save(); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			kind, scope := c.Describe(time.Now())
			switch kind {
			case cli.KindUnknown:
				fmt.Fprintf(out, "added context %s (%s); the server did not say what the key is (%v), vink ctx ls asks again\n", args[0], c.Server, lookupErr)
			case cli.KindAdmin:
				fmt.Fprintf(out, "added context %s (%s): instance admin key, %s\n", args[0], c.Server, strings.TrimPrefix(strings.TrimPrefix(scope, "instance"), ", "))
			default:
				fmt.Fprintf(out, "added context %s (%s): %s key for %s\n", args[0], c.Server, kind, scope)
			}
			return nil
		},
	}
	add.Flags().StringVar(&server, "server", "", "server URL, for example https://vink.w4j.nl")
	add.Flags().StringVar(&key, "key", "", "API key: a project or org key (vk_…), or an instance admin key (vka_…)")
	_ = add.MarkFlagRequired("server")
	_ = add.MarkFlagRequired("key")

	use := &cobra.Command{
		Use:   "use <name>",
		Short: "Make a context current",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := cli.LoadConfig(cli.ConfigPath())
			if err != nil {
				return err
			}
			if err := cfg.Use(args[0]); err != nil {
				return err
			}
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "using context %s\n", args[0])
			return nil
		},
	}
	rm := &cobra.Command{
		Use:   "rm <name>",
		Short: "Remove a context",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := cli.LoadConfig(cli.ConfigPath())
			if err != nil {
				return err
			}
			if err := cfg.Remove(args[0]); err != nil {
				return err
			}
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed context %s\n", args[0])
			return nil
		},
	}
	var asJSON bool
	ls := &cobra.Command{
		Use:   "ls",
		Short: "List contexts",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := cli.LoadConfig(cli.ConfigPath())
			if err != nil {
				return err
			}
			if err := lookupKinds(cmd.Context(), cfg); err != nil {
				return err
			}
			now := time.Now()
			if asJSON {
				type row struct {
					Name, Server string
					Current      bool
					Kind, Scope  string
					ExpiresAt    *time.Time `json:",omitempty"`
				}
				rows := []row{}
				for _, n := range cfg.Names() {
					c := cfg.Contexts[n]
					kind, scope := c.Describe(now)
					if kind == cli.KindAdmin {
						scope = "instance"
					}
					rows = append(rows, row{n, c.Server, n == cfg.Current, kind, scope, c.ExpiresAt})
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(rows)
			}
			p := &cli.Printer{Out: cmd.OutOrStdout()}
			var rows [][]string
			for _, n := range cfg.Names() {
				c := cfg.Contexts[n]
				mark := " "
				if n == cfg.Current {
					mark = "*"
				}
				kind, scope := c.Describe(now)
				if scope == "" {
					scope = "-"
				}
				rows = append(rows, []string{mark, n, kind, scope, c.Server, c.Key[:min(12, len(c.Key))] + "…"})
			}
			if len(rows) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no contexts; run vink ctx add <name> --server <url> --key <api key>")
				return nil
			}
			p.Table([]string{" ", "NAME", "KIND", "SCOPE", "SERVER", "KEY"}, rows)
			return nil
		},
	}
	ls.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	cmd.AddCommand(add, use, rm, ls)
	_ = g
	return cmd
}

// keyLookupTimeout bounds asking a server what a key is, as completion's
// lookups are bounded.
const keyLookupTimeout = 2 * time.Second

// lookupKinds asks, at once and in parallel, the servers of contexts
// whose key kind is not known yet (saved before vink knew kinds, or added
// while the server was away), and saves what they say. A server that does
// not answer leaves its row unknown, to be asked again next time.
func lookupKinds(ctx context.Context, cfg *cli.Config) error {
	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		changed bool
	)
	pending := map[string]cli.Context{}
	for _, n := range cfg.Names() {
		if c := cfg.Contexts[n]; c.Kind == "" {
			pending[n] = c
		}
	}
	for n, c := range pending {
		wg.Add(1)
		go func(name string, c cli.Context) {
			defer wg.Done()
			info, err := cli.LookupKey(ctx, c.Server, c.Key, keyLookupTimeout)
			if err != nil {
				return
			}
			info.Apply(&c)
			mu.Lock()
			cfg.Contexts[name] = c
			changed = true
			mu.Unlock()
		}(n, c)
	}
	wg.Wait()
	if !changed {
		return nil
	}
	return cfg.Save()
}
