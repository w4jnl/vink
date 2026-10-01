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
		"no auth":           {"VINK_AUTH_LOCAL_ENABLED": "false"},
		"proxy no secret":   {"VINK_AUTH_PROXY_ENABLED": "true"},
		"proxy bad pattern": {"VINK_AUTH_PROXY_ENABLED": "true", "VINK_AUTH_PROXY_SECRET": "x", "VINK_AUTH_PROXY_GROUP_PATTERN": "^vink:(.*)$"},
		"bad smtp tls":      {"VINK_SMTP_TLS": "maybe"},
		"bad bool":          {"VINK_AUTH_LOCAL_ENABLED": "yes please"},
		"bad int":           {"VINK_CHECKS_WORKERS": "many"},
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
