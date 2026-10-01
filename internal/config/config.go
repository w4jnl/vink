// Package config loads vink.toml, overlays VINK_* environment variables,
// resolves env: references, validates, and redacts secrets for display.
package config

import (
	"bytes"
	"encoding"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/w4jnl/vink/internal/domain"
)

// EnvPrefix is the prefix of every environment override: VINK_SERVER_LISTEN.
const EnvPrefix = "VINK"

// Config is the whole vink.toml.
type Config struct {
	Server    Server    `toml:"server"`
	Ping      Ping      `toml:"ping"`
	DB        DB        `toml:"db"`
	Retention Retention `toml:"retention"`
	Checks    Checks    `toml:"checks"`
	Agents    Agents    `toml:"agents"`
	Outbound  Outbound  `toml:"outbound"`
	Log       Log       `toml:"log"`
	Metrics   Metrics   `toml:"metrics"`
	Auth      Auth      `toml:"auth"`
	Secrets   Secrets   `toml:"secrets"`
	SMTP      SMTP      `toml:"smtp"`
}

type Server struct {
	Listen         string   `toml:"listen"`
	BaseURL        string   `toml:"base_url"`
	TrustedProxies []string `toml:"trusted_proxies"`
}

type Ping struct {
	Listen         string   `toml:"listen"`
	BaseURL        string   `toml:"base_url"`
	BodyLimit      ByteSize `toml:"body_limit"`
	RatePerMonitor int      `toml:"rate_per_monitor"`
	RatePerIP      int      `toml:"rate_per_ip"`
}

type DB struct {
	Path string `toml:"path"`
}

type Retention struct {
	ObservationsDays int `toml:"observations_days"`
	BodiesDays       int `toml:"bodies_days"`
}

type Agents struct {
	// OfflineAfter is how long without a heartbeat before an agent's
	// monitors turn late with reason agent offline.
	OfflineAfter domain.Duration `toml:"offline_after"`
}

type Checks struct {
	Workers     int             `toml:"workers"`
	MinInterval domain.Duration `toml:"min_interval"`
}

type Outbound struct {
	Proxy               string `toml:"proxy"`
	CAPem               string `toml:"ca_pem"`
	AllowPrivateTargets bool   `toml:"allow_private_targets"`
}

type Log struct {
	Level  string `toml:"level"`
	Format string `toml:"format"`
}

type Metrics struct {
	Token string `toml:"token" redact:"true"`
}

type Auth struct {
	Local AuthLocal `toml:"local"`
	Proxy AuthProxy `toml:"proxy"`
}

type AuthLocal struct {
	Enabled bool `toml:"enabled"`
}

type AuthProxy struct {
	Enabled            bool              `toml:"enabled"`
	TrustedCIDRs       []string          `toml:"trusted_cidrs"`
	SecretHeader       string            `toml:"secret_header"`
	Secret             string            `toml:"secret" redact:"true"`
	UserHeader         string            `toml:"user_header"`
	EmailHeader        string            `toml:"email_header"`
	NameHeader         string            `toml:"name_header"`
	GroupsHeader       string            `toml:"groups_header"`
	GroupsSeparator    string            `toml:"groups_separator"`
	StripRealm         bool              `toml:"strip_realm"`
	Lowercase          bool              `toml:"lowercase"`
	GroupPattern       string            `toml:"group_pattern"`
	InstanceAdminGroup string            `toml:"instance_admin_group"`
	DefaultOrg         string            `toml:"default_org"`
	LogoutURL          string            `toml:"logout_url"`
	GroupMap           map[string]string `toml:"group_map"`
}

type Secrets struct {
	KeyFile string `toml:"key_file"`
}

// SMTP is the instance-level mail transport used by smtp channels.
type SMTP struct {
	Host     string `toml:"host"`
	Port     int    `toml:"port"`
	Username string `toml:"username"`
	Password string `toml:"password" redact:"true"`
	From     string `toml:"from"`
	// TLS is starttls (default), tls (implicit) or none.
	TLS string `toml:"tls"`
}

// Default returns the built-in defaults from the design document.
func Default() *Config {
	return &Config{
		Server: Server{Listen: ":8080", BaseURL: "http://localhost:8080"},
		Ping: Ping{
			BodyLimit:      64 * 1024,
			RatePerMonitor: 10,
			RatePerIP:      300,
		},
		DB:        DB{Path: "vink.db"},
		Retention: Retention{ObservationsDays: 90, BodiesDays: 14},
		Checks:    Checks{Workers: 32, MinInterval: domain.MustDuration("10s")},
		Agents:    Agents{OfflineAfter: domain.MustDuration("2m")},
		Outbound:  Outbound{AllowPrivateTargets: true},
		Log:       Log{Level: "info", Format: "json"},
		Auth: Auth{
			Local: AuthLocal{Enabled: true},
			Proxy: AuthProxy{ //nolint:gosec // G101: header names, not credentials
				TrustedCIDRs:       []string{"127.0.0.1/32", "::1/128"},
				SecretHeader:       "X-Auth-Proxy-Secret",
				UserHeader:         "Remote-User",
				EmailHeader:        "Remote-Email",
				NameHeader:         "Remote-Name",
				GroupsHeader:       "Remote-Groups",
				GroupsSeparator:    ",",
				StripRealm:         true,
				Lowercase:          true,
				GroupPattern:       `^vink:(?P<org>[a-z0-9-]+):(?P<role>owner|admin|member|viewer)$`,
				InstanceAdminGroup: "vink:admin",
			},
		},
		SMTP: SMTP{Port: 587, TLS: "starttls"},
	}
}

// Load reads the TOML file at path (optional), applies VINK_* environment
// overrides, resolves env: references and validates the result.
func Load(path string) (*Config, error) {
	return LoadWith(path, os.LookupEnv)
}

// LoadWith is Load with an injectable environment, for tests.
func LoadWith(path string, lookup func(string) (string, bool)) (*Config, error) {
	cfg := Default()
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
		dec := toml.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(cfg); err != nil {
			var sme *toml.StrictMissingError
			if errors.As(err, &sme) {
				return nil, fmt.Errorf("parse config %s: unknown keys:\n%s", path, sme.String())
			}
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
	}
	if err := applyEnv(reflect.ValueOf(cfg).Elem(), EnvPrefix, lookup); err != nil {
		return nil, err
	}
	if err := resolveEnvRefs(reflect.ValueOf(cfg).Elem(), lookup); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// SecretKeyFile returns secrets.key_file, or secret.key next to the DB file.
func (c *Config) SecretKeyFile() string {
	if c.Secrets.KeyFile != "" {
		return c.Secrets.KeyFile
	}
	dir := "."
	if i := strings.LastIndexAny(c.DB.Path, `/\`); i >= 0 {
		dir = c.DB.Path[:i]
	}
	return dir + "/secret.key"
}

// PingBaseURL is the base for ping URLs: ping.base_url or server.base_url.
func (c *Config) PingBaseURL() string {
	if c.Ping.BaseURL != "" {
		return strings.TrimRight(c.Ping.BaseURL, "/")
	}
	return strings.TrimRight(c.Server.BaseURL, "/")
}

// Validate checks values that would otherwise fail later and less clearly.
func (c *Config) Validate() error {
	var errs []error
	fail := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if c.Server.Listen == "" {
		fail("server.listen must not be empty")
	}
	if u, err := url.Parse(c.Server.BaseURL); err != nil || u.Scheme == "" || u.Host == "" {
		fail("server.base_url must be an absolute URL, got %q", c.Server.BaseURL)
	}
	if c.Ping.BaseURL != "" {
		if u, err := url.Parse(c.Ping.BaseURL); err != nil || u.Scheme == "" || u.Host == "" {
			fail("ping.base_url must be an absolute URL, got %q", c.Ping.BaseURL)
		}
	}
	for _, cidr := range c.Server.TrustedProxies {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			fail("server.trusted_proxies: %q is not a CIDR", cidr)
		}
	}
	if c.Ping.BodyLimit <= 0 {
		fail("ping.body_limit must be positive")
	}
	if c.Ping.RatePerMonitor <= 0 || c.Ping.RatePerIP <= 0 {
		fail("ping.rate_per_monitor and ping.rate_per_ip must be positive")
	}
	if c.DB.Path == "" {
		fail("db.path must not be empty")
	}
	if c.Retention.ObservationsDays <= 0 || c.Retention.BodiesDays <= 0 {
		fail("retention days must be positive")
	}
	if c.Checks.Workers <= 0 {
		fail("checks.workers must be positive")
	}
	switch strings.ToLower(c.Log.Level) {
	case "debug", "info", "warn", "warning", "error":
	default:
		fail("log.level must be debug, info, warn or error, got %q", c.Log.Level)
	}
	switch strings.ToLower(c.Log.Format) {
	case "json", "text":
	default:
		fail("log.format must be json or text, got %q", c.Log.Format)
	}
	if !c.Auth.Local.Enabled && !c.Auth.Proxy.Enabled {
		fail("auth: enable auth.local or auth.proxy, otherwise nobody can sign in")
	}
	if c.Auth.Proxy.Enabled {
		if c.Auth.Proxy.Secret == "" {
			fail("auth.proxy.secret must be set when auth.proxy is enabled")
		}
		if c.Auth.Proxy.SecretHeader == "" || c.Auth.Proxy.UserHeader == "" {
			fail("auth.proxy.secret_header and user_header must not be empty")
		}
		if len(c.Auth.Proxy.TrustedCIDRs) == 0 {
			fail("auth.proxy.trusted_cidrs must list the proxy's addresses")
		}
		for _, cidr := range c.Auth.Proxy.TrustedCIDRs {
			if _, _, err := net.ParseCIDR(cidr); err != nil {
				fail("auth.proxy.trusted_cidrs: %q is not a CIDR", cidr)
			}
		}
		re, err := regexp.Compile(c.Auth.Proxy.GroupPattern)
		if err != nil {
			fail("auth.proxy.group_pattern: %v", err)
		} else if re.SubexpIndex("org") < 0 || re.SubexpIndex("role") < 0 {
			fail("auth.proxy.group_pattern must have named groups (?P<org>…) and (?P<role>…)")
		}
	}
	switch strings.ToLower(c.SMTP.TLS) {
	case "starttls", "tls", "none":
	default:
		fail("smtp.tls must be starttls, tls or none, got %q", c.SMTP.TLS)
	}
	return errors.Join(errs...)
}

// Redacted returns a copy with every secret replaced by "***".
func (c *Config) Redacted() *Config {
	cp := *c
	cp.Server.TrustedProxies = append([]string(nil), c.Server.TrustedProxies...)
	cp.Auth.Proxy.TrustedCIDRs = append([]string(nil), c.Auth.Proxy.TrustedCIDRs...)
	if c.Auth.Proxy.GroupMap != nil {
		cp.Auth.Proxy.GroupMap = make(map[string]string, len(c.Auth.Proxy.GroupMap))
		for k, v := range c.Auth.Proxy.GroupMap {
			cp.Auth.Proxy.GroupMap[k] = v
		}
	}
	redact(reflect.ValueOf(&cp).Elem())
	return &cp
}

// TOML renders the config as TOML text.
func (c *Config) TOML() ([]byte, error) {
	return toml.Marshal(c)
}

func redact(v reflect.Value) {
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		fv := v.Field(i)
		switch {
		case fv.Kind() == reflect.Struct:
			redact(fv)
		case f.Tag.Get("redact") == "true" && fv.Kind() == reflect.String && fv.String() != "":
			fv.SetString("***")
		}
	}
}

// applyEnv overlays VINK_<SECTION>_<KEY> values onto v. Lists are comma
// separated. Maps are not overridable from the environment.
func applyEnv(v reflect.Value, prefix string, lookup func(string) (string, bool)) error {
	t := v.Type()
	for i := range t.NumField() {
		f := t.Field(i)
		key, _, _ := strings.Cut(f.Tag.Get("toml"), ",")
		if key == "" || key == "-" {
			continue
		}
		name := prefix + "_" + strings.ToUpper(key)
		fv := v.Field(i)
		if fv.Kind() == reflect.Struct {
			if err := applyEnv(fv, name, lookup); err != nil {
				return err
			}
			continue
		}
		raw, ok := lookup(name)
		if !ok {
			continue
		}
		if err := setFromString(fv, raw); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func setFromString(fv reflect.Value, raw string) error {
	if fv.CanAddr() {
		if u, ok := fv.Addr().Interface().(encoding.TextUnmarshaler); ok {
			return u.UnmarshalText([]byte(raw))
		}
	}
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(raw)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("expected true or false, got %q", raw)
		}
		fv.SetBool(b)
	case reflect.Int, reflect.Int64:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return fmt.Errorf("expected an integer, got %q", raw)
		}
		fv.SetInt(n)
	case reflect.Slice:
		if fv.Type().Elem().Kind() != reflect.String {
			return errors.New("unsupported list type")
		}
		var items []string
		for part := range strings.SplitSeq(raw, ",") {
			if p := strings.TrimSpace(part); p != "" {
				items = append(items, p)
			}
		}
		fv.Set(reflect.ValueOf(items))
	case reflect.Map:
		return errors.New("maps cannot be set from the environment")
	default:
		return fmt.Errorf("unsupported type %s", fv.Type())
	}
	return nil
}

// resolveEnvRefs replaces string values of the form env:NAME with the value
// of that environment variable.
func resolveEnvRefs(v reflect.Value, lookup func(string) (string, bool)) error {
	t := v.Type()
	for i := range t.NumField() {
		fv := v.Field(i)
		switch fv.Kind() {
		case reflect.Struct:
			if err := resolveEnvRefs(fv, lookup); err != nil {
				return err
			}
		case reflect.String:
			s := fv.String()
			if name, ok := strings.CutPrefix(s, "env:"); ok {
				val, found := lookup(name)
				if !found {
					return fmt.Errorf("%s.%s refers to env:%s but %s is not set", t.Name(), t.Field(i).Name, name, name)
				}
				fv.SetString(val)
			}
		}
	}
	return nil
}

// ByteSize is a byte count that reads "64KB", "1MB", "512" and similar.
type ByteSize int64

// UnmarshalText implements encoding.TextUnmarshaler.
func (b *ByteSize) UnmarshalText(text []byte) error {
	s := strings.ToUpper(strings.TrimSpace(string(text)))
	if s == "" {
		return errors.New("empty size")
	}
	mult := int64(1)
	for _, suf := range []struct {
		s string
		m int64
	}{{"GB", 1 << 30}, {"MB", 1 << 20}, {"KB", 1 << 10}, {"G", 1 << 30}, {"M", 1 << 20}, {"K", 1 << 10}, {"B", 1}} {
		if strings.HasSuffix(s, suf.s) {
			mult = suf.m
			s = strings.TrimSpace(strings.TrimSuffix(s, suf.s))
			break
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return fmt.Errorf("invalid size %q", string(text))
	}
	*b = ByteSize(n * mult)
	return nil
}

// MarshalText implements encoding.TextMarshaler.
func (b ByteSize) MarshalText() ([]byte, error) { return []byte(b.String()), nil }

// String renders the size with the largest exact unit.
func (b ByteSize) String() string {
	v := int64(b)
	switch {
	case v >= 1<<30 && v%(1<<30) == 0:
		return strconv.FormatInt(v>>30, 10) + "GB"
	case v >= 1<<20 && v%(1<<20) == 0:
		return strconv.FormatInt(v>>20, 10) + "MB"
	case v >= 1<<10 && v%(1<<10) == 0:
		return strconv.FormatInt(v>>10, 10) + "KB"
	default:
		return strconv.FormatInt(v, 10)
	}
}
