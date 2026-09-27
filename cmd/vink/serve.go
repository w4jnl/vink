package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/spf13/cobra"

	"github.com/w4jnl/vink/internal/auth"
	"github.com/w4jnl/vink/internal/config"
	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/engine"
	vhttp "github.com/w4jnl/vink/internal/http"
	"github.com/w4jnl/vink/internal/logging"
	"github.com/w4jnl/vink/internal/secrets"
	"github.com/w4jnl/vink/internal/service"
	"github.com/w4jnl/vink/internal/version"
)

func newServeCmd(g *globals) *cobra.Command {
	f := &serverFlags{}
	var printConfig bool
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the server: migrations, scheduler, dispatcher and HTTP",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := f.load()
			if err != nil {
				return err
			}
			if printConfig {
				out, err := cfg.Redacted().TOML()
				if err != nil {
					return err
				}
				_, err = cmd.OutOrStdout().Write(out)
				return err
			}
			log := logging.FromContext(cmd.Context())
			if !g.debug {
				mode, _ := logging.ParseColorMode(g.color)
				log = logging.New(logging.Options{Level: cfg.Log.Level, Format: cfg.Log.Format, Color: mode, Out: cmd.ErrOrStderr()})
				slog.SetDefault(log)
			}
			return runServe(cmd.Context(), cfg, log)
		},
	}
	f.add(cmd)
	cmd.Flags().BoolVar(&printConfig, "print-config", false, "print the effective configuration with secrets redacted and exit")
	return cmd
}

// runServe wires the process and blocks until ctx is cancelled.
func runServe(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	log.Info("starting", "version", version.String(), "db", cfg.DB.Path, "listen", cfg.Server.Listen)
	d, err := db.Open(ctx, cfg.DB.Path)
	if err != nil {
		return err
	}
	defer d.Close()
	if _, err := db.Migrate(ctx, d.Writer, log); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	keyring, err := secrets.Load(cfg.SecretKeyFile())
	if err != nil {
		return fmt.Errorf("secrets: %w", err)
	}
	bus := engine.NewBus()
	svcCfg := service.DefaultConfig()
	svcCfg.PingBaseURL = cfg.PingBaseURL()
	svcCfg.BodyLimit = int64(cfg.Ping.BodyLimit)
	svcCfg.Keyring = keyring
	svc := service.New(d, bus, log, svcCfg)
	sched := engine.NewScheduler(svc, bus, logging.Sub(log, "scheduler"), nil)
	authn, err := auth.New(svc, cfg.Auth, cfg.Server.BaseURL, logging.Sub(log, "auth"))
	if err != nil {
		return fmt.Errorf("auth: %w", err)
	}

	deps := vhttp.Deps{Cfg: cfg, Svc: svc, Auth: authn, Log: log, Sched: sched}
	withPing := cfg.Ping.Listen == ""

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	errc := make(chan error, 4)
	run := func(name string, fn func(context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(ctx); err != nil && !errors.Is(err, context.Canceled) {
				errc <- fmt.Errorf("%s: %w", name, err)
				cancel()
			}
		}()
	}
	run("scheduler", sched.Run)
	run("http", func(ctx context.Context) error {
		return vhttp.Run(ctx, log, "http", cfg.Server.Listen, vhttp.Handler(deps, withPing))
	})
	if !withPing {
		run("ping", func(ctx context.Context) error {
			return vhttp.Run(ctx, log, "ping", cfg.Ping.Listen, vhttp.PingHandler(deps))
		})
	}

	var firstErr error
	select {
	case <-ctx.Done():
	case firstErr = <-errc:
		cancel()
	}
	wg.Wait()
	close(errc)
	if firstErr == nil {
		for err := range errc {
			firstErr = err
			break
		}
	}
	if firstErr != nil {
		return firstErr
	}
	log.Info("stopped")
	return nil
}
