package http

import (
	"context"
	"testing"

	"github.com/w4jnl/vink/internal/config"
)

// TestServerFactsSignIn: the Server tab says where each provider's roles
// come from and where admin keys are accepted.
func TestServerFactsSignIn(t *testing.T) {
	cfg := config.Default()
	cfg.DB.Path = t.TempDir() + "/vink.db"
	cfg.Auth.Proxy.Enabled = true
	cfg.Auth.Proxy.TrustedCIDRs = []string{"10.0.0.5/32"}
	cfg.Auth.Proxy.Roles = config.RolesVink
	cfg.Auth.AdminKeys.AllowedCIDRs = []string{"10.0.0.0/8", "192.168.1.0/24"}
	got := map[string]string{}
	for _, kv := range (Deps{Cfg: cfg}).serverFacts(context.Background()).SignIn {
		got[kv[0]] = kv[1]
	}
	for k, want := range map[string]string{
		"proxy":      "on, from 10.0.0.5/32; roles set in vink",
		"oidc":       "off",
		"admin keys": "from 10.0.0.0/8 and 192.168.1.0/24",
	} {
		if got[k] != want {
			t.Errorf("%s: %q, want %q", k, got[k], want)
		}
	}
	cfg.Auth.Proxy.Roles = config.RolesGroups
	cfg.Auth.AdminKeys.AllowedCIDRs = nil
	for _, kv := range (Deps{Cfg: cfg}).serverFacts(context.Background()).SignIn {
		got[kv[0]] = kv[1]
	}
	if got["proxy"] != "on, from 10.0.0.5/32; roles from its groups" || got["admin keys"] != "from any address" {
		t.Errorf("groups mode: %q, %q", got["proxy"], got["admin keys"])
	}
}
