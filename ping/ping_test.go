package ping

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// got is one request the fake server saw.
type got struct {
	method, path, query, body, contentType, userAgent string
}

// fake is a vink ping endpoint that records requests and answers from a
// script: one status per request, 200 once the script runs out.
type fake struct {
	srv     *httptest.Server
	mu      sync.Mutex
	reqs    []got
	answers []answer
}

type answer struct {
	status int
	header map[string]string
	body   string
}

func newFake(t *testing.T, answers ...answer) *fake {
	t.Helper()
	f := &fake{answers: answers}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.reqs = append(f.reqs, got{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, string(b), r.Header.Get("Content-Type"), r.UserAgent()})
		a := answer{status: http.StatusOK}
		if len(f.answers) > 0 {
			a, f.answers = f.answers[0], f.answers[1:]
		}
		f.mu.Unlock()
		for k, v := range a.header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(a.status)
		_, _ = io.WriteString(w, a.body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) requests() []got {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]got(nil), f.reqs...)
}

// client makes a client on the fake whose retries wait no time, recording
// the pauses it asked for.
func (f *fake) client(t *testing.T, key string, opts ...Option) (*Client, *[]time.Duration) {
	t.Helper()
	c, err := New(f.srv.URL+"/vink/ping/", key, opts...)
	if err != nil {
		t.Fatal(err)
	}
	var waits []time.Duration
	c.wait = func(ctx context.Context, d time.Duration) bool {
		waits = append(waits, d)
		return ctx.Err() == nil
	}
	return c, &waits
}

func TestSignalsAndOptions(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	c, _ := f.client(t, "k3y")
	m := c.Monitor("nightly-backup")
	byID := c.MonitorID("01J9Z3K6V4W8X2Y5Z7A9B1C3D5")
	cases := []struct {
		name string
		send func() error
		want got
	}{
		{"success", func() error { return m.Success(ctx) }, got{path: "/vink/ping/k3y/nightly-backup"}},
		{"start", func() error { return m.Start(ctx) }, got{path: "/vink/ping/k3y/nightly-backup/start"}},
		{"fail with msg", func() error { return m.Fail(ctx, Msg("disk full")) }, got{path: "/vink/ping/k3y/nightly-backup/fail", query: "msg=disk+full"}},
		{"exit 3 with body", func() error { return m.Exit(ctx, 3, Body([]byte("out\n"))) },
			got{path: "/vink/ping/k3y/nightly-backup/3", body: "out\n", contentType: "text/plain; charset=utf-8"}},
		{"exit 0", func() error { return m.Exit(ctx, 0) }, got{path: "/vink/ping/k3y/nightly-backup/0"}},
		{"log msg", func() error { return m.Log(ctx, "step 2 of 5") }, got{path: "/vink/ping/k3y/nightly-backup/log", query: "msg=step+2+of+5"}},
		{"log json body", func() error { return m.Log(ctx, "", Body([]byte(`{"files":3}`)), ContentType("application/json")) },
			got{path: "/vink/ping/k3y/nightly-backup/log", body: `{"files":3}`, contentType: "application/json"}},
		{"run id and create", func() error { return m.Success(ctx, RunID("r1"), Create()) }, got{path: "/vink/ping/k3y/nightly-backup", query: "create=1&rid=r1"}},
		{"by id", func() error { return byID.Fail(ctx) }, got{path: "/vink/ping/id/01J9Z3K6V4W8X2Y5Z7A9B1C3D5/fail"}},
		{"escaped slug", func() error { return c.Monitor("a b").Success(ctx) }, got{path: "/vink/ping/k3y/a%20b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := len(f.requests())
			if err := tc.send(); err != nil {
				t.Fatal(err)
			}
			reqs := f.requests()
			if len(reqs) != before+1 {
				t.Fatalf("%d requests, want 1", len(reqs)-before)
			}
			g := reqs[len(reqs)-1]
			tc.want.method, tc.want.userAgent = "POST", "vink-ping-go"
			if g != tc.want {
				t.Fatalf("sent  %+v\nwant %+v", g, tc.want)
			}
		})
	}
}

func TestRefusedBeforeSending(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	c, _ := f.client(t, "k3y")
	noKey, _ := f.client(t, "")
	cases := []struct {
		name string
		send func() error
		want string
	}{
		{"empty note", func() error { return c.Monitor("job").Log(ctx, "") }, ErrEmptyNote.Error()},
		{"empty run note", func() error { return c.Monitor("job").NewRun().Log(ctx, "") }, ErrEmptyNote.Error()},
		{"negative exit", func() error { return c.Monitor("job").Exit(ctx, -1) }, "outside 0"},
		{"no slug", func() error { return c.Monitor("").Success(ctx) }, "needs a slug"},
		{"no id", func() error { return c.MonitorID("").Success(ctx) }, "needs an id"},
		{"slug without a key", func() error { return noKey.Monitor("job").Success(ctx) }, ErrNoKey.Error()},
		{"create by id", func() error { return c.MonitorID("x").Success(ctx, Create()) }, "by slug"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.send(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	if n := len(f.requests()); n != 0 {
		t.Errorf("%d requests sent for refused pings", n)
	}
	// a client without a key still pings by id
	if err := noKey.MonitorID("x").Success(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestNew(t *testing.T) {
	for _, tc := range []struct{ url, key, base, err string }{
		{"https://vink.example.com/ping/", "k", "https://vink.example.com/ping/", ""},
		{"https://vink.example.com/ping", "k", "https://vink.example.com/ping/", ""},
		{"https://www.example.com/vink/ping//", "k", "https://www.example.com/vink/ping/", ""},
		{"https://vink.example.com/ping/", "", "https://vink.example.com/ping/", ""},
		{"https://vink.example.com", "k", "", "ending in /ping/"},
		{"https://vink.example.com/ping/k/job", "k", "", "ending in /ping/"},
		{"vink.example.com/ping/", "k", "", "ending in /ping/"},
		{"https://vink.example.com/ping/?a=1", "k", "", "ending in /ping/"},
		{"https://vink.example.com/ping/", "https://vink.example.com/ping/k", "", "the key alone"},
		{"https://vink.example.com/ping/", "k y", "", "the key alone"},
	} {
		c, err := New(tc.url, tc.key)
		switch {
		case tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)):
			t.Errorf("New(%q, %q) err = %v, want %q", tc.url, tc.key, err, tc.err)
		case tc.err == "" && (err != nil || c.base != tc.base):
			t.Errorf("New(%q, %q) = %v, %v; want base %q", tc.url, tc.key, c, err, tc.base)
		}
	}
}

func TestFromEnv(t *testing.T) {
	for _, tc := range []struct{ pingURL, server, key, base, err string }{
		{"https://vink.example.com/ping/", "", "k", "https://vink.example.com/ping/", ""},
		{"", "https://www.example.com/vink/", "k", "https://www.example.com/vink/ping/", ""},
		{"https://ping.example.com/ping", "https://vink.example.com", "k", "https://ping.example.com/ping/", ""},
		{"", "", "k", "", "VINK_PING_URL"},
		{"https://vink.example.com/ping/", "", "", "", "VINK_PING_KEY"},
	} {
		t.Setenv("VINK_PING_URL", tc.pingURL)
		t.Setenv("VINK_SERVER", tc.server)
		t.Setenv("VINK_PING_KEY", tc.key)
		c, err := FromEnv(WithUserAgent("job-host"))
		switch {
		case tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)):
			t.Errorf("%+v: err = %v", tc, err)
		case tc.err == "" && (err != nil || c.base != tc.base || c.key != tc.key || c.userAgent != "job-host"):
			t.Errorf("%+v: client %+v, err %v", tc, c, err)
		}
	}
}

func TestRetries(t *testing.T) {
	ctx := context.Background()
	unavailable := answer{status: http.StatusServiceUnavailable}
	t.Run("5xx is retried until it succeeds", func(t *testing.T) {
		f := newFake(t, unavailable, unavailable)
		c, waits := f.client(t, "k")
		if err := c.Monitor("job").Success(ctx); err != nil {
			t.Fatal(err)
		}
		if n := len(f.requests()); n != 3 || len(*waits) != 2 || (*waits)[0] < 500*time.Millisecond || (*waits)[1] < time.Second {
			t.Fatalf("%d requests, waits %v", n, *waits)
		}
	})
	t.Run("attempts run out", func(t *testing.T) {
		f := newFake(t, unavailable, unavailable, unavailable, unavailable)
		c, _ := f.client(t, "k")
		var se *StatusError
		if err := c.Monitor("job").Success(ctx); !errors.As(err, &se) || se.StatusCode != 503 {
			t.Fatalf("err = %v", err)
		}
		if n := len(f.requests()); n != DefaultAttempts {
			t.Fatalf("%d requests", n)
		}
	})
	t.Run("one attempt", func(t *testing.T) {
		f := newFake(t, unavailable)
		c, _ := f.client(t, "k", WithAttempts(0))
		if err := c.Monitor("job").Success(ctx); err == nil || len(f.requests()) != 1 {
			t.Fatalf("err %v after %d requests", err, len(f.requests()))
		}
	})
	t.Run("404 is final", func(t *testing.T) {
		f := newFake(t, answer{status: http.StatusNotFound})
		c, _ := f.client(t, "k")
		if err := c.Monitor("job").Success(ctx); !errors.Is(err, ErrNotFound) || len(f.requests()) != 1 {
			t.Fatalf("err %v after %d requests", err, len(f.requests()))
		}
	})
	t.Run("429 waits for Retry-After", func(t *testing.T) {
		f := newFake(t, answer{status: http.StatusTooManyRequests, header: map[string]string{"Retry-After": "4"}})
		c, waits := f.client(t, "k")
		if err := c.Monitor("job").Success(ctx); err != nil || len(*waits) != 1 || (*waits)[0] != 4*time.Second {
			t.Fatalf("err %v, waits %v", err, *waits)
		}
	})
	t.Run("429 with a long Retry-After gives up", func(t *testing.T) {
		f := newFake(t, answer{status: http.StatusTooManyRequests, header: map[string]string{"Retry-After": "120"}})
		c, _ := f.client(t, "k")
		err := c.Monitor("job").Success(ctx)
		var se *StatusError
		if !errors.Is(err, ErrRateLimited) || !errors.As(err, &se) || se.RetryAfter != 2*time.Minute || len(f.requests()) != 1 {
			t.Fatalf("err %v after %d requests", err, len(f.requests()))
		}
	})
	t.Run("a cancelled context stops retrying", func(t *testing.T) {
		f := newFake(t, unavailable, unavailable)
		c, _ := f.client(t, "k")
		cctx, cancel := context.WithCancel(ctx)
		c.wait = func(context.Context, time.Duration) bool { cancel(); return false }
		if err := c.Monitor("job").Success(cctx); err == nil || len(f.requests()) != 1 {
			t.Fatalf("err %v after %d requests", err, len(f.requests()))
		}
	})
	t.Run("a slow server times out per attempt", func(t *testing.T) {
		slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-time.After(2 * time.Second):
			case <-r.Context().Done():
			}
		}))
		defer slow.Close()
		c, err := New(slow.URL+"/ping/", "k", WithTimeout(50*time.Millisecond), WithAttempts(2))
		if err != nil {
			t.Fatal(err)
		}
		c.wait = func(context.Context, time.Duration) bool { return true }
		start := time.Now()
		if err := c.Monitor("job").Success(ctx); err == nil || time.Since(start) > time.Second {
			t.Fatalf("err %v after %s", err, time.Since(start))
		}
	})
}

// TestErrorsHideTheKey: a transport error quotes the URL; the key and a
// monitor id must not reach the caller's logs.
func TestErrorsHideTheKey(t *testing.T) {
	f := newFake(t)
	f.srv.Close() // nothing listens any more
	c, _ := f.client(t, "s3cretkey", WithAttempts(1))
	err := c.Monitor("job").Success(context.Background())
	if err == nil || strings.Contains(err.Error(), "s3cretkey") || !strings.Contains(err.Error(), "<ping key>/job") {
		t.Fatalf("err = %v", err)
	}
	err = c.MonitorID("01J9Z3K6V4W8X2Y5Z7A9B1C3D5").Success(context.Background())
	if err == nil || strings.Contains(err.Error(), "01J9Z3K6V4W8X2Y5Z7A9B1C3D5") || !strings.Contains(err.Error(), "id/<id>") {
		t.Fatalf("err = %v", err)
	}
}

func TestBodyAndMessageLimits(t *testing.T) {
	ctx := context.Background()
	t.Run("a long body keeps its end, on a whole character", func(t *testing.T) {
		// "abcdéfg" is 8 bytes, é two of them: a 3-byte tail starts inside é
		for limit, want := range map[int]string{3: "fg", 4: "éfg", 8: "abcdéfg"} {
			f := newFake(t)
			c, _ := f.client(t, "k", WithBodyLimit(limit))
			if err := c.Monitor("job").Fail(ctx, Body([]byte("abcdéfg"))); err != nil {
				t.Fatal(err)
			}
			if b := f.requests()[0].body; b != want {
				t.Errorf("limit %d: body %q, want %q", limit, b, want)
			}
		}
	})
	t.Run("a binary body is cut by bytes", func(t *testing.T) {
		f := newFake(t)
		c, _ := f.client(t, "k", WithBodyLimit(3))
		if err := c.Monitor("job").Log(ctx, "", Body([]byte{1, 2, 3, 0x80, 0x81}), ContentType("application/octet-stream")); err != nil {
			t.Fatal(err)
		}
		if b := f.requests()[0].body; b != "\x03\x80\x81" {
			t.Fatalf("body %q", b)
		}
	})
	t.Run("413 is sent again cut to the monitor's limit", func(t *testing.T) {
		f := newFake(t, answer{status: http.StatusRequestEntityTooLarge, header: map[string]string{"Ping-Body-Limit": "4"}})
		c, _ := f.client(t, "k")
		if err := c.Monitor("job").Exit(ctx, 1, Body([]byte("line one\nlast"))); err != nil {
			t.Fatal(err)
		}
		reqs := f.requests()
		if len(reqs) != 2 || reqs[1].body != "last" {
			t.Fatalf("requests %+v", reqs)
		}
	})
	t.Run("a second 413 is final", func(t *testing.T) {
		tooLarge := answer{status: http.StatusRequestEntityTooLarge, header: map[string]string{"Ping-Body-Limit": "4"}}
		f := newFake(t, tooLarge, tooLarge)
		c, _ := f.client(t, "k")
		var se *StatusError
		if err := c.Monitor("job").Exit(ctx, 1, Body([]byte("0123456789"))); !errors.As(err, &se) || se.BodyLimit != 4 || len(f.requests()) != 2 {
			t.Fatalf("err %v after %d requests", err, len(f.requests()))
		}
	})
	t.Run("a long message is cut on a character", func(t *testing.T) {
		f := newFake(t)
		c, _ := f.client(t, "k")
		msg := strings.Repeat("a", MaxMsgLen-1) + "é and more"
		if err := c.Monitor("job").Fail(ctx, Msg(msg)); err != nil {
			t.Fatal(err)
		}
		if q := f.requests()[0].query; q != "msg="+strings.Repeat("a", MaxMsgLen-1) {
			t.Fatalf("query %q", q[len(q)-20:])
		}
	})
}

type exitErr struct{ code int }

func (e exitErr) Error() string { return "exit status " + strconv.Itoa(e.code) }
func (e exitErr) ExitCode() int { return e.code }

func TestRun(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	c, _ := f.client(t, "k")
	m := c.Monitor("job")
	run := m.NewRun()
	rid := "rid=" + run.ID()
	if len(run.ID()) != 32 || m.NewRun().ID() == run.ID() {
		t.Fatalf("run ids %q", run.ID())
	}
	sends := []func() error{
		func() error { return run.Start(ctx) },
		func() error { return run.Log(ctx, "half way") },
		func() error { return run.Finish(ctx, nil) },
		func() error { return run.Finish(ctx, errors.New("disk full")) },
		func() error { return run.Finish(ctx, exitErr{3}, Body([]byte("tail"))) },
		func() error { return run.Finish(ctx, errors.New("x"), Msg("replaced")) },
		func() error { return run.Exit(ctx, 0) },
		func() error { return run.Fail(ctx) },
		func() error { return run.Success(ctx, RunID("ignored")) },
	}
	for _, send := range sends {
		if err := send(); err != nil {
			t.Fatal(err)
		}
	}
	want := []got{
		{path: "/vink/ping/k/job/start", query: rid},
		{path: "/vink/ping/k/job/log", query: "msg=half+way&" + rid},
		{path: "/vink/ping/k/job", query: rid},
		{path: "/vink/ping/k/job/fail", query: "msg=disk+full&" + rid},
		{path: "/vink/ping/k/job/3", query: "msg=exit+status+3&" + rid, body: "tail", contentType: "text/plain; charset=utf-8"},
		{path: "/vink/ping/k/job/fail", query: "msg=replaced&" + rid},
		{path: "/vink/ping/k/job/0", query: rid},
		{path: "/vink/ping/k/job/fail", query: rid},
		{path: "/vink/ping/k/job", query: rid},
	}
	reqs := f.requests()
	if len(reqs) != len(want) {
		t.Fatalf("%d requests, want %d", len(reqs), len(want))
	}
	for i := range want {
		want[i].method, want[i].userAgent = "POST", "vink-ping-go"
		if reqs[i] != want[i] {
			t.Errorf("request %d\n sent %+v\n want %+v", i, reqs[i], want[i])
		}
	}
}

func TestFinishWithAnExecError(t *testing.T) {
	f := newFake(t)
	c, _ := f.client(t, "k")
	err := exec.Command("sh", "-c", "exit 7").Run()
	if err := c.Monitor("job").NewRun().Finish(context.Background(), err); err != nil {
		t.Fatal(err)
	}
	if p := f.requests()[0].path; p != "/vink/ping/k/job/7" {
		t.Fatalf("path %s", p)
	}
}

func TestMonitorRun(t *testing.T) {
	signals := func(f *fake) []string {
		var out []string
		for _, g := range f.requests() {
			out = append(out, strings.TrimPrefix(g.path, "/vink/ping/k/job"))
		}
		return out
	}
	t.Run("success", func(t *testing.T) {
		f := newFake(t)
		c, _ := f.client(t, "k")
		called := false
		err := c.Monitor("job").Run(context.Background(), func(context.Context) error { called = true; return nil })
		if err != nil || !called || strings.Join(signals(f), ",") != "/start," {
			t.Fatalf("err %v called %v signals %v", err, called, signals(f))
		}
		reqs := f.requests()
		if reqs[0].query != reqs[1].query || !strings.HasPrefix(reqs[0].query, "rid=") {
			t.Errorf("start and finish must share a run id: %q %q", reqs[0].query, reqs[1].query)
		}
	})
	t.Run("the job's error comes back and is reported", func(t *testing.T) {
		f := newFake(t)
		c, _ := f.client(t, "k")
		boom := errors.New("boom")
		if err := c.Monitor("job").Run(context.Background(), func(context.Context) error { return boom }); !errors.Is(err, boom) {
			t.Fatalf("err %v", err)
		}
		if s := signals(f); strings.Join(s, ",") != "/start,/fail" || !strings.HasPrefix(f.requests()[1].query, "msg=boom&") {
			t.Fatalf("signals %v %+v", s, f.requests())
		}
	})
	t.Run("ping failures never fail the job", func(t *testing.T) {
		f := newFake(t)
		f.srv.Close()
		var reported []error
		c, _ := f.client(t, "k", WithAttempts(1), WithErrorHandler(func(err error) { reported = append(reported, err) }))
		called := false
		if err := c.Monitor("job").Run(context.Background(), func(context.Context) error { called = true; return nil }); err != nil || !called {
			t.Fatalf("err %v called %v", err, called)
		}
		if len(reported) != 2 {
			t.Fatalf("reported %v", reported)
		}
	})
	t.Run("a panic is reported and re-raised", func(t *testing.T) {
		f := newFake(t)
		c, _ := f.client(t, "k")
		defer func() {
			if p := recover(); p != "kaboom" {
				t.Fatalf("recovered %v", p)
			}
			if s := signals(f); strings.Join(s, ",") != "/start,/fail" || !strings.HasPrefix(f.requests()[1].query, "msg=panic%3A+kaboom&") {
				t.Fatalf("signals %v %+v", s, f.requests())
			}
		}()
		_ = c.Monitor("job").Run(context.Background(), func(context.Context) error { panic("kaboom") })
	})
	t.Run("the finish goes out after the context is cancelled", func(t *testing.T) {
		f := newFake(t)
		c, _ := f.client(t, "k")
		ctx, cancel := context.WithCancel(context.Background())
		err := c.Monitor("job").Run(ctx, func(ctx context.Context) error { cancel(); return ctx.Err() })
		if !errors.Is(err, context.Canceled) || strings.Join(signals(f), ",") != "/start,/fail" {
			t.Fatalf("err %v signals %v", err, signals(f))
		}
	})
}

func TestUserAgent(t *testing.T) {
	f := newFake(t)
	c, _ := f.client(t, "k", WithUserAgent("nas-backup"))
	if err := c.Monitor("job").Success(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ua := f.requests()[0].userAgent; ua != "nas-backup" {
		t.Fatalf("User-Agent %q", ua)
	}
}

// TestCreateSettings: Create's options travel as the query vink reads,
// WasCreated reports the server's answer, and a 400 is not retried.
func TestCreateSettings(t *testing.T) {
	f := newFake(t,
		answer{status: http.StatusOK, header: map[string]string{"Ping-Monitor": "created"}},
		answer{status: http.StatusOK, header: map[string]string{"Ping-Monitor": "existing"}},
		answer{status: http.StatusBadRequest, body: "create: grace: must be at least 60s\ncreate: tags: bad\n"},
	)
	c, waits := f.client(t, "k")
	m := c.Monitor("nightly")
	ctx := context.Background()
	var created bool
	err := m.Success(ctx, Create(Name("Nightly backup"), Cron("0 3 * * *"), Timezone("Europe/Amsterdam"),
		Grace(30*time.Minute), Tolerance(time.Minute), MaxRuntime(2*time.Hour), Tags("backup", "prod"), WasCreated(&created)))
	if err != nil || !created {
		t.Fatalf("first: %v created=%v", err, created)
	}
	q, _ := url.ParseQuery(f.requests()[0].query)
	want := map[string]string{"create": "1", "name": "Nightly backup", "cron": "0 3 * * *", "tz": "Europe/Amsterdam",
		"grace": "30m0s", "tolerance": "1m0s", "max_runtime": "2h0m0s", "tags": "backup,prod"}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
	if q.Has("period") {
		t.Error("period sent without Period")
	}
	created = true
	if err := m.Success(ctx, Create(Period(time.Hour), WasCreated(&created))); err != nil || created {
		t.Fatalf("existing: %v created=%v", err, created)
	}
	if q, _ := url.ParseQuery(f.requests()[1].query); q.Get("period") != "1h0m0s" {
		t.Errorf("period %q", q.Get("period"))
	}
	err = m.Success(ctx, Create(Grace(time.Second)))
	var se *StatusError
	if !errors.As(err, &se) || se.StatusCode != http.StatusBadRequest || len(f.requests()) != 3 || len(*waits) != 0 {
		t.Fatalf("400: %v, %d requests, waits %v", err, len(f.requests()), *waits)
	}
	if se.Detail != "create: grace: must be at least 60s; create: tags: bad" || !strings.HasSuffix(err.Error(), "400 Bad Request: "+se.Detail) {
		t.Errorf("detail %q, error %q", se.Detail, err)
	}
	// without options it is the plain create of before
	if err := m.Success(ctx, Create()); err != nil {
		t.Fatal(err)
	}
	if q := f.requests()[3].query; q != "create=1" {
		t.Errorf("plain create: %q", q)
	}
}
