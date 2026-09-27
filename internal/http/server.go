// Package http assembles the HTTP surface: the ping ingress, the API, the
// web UI, static files and the health endpoints, each with its own
// middleware chain, on one or two listeners.
package http

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/w4jnl/vink/internal/auth"
	"github.com/w4jnl/vink/internal/config"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/engine"
	"github.com/w4jnl/vink/internal/http/api"
	"github.com/w4jnl/vink/internal/http/middleware"
	"github.com/w4jnl/vink/internal/http/ping"
	"github.com/w4jnl/vink/internal/logging"
	"github.com/w4jnl/vink/internal/service"
	"github.com/w4jnl/vink/internal/version"
)

// Deps is everything the handlers need.
type Deps struct {
	Cfg   *config.Config
	Svc   *service.Service
	Auth  *auth.Authenticator
	Log   *slog.Logger
	Sched *engine.Scheduler
	// Mount lets later packages (api, web) register on the main mux.
	Mount []func(mux *http.ServeMux)
}

// pingMux is the ping ingress without middleware.
func pingMux(d Deps) *http.ServeMux {
	mux := http.NewServeMux()
	h := ping.New(d.Svc, d.Log, ping.Options{
		BodyLimit: int64(d.Cfg.Ping.BodyLimit), RatePerMonitor: d.Cfg.Ping.RatePerMonitor, RatePerIP: d.Cfg.Ping.RatePerIP,
	})
	h.Routes(mux)
	return mux
}

// PingHandler is the ping ingress with its own chain, for a separate
// listener.
func PingHandler(d Deps) http.Handler {
	return chain(d, pingMux(d))
}

func chain(d Deps, h http.Handler) http.Handler {
	return middleware.Chain(h,
		middleware.RequestID,
		middleware.RealIP(d.Cfg.Server.TrustedProxies),
		middleware.Logger(d.Log),
		middleware.Recover(d.Log),
	)
}

// Handler is the main listener: everything, including pings unless a
// separate ping listener is configured.
func Handler(d Deps, withPing bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintf(w, "ok %s\n", version.Version)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := Ready(r.Context(), d); err != nil {
			d.Log.Warn("not ready", "err", err)
			http.Error(w, "not ready: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = fmt.Fprintln(w, "ready")
	})
	if withPing {
		mux.Handle("/ping/", pingMux(d))
	}
	if d.Auth != nil {
		api.New(d.Svc, d.Auth, logging.Sub(d.Log, "api")).Mount(mux)
	}
	mux.HandleFunc("GET /a/{token}", d.ackLink)
	for _, m := range d.Mount {
		m(mux)
	}
	return chain(d, mux)
}

// ackLink acknowledges an incident from a signed one-click token and
// sends the person to the project's incidents page.
func (d Deps) ackLink(w http.ResponseWriter, r *http.Request) {
	inc, err := d.Svc.AckIncidentByToken(r.Context(), r.PathValue("token"))
	if err != nil {
		if errors.Is(err, domain.ErrUnauthorized) || errors.Is(err, domain.ErrNotFound) {
			http.Error(w, "this acknowledgement link is invalid or has expired", http.StatusNotFound)
			return
		}
		if errors.Is(err, domain.ErrConflict) {
			http.Error(w, "this incident is already resolved", http.StatusConflict)
			return
		}
		d.Log.Error("ack link", "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	project, err := d.Svc.ProjectByID(r.Context(), inc.ProjectID)
	if err != nil {
		http.Error(w, "acknowledged", http.StatusOK)
		return
	}
	org, err := d.Svc.OrgByID(r.Context(), project.OrgID)
	if err != nil {
		http.Error(w, "acknowledged", http.StatusOK)
		return
	}
	http.Redirect(w, r, "/o/"+org.Slug+"/p/"+project.Slug+"/incidents", http.StatusSeeOther) //nolint:gosec // G710: slugs are validated [a-z0-9-] values from our own database, and the path is relative
}

// Ready reports whether the writer can take a lock within two seconds
// and the scheduler has ticked in the last thirty.
func Ready(ctx context.Context, d Deps) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	conn, err := d.Svc.DB().Writer.Conn(ctx)
	if err != nil {
		return fmt.Errorf("db: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("db not writable: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
		return fmt.Errorf("db: %w", err)
	}
	if d.Sched != nil {
		last := d.Sched.LastTick()
		if last.IsZero() || time.Since(last) > 30*time.Second {
			return errors.New("scheduler has not ticked in the last 30s")
		}
	}
	return nil
}

// Run serves h on listen until ctx is done, then drains for up to ten
// seconds.
func Run(ctx context.Context, log *slog.Logger, name, listen string, h http.Handler) error {
	srv := &http.Server{
		Addr: listen, Handler: h,
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 60 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 120 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", listen)
	if err != nil {
		return fmt.Errorf("%s: listen %s: %w", name, listen, err)
	}
	log.Info("listening", "name", name, "addr", ln.Addr().String())
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("%s: shutdown: %w", name, err)
		}
		return nil
	}
}
