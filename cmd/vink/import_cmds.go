package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/w4jnl/vink/internal/apply"
	"github.com/w4jnl/vink/internal/cli"
	"github.com/w4jnl/vink/internal/importer"
)

func newImportCmd(g *globals) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Convert another monitor's export into an apply file",
		Long:  "Writes a vink.yaml you can read, edit and apply; --apply sends it to the current context at once. What vink has no equivalent for is listed on stderr.",
	}
	cmd.AddCommand(
		newImportSourceCmd(g, "healthchecks", "The Healthchecks listing from GET /api/v3/checks/ (v1 and v2 work too)", importer.Healthchecks),
		newImportSourceCmd(g, "kuma", "An Uptime Kuma backup from Settings → Backup → Export", importer.Kuma),
	)
	return cmd
}

func newImportSourceCmd(g *globals, name, short string, convert func([]byte) (*importer.Result, error)) *cobra.Command {
	f := &clientFlags{g: g}
	var file, out string
	var doApply, dryRun bool
	cmd := &cobra.Command{
		Use:   name + " -f export.json [-o vink.yaml | --apply]",
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			data, err := readInput(cmd, file)
			if err != nil {
				return err
			}
			res, err := convert(data)
			if err != nil {
				return cli.UserError("%v", err)
			}
			errOut := cmd.ErrOrStderr()
			for _, s := range res.Skipped {
				fmt.Fprintln(errOut, "skipped  "+s)
			}
			for _, n := range res.Notes {
				fmt.Fprintln(errOut, "note     "+n)
			}
			yaml, err := apply.Encode(res.File)
			if err != nil {
				return err
			}
			if !doApply {
				if out == "" || out == "-" {
					_, err := cmd.OutOrStdout().Write(yaml)
					return err
				}
				if err := os.WriteFile(out, yaml, 0o600); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "wrote %s: %d monitors, %d channels\n", out, len(res.File.Monitors), len(res.File.Channels))
				return nil
			}
			parsed, err := apply.Parse(yaml, true)
			if err != nil {
				return cli.UserError("%v", err)
			}
			c, _, _, p, err := f.connect(cmd)
			if err != nil {
				return err
			}
			path := "/apply"
			if dryRun {
				path += "?dry_run=1"
			}
			var diff apply.Diff
			raw, err := c.DoRaw(cmd.Context(), "PUT", path, parsed, &diff)
			if err != nil {
				return err
			}
			if f.asJSON {
				p.JSON(raw)
				return nil
			}
			if !f.quiet {
				printDiff(cmd.OutOrStdout(), diff)
			}
			return nil
		},
	}
	f.add(cmd, true)
	cmd.Flags().StringVarP(&file, "file", "f", "", "the export; - reads stdin")
	_ = cmd.MarkFlagRequired("file")
	cmd.Flags().StringVarP(&out, "out", "o", "", "write the apply file here instead of stdout")
	cmd.Flags().BoolVar(&doApply, "apply", false, "apply to the current context instead of printing the file")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "with --apply: print the diff without applying")
	return cmd
}
