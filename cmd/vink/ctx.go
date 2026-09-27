package main

import (
	"encoding/json"
	"fmt"

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
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "added context %s (%s)\n", args[0], cfg.Contexts[args[0]].Server)
			return nil
		},
	}
	add.Flags().StringVar(&server, "server", "", "server URL, for example https://vink.w4j.nl")
	add.Flags().StringVar(&key, "key", "", "project API key")
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
			if asJSON {
				type row struct {
					Name, Server string
					Current      bool
				}
				var rows []row
				for _, n := range cfg.Names() {
					rows = append(rows, row{n, cfg.Contexts[n].Server, n == cfg.Current})
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(rows)
			}
			p := &cli.Printer{Out: cmd.OutOrStdout()}
			var rows [][]string
			for _, n := range cfg.Names() {
				mark := " "
				if n == cfg.Current {
					mark = "*"
				}
				rows = append(rows, []string{mark, n, cfg.Contexts[n].Server, cfg.Contexts[n].Key[:min(11, len(cfg.Contexts[n].Key))] + "…"})
			}
			if len(rows) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no contexts; run vink ctx add <name> --server <url> --key <api key>")
				return nil
			}
			p.Table([]string{" ", "NAME", "SERVER", "KEY"}, rows)
			return nil
		},
	}
	ls.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	cmd.AddCommand(add, use, rm, ls)
	_ = g
	return cmd
}
