package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/w4jnl/vink/internal/apply"
	"github.com/w4jnl/vink/internal/cli"
)

func readInput(cmd *cobra.Command, path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(cmd.InOrStdin())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, cli.UserError("%v", err)
	}
	return data, nil
}

func newApplyCmd(g *globals) *cobra.Command {
	f := &clientFlags{g: g}
	var file string
	var dryRun, prune bool
	cmd := &cobra.Command{
		Use:   "apply -f vink.yaml",
		Short: "Apply a declarative file to the project, or an org file to its projects, and print the diff",
		Long:  "Reads the file, expands ${VAR} from the environment, checks it against docs/apply-schema.json and sends it to the server. A project file applies to the context's project; an org file (org: and projects:) needs a context with an org key and applies every project it names. Exit 1 on a validation error, 2 on a server error.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			data, err := readInput(cmd, file)
			if err != nil {
				return err
			}
			data, err = apply.ExpandEnv(data)
			if err != nil {
				return cli.UserError("%v", err)
			}
			project, org, err := apply.ParseAny(data, true)
			if err != nil {
				return cli.UserError("%v", err)
			}
			c, _, _, p, err := f.connect(cmd)
			if err != nil {
				return err
			}
			var params []string
			if dryRun {
				params = append(params, "dry_run=1")
			}
			if prune {
				params = append(params, "prune=1")
			}
			query := ""
			if len(params) > 0 {
				query = "?" + strings.Join(params, "&")
			}
			if org != nil {
				// an org file goes to the org route with the context's org key
				if org.Org == "" {
					return cli.UserError("the org file needs org: <slug>")
				}
				var diff apply.OrgDiff
				raw, err := c.DoRaw(cmd.Context(), "PUT", "/orgs/"+org.Org+"/apply"+query, org, &diff)
				if err != nil {
					return err
				}
				if f.asJSON {
					p.JSON(raw)
					return nil
				}
				if !f.quiet {
					printOrgDiff(cmd.OutOrStdout(), diff)
				}
				return nil
			}
			var diff apply.Diff
			raw, err := c.DoRaw(cmd.Context(), "PUT", "/apply"+query, project, &diff)
			if err != nil {
				return err
			}
			if f.asJSON {
				p.JSON(raw)
				return nil
			}
			if f.quiet {
				return nil
			}
			printDiff(cmd.OutOrStdout(), diff)
			return nil
		},
	}
	f.add(cmd, true)
	cmd.Flags().StringVarP(&file, "file", "f", "", "the apply file; - reads stdin")
	_ = cmd.MarkFlagRequired("file")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the diff without applying")
	cmd.Flags().BoolVar(&prune, "prune", false, "delete monitors, channels and routes the file does not name")
	return cmd
}

func newExportCmd(g *globals) *cobra.Command {
	f := &clientFlags{g: g}
	var out, org string
	var secrets bool
	cmd := &cobra.Command{
		Use:   "export [-o vink.yaml] [--org slug]",
		Short: "Write the project as an apply file, or every project of an org with --org",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, _, _, _, err := f.connect(cmd)
			if err != nil {
				return err
			}
			path := "/export"
			if org != "" {
				path = "/orgs/" + org + "/export"
			}
			if secrets {
				path += "?secrets=1"
			}
			raw, err := c.DoRaw(cmd.Context(), "GET", path, nil, nil)
			if err != nil {
				return err
			}
			if out == "" || out == "-" {
				_, err := cmd.OutOrStdout().Write(raw)
				return err
			}
			mode := os.FileMode(0o644)
			if secrets {
				mode = 0o600
			}
			if err := os.WriteFile(out, raw, mode); err != nil {
				return cli.UserError("%v", err)
			}
			if !f.quiet {
				fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", out)
			}
			return nil
		},
	}
	f.add(cmd, false)
	cmd.Flags().StringVarP(&out, "out", "o", "", "write to this file instead of stdout")
	cmd.Flags().BoolVar(&secrets, "secrets", false, "include channel secrets (needs an rw key); the file is written with mode 0600")
	cmd.Flags().StringVar(&org, "org", "", "export every project of this org as one file; the context needs an org key")
	return cmd
}

// printOrgDiff writes an org apply's diff, one block per project.
func printOrgDiff(out io.Writer, diff apply.OrgDiff) {
	created, updated, recreated, deleted, unchanged := 0, 0, 0, 0, 0
	for _, p := range diff.Projects {
		head := p.Slug
		if p.Created {
			head += " (created)"
		}
		fmt.Fprintln(out, head)
		for _, name := range p.Diff.Created {
			fmt.Fprintf(out, "  + %s\n", name)
		}
		for _, name := range p.Updated {
			fmt.Fprintf(out, "  ~ %s\n", name)
		}
		for _, name := range p.Recreated {
			fmt.Fprintf(out, "  ! %s (recreated, state reset)\n", name)
		}
		for _, name := range p.Deleted {
			fmt.Fprintf(out, "  - %s\n", name)
		}
		created += len(p.Diff.Created)
		updated += len(p.Updated)
		recreated += len(p.Recreated)
		deleted += len(p.Deleted)
		unchanged += len(p.Unchanged)
	}
	scope := fmt.Sprintf("%d projects", len(diff.Projects))
	if pages := diff.StatusPages; pages != nil {
		fmt.Fprintln(out, "status pages of the org")
		for _, name := range pages.Created {
			fmt.Fprintf(out, "  + %s\n", name)
		}
		for _, name := range pages.Updated {
			fmt.Fprintf(out, "  ~ %s\n", name)
		}
		created += len(pages.Created)
		updated += len(pages.Updated)
		unchanged += len(pages.Unchanged)
		scope += " and the org's pages"
	}
	summary := fmt.Sprintf("%s: %d created, %d updated, %d recreated, %d deleted, %d unchanged", scope, created, updated, recreated, deleted, unchanged)
	if diff.DryRun {
		summary += " (dry run, nothing applied)"
	}
	fmt.Fprintln(out, summary)
}

// printDiff writes the apply diff the way apply and import show it.
func printDiff(out io.Writer, diff apply.Diff) {
	for _, name := range diff.Created {
		fmt.Fprintf(out, "+ %s\n", name)
	}
	for _, name := range diff.Updated {
		fmt.Fprintf(out, "~ %s\n", name)
	}
	for _, name := range diff.Recreated {
		fmt.Fprintf(out, "! %s (recreated, state reset)\n", name)
	}
	for _, name := range diff.Deleted {
		fmt.Fprintf(out, "- %s\n", name)
	}
	summary := fmt.Sprintf("%d created, %d updated, %d recreated, %d deleted, %d unchanged", len(diff.Created), len(diff.Updated), len(diff.Recreated), len(diff.Deleted), len(diff.Unchanged))
	if diff.DryRun {
		summary += " (dry run, nothing applied)"
	}
	fmt.Fprintln(out, summary)
}
