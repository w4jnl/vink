package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/w4jnl/vink/internal/config"
	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/logging"
)

// serverFlags are shared by the commands that run on the server host
// against the database file: serve, migrate and admin.
type serverFlags struct {
	config string
	dbPath string
}

func (f *serverFlags) add(cmd *cobra.Command) {
	cmd.PersistentFlags().StringVar(&f.config, "config", "", "path to vink.toml (default: $VINK_CONFIG_FILE, else built-in defaults plus VINK_* env)")
	cmd.PersistentFlags().StringVar(&f.dbPath, "db", "", "database file (overrides db.path)")
}

func (f *serverFlags) load() (*config.Config, error) {
	path := f.config
	if path == "" {
		// make dev and containers point at a file this way instead of
		// passing --config to every command
		path = os.Getenv("VINK_CONFIG_FILE")
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	if f.dbPath != "" {
		cfg.DB.Path = f.dbPath
	}
	return cfg, nil
}

func (f *serverFlags) open(ctx context.Context) (*db.DB, *config.Config, error) {
	cfg, err := f.load()
	if err != nil {
		return nil, nil, err
	}
	d, err := db.Open(ctx, cfg.DB.Path)
	if err != nil {
		return nil, nil, err
	}
	return d, cfg, nil
}

// dbMigrate applies pending migrations before a server-side command runs.
func dbMigrate(cmd *cobra.Command, d *db.DB, log *slog.Logger) ([]string, error) {
	applied, err := db.Migrate(cmd.Context(), d.Writer, log)
	if err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return applied, nil
}

func newMigrateCmd() *cobra.Command {
	f := &serverFlags{}
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Apply, revert, list, create or dump database migrations",
	}
	f.add(cmd)

	up := &cobra.Command{
		Use:   "up",
		Short: "Apply pending migrations",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, _, err := f.open(cmd.Context())
			if err != nil {
				return err
			}
			defer d.Close()
			applied, err := db.Migrate(cmd.Context(), d.Writer, logging.FromContext(cmd.Context()))
			if err != nil {
				return err
			}
			if len(applied) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no pending migrations")
			}
			for _, v := range applied {
				fmt.Fprintln(cmd.OutOrStdout(), "applied", v)
			}
			return nil
		},
	}

	down := &cobra.Command{
		Use:   "down",
		Short: "Revert the most recent migration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, _, err := f.open(cmd.Context())
			if err != nil {
				return err
			}
			defer d.Close()
			v, err := db.Rollback(cmd.Context(), d.Writer, logging.FromContext(cmd.Context()))
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "rolled back", v)
			return nil
		},
	}

	status := &cobra.Command{
		Use:   "status",
		Short: "List migrations and whether they are applied",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, _, err := f.open(cmd.Context())
			if err != nil {
				return err
			}
			defer d.Close()
			list, err := db.List(cmd.Context(), d.Writer)
			if err != nil {
				return err
			}
			for _, m := range list {
				mark := "[ ]"
				if m.Applied {
					mark = "[X]"
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s %s %s\n", mark, m.Version, m.Name)
			}
			return nil
		},
	}

	var dir string
	newCmd := &cobra.Command{
		Use:   "new <name>",
		Short: "Create an empty migration file in the source tree",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := db.NewMigration(dir, args[0], time.Now())
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "created", path)
			return nil
		},
	}
	newCmd.Flags().StringVar(&dir, "dir", db.MigrationsDir, "migrations directory")

	var out string
	dump := &cobra.Command{
		Use:   "dump",
		Short: "Write the current schema as SQL (sqlc's input)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, _, err := f.open(cmd.Context())
			if err != nil {
				return err
			}
			defer d.Close()
			schema, err := db.Dump(cmd.Context(), d.Reader)
			if err != nil {
				return err
			}
			if out == "" || out == "-" {
				_, err = cmd.OutOrStdout().Write(schema)
				return err
			}
			if err := os.WriteFile(out, schema, 0o644); err != nil { //nolint:gosec // schema file is not sensitive
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "wrote", out)
			return nil
		},
	}
	dump.Flags().StringVar(&out, "out", "", "output file (default stdout)")

	cmd.AddCommand(up, down, status, newCmd, dump)
	return cmd
}
