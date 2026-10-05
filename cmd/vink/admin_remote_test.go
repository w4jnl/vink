package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/w4jnl/vink/internal/cli"
	"github.com/w4jnl/vink/internal/domain"
)

// TestAdminModeSelection: where vink admin runs, decided in order.
func TestAdminModeSelection(t *testing.T) {
	cases := []struct {
		name           string
		db, cfg, ctx   string
		env            map[string]string
		vinkDBHere     bool
		local, mistake bool
		why            string
	}{
		{"--db", "x.db", "", "", nil, false, true, false, "--db"},
		{"--config", "", "vink.toml", "", nil, false, true, false, "--config"},
		{"VINK_DB_PATH", "", "", "", map[string]string{"VINK_DB_PATH": "/var/lib/vink/vink.db"}, false, true, false, "VINK_DB_PATH"},
		{"VINK_CONFIG_FILE", "", "", "", map[string]string{"VINK_CONFIG_FILE": "/etc/vink.toml"}, true, true, false, "VINK_CONFIG_FILE"},
		{"--db and --context", "x.db", "", "admin", nil, false, false, true, ""},
		{"VINK_DB_PATH and --context", "", "", "admin", map[string]string{"VINK_DB_PATH": "x.db"}, false, false, true, ""},
		{"--context beats ./vink.db", "", "", "admin", nil, true, false, false, "--context admin"},
		{"./vink.db", "", "", "", nil, true, true, false, "./vink.db is here"},
		{"the current context", "", "", "", nil, false, false, false, "the current context"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			getenv := func(k string) string { return tc.env[k] }
			exists := func(p string) bool { return p == "vink.db" && tc.vinkDBHere }
			m, err := pickAdminMode(tc.db, tc.cfg, tc.ctx, getenv, exists)
			if tc.mistake {
				if err == nil || !strings.Contains(err.Error(), "pass one of them") {
					t.Fatalf("want a refusal, got %+v %v", m, err)
				}
				return
			}
			if err != nil || m.local != tc.local || !strings.Contains(m.why, tc.why) {
				t.Fatalf("got %+v %v, want local %v (%s)", m, err, tc.local, tc.why)
			}
		})
	}

	// through the command, in a directory of its own
	t.Chdir(t.TempDir())
	t.Setenv("VINK_CONFIG", filepath.Join(t.TempDir(), "config.toml"))
	for _, k := range []string{"VINK_DB_PATH", "VINK_CONFIG_FILE", "VINK_SERVER", "VINK_KEY"} {
		t.Setenv(k, "") // restored afterwards
		_ = os.Unsetenv(k)
	}
	if _, errs, code := runCLI(t, "", "admin", "user", "ls"); code != 1 || !strings.Contains(errs, "no database at ./vink.db and no context") {
		t.Fatalf("nothing to run against: %d %s", code, errs)
	}
	if _, errs, code := runCLI(t, "hunter2hunter2\n", "admin", "init", "--db", "vink.db", "--org", "homelab", "--user", "j", "--password-stdin"); code != 0 {
		t.Fatalf("init: %s", errs)
	}
	out, errs, code := runCLI(t, "", "-d", "admin", "user", "ls")
	if code != 0 || !strings.Contains(out, "j") || !strings.Contains(errs, "vink admin: on the database file vink.db (./vink.db is here)") {
		t.Fatalf("./vink.db: %d %s %s", code, out, errs)
	}
	if _, errs, code := runCLI(t, "", "admin", "user", "ls", "--db", "vink.db", "--context", "admin"); code != 1 || !strings.Contains(errs, "--db runs vink admin on the database file") {
		t.Fatalf("--db and --context: %d %s", code, errs)
	}
}

func runCLI(t *testing.T, stdin string, args ...string) (string, string, int) {
	t.Helper()
	e := &cliEnv{t: t}
	return e.run(stdin, args...)
}

// adminKey makes an instance admin key as vink admin on the host would.
func (e *cliEnv) adminKey(access domain.Access) (*domain.AdminKey, string) {
	e.t.Helper()
	k, token, err := e.svc.CreateAdminKey(context.Background(), adminScope, "laptop", access, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	return k, token
}

// TestCtxKinds: vink ctx add says what a key is, and vink ctx ls shows
// every context's kind and scope; contexts whose kind is not known yet
// are asked once more and remembered.
func TestCtxKinds(t *testing.T) {
	e := newCLIEnv(t)
	t.Setenv("VINK_SERVER", "")
	t.Setenv("VINK_KEY", "")
	ctx := context.Background()
	_, orgKey, err := e.svc.CreateOrgAPIKey(ctx, domain.Scope{OrgID: e.scope.OrgID, Role: domain.RoleAdmin, Actor: "test"}, "gitops", domain.AccessRO)
	if err != nil {
		t.Fatal(err)
	}
	ak, adminTok := e.adminKey(domain.AccessRW)
	expires := ak.ExpiresAt.Local().Format("2 Jan 2006")
	for _, tc := range []struct{ name, key, server, want string }{
		{"prod", e.rwKey, e.srv.URL, "project key for homelab/prod"},
		{"gitops", orgKey, e.srv.URL, "org key for homelab"},
		{"admin", adminTok, e.srv.URL, "instance admin key, expires " + expires},
		{"away", "vk_abcdefgh_" + strings.Repeat("x", 32), "http://127.0.0.1:1", "the server did not say what the key is"},
	} {
		out, errs, code := e.run("", "ctx", "add", tc.name, "--server", tc.server, "--key", tc.key)
		if code != 0 || !strings.Contains(out, tc.want) {
			t.Fatalf("ctx add %s: %d %s %s", tc.name, code, out, errs)
		}
	}
	out, _, _ := e.run("", "ctx", "ls")
	for _, want := range []string{"KIND", "SCOPE", "project  homelab/prod", "org      homelab", "admin    instance, expires " + expires, "unknown  -"} {
		if !strings.Contains(out, want) {
			t.Errorf("ctx ls lacks %q:\n%s", want, out)
		}
	}
	// the away server comes back; and a context saved before vink knew kinds
	cfg, err := cli.LoadConfig(cli.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	away := cfg.Contexts["away"]
	away.Server, away.Key = e.srv.URL, e.roKey
	cfg.Contexts["away"] = away
	old := cfg.Contexts["gitops"]
	old.Kind, old.Scope = "", ""
	cfg.Contexts["gitops"] = old
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	out, _, _ = e.run("", "ctx", "ls", "--json")
	for _, want := range []string{`"Name":"away","Server":"` + e.srv.URL + `","Current":false,"Kind":"project","Scope":"homelab/prod"`, `"Name":"gitops","Server":"` + e.srv.URL + `","Current":false,"Kind":"org","Scope":"homelab"`, `"Kind":"admin","Scope":"instance","ExpiresAt":"`} {
		if !strings.Contains(out, want) {
			t.Errorf("ctx ls --json lacks %s:\n%s", want, out)
		}
	}
	cfg, _ = cli.LoadConfig(cli.ConfigPath())
	if cfg.Contexts["away"].Kind != "project" || cfg.Contexts["gitops"].Kind != "org" {
		t.Errorf("what the server said was not kept: %+v", cfg.Contexts)
	}
	raw, _ := os.ReadFile(cli.ConfigPath())
	if !strings.Contains(string(raw), "kind = 'admin'") || !strings.Contains(string(raw), "expires_at") {
		t.Errorf("config file:\n%s", raw)
	}
}

// TestAdminRemote: vink admin through a context with an instance admin
// key, and the refusals: a project key, and commands that need the
// database file.
func TestAdminRemote(t *testing.T) {
	t.Chdir(t.TempDir())
	e := newCLIEnv(t)
	k, adminTok := e.adminKey(domain.AccessRW)
	t.Setenv("VINK_KEY", adminTok)
	steps := []struct {
		args []string
		want string
	}{
		{[]string{"admin", "org", "ls"}, "homelab"},
		{[]string{"admin", "org", "create", "acme", "--name", "Acme"}, "created org acme"},
		{[]string{"admin", "user", "create", "--source", "proxy", "jdoe"}, "created user jdoe, who signs in through proxy"},
		{[]string{"admin", "user", "grant", "jdoe", "--org", "acme", "--role", "admin"}, "jdoe is now admin in acme"},
		{[]string{"admin", "user", "ls"}, "acme:admin"},
		{[]string{"admin", "org", "key", "create", "--org", "acme", "--name", "gitops"}, "vink ctx add acme-org --server " + e.srv.URL + " --key vk_"},
		{[]string{"admin", "agent", "add", "edge", "--org", "acme"}, "vink agent --server ws://127.0.0.1"},
		{[]string{"admin", "agent", "ls", "--org", "acme"}, "edge"},
		{[]string{"admin", "key", "ls"}, "vka_" + k.Prefix + "…  rw      server host"},
		{[]string{"admin", "user", "revoke", "jdoe", "--org", "acme"}, "jdoe is no longer in acme"},
	}
	for _, st := range steps {
		out, errs, code := e.run("", st.args...)
		if code != 0 || !strings.Contains(out, st.want) {
			t.Fatalf("%s: %d %q %s", strings.Join(st.args, " "), code, out, errs)
		}
	}
	var audit struct{ kind, via string }
	if err := e.svc.DB().Reader.QueryRowContext(context.Background(), `SELECT actor_kind, via FROM audit WHERE act = 'org.create' AND target = 'acme'`).Scan(&audit.kind, &audit.via); err != nil || audit.kind != "key" || audit.via != "api vka_"+k.Prefix {
		t.Errorf("audit: %+v %v", audit, err)
	}
	if _, _, code := e.run("", "ctx", "add", "admin", "--server", e.srv.URL, "--key", adminTok); code != 0 {
		t.Fatal("ctx add")
	}
	for _, args := range [][]string{
		{"admin", "key", "create", "--context", "admin"},
		{"admin", "init", "--context", "admin", "--org", "o", "--user", "u", "--password-stdin"},
		{"admin", "backup", "--context", "admin"},
	} {
		if _, errs, code := e.run("x\n", args...); code != 1 || !strings.Contains(errs, "runs on the server host against the database file; pass --db or --config") {
			t.Errorf("%s: %d %s", strings.Join(args, " "), code, errs)
		}
	}
	t.Setenv("VINK_KEY", e.rwKey)
	if _, errs, code := e.run("", "admin", "org", "ls"); code != 1 || !strings.Contains(errs, "this context's key is not an instance admin key; create one in Instance admin › API keys or with vink admin key create on the server host") {
		t.Fatalf("project key: %d %s", code, errs)
	}
	t.Setenv("VINK_KEY", adminTok)
	if out, errs, code := e.run("", "admin", "key", "revoke", k.ID); code != 0 || !strings.Contains(out, "revoked admin key") {
		t.Fatalf("revoke: %d %s %s", code, out, errs)
	}
	if _, errs, code := e.run("", "admin", "org", "ls"); code != 1 || !strings.Contains(errs, "Authentication required") {
		t.Fatalf("after revoke: %d %s", code, errs)
	}
}

// TestAdminBackendsAgree: the same commands print the same JSON on the
// server host and through a context.
func TestAdminBackendsAgree(t *testing.T) {
	t.Chdir(t.TempDir())
	e := newCLIEnv(t)
	ctx := context.Background()
	orgSc := domain.Scope{OrgID: e.scope.OrgID, InstanceAdmin: true, Role: domain.RoleOwner, Actor: "test"}
	if _, _, err := e.svc.CreateOrgAPIKey(ctx, orgSc, "gitops", domain.AccessRO); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.svc.CreateAgent(ctx, orgSc, "edge", map[string]string{"site": "dc2"}); err != nil {
		t.Fatal(err)
	}
	u, err := e.svc.CreateProviderUser(ctx, adminScope, "jdoe", "j@example.com", "J Doe", "proxy")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.SetMembership(ctx, adminScope, u.ID, e.scope.OrgID, domain.RoleViewer); err != nil {
		t.Fatal(err)
	}
	_, adminTok := e.adminKey(domain.AccessRO)
	t.Setenv("VINK_KEY", adminTok)
	for _, args := range [][]string{
		{"admin", "org", "ls"},
		{"admin", "user", "ls"},
		{"admin", "org", "key", "ls", "--org", "homelab"},
		{"admin", "agent", "ls", "--org", "homelab"},
		{"admin", "key", "ls"},
	} {
		remote, errs, code := e.run("", append(args, "--json")...)
		if code != 0 {
			t.Fatalf("remote %v: %s", args, errs)
		}
		local, errs, code := e.run("", append(args, "--json", "--db", e.svc.DB().Path)...)
		if code != 0 {
			t.Fatalf("local %v: %s", args, errs)
		}
		if remote != local || !strings.HasPrefix(remote, `{"items":[{`) {
			t.Errorf("%v differ:\nremote %s\nlocal  %s", args, remote, local)
		}
	}
}
