//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// instance is one built binary serving one temp database.
type instance struct {
	t    *testing.T
	bin  string
	dir  string
	base string
	key  string
	ping string
	env  []string
	log  bytes.Buffer
}

// startInstance builds vink, bootstraps a database, starts the server on
// a free port and points the CLI context at it. extraEnv is appended to
// the server's environment.
func startInstance(t *testing.T, extraEnv ...string) *instance {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "vink")
	build := exec.Command("go", "build", "-o", bin, "./cmd/vink")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	addr := freePort(t)
	base := "http://" + addr
	in := &instance{t: t, bin: bin, dir: dir, base: base}
	in.env = append(os.Environ(), "VINK_DB_PATH="+filepath.Join(dir, "vink.db"), "VINK_SERVER_LISTEN="+addr, "VINK_SERVER_BASE_URL="+base, "VINK_CONFIG="+filepath.Join(dir, "cli.toml"), "NO_COLOR=1")
	in.env = append(in.env, extraEnv...)

	initOut, err := in.vink("e2e-password\n", "admin", "init", "--org", "homelab", "--user", "j", "--password-stdin", "--timezone", "Europe/Amsterdam", "--json")
	if err != nil {
		t.Fatalf("admin init: %v", err)
	}
	var init map[string]string
	if err := json.Unmarshal([]byte(initOut), &init); err != nil {
		t.Fatalf("init json: %s", initOut)
	}
	in.key, in.ping = init["api_key"], init["ping_key"]

	ctx, cancel := context.WithCancel(context.Background())
	serve := exec.CommandContext(ctx, bin, "serve")
	serve.Env = in.env
	serve.Stdout, serve.Stderr = &in.log, &in.log
	if err := serve.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = serve.Wait()
		if t.Failed() {
			t.Logf("server log:\n%s", in.log.String())
		}
	})
	waitFor(t, 15*time.Second, func() bool {
		resp, err := http.Get(base + "/readyz")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == 200
	})
	if _, err := in.vink("", "ctx", "add", "e2e", "--server", base, "--key", in.key); err != nil {
		t.Fatalf("ctx add: %v", err)
	}
	return in
}

// vink runs the CLI with the instance's environment.
func (in *instance) vink(stdin string, args ...string) (string, error) {
	cmd := exec.Command(in.bin, args...)
	cmd.Env = in.env
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), &cmdErr{err: err, stderr: errb.String()}
	}
	return out.String(), nil
}

// api calls the REST API with the rw key and fails the test on a non-2xx.
func (in *instance) api(method, path string, body any) map[string]any {
	in.t.Helper()
	raw, code := in.apiRaw(method, path, body)
	if code >= 300 {
		in.t.Fatalf("%s %s: %d %s", method, path, code, raw)
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}

func (in *instance) apiRaw(method, path string, body any) ([]byte, int) {
	in.t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	}
	req, _ := http.NewRequest(method, in.base+"/api/v1"+path, rdr)
	req.Header.Set("Authorization", "Bearer "+in.key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		in.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return raw, resp.StatusCode
}

// get fetches a public path and returns the status and body.
func (in *instance) get(path string) (int, string, http.Header) {
	in.t.Helper()
	resp, err := http.Get(in.base + path)
	if err != nil {
		in.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw), resp.Header
}

func (in *instance) state(slug string) string {
	m := in.api("GET", "/monitors/"+slug, nil)
	s, _ := m["state"].(string)
	return s
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

type cmdErr struct {
	err    error
	stderr string
}

func (e *cmdErr) Error() string { return e.err.Error() + ": " + strings.TrimSpace(e.stderr) }

func waitFor(t *testing.T, max time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(max)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("condition not met within %s", max)
}
