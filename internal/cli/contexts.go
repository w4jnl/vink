package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Context is one server plus one API key, like a kubectl context.
type Context struct {
	Server string `toml:"server"`
	Key    string `toml:"key"`
	// PingBase and PingKey are cached from /me for `vink ping`.
	PingBase string `toml:"ping_base,omitempty"`
	PingKey  string `toml:"ping_key,omitempty"`
	// Kind, Scope and ExpiresAt say what the key is, from /me: a project
	// key (scope homelab/prod), an org key (homelab) or an instance admin
	// key (instance), which expires. Empty until the server has answered.
	Kind      string     `toml:"kind,omitempty"`
	Scope     string     `toml:"scope,omitempty"`
	ExpiresAt *time.Time `toml:"expires_at,omitempty"`
}

// Key kinds a context can hold.
const (
	KindProject = "project"
	KindOrg     = "org"
	KindAdmin   = "admin"
	KindUnknown = "unknown"
)

// IsAdminKey reports whether a key is an instance admin key, by its
// prefix; no server is asked.
func IsAdminKey(key string) bool { return strings.HasPrefix(key, "vka_") }

// Describe says what the context's key is, for vink ctx ls: an admin key
// is known by its prefix, the others by what the server said.
func (c Context) Describe(now time.Time) (kind, scope string) {
	kind, scope = c.Kind, c.Scope
	if IsAdminKey(c.Key) {
		kind, scope = KindAdmin, "instance"
		if c.ExpiresAt != nil {
			word := "expires"
			if !now.Before(*c.ExpiresAt) {
				word = "expired"
			}
			scope += ", " + word + " " + c.ExpiresAt.Local().Format("2 Jan 2006")
		}
	}
	if kind == "" {
		kind = KindUnknown
	}
	return kind, scope
}

// KeyInfo is what the server says a key is.
type KeyInfo struct {
	Kind      string
	Scope     string
	ExpiresAt *time.Time
}

// Apply stores what the server said in the context.
func (k KeyInfo) Apply(c *Context) {
	c.Kind, c.Scope, c.ExpiresAt = k.Kind, k.Scope, k.ExpiresAt
}

// LookupKey asks the server's /me what a key is, giving up after timeout.
func LookupKey(ctx context.Context, server, key string, timeout time.Duration) (KeyInfo, error) {
	c := NewClient(server, key)
	c.HTTP.Timeout = timeout
	var me struct {
		Key *struct {
			Kind      string     `json:"kind"`
			ExpiresAt *time.Time `json:"expires_at"`
		} `json:"key"`
		Org *struct {
			Slug string `json:"slug"`
		} `json:"org"`
		Project *struct {
			Slug string `json:"slug"`
		} `json:"project"`
	}
	if err := c.Do(ctx, "GET", "/me", nil, &me); err != nil {
		return KeyInfo{}, err
	}
	var info KeyInfo
	if me.Key != nil {
		info.Kind, info.ExpiresAt = me.Key.Kind, me.Key.ExpiresAt
	}
	switch {
	case info.Kind == KindAdmin || IsAdminKey(key):
		info.Kind, info.Scope = KindAdmin, "instance"
	case me.Project != nil && me.Org != nil:
		info.Kind, info.Scope = KindProject, me.Org.Slug+"/"+me.Project.Slug
	case me.Org != nil:
		info.Kind, info.Scope = KindOrg, me.Org.Slug
	default:
		return KeyInfo{}, fmt.Errorf("%s did not say what the key is", server)
	}
	return info, nil
}

// Config is ~/.config/vink/config.toml.
type Config struct {
	Current  string             `toml:"current"`
	Contexts map[string]Context `toml:"contexts"`
	path     string
}

// ConfigPath returns VINK_CONFIG or the XDG default.
func ConfigPath() string {
	if p := os.Getenv("VINK_CONFIG"); p != "" {
		return p
	}
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "vink", "config.toml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "vink.config.toml"
	}
	return filepath.Join(home, ".config", "vink", "config.toml")
}

// LoadConfig reads the contexts file; a missing file is an empty config.
func LoadConfig(path string) (*Config, error) {
	cfg := &Config{Contexts: map[string]Context{}, path: path}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if err := toml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Contexts == nil {
		cfg.Contexts = map[string]Context{}
	}
	return cfg, nil
}

// Save writes the file with mode 0600, since it holds API keys.
func (c *Config) Save() error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	out, err := toml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(c.path, out, 0o600)
}

// Names lists contexts alphabetically.
func (c *Config) Names() []string {
	names := make([]string, 0, len(c.Contexts))
	for n := range c.Contexts {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Add stores a context and makes it current when it is the first.
func (c *Config) Add(name string, ctx Context) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return UserError("context name must not be empty")
	}
	ctx.Server = strings.TrimRight(strings.TrimSpace(ctx.Server), "/")
	if !strings.HasPrefix(ctx.Server, "http://") && !strings.HasPrefix(ctx.Server, "https://") {
		return UserError("server must be an http(s) URL, got %q", ctx.Server)
	}
	if strings.TrimSpace(ctx.Key) == "" {
		return UserError("key must not be empty")
	}
	c.Contexts[name] = ctx
	if c.Current == "" || len(c.Contexts) == 1 {
		c.Current = name
	}
	return nil
}

// Use switches the current context.
func (c *Config) Use(name string) error {
	if _, ok := c.Contexts[name]; !ok {
		return UserError("no context named %q; run vink ctx ls", name)
	}
	c.Current = name
	return nil
}

// Remove deletes a context.
func (c *Config) Remove(name string) error {
	if _, ok := c.Contexts[name]; !ok {
		return UserError("no context named %q", name)
	}
	delete(c.Contexts, name)
	if c.Current == name {
		c.Current = ""
		if names := c.Names(); len(names) > 0 {
			c.Current = names[0]
		}
	}
	return nil
}

// Resolved is the effective server and key.
type Resolved struct {
	Name   string
	Server string
	Key    string
	// FromEnv is true when VINK_SERVER and VINK_KEY were used.
	FromEnv bool
}

// Resolve picks VINK_SERVER and VINK_KEY when both are set, else the
// named or current context.
func (c *Config) Resolve(name string, env func(string) string) (Resolved, error) {
	if s, k := env("VINK_SERVER"), env("VINK_KEY"); s != "" && k != "" {
		return Resolved{Server: strings.TrimRight(s, "/"), Key: k, FromEnv: true}, nil
	}
	if name == "" {
		name = c.Current
	}
	if name == "" {
		return Resolved{}, UserError("no context: run vink ctx add <name> --server <url> --key <api key>, or set VINK_SERVER and VINK_KEY")
	}
	ctx, ok := c.Contexts[name]
	if !ok {
		return Resolved{}, UserError("no context named %q; run vink ctx ls", name)
	}
	return Resolved{Name: name, Server: ctx.Server, Key: ctx.Key}, nil
}
