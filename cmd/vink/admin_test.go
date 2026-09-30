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
