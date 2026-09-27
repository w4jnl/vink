package main

import (
	"encoding/json"
	"fmt"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/w4jnl/vink/internal/version"
)

func newVersionCmd(_ *globals) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the build version, commit and Go version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			if asJSON {
				enc := json.NewEncoder(out)
				return enc.Encode(map[string]string{
					"version": version.Version,
					"commit":  version.Commit,
					"date":    version.Date,
					"go":      runtime.Version(),
				})
			}
			if err := version.Lockup(out); err != nil {
				return err
			}
			_, err := fmt.Fprintf(out, "\n%s\n", version.String())
			return err
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	return cmd
}
