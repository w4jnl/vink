// Package ping is the heartbeat ingress: unauthenticated beyond the
// project ping key, no session, no CSRF, no HTML, its own rate limits.
package ping

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/http/middleware"
	"github.com/w4jnl/vink/internal/service"
)

// MaxMsgLen caps the msg= query parameter.
const MaxMsgLen = 2000

// Options tune the ingress.
type Options struct {
	BodyLimit      int64
	RatePerMonitor int
	RatePerIP      int
}

// Handler serves every /ping/ URL form.
type Handler struct {
	svc       *service.Service
	log       *slog.Logger
	opts      Options
	byMonitor *Limiter
	byIP      *Limiter
	now       func() time.Time
}

// New builds the handler.
func New(svc *service.Service, log *slog.Logger, opts Options) *Handler {
	if opts.BodyLimit <= 0 {
		opts.BodyLimit = 64 * 1024
	}
	if opts.RatePerMonitor <= 0 {
		opts.RatePerMonitor = 10
	}
	if opts.RatePerIP <= 0 {
		opts.RatePerIP = 300
	}
	return &Handler{
		svc: svc, log: log, opts: opts,
		byMonitor: NewLimiter(opts.RatePerMonitor, 30),
		byIP:      NewLimiter(opts.RatePerIP, opts.RatePerIP),
		now:       func() time.Time { return time.Now().UTC() },
	}
}

// Routes registers the URL forms on mux. Literal "id" beats the {key}
// wildcard, so /ping/id/{id} and /ping/{key}/{slug} coexist. A GET
// pattern also matches HEAD, so HEAD is not registered on its own.
func (h *Handler) Routes(mux *http.ServeMux) {
	for _, m := range domain.AllowedMethods {
		if m == http.MethodHead {
			continue
		}
		mux.HandleFunc(m+" /ping/{key}/{slug}", h.bySlug)
		mux.HandleFunc(m+" /ping/{key}/{slug}/{signal}", h.bySlug)
		mux.HandleFunc(m+" /ping/id/{id}", h.byID)
		mux.HandleFunc(m+" /ping/id/{id}/{signal}", h.byID)
	}
}

func (h *Handler) bySlug(w http.ResponseWriter, r *http.Request) {
	h.handle(w, r, r.PathValue("key"), r.PathValue("slug"), "")
}

func (h *Handler) byID(w http.ResponseWriter, r *http.Request) {
	h.handle(w, r, "", "", r.PathValue("id"))
}

func (h *Handler) handle(w http.ResponseWriter, r *http.Request, key, slug, id string) {
	w.Header().Set("Ping-Body-Limit", strconv.FormatInt(h.opts.BodyLimit, 10))
	w.Header().Set("Cache-Control", "no-store")

	ip := middleware.ClientIP(r)
	if ok, wait := h.byIP.Allow(ip); !ok {
		h.tooMany(w, wait)
		return
	}
	signal, exitCode, ok := parseSignal(r.PathValue("signal"))
	if !ok {
		h.notFound(w)
		return
	}
	q := r.URL.Query()
	create := q.Get("create") == "1" || q.Get("create") == "true"
	target, err := h.svc.ResolvePing(r.Context(), key, slug, id, create)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			h.notFound(w)
			return
		}
		if ve, isVal := domain.AsValidation(err); isVal {
			// create=1 with a slug that cannot be a monitor
			h.log.Debug("ping auto-create rejected", "slug", slug, "err", ve)
			h.notFound(w)
			return
		}
		h.fail(w, r, err)
		return
	}
	m := target.Monitor
	middleware.AddLogFields(r.Context(), slog.String("project_id", m.ProjectID), slog.String("monitor", m.Slug))
	if target.Created {
		h.log.Info("ping created monitor", "project_id", m.ProjectID, "monitor", m.Slug, "ip", ip)
	}
	if m.Heartbeat == nil || !m.Heartbeat.AcceptsMethod(r.Method) {
		w.Header().Set("Allow", strings.Join(m.Heartbeat.Methods, ", "))
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if ok, wait := h.byMonitor.Allow(m.ID); !ok {
		h.tooMany(w, wait)
		return
	}
	limit := h.opts.BodyLimit
	if m.Heartbeat.BodyLimit > 0 && m.Heartbeat.BodyLimit < limit {
		limit = m.Heartbeat.BodyLimit
	}
	if r.ContentLength > limit {
		http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
		return
	}
	body, truncated, err := readBody(r, limit)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	msg := q.Get("msg")
	if len(msg) > MaxMsgLen {
		msg = msg[:MaxMsgLen]
	}
	obs := service.PingObservation{
		At: h.now(), Signal: signal, ExitCode: exitCode, RunID: q.Get("rid"), Method: r.Method,
		RemoteAddr: ip, UserAgent: r.UserAgent(), Body: body, ContentType: r.Header.Get("Content-Type"), Msg: msg, Truncated: truncated,
	}
	if obs.RunID != "" && !domain.IsID(obs.RunID) && len(obs.RunID) > 64 {
		obs.RunID = obs.RunID[:64]
	}
	if _, d, err := h.svc.RecordPing(r.Context(), target, obs); err != nil {
		h.fail(w, r, err)
		return
	} else if d.Changed {
		h.log.Debug("ping flipped state", "monitor", m.Slug, "from", d.From, "to", d.To)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, "OK\n")
	}
}

// parseSignal maps the URL suffix to a signal: none means ok, "start",
// "fail", "log", or an exit code where 0 is ok.
func parseSignal(s string) (domain.Signal, *int64, bool) {
	switch s {
	case "":
		return domain.SignalOK, nil, true
	case "start":
		return domain.SignalStart, nil, true
	case "fail":
		return domain.SignalFail, nil, true
	case "log":
		return domain.SignalLog, nil, true
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 || n > math.MaxInt32 {
		return "", nil, false
	}
	return domain.SignalExit, &n, true
}

// readBody reads at most limit bytes and reports whether more followed.
func readBody(r *http.Request, limit int64) ([]byte, bool, error) {
	if r.Body == nil || r.Method == http.MethodHead {
		return nil, false, nil
	}
	buf, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(buf)) > limit {
		return buf[:limit], true, nil
	}
	return buf, false, nil
}

func (h *Handler) notFound(w http.ResponseWriter) {
	// Unknown key and unknown monitor answer identically.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, "not found\n")
}

func (h *Handler) tooMany(w http.ResponseWriter, wait time.Duration) {
	secs := int(math.Ceil(wait.Seconds()))
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	http.Error(w, "too many pings", http.StatusTooManyRequests)
}

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	h.log.Error("ping failed", "err", err, "req_id", middleware.GetRequestID(r.Context()))
	http.Error(w, fmt.Sprintf("internal error (request %s)", middleware.GetRequestID(r.Context())), http.StatusInternalServerError)
}
