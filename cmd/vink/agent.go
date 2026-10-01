package main

import (
	"errors"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/w4jnl/vink/internal/agent"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/logging"
	"github.com/w4jnl/vink/internal/version"
)

func newAgentCmd(g *globals) *cobra.Command {
	var server, token, tokenFile, labels, ca, pin, proxy string
	cmd := &cobra.Command{
		Use:   "agent --server wss://vink.example.com --token vat_…",
		Short: "Run a probe agent: connects out to the server and runs the checks it is given",
		Long: `vink agent runs inside a closed network. It dials out to the server over one
WebSocket, takes the checks assigned to it (by name or by labels) and reports
the results. It keeps nothing on disk and never listens on a port.

The token can also come from VINK_AGENT_TOKEN or --token-file, so it does not
show in the process list.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if token == "" {
				token = os.Getenv("VINK_AGENT_TOKEN")
			}
			if token == "" && tokenFile != "" {
				b, err := os.ReadFile(tokenFile)
				if err != nil {
					return err
				}
				token = strings.TrimSpace(string(b))
			}
			if token == "" {
				return errors.New("pass --token, --token-file or VINK_AGENT_TOKEN")
			}
			parsed, err := domain.ParseLabels(labels)
			if err != nil {
				return err
			}
			log := logging.FromContext(cmd.Context())
			if !g.debug {
				mode, _ := logging.ParseColorMode(g.color)
				log = logging.New(logging.Options{Level: "info", Color: mode, Out: cmd.ErrOrStderr()})
			}
			a, err := agent.New(agent.Options{Server: server, Token: token, Labels: parsed, CAPem: ca, Pin: pin, Proxy: proxy, Version: version.Version, Log: log})
			if err != nil {
				return err
			}
			log.Info("vink agent starting", "version", version.String(), "server", server, "labels", labels)
			if err := a.Run(cmd.Context()); err != nil && cmd.Context().Err() == nil {
				return err
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&server, "server", "", "server base URL, wss://vink.example.com (required)")
	f.StringVar(&token, "token", "", "agent token (or VINK_AGENT_TOKEN)")
	f.StringVar(&tokenFile, "token-file", "", "file holding the agent token")
	f.StringVar(&labels, "labels", "", "labels to announce on first contact, like site=dc2,zone=dmz")
	f.StringVar(&ca, "ca", "", "extra CA certificate file (PEM) for the server and the checks")
	f.StringVar(&pin, "pin", "", "SHA-256 of the server certificate; when set the chain is not consulted")
	f.StringVar(&proxy, "proxy", "", "http(s) proxy for the server connection and http checks")
	_ = cmd.MarkFlagRequired("server")
	return cmd
}
