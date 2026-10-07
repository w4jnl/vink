package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func envOf(m map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func TestDefaultsValidate(t *testing.T) {
	cfg, err := LoadWith("", envOf(nil))
	if err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
	if cfg.Server.Listen != ":8080" || cfg.Ping.BodyLimit != 64*1024 || !cfg.Auth.Local.Enabled {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
	if got := cfg.SecretKeyFile(); got != "./secret.key" {
		t.Errorf("SecretKeyFile() = %q", got)
	}
}

func TestLoadFileAndEnvOverlay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vink.toml")
	body := `
[server]
listen = ":9090"
base_url = "https://vink.example.com"
trusted_proxies = ["10.0.0.0/8"]
[ping]
body_limit = "128KB"
[db]
path = "/var/lib/vink/vink.db"
[checks]
min_interval = "30s"
[auth.proxy]
enabled = true
secret = "env:VINK_PROXY_SECRET"
trusted_cidrs = ["10.0.0.0/8"]
group_map = { "CN=Monitoring Admins" = "corp:admin" }
[smtp]
host = "mail.example.com"
password = "hunter2"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	env := envOf(map[string]string{
		"VINK_PROXY_SECRET":           "s3cret",
		"VINK_SERVER_LISTEN":          ":7070",
		"VINK_PING_RATE_PER_IP":       "600",
		"VINK_AUTH_LOCAL_ENABLED":     "false",
		"VINK_SERVER_TRUSTED_PROXIES": "10.0.0.0/8, 192.168.0.0/16",
		"VINK_PING_BODY_LIMIT":        "1MB",
	})
	cfg, err := LoadWith(path, env)
	if err != nil {
		t.Fatalf("LoadWith: %v", err)
	}
	if cfg.Server.Listen != ":7070" {
		t.Errorf("env must override file: listen = %q", cfg.Server.Listen)
	}
	if cfg.Server.BaseURL != "https://vink.example.com" {
		t.Errorf("file value lost: base_url = %q", cfg.Server.BaseURL)
	}
	if cfg.Ping.RatePerIP != 600 || cfg.Ping.BodyLimit != 1<<20 {
		t.Errorf("env ints not applied: %+v", cfg.Ping)
	}
	if cfg.Auth.Local.Enabled {
		t.Error("VINK_AUTH_LOCAL_ENABLED=false not applied")
	}
	if len(cfg.Server.TrustedProxies) != 2 || cfg.Server.TrustedProxies[1] != "192.168.0.0/16" {
		t.Errorf("list override: %v", cfg.Server.TrustedProxies)
	}
	if cfg.Auth.Proxy.Secret != "s3cret" {
		t.Errorf("env: reference not resolved: %q", cfg.Auth.Proxy.Secret)
	}
	if cfg.Auth.Proxy.GroupMap["CN=Monitoring Admins"] != "corp:admin" {
		t.Errorf("group_map not decoded: %v", cfg.Auth.Proxy.GroupMap)
	}
	if cfg.Checks.MinInterval.String() != "30s" {
		t.Errorf("duration not decoded: %v", cfg.Checks.MinInterval)
	}
	if got := cfg.SecretKeyFile(); got != "/var/lib/vink/secret.key" {
		t.Errorf("SecretKeyFile() = %q", got)
	}

	red := cfg.Redacted()
	if red.SMTP.Password != "***" || red.Auth.Proxy.Secret != "***" {
		t.Errorf("secrets not redacted: %+v %+v", red.SMTP, red.Auth.Proxy)
	}
	if cfg.SMTP.Password != "hunter2" {
		t.Error("Redacted must not modify the original")
	}
	out, err := red.TOML()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "hunter2") || strings.Contains(string(out), "s3cret") {
		t.Errorf("secret leaked into TOML output:\n%s", out)
	}
}

func TestUnknownKeyIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vink.toml")
	if err := os.WriteFile(path, []byte("[server]\nlisten = \":1\"\nlisen_typo = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWith(path, envOf(nil)); err == nil || !strings.Contains(err.Error(), "unknown keys") {
		t.Fatalf("expected unknown key error, got %v", err)
	}
}

func TestMissingEnvRef(t *testing.T) {
	env := envOf(map[string]string{"VINK_METRICS_TOKEN": "env:NOPE"})
	if _, err := LoadWith("", env); err == nil || !strings.Contains(err.Error(), "NOPE") {
		t.Fatalf("expected missing env error, got %v", err)
	}
}

func TestValidation(t *testing.T) {
	cases := map[string]map[string]string{
		"bad level":         {"VINK_LOG_LEVEL": "loud"},
		"bad format":        {"VINK_LOG_FORMAT": "xml"},
		"bad base url":      {"VINK_SERVER_BASE_URL": "vink.example.com"},
		"bad cidr":          {"VINK_SERVER_TRUSTED_PROXIES": "10.0.0.0"},
		"zero body limit":   {"VINK_PING_BODY_LIMIT": "0"},
		"pings nowhere":     {"VINK_PING_MAIN": "false"},
		"no auth":           {"VINK_AUTH_LOCAL_ENABLED": "false"},
		"proxy no secret":   {"VINK_AUTH_PROXY_ENABLED": "true"},
		"proxy bad pattern": {"VINK_AUTH_PROXY_ENABLED": "true", "VINK_AUTH_PROXY_SECRET": "x", "VINK_AUTH_PROXY_GROUP_PATTERN": "^vink:(.*)$"},
		"bad smtp tls":      {"VINK_SMTP_TLS": "maybe"},
		"bad bool":          {"VINK_AUTH_LOCAL_ENABLED": "yes please"},
		"bad int":           {"VINK_CHECKS_WORKERS": "many"},
		"bad proxy roles":   {"VINK_AUTH_PROXY_ROLES": "ad"},
		"bad oidc roles":    {"VINK_AUTH_OIDC_ROLES": "manual"},
		"spaced admin name": {"VINK_AUTH_OIDC_INSTANCE_ADMINS": "j doe"},
		"bad key network":   {"VINK_AUTH_ADMIN_KEYS_ALLOWED_CIDRS": "office"},
	}
	for name, env := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadWith("", envOf(env)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
	ok := envOf(map[string]string{"VINK_AUTH_PROXY_ENABLED": "true", "VINK_AUTH_PROXY_SECRET": "x"})
	if _, err := LoadWith("", ok); err != nil {
		t.Fatalf("proxy mode with a secret must validate: %v", err)
	}
}

func TestPathPrefix(t *testing.T) {
	ok := map[string]string{
		"https://vink.example.com":       "",
		"https://vink.example.com/":      "",
		"https://www.example.com/vink":   "/vink",
		"https://www.example.com/vink/":  "/vink",
		"https://www.example.com/it/mon": "/it/mon",
	}
	for raw, want := range ok {
		if got, err := pathPrefix(raw); err != nil || got != want {
			t.Errorf("pathPrefix(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"https://h/o", "https://h/api/x", "https://h/vink//x", "https://h/vink/..", "https://h/vink?x=1", "https://h/vink#f", "https://h/ping/"} {
		if _, err := pathPrefix(raw); err == nil {
			t.Errorf("pathPrefix(%q) must be refused", raw)
		}
	}
	cfg, err := LoadWith("", envOf(map[string]string{"VINK_SERVER_BASE_URL": "https://www.example.com/vink/"}))
	if err != nil || cfg.PathPrefix() != "/vink" || cfg.PingPathPrefix() != "/vink" {
		t.Fatalf("prefix from the environment: %v %q %q", err, cfg.PathPrefix(), cfg.PingPathPrefix())
	}
	// pings on the main listener must live under the same path
	if _, err := LoadWith("", envOf(map[string]string{"VINK_SERVER_BASE_URL": "https://www.example.com/vink", "VINK_PING_BASE_URL": "https://ping.example.com/other"})); err == nil {
		t.Fatal("a different ping path on the shared listener must be refused")
	}
	cfg, err = LoadWith("", envOf(map[string]string{"VINK_SERVER_BASE_URL": "https://www.example.com/vink", "VINK_PING_BASE_URL": "https://ping.example.com/other", "VINK_PING_LISTEN": ":8081"}))
	if err != nil || cfg.PingPathPrefix() != "/other" {
		t.Fatalf("a separate ping listener takes its own path: %v %q", err, cfg.PingPathPrefix())
	}
	for _, raw := range []string{"https://h/o", "https://h/vink?x=1"} {
		if _, err := LoadWith("", envOf(map[string]string{"VINK_SERVER_BASE_URL": raw})); err == nil {
			t.Errorf("base_url %q must fail validation", raw)
		}
	}
}

func TestByteSize(t *testing.T) {
	cases := map[string]int64{"64KB": 65536, "1MB": 1 << 20, "512": 512, "2k": 2048, "1 GB": 1 << 30, "0": 0}
	for in, want := range cases {
		var b ByteSize
		if err := b.UnmarshalText([]byte(in)); err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if int64(b) != want {
			t.Errorf("%q = %d, want %d", in, b, want)
		}
	}
	for _, bad := range []string{"", "-1", "lots", "1.5MB"} {
		var b ByteSize
		if err := b.UnmarshalText([]byte(bad)); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
	if ByteSize(65536).String() != "64KB" || ByteSize(1000).String() != "1000" {
		t.Error("String() rendering")
	}
}

func TestDisabledProxySecretNeedsNoEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vink.toml")
	if err := os.WriteFile(path, []byte("[auth.proxy]\nenabled = false\nsecret = \"env:VINK_PROXY_SECRET\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	none := func(string) (string, bool) { return "", false }
	cfg, err := LoadWith(path, none)
	if err != nil || cfg.Auth.Proxy.Secret != "" {
		t.Fatalf("disabled proxy: %v %q", err, cfg.Auth.Proxy.Secret)
	}
	if err := os.WriteFile(path, []byte("[auth.proxy]\nenabled = true\nsecret = \"env:VINK_PROXY_SECRET\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWith(path, none); err == nil || !strings.Contains(err.Error(), "VINK_PROXY_SECRET is not set") {
		t.Fatalf("enabled proxy must need the secret: %v", err)
	}
}

func TestProxyModeNeedsTrustedCIDRs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vink.toml")
	if err := os.WriteFile(path, []byte("[auth.proxy]\nenabled = true\ntrusted_cidrs = []\nsecret = \"s\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWith(path, func(string) (string, bool) { return "", false }); err == nil || !strings.Contains(err.Error(), "trusted_cidrs is empty") {
		t.Fatalf("want the guard, got %v", err)
	}
	if err := os.WriteFile(path, []byte("[auth.proxy]\nenabled = true\ntrusted_cidrs = [\"10.0.0.0/8\"]\nsecret = \"s\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWith(path, func(string) (string, bool) { return "", false }); err != nil {
		t.Fatalf("with a cidr: %v", err)
	}
}

func TestOIDCNeedsIssuerAndClient(t *testing.T) {
	c := Default()
	c.Auth.OIDC.Enabled = true
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "auth.oidc.issuer") || !strings.Contains(err.Error(), "auth.oidc.client_id") {
		t.Fatalf("validate: %v", err)
	}
	c.Auth.OIDC.Issuer, c.Auth.OIDC.ClientID = "https://auth.example.com/realms/w4j", "vink"
	c.Auth.Local.Enabled, c.Auth.Proxy.Enabled = false, false
	if err := c.Validate(); err != nil {
		t.Fatalf("oidc alone must do: %v", err)
	}
}

// TestRoleSourcesAndAdminKeys: the role source per provider, the
// instance_admins lists and the admin key networks load from the
// environment, survive Redacted as copies, and warn where they are
// probably not what was meant.
func TestRoleSourcesAndAdminKeys(t *testing.T) {
	cfg, err := LoadWith("", envOf(map[string]string{
		"VINK_AUTH_PROXY_ENABLED": "true", "VINK_AUTH_PROXY_SECRET": "x", "VINK_AUTH_PROXY_ROLES": "vink",
		"VINK_AUTH_PROXY_INSTANCE_ADMINS": "jdoe,asmith", "VINK_AUTH_PROXY_DEFAULT_ORG": "homelab",
		"VINK_AUTH_ADMIN_KEYS_ALLOWED_CIDRS": "10.0.0.0/8,fd00::/8",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.Proxy.Roles != RolesVink || strings.Join(cfg.Auth.Proxy.InstanceAdmins, ",") != "jdoe,asmith" ||
		len(cfg.Auth.AdminKeys.AllowedCIDRs) != 2 || cfg.Auth.OIDC.Roles != RolesGroups {
		t.Fatalf("loaded: %+v %+v", cfg.Auth.Proxy, cfg.Auth.AdminKeys)
	}
	red := cfg.Redacted()
	red.Auth.Proxy.InstanceAdmins[0] = "changed"
	red.Auth.AdminKeys.AllowedCIDRs[0] = "changed"
	if cfg.Auth.Proxy.InstanceAdmins[0] != "jdoe" || cfg.Auth.AdminKeys.AllowedCIDRs[0] != "10.0.0.0/8" {
		t.Error("Redacted shares the lists with the original")
	}
	warnings := strings.Join(cfg.Warnings(), "\n")
	for _, want := range []string{"auth.proxy.default_org give no roles", "allowed_cidrs is set without server.trusted_proxies"} {
		if !strings.Contains(warnings, want) {
			t.Errorf("warnings %q lack %q", warnings, want)
		}
	}
	// a blank name can only come from a file; the environment drops it
	path := filepath.Join(t.TempDir(), "vink.toml")
	if err := os.WriteFile(path, []byte("[auth.proxy]\ninstance_admins = [\"jdoe\", \"\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWith(path, envOf(nil)); err == nil || !strings.Contains(err.Error(), "instance_admins") {
		t.Errorf("a blank name: %v", err)
	}
	if w := config0Warnings(t); len(w) != 0 {
		t.Errorf("defaults warn: %v", w)
	}
}

func config0Warnings(t *testing.T) []string {
	t.Helper()
	cfg, err := LoadWith("", envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Warnings()
}

// TestPingsOnMain: pings stay on the main listener unless a ping listener
// is set and ping.main is turned off.
func TestPingsOnMain(t *testing.T) {
	for _, tc := range []struct {
		env  map[string]string
		want bool
	}{
		{map[string]string{}, true},
		{map[string]string{"VINK_PING_LISTEN": ":8081"}, true},
		{map[string]string{"VINK_PING_LISTEN": ":8081", "VINK_PING_MAIN": "false"}, false},
	} {
		cfg, err := LoadWith("", func(k string) (string, bool) { v, ok := tc.env[k]; return v, ok })
		if err != nil {
			t.Fatal(err)
		}
		if cfg.PingsOnMain() != tc.want {
			t.Errorf("%v: PingsOnMain %v", tc.env, cfg.PingsOnMain())
		}
	}
}
