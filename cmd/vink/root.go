package main

import (
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/w4jnl/vink/internal/logging"
	"github.com/w4jnl/vink/internal/version"
)

// globals are the flags every subcommand accepts.
type globals struct {
	debug bool
	color string
}

func newRootCmd() *cobra.Command {
	g := &globals{}
	root := &cobra.Command{
		Use:           "vink",
		Short:         "vink · " + version.Tagline,
		Long:          "vink is a self-hosted heartbeat and uptime monitor. Jobs ping it, and it probes services.",
		Version:       version.Version,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			mode, err := logging.ParseColorMode(g.color)
			if err != nil {
				return err
			}
			logger := logging.New(logging.Options{Debug: g.debug, Color: mode, Out: cmd.ErrOrStderr()})
			// The default logger is the one process-wide setting we allow, so
			// library code that logs through slog lands in the same handler.
			slog.SetDefault(logger)
			cmd.SetContext(logging.WithLogger(cmd.Context(), logger))
			return nil
		},
	}
	root.SetVersionTemplate("vink {{.Version}}\n")
	pf := root.PersistentFlags()
	pf.BoolVarP(&g.debug, "debug", "d", false, "debug logging with colour")
	pf.StringVar(&g.color, "color", "auto", "colour output: auto, always or never")

	root.AddCommand(newVersionCmd(g), newMigrateCmd())
	return root
}
