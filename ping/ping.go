package ping

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// DefaultBodyLimit is the body size a vink server keeps unless its
	// operator changed ping.body_limit. A longer body is cut to its last
	// DefaultBodyLimit bytes before it is sent; see [WithBodyLimit].
	DefaultBodyLimit = 64 << 10

	// MaxMsgLen is the longest message the server keeps, in bytes. A
	// longer [Msg] is cut at a character boundary before it is sent.
	MaxMsgLen = 2000

	// DefaultAttempts is how many times a ping is tried when the server
	// cannot be reached, answers 5xx, or rate-limits it.
	DefaultAttempts = 3

	// DefaultTimeout bounds one attempt.
	DefaultTimeout = 10 * time.Second

	// maxRetryWait is the longest pause before a retry; a Retry-After
	// longer than this ends the ping instead.
	maxRetryWait = 30 * time.Second

	modulePath = "github.com/w4jnl/vink/ping"
)

var (
	// ErrNotFound: the server knows no monitor for this key and slug, or
	// for this id. A slug ping with [Create] makes the monitor instead.
	ErrNotFound = errors.New("ping: unknown ping key or monitor")
	// ErrRateLimited: the monitor or this address sent too many pings and
	// the server dropped this one.
	ErrRateLimited = errors.New("ping: rate limited")
	// ErrNoKey: a ping by slug needs the project's ping key, and the
	// client was made without one.
	ErrNoKey = errors.New("ping: a ping by slug needs the project's ping key")
	// ErrEmptyNote: a log ping carries a note, as a message or a body.
	ErrEmptyNote = errors.New("ping: a log ping needs a message or a body")
)

// StatusError is a ping the server answered with a status other than 200.
// errors.Is matches it against [ErrNotFound] for a 404 and
// [ErrRateLimited] for a 429.
type StatusError struct {
	StatusCode int
	Status     string        // the status line, such as "404 Not Found"
	RetryAfter time.Duration // from Retry-After, on a 429
	BodyLimit  int           // from Ping-Body-Limit, the body size the monitor accepts
	// Detail is the server's reason on a 400, such as why it refused the
	// settings of a [Create]: "create: grace: must be at least 60s".
	Detail string
}

func (e *StatusError) Error() string {
	if e.Detail != "" {
		return "ping: the server answered " + e.Status + ": " + e.Detail
	}
	return "ping: the server answered " + e.Status
}

// Is reports whether the answer means target.
func (e *StatusError) Is(target error) bool {
	switch target {
	case ErrNotFound:
		return e.StatusCode == http.StatusNotFound
	case ErrRateLimited:
		return e.StatusCode == http.StatusTooManyRequests
	}
	return false
}

// Client sends pings to one vink server for one project. Make it once and
// share it: it is safe for concurrent use and holds no connection state of
// its own beyond its [http.Client].
type Client struct {
	base      string // the ping URL up to the key, ending in /ping/
	key       string
	hc        *http.Client
	userAgent string
	attempts  int
	timeout   time.Duration
	bodyLimit int
	onError   func(error)
	// wait pauses before a retry and reports false when ctx ended first;
	// tests replace it to keep time out of the suite.
	wait func(ctx context.Context, d time.Duration) bool
}

// Option changes a [Client].
type Option func(*Client)

// WithHTTPClient sends the pings through hc, for a proxy, a private CA or
// instrumentation. The default is [http.DefaultClient], which honours
// HTTPS_PROXY and the system's certificates.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		if hc != nil {
			c.hc = hc
		}
	}
}

// WithUserAgent names the sender. vink shows the User-Agent with every
// observation, so a name such as "nas-backup" tells hosts apart where
// their address does not. The default is vink-ping-go/<module version>.
func WithUserAgent(ua string) Option {
	return func(c *Client) {
		if ua != "" {
			c.userAgent = ua
		}
	}
}

// WithAttempts sets how many times one ping is tried, [DefaultAttempts]
// by default. 1 turns retries off.
func WithAttempts(n int) Option {
	return func(c *Client) { c.attempts = max(n, 1) }
}

// WithTimeout bounds each attempt, [DefaultTimeout] by default. 0 leaves
// only the caller's context to end it.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.timeout = max(d, 0) }
}

// WithBodyLimit sets the size a body is cut to, keeping its end, which is
// where a log says why a job failed. Use the monitor's or the server's
// limit when it is not [DefaultBodyLimit]; 0 sends bodies whole and lets
// the server's 413 and Ping-Body-Limit answer decide.
func WithBodyLimit(n int) Option {
	return func(c *Client) { c.bodyLimit = max(n, 0) }
}

// WithErrorHandler receives the pings [Monitor.Run] could not send. Run
// never lets a ping failure change what it returns, so this is where to
// log one. The default drops them.
func WithErrorHandler(fn func(error)) Option {
	return func(c *Client) { c.onError = fn }
}

// New makes a client. pingURL is a ping URL cut before the key, as the
// monitor's page in vink shows it: https://vink.example.com/ping/, or
// https://www.example.com/vink/ping/ when vink lives under a path. key is
// the project's ping key (Settings › Keys in vink, or the part after
// /ping/ in any of the project's ping URLs). key may be empty when the
// client only pings by id ([Client.MonitorID]).
func New(pingURL, key string, opts ...Option) (*Client, error) {
	base, err := baseURL(pingURL)
	if err != nil {
		return nil, err
	}
	if strings.ContainsAny(key, "/?#%& \t\r\n") {
		return nil, errors.New("ping: the ping key is the key alone, the part after /ping/ in a ping URL")
	}
	c := &Client{
		base: base, key: key, hc: http.DefaultClient, userAgent: defaultUserAgent(),
		attempts: DefaultAttempts, timeout: DefaultTimeout, bodyLimit: DefaultBodyLimit,
		wait: sleep,
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// FromEnv makes a client from the environment the vink CLI reads:
// VINK_PING_URL (a ping URL up to the key) and VINK_PING_KEY. Without
// VINK_PING_URL it uses VINK_SERVER with /ping/ added. Both an address
// and a key are required, so a missing variable shows at start-up rather
// than at the first ping.
func FromEnv(opts ...Option) (*Client, error) {
	u := os.Getenv("VINK_PING_URL")
	if u == "" {
		if s := os.Getenv("VINK_SERVER"); s != "" {
			u = strings.TrimRight(s, "/") + "/ping/"
		}
	}
	if u == "" {
		return nil, errors.New("ping: set VINK_PING_URL to a ping URL up to the key (https://vink.example.com/ping/), or VINK_SERVER")
	}
	key := os.Getenv("VINK_PING_KEY")
	if key == "" {
		return nil, errors.New("ping: set VINK_PING_KEY to the project's ping key")
	}
	return New(u, key, opts...)
}

// baseURL checks a ping address and gives it its trailing slash. Its path
// ends in /ping, since it is a ping URL cut before the key.
func baseURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" ||
		!strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/ping") {
		return "", fmt.Errorf("ping: the ping URL is a ping URL up to the key, ending in /ping/, like https://vink.example.com/ping/; got %q", raw)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/"
	u.RawPath = ""
	return u.String(), nil
}

// Monitor is one heartbeat monitor, named by its slug or its id. It is a
// cheap value: make one per call or keep it, either works.
type Monitor struct {
	c      *Client
	path   string // after the base: <key>/<slug> or id/<id>
	secret string // the base and the key or id, hidden in errors
	shown  string
	bySlug bool
	err    error // a construction mistake, returned by every ping
}

// Monitor names a monitor of the client's project by its slug, the name
// in its ping URL.
func (c *Client) Monitor(slug string) *Monitor {
	m := &Monitor{c: c, bySlug: true, path: c.key + "/" + url.PathEscape(slug), secret: c.base + c.key + "/", shown: c.base + "<ping key>/"}
	switch {
	case slug == "":
		m.err = errors.New("ping: a monitor needs a slug")
	case c.key == "":
		m.err = ErrNoKey
	}
	return m
}

// MonitorID names a monitor by its id, which works without a ping key:
// the id alone lets anyone ping that one monitor, so treat it like a key.
// [Create] does not apply.
func (c *Client) MonitorID(id string) *Monitor {
	m := &Monitor{c: c, path: "id/" + url.PathEscape(id), secret: c.base + "id/" + url.PathEscape(id), shown: c.base + "id/<id>"}
	if id == "" {
		m.err = errors.New("ping: a monitor needs an id")
	}
	return m
}

// PingOption adds to one ping.
type PingOption func(*request)

type request struct {
	msg         string
	body        []byte
	contentType string
	runID       string
	create      *createSpec
}

// createSpec is what a creating ping sends, and where it says whether
// it made the monitor.
type createSpec struct {
	values  url.Values
	created *bool
}

// Msg attaches a short message, shown on the observation's row and, on a
// failure, in the alert. It is cut to [MaxMsgLen] bytes.
func Msg(s string) PingOption { return func(r *request) { r.msg = s } }

// Body attaches a body, stored with the observation and opened from its
// row: a command's output, a report. Without a [ContentType] it is sent
// as text/plain; charset=utf-8. A body over the client's limit keeps its
// end; see [WithBodyLimit]. On a failure without a [Msg], the alert
// carries the body's last lines.
func Body(b []byte) PingOption { return func(r *request) { r.body = b } }

// ContentType sets the body's media type, for a JSON report, say.
func ContentType(ct string) PingOption { return func(r *request) { r.contentType = ct } }

// RunID pairs a start with its finish when runs of one monitor overlap:
// send the same id on both. A [Run] does this for you. The server keeps
// up to 64 characters.
func RunID(id string) PingOption { return func(r *request) { r.runID = id } }

// Create makes the monitor from this ping when the project has none with
// this slug: a heartbeat with a one-day period and a one-hour grace,
// unless [CreateOption]s set it up otherwise. The slug must be one vink
// accepts (up to 64 lowercase letters, digits and inner dashes);
// otherwise the answer is [ErrNotFound].
//
// The options apply on create only: a ping never changes a monitor that
// exists, and its ping goes through as usual. [WasCreated] tells the two
// apart. Settings vink refuses (a grace shorter than its tolerance, an
// unknown timezone) make the ping fail with a 400 [StatusError] that is
// not retried and whose Detail says why, and nothing is recorded.
// Pings by id cannot create.
func Create(opts ...CreateOption) PingOption {
	return func(r *request) {
		c := &createSpec{values: url.Values{}}
		for _, o := range opts {
			o(c)
		}
		r.create = c
	}
}

// CreateOption sets up the monitor a [Create] ping makes.
type CreateOption func(*createSpec)

// Name is the monitor's display name; the slug otherwise.
func Name(name string) CreateOption { return func(c *createSpec) { c.values.Set("name", name) } }

// Period expects a ping every d. Give Period or [Cron], not both.
func Period(d time.Duration) CreateOption { return dur("period", d) }

// Cron expects a ping at each time of a cron expression ("0 3 * * *"),
// in [Timezone].
func Cron(expr string) CreateOption { return func(c *createSpec) { c.values.Set("cron", expr) } }

// Timezone is the IANA name ("Europe/Amsterdam") the schedule is read
// in; the project's otherwise.
func Timezone(tz string) CreateOption { return func(c *createSpec) { c.values.Set("tz", tz) } }

// Grace is how long after the expected time a missing ping turns the
// monitor down.
func Grace(d time.Duration) CreateOption { return dur("grace", d) }

// Tolerance is how long after the expected time a ping still counts as on
// time; late starts when it runs out. At most the grace.
func Tolerance(d time.Duration) CreateOption { return dur("tolerance", d) }

// MaxRuntime fails a run whose finish has not come d after its start.
func MaxRuntime(d time.Duration) CreateOption { return dur("max_runtime", d) }

// Tags labels the monitor, for routes, filters and status pages.
func Tags(tags ...string) CreateOption {
	return func(c *createSpec) { c.values.Set("tags", strings.Join(tags, ",")) }
}

// WasCreated sets *created, after a ping that went through, to whether
// that ping made the monitor (true) or found it already there (false,
// and the other options were not used).
func WasCreated(created *bool) CreateOption { return func(c *createSpec) { c.created = created } }

func dur(key string, d time.Duration) CreateOption {
	return func(c *createSpec) { c.values.Set(key, d.String()) }
}

func build(opts []PingOption) request {
	var r request
	for _, o := range opts {
		o(&r)
	}
	return r
}

// Success says the job ran and succeeded. It is the plain ping: the
// monitor goes up, and the next deadline counts from now.
func (m *Monitor) Success(ctx context.Context, opts ...PingOption) error {
	return m.send(ctx, "", build(opts))
}

// Start says the job began. vink then shows it running, measures its
// duration when the finish arrives, and fails it when the monitor's
// max_runtime passes first. It changes no state.
func (m *Monitor) Start(ctx context.Context, opts ...PingOption) error {
	return m.send(ctx, "start", build(opts))
}

// Fail says the job failed. The monitor goes down once its failure
// threshold is reached (one failure by default) and alerts go out.
func (m *Monitor) Fail(ctx context.Context, opts ...PingOption) error {
	return m.send(ctx, "fail", build(opts))
}

// Exit reports a process's exit code: 0 is a success, anything else a
// failure that carries the code into the alert.
func (m *Monitor) Exit(ctx context.Context, code int, opts ...PingOption) error {
	if err := checkExit(code); err != nil {
		return err
	}
	return m.send(ctx, strconv.Itoa(code), build(opts))
}

func checkExit(code int) error {
	if code < 0 || code > math.MaxInt32 {
		return fmt.Errorf("ping: exit code %d is outside 0 to %d", code, math.MaxInt32)
	}
	return nil
}

// Log adds a progress note to the monitor's history and changes nothing
// else: no state, no deadline, and an open run stays open. msg is the
// note's line; a [Body] can carry more, alone or with it. A note with
// neither is refused with [ErrEmptyNote].
func (m *Monitor) Log(ctx context.Context, msg string, opts ...PingOption) error {
	r := build(opts)
	if msg != "" {
		r.msg = msg
	}
	if r.msg == "" && len(r.body) == 0 {
		return ErrEmptyNote
	}
	return m.send(ctx, "log", r)
}

func (m *Monitor) send(ctx context.Context, signal string, r request) error {
	if m.err != nil {
		return m.err
	}
	if r.create != nil && !m.bySlug {
		return errors.New("ping: Create works on pings by slug, not by id")
	}
	u := m.c.base + m.path
	if signal != "" {
		u += "/" + signal
	}
	q := url.Values{}
	if r.msg != "" {
		q.Set("msg", cutMsg(r.msg))
	}
	if r.runID != "" {
		q.Set("rid", r.runID)
	}
	var onOK func(http.Header)
	if r.create != nil {
		q.Set("create", "1")
		for k, v := range r.create.values {
			q[k] = v
		}
		if p := r.create.created; p != nil {
			onOK = func(h http.Header) { *p = h.Get("Ping-Monitor") == "created" }
		}
	}
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	ct := r.contentType
	if len(r.body) > 0 && ct == "" {
		ct = "text/plain; charset=utf-8"
	}
	hide := func(s string) string { return strings.Replace(s, m.secret, m.shown, 1) }
	return m.c.do(ctx, u, hide, keepTail(r.body, m.c.bodyLimit, ct), ct, onOK)
}

// do sends one ping, retrying what a retry can fix: no answer, a 5xx, a
// 429 after its Retry-After, and a 413 once, with the body cut to the
// limit the server named.
func (c *Client) do(ctx context.Context, u string, hide func(string) string, body []byte, ct string, onOK func(http.Header)) error {
	shrunk := false
	for attempt := 1; ; attempt++ {
		err := c.once(ctx, u, hide, body, ct, onOK)
		if err == nil || ctx.Err() != nil {
			return err
		}
		var wait time.Duration
		var se *StatusError
		if errors.As(err, &se) {
			switch {
			case se.StatusCode == http.StatusRequestEntityTooLarge && !shrunk && se.BodyLimit > 0 && se.BodyLimit < len(body):
				body, shrunk = keepTail(body, se.BodyLimit, ct), true
				attempt--
				continue
			case se.StatusCode == http.StatusTooManyRequests:
				wait = se.RetryAfter
			case se.StatusCode < 500:
				return err
			}
		}
		if attempt >= c.attempts {
			return err
		}
		if wait <= 0 {
			wait = backoff(attempt)
		}
		if wait > maxRetryWait || !c.wait(ctx, wait) {
			return err
		}
	}
}

// once sends one attempt. hide takes the key or id out of a URL, since a
// transport error quotes it and the caller will likely log the error.
func (c *Client) once(ctx context.Context, u string, hide func(string) string, body []byte, ct string, onOK func(http.Header)) error {
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	var rdr io.Reader
	if len(body) > 0 {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, rdr)
	if err != nil {
		return fmt.Errorf("ping: bad URL %s", hide(u))
	}
	req.Header.Set("User-Agent", c.userAgent)
	if len(body) > 0 {
		req.Header.Set("Content-Type", ct)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			ue.URL = hide(ue.URL)
		}
		return fmt.Errorf("ping: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == http.StatusOK {
		if onOK != nil {
			onOK(resp.Header)
		}
		return nil
	}
	se := &StatusError{StatusCode: resp.StatusCode, Status: resp.Status}
	if resp.StatusCode == http.StatusBadRequest {
		se.Detail = detail(answer)
	}
	if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
		se.RetryAfter = time.Duration(s) * time.Second
	}
	if n, err := strconv.Atoi(resp.Header.Get("Ping-Body-Limit")); err == nil && n > 0 {
		se.BodyLimit = n
	}
	return se
}

// detail turns a plain-text answer into one line: its lines joined by "; ",
// at most 500 bytes.
func detail(b []byte) string {
	var lines []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" && utf8.ValidString(l) {
			lines = append(lines, l)
		}
	}
	s := strings.Join(lines, "; ")
	if len(s) > 500 {
		s = s[:500]
		for len(s) > 0 && !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return s
}

// backoff is the pause before retry n: 0.5 s, 1 s, 2 s, up to 5 s, with
// up to a quarter more so a fleet of jobs does not retry in step.
func backoff(n int) time.Duration {
	d := min(500*time.Millisecond<<(n-1), 5*time.Second)
	return d + rand.N(d/4+1) //nolint:gosec // G404: jitter needs spread, not secrecy
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// keepTail cuts b to its last limit bytes; a text body starts on a whole
// character.
func keepTail(b []byte, limit int, ct string) []byte {
	if limit <= 0 || len(b) <= limit {
		return b
	}
	b = b[len(b)-limit:]
	if strings.HasPrefix(ct, "text/") {
		for i := 0; i < utf8.UTFMax-1 && len(b) > 0 && !utf8.RuneStart(b[0]); i++ {
			b = b[1:]
		}
	}
	return b
}

// cutMsg cuts s to MaxMsgLen bytes on a character boundary.
func cutMsg(s string) string {
	if len(s) <= MaxMsgLen {
		return s
	}
	i := MaxMsgLen
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i]
}

func defaultUserAgent() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, d := range bi.Deps {
			if d.Path == modulePath && d.Version != "" && d.Version != "(devel)" {
				return "vink-ping-go/" + d.Version
			}
		}
	}
	return "vink-ping-go"
}
