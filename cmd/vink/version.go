package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/w4jnl/vink/internal/cli"
	"github.com/w4jnl/vink/internal/version"
)

func newVersionCmd(g *globals) *cobra.Command {
	var asJSON, checkServer bool
	f := &clientFlags{g: g}
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print the build version, commit and Go version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			serverVersion := ""
			if checkServer {
				c, _, _, _, err := f.connect(cmd)
				if err != nil {
					return err
				}
				serverVersion, err = healthVersion(cmd, c)
				if err != nil {
					return err
				}
			}
			if asJSON {
				enc := json.NewEncoder(out)
				payload := map[string]string{"version": version.Version, "commit": version.Commit, "date": version.Date, "go": runtime.Version()}
				if checkServer {
					payload["server"] = serverVersion
				}
				return enc.Encode(payload)
			}
			if err := version.Lockup(out); err != nil {
				return err
			}
			if _, err := fmt.Fprintf(out, "\n%s\n", version.String()); err != nil {
				return err
			}
			if checkServer {
				match := "matches"
				if serverVersion != version.Version {
					match = "differs from this cli"
				}
				_, err := fmt.Fprintf(out, "server %s (%s)\n", serverVersion, match)
				return err
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	cmd.Flags().BoolVar(&checkServer, "check-server", false, "also report the server's version")
	cmd.Flags().StringVar(&f.context, "context", "", "context name for --check-server")
	return cmd
}

// healthVersion reads the version from /healthz ("ok <version>").
func healthVersion(cmd *cobra.Command, c *cli.Client) (string, error) {
	req, err := http.NewRequestWithContext(cmd.Context(), http.MethodGet, c.Server+"/healthz", nil)
	if err != nil {
		return "", err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", cli.ServerError(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		return "", cli.ServerError(err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", cli.ServerError(fmt.Errorf("%s/healthz returned %s", c.Server, resp.Status))
	}
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(body)), "ok ")), nil
}
