package http

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/config"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/http/web"
	"github.com/w4jnl/vink/internal/service"
	"github.com/w4jnl/vink/internal/timefmt"
	"github.com/w4jnl/vink/internal/version"
)

// started is when this process came up; the Server tab shows it as "up for".
var started = time.Now()

// serverFacts gathers what the instance admin's Server tab shows: the
// build, the database, how people sign in, and the network. Everything
// comes from the running config and the loops; nothing is fetched.
func (d Deps) serverFacts(ctx context.Context) web.ServerFacts {
	cfg := d.Cfg
	f := web.ServerFacts{
		Build: [][2]string{{"version", version.Version}, {"commit", version.Commit}, {"built", version.Date},
			{"go", runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH}, {"up for", timefmt.Span(time.Since(started))}},
	}

	size := "unknown"
	if st, err := os.Stat(cfg.DB.Path); err == nil {
		size = bytesOf(st.Size())
		if wal, err := os.Stat(cfg.DB.Path + "-wal"); err == nil {
			size += ", WAL " + bytesOf(wal.Size())
		}
	}
	f.Database = [][2]string{{"path", cfg.DB.Path}, {"size", size},
		{"observations", "kept " + strconv.Itoa(cfg.Retention.ObservationsDays) + " d"}, {"bodies", "kept " + strconv.Itoa(cfg.Retention.BodiesDays) + " d"}}

	local := "off"
	if cfg.Auth.Local.Enabled {
		local = "on"
	}
	roles := func(r string) string {
		if r == config.RolesVink {
			return "roles set in vink"
		}
		return "roles from its groups"
	}
	proxy := "off"
	if cfg.Auth.Proxy.Enabled {
		proxy = "on, from " + joinAnd(cfg.Auth.Proxy.TrustedCIDRs) + "; " + roles(cfg.Auth.Proxy.Roles)
	}
	oidcFact := "off"
	if cfg.Auth.OIDC.Enabled {
		oidcFact = "on, " + cfg.Auth.OIDC.Issuer + "; " + roles(cfg.Auth.OIDC.Roles)
	}
	adminKeys := "from any address"
	if len(cfg.Auth.AdminKeys.AllowedCIDRs) > 0 {
		adminKeys = "from " + joinAnd(cfg.Auth.AdminKeys.AllowedCIDRs)
	}
	adminGroup := cfg.Auth.Proxy.InstanceAdminGroup
	if !cfg.Auth.Proxy.Enabled && cfg.Auth.OIDC.Enabled {
		adminGroup = cfg.Auth.OIDC.InstanceAdminGroup
	}
	if cfg.Auth.Local.Enabled {
		local = "on, two-factor " + cfg.Auth.Local.TOTP
	}
	f.SignIn = [][2]string{{"local accounts", local}, {"proxy", proxy}, {"oidc", oidcFact},
		{"admin group", adminGroup}, {"admin keys", adminKeys}, {"sessions", timefmt.Span(service.SessionTTL) + ", sliding"}}

	outbound := "none"
	if cfg.Outbound.Proxy != "" {
		outbound = cfg.Outbound.Proxy
	}
	private := "refused"
	if cfg.Outbound.AllowPrivateTargets {
		private = "allowed"
	}
	lag := "idle"
	if d.Sched != nil {
		if last := d.Sched.LastTick(); !last.IsZero() {
			lag = time.Since(last).Truncate(time.Millisecond).String()
		}
	}
	agents := "none"
	if d.Gateway != nil {
		total := 0
		if sums, err := d.Svc.OrgSummaries(ctx, domain.Scope{InstanceAdmin: true, Role: domain.RoleOwner}); err == nil {
			for _, s := range sums {
				total += s.Agents
			}
		}
		if total > 0 {
			agents = strconv.Itoa(d.Gateway.ConnectedCount()) + " of " + strconv.Itoa(total) + " connected"
		}
	}
	pings := "the main listener"
	switch {
	case cfg.Ping.Listen != "" && cfg.PingsOnMain():
		pings = "the main listener and " + cfg.Ping.Listen
	case cfg.Ping.Listen != "":
		pings = cfg.Ping.Listen + " only"
	}
	f.Network = [][2]string{{"base url", cfg.Server.BaseURL}, {"pings", pings}, {"outbound proxy", outbound}, {"private targets", private}, {"scheduler lag", lag}, {"agents", agents}}
	return f
}

func bytesOf(n int64) string {
	switch {
	case n >= 1<<30:
		return strconv.FormatFloat(float64(n)/(1<<30), 'f', 1, 64) + " GB"
	case n >= 1<<20:
		return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + " MB"
	case n >= 1<<10:
		return strconv.FormatFloat(float64(n)/(1<<10), 'f', 1, 64) + " kB"
	}
	return strconv.FormatInt(n, 10) + " B"
}

func joinAnd(list []string) string {
	switch len(list) {
	case 0:
		return "nowhere"
	case 1:
		return list[0]
	}
	return strings.Join(list[:len(list)-1], ", ") + " and " + list[len(list)-1]
}
