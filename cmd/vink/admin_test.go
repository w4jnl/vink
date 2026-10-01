package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdminInitAndFriends(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "vink.db")
	runCLI := func(stdin string, args ...string) (string, string, int) {
		var out, errb bytes.Buffer
		code := run(context.Background(), append([]string{"--color", "never"}, args...), strings.NewReader(stdin), &out, &errb)
		return out.String(), errb.String(), code
	}
	out, errs, code := runCLI("hunter2hunter2\n", "admin", "init", "--db", dbPath, "--org", "homelab", "--user", "j", "--timezone", "Europe/Amsterdam", "--password-stdin", "--json")
	if code != 0 {
		t.Fatalf("init failed: %s", errs)
	}
	var res map[string]string
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("init output not JSON: %q", out)
	}
	if res["user"] != "j" || res["org"] != "homelab" || res["project"] != "homelab" || len(res["ping_key"]) != 22 || !strings.HasPrefix(res["api_key"], "vk_") {
		t.Fatalf("init result: %v", res)
	}
	if !strings.Contains(res["ping_url"], "/ping/"+res["ping_key"]+"/<slug>") {
		t.Errorf("ping url: %s", res["ping_url"])
	}
	// a second init is refused
	if _, errs, code := runCLI("x\n", "admin", "init", "--db", dbPath, "--org", "o", "--user", "u", "--password-stdin"); code == 0 || !strings.Contains(errs, "already") {
		t.Fatalf("second init: code=%d %s", code, errs)
	}
	// short password refused
	if _, errs, code := runCLI("short\n", "admin", "user", "create", "--db", dbPath, "bob", "--password-stdin"); code == 0 || !strings.Contains(errs, "password") {
		t.Fatalf("short password: %d %s", code, errs)
	}
	if _, errs, code := runCLI("bobpassword1\n", "admin", "user", "create", "--db", dbPath, "bob", "--org", "homelab", "--role", "viewer", "--password-stdin"); code != 0 {
		t.Fatalf("user create: %s", errs)
	}
	if _, errs, code := runCLI("", "admin", "org", "create", "--db", dbPath, "second", "--name", "Second"); code != 0 {
		t.Fatalf("org create: %s", errs)
	}
	out, _, _ = runCLI("", "admin", "org", "ls", "--db", dbPath)
	if !strings.Contains(out, "homelab") || !strings.Contains(out, "second") {
		t.Errorf("org ls: %s", out)
	}
	out, _, _ = runCLI("", "admin", "user", "ls", "--db", dbPath)
	if !strings.Contains(out, "j") || !strings.Contains(out, "bob") || !strings.Contains(out, "true") {
		t.Errorf("user ls: %s", out)
	}
	if _, errs, code := runCLI("", "admin", "user", "promote", "--db", dbPath, "bob"); code != 0 {
		t.Fatalf("promote: %s", errs)
	}
	out, _, _ = runCLI("", "admin", "user", "ls", "--db", dbPath, "--json")
	if strings.Count(out, `"InstanceAdmin":true`) != 2 {
		t.Errorf("promote not applied: %s", out)
	}
	// text output of init on a fresh db
	out, errs, code = runCLI("hunter2hunter2\n", "admin", "init", "--db", filepath.Join(t.TempDir(), "v2.db"), "--org", "o", "--user", "u", "--password-stdin")
	if code != 0 || !strings.Contains(out, "api key (rw)    vk_") || !strings.Contains(out, "ping key") {
		t.Fatalf("text init: %d %s %s", code, out, errs)
	}
	// missing --password-stdin
	if _, errs, code := runCLI("", "admin", "init", "--db", filepath.Join(t.TempDir(), "v3.db"), "--org", "o", "--user", "u"); code == 0 || !strings.Contains(errs, "password-stdin") {
		t.Fatalf("no password: %d %s", code, errs)
	}
}

func TestAdminAgents(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "vink.db")
	runCLI := func(stdin string, args ...string) (string, string, int) {
		var out, errb bytes.Buffer
		code := run(context.Background(), append([]string{"--color", "never"}, args...), strings.NewReader(stdin), &out, &errb)
		return out.String(), errb.String(), code
	}
	if _, errs, code := runCLI("hunter2hunter2\n", "admin", "init", "--db", dbPath, "--org", "homelab", "--user", "j", "--password-stdin"); code != 0 {
		t.Fatalf("init: %s", errs)
	}
	out, errs, code := runCLI("", "admin", "agent", "add", "--db", dbPath, "--org", "homelab", "dc2-probe", "--labels", "site=dc2,zone=dmz", "--json")
	if code != 0 {
		t.Fatalf("add: %s", errs)
	}
	var res struct {
		Name, Token, Command string
		Labels               map[string]string
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("add output not JSON: %q", out)
	}
	if res.Name != "dc2-probe" || !strings.HasPrefix(res.Token, "vat_") || res.Labels["zone"] != "dmz" || !strings.Contains(res.Command, "vink agent --server ws://localhost:8080") || !strings.Contains(res.Command, "--labels site=dc2,zone=dmz") {
		t.Fatalf("add result: %+v", res)
	}
	out, errs, code = runCLI("", "admin", "agent", "add", "--db", dbPath, "--org", "homelab", "edge", "--labels", "site=edge")
	if code != 0 || !strings.Contains(out, "token (shown once)  vat_") || !strings.Contains(out, "run on the agent's host:") {
		t.Fatalf("text add: %d %s %s", code, out, errs)
	}
	if _, errs, code := runCLI("", "admin", "agent", "add", "--db", dbPath, "--org", "homelab", "edge"); code == 0 || !strings.Contains(errs, "exists") {
		t.Fatalf("duplicate: %d %s", code, errs)
	}
	if _, errs, code := runCLI("", "admin", "agent", "add", "--db", dbPath, "--org", "homelab", "bad", "--labels", "no-equals"); code == 0 || errs == "" {
		t.Fatalf("bad labels: %d %s", code, errs)
	}
	if _, errs, code := runCLI("", "admin", "agent", "ls", "--db", dbPath); code == 0 || !strings.Contains(errs, "org") {
		t.Fatalf("missing org: %d %s", code, errs)
	}
	out, _, _ = runCLI("", "admin", "agent", "ls", "--db", dbPath, "--org", "homelab")
	if !strings.Contains(out, "dc2-probe") || !strings.Contains(out, "site=dc2,zone=dmz") || !strings.Contains(out, "never") || strings.Contains(out, res.Token) {
		t.Errorf("agent ls: %s", out)
	}
	if _, errs, code := runCLI("", "admin", "agent", "revoke", "--db", dbPath, "--org", "homelab", "edge"); code != 0 {
		t.Fatalf("revoke: %s", errs)
	}
	out, _, _ = runCLI("", "admin", "agent", "ls", "--db", dbPath, "--org", "homelab", "--json")
	if strings.Contains(out, "edge") || !strings.Contains(out, "dc2-probe") {
		t.Errorf("after revoke: %s", out)
	}
	if _, errs, code := runCLI("", "admin", "agent", "revoke", "--db", dbPath, "--org", "nope", "dc2-probe"); code == 0 || errs == "" {
		t.Fatalf("unknown org: %d %s", code, errs)
	}
}

func TestAdminBackup(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "vink.db")
	runCLI := func(stdin string, args ...string) (string, string, int) {
		var out, errb bytes.Buffer
		code := run(context.Background(), append([]string{"--color", "never"}, args...), strings.NewReader(stdin), &out, &errb)
		return out.String(), errb.String(), code
	}
	if _, errs, code := runCLI("hunter2hunter2\n", "admin", "init", "--db", dbPath, "--org", "homelab", "--user", "j", "--password-stdin"); code != 0 {
		t.Fatalf("init: %s", errs)
	}
	out := filepath.Join(dir, "copy.db")
	stdout, errs, code := runCLI("", "admin", "backup", "--db", dbPath, "--out", out)
	if code != 0 || !strings.Contains(stdout, "wrote "+out) {
		t.Fatalf("backup: %d %s %s", code, stdout, errs)
	}
	head := make([]byte, 16)
	fh, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fh.Read(head)
	_ = fh.Close()
	if !strings.HasPrefix(string(head), "SQLite format 3") {
		t.Fatalf("not a database: %q", head)
	}
	if _, errs, code := runCLI("", "admin", "backup", "--db", dbPath, "--out", out); code == 0 || !strings.Contains(errs, "exists") {
		t.Fatalf("overwrite must be refused: %d %s", code, errs)
	}
}

func TestAdminOrgKeys(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "vink.db")
	runCLI := func(stdin string, args ...string) (string, string, int) {
		var out, errb bytes.Buffer
		code := run(context.Background(), append([]string{"--color", "never"}, args...), strings.NewReader(stdin), &out, &errb)
		return out.String(), errb.String(), code
	}
	if _, errs, code := runCLI("hunter2hunter2\n", "admin", "init", "--db", dbPath, "--org", "homelab", "--user", "j", "--password-stdin"); code != 0 {
		t.Fatalf("init: %s", errs)
	}
	out, errs, code := runCLI("", "admin", "org", "key", "create", "--db", dbPath, "--org", "homelab", "--name", "gitops", "--access", "rw", "--json")
	if code != 0 {
		t.Fatalf("create: %s", errs)
	}
	var k struct{ ID, Name, Prefix, Access, Token string }
	if err := json.Unmarshal([]byte(out), &k); err != nil || !strings.HasPrefix(k.Token, "vk_") || k.Access != "rw" || k.Name != "gitops" {
		t.Fatalf("create output: %s %v", out, err)
	}
	out, errs, code = runCLI("", "admin", "org", "key", "create", "--db", dbPath, "--org", "homelab")
	if code != 0 || !strings.Contains(out, "token (shown once)  vk_") || !strings.Contains(out, "vink export --org homelab") || !strings.Contains(out, "(") {
		t.Fatalf("text create: %d %s %s", code, out, errs)
	}
	if _, errs, code := runCLI("", "admin", "org", "key", "create", "--db", dbPath, "--org", "homelab", "--access", "rwx"); code == 0 || !strings.Contains(errs, "ro or rw") {
		t.Fatalf("bad access: %d %s", code, errs)
	}
	out, _, _ = runCLI("", "admin", "org", "key", "ls", "--db", dbPath, "--org", "homelab")
	if !strings.Contains(out, "gitops") || !strings.Contains(out, "org key") || !strings.Contains(out, "vk_"+k.Prefix) || strings.Contains(out, k.Token) {
		t.Fatalf("ls: %s", out)
	}
	if _, errs, code := runCLI("", "admin", "org", "key", "revoke", "--db", dbPath, "--org", "homelab", k.ID); code != 0 {
		t.Fatalf("revoke: %s", errs)
	}
	out, _, _ = runCLI("", "admin", "org", "key", "ls", "--db", dbPath, "--org", "homelab", "--json")
	if strings.Contains(out, k.ID) {
		t.Fatalf("after revoke: %s", out)
	}
	if _, errs, code := runCLI("", "admin", "org", "key", "revoke", "--db", dbPath, "--org", "homelab", k.ID); code == 0 || errs == "" {
		t.Fatalf("revoke twice: %d %s", code, errs)
	}
}

func TestAdminUserGrantAndRevoke(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "vink.db")
	runCLI := func(stdin string, args ...string) (string, string, int) {
		var out, errb bytes.Buffer
		code := run(context.Background(), append([]string{"--color", "never"}, args...), strings.NewReader(stdin), &out, &errb)
		return out.String(), errb.String(), code
	}
	if _, errs, code := runCLI("hunter2hunter2\n", "admin", "init", "--db", dbPath, "--org", "homelab", "--user", "j", "--password-stdin"); code != 0 {
		t.Fatalf("init: %s", errs)
	}
	if _, errs, code := runCLI("", "admin", "org", "create", "--db", dbPath, "acme", "--name", "Acme"); code != 0 {
		t.Fatalf("org create: %s", errs)
	}
	if _, errs, code := runCLI("bobpassword1\n", "admin", "user", "create", "--db", dbPath, "bob", "--password-stdin"); code != 0 {
		t.Fatalf("user create: %s", errs)
	}
	out, errs, code := runCLI("", "admin", "user", "grant", "--db", dbPath, "bob", "--org", "acme", "--role", "admin")
	if code != 0 || !strings.Contains(out, "bob is now admin in acme") {
		t.Fatalf("grant: %d %s %s", code, out, errs)
	}
	if _, errs, code := runCLI("", "admin", "user", "grant", "--db", dbPath, "bob", "--org", "acme", "--role", "boss"); code == 0 || !strings.Contains(errs, "owner, admin, member or viewer") {
		t.Fatalf("bad role: %d %s", code, errs)
	}
	if _, errs, code := runCLI("", "admin", "user", "grant", "--db", dbPath, "nobody", "--org", "acme"); code == 0 || errs == "" {
		t.Fatalf("unknown user: %d %s", code, errs)
	}
	if _, errs, code := runCLI("", "admin", "user", "grant", "--db", dbPath, "bob", "--org", "nope"); code == 0 || errs == "" {
		t.Fatalf("unknown org: %d %s", code, errs)
	}
	// j is homelab's only owner
	if _, errs, code := runCLI("", "admin", "user", "revoke", "--db", dbPath, "j", "--org", "homelab"); code == 0 || !strings.Contains(errs, "last owner") {
		t.Fatalf("last owner: %d %s", code, errs)
	}
	if _, errs, code := runCLI("", "admin", "user", "grant", "--db", dbPath, "j", "--org", "homelab", "--role", "viewer"); code == 0 || !strings.Contains(errs, "last owner") {
		t.Fatalf("demote last owner: %d %s", code, errs)
	}
	out, errs, code = runCLI("", "admin", "user", "revoke", "--db", dbPath, "bob", "--org", "acme")
	if code != 0 || !strings.Contains(out, "bob is no longer in acme") {
		t.Fatalf("revoke: %d %s %s", code, out, errs)
	}
	if _, errs, code := runCLI("", "admin", "user", "revoke", "--db", dbPath, "bob", "--org", "acme"); code == 0 || errs == "" {
		t.Fatalf("revoke twice: %d %s", code, errs)
	}
}
