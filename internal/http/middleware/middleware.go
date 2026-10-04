// Package middleware holds the request plumbing shared by every handler
// group: request ids, client IP behind trusted proxies, panic recovery
// and the one-line request log.
package middleware

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/w4jnl/vink/internal/domain"
)

type ctxKey int

const (
	keyRequestID ctxKey = iota
	keyClientIP
	keyLogFields
)

// Chain applies middlewares so the first one listed is the outermost.
func Chain(h http.Handler, mws ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// RequestID assigns a ULID to every request and echoes it in X-Request-Id.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := domain.NewID()
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyRequestID, id)))
	})
}

// GetRequestID returns the id set by RequestID, or "".
func GetRequestID(ctx context.Context) string {
	id, _ := ctx.Value(keyRequestID).(string)
	return id
}

// RealIP records the client IP: the last X-Forwarded-For hop that is not a
// trusted proxy when the peer is trusted, otherwise the peer address.
func RealIP(trustedCIDRs []string) func(http.Handler) http.Handler {
	var nets []*net.IPNet
	for _, c := range trustedCIDRs {
		if _, n, err := net.ParseCIDR(c); err == nil {
			nets = append(nets, n)
		}
	}
	trusted := func(ip net.IP) bool {
		for _, n := range nets {
			if n.Contains(ip) {
				return true
			}
		}
		return false
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := peerIP(r.RemoteAddr)
			if ip != nil && trusted(ip) {
				if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
					hops := strings.Split(xff, ",")
					for i := len(hops) - 1; i >= 0; i-- {
						cand := net.ParseIP(strings.TrimSpace(hops[i]))
						if cand == nil {
							break
						}
						ip = cand
						if !trusted(cand) {
							break
						}
					}
				}
			}
			s := ""
			if ip != nil {
				s = ip.String()
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyClientIP, s)))
		})
	}
}

func peerIP(remoteAddr string) net.IP {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	return net.ParseIP(host)
}

// Peer returns the TCP peer's address: the proxy when there is one.
func Peer(r *http.Request) string {
	if ip := peerIP(r.RemoteAddr); ip != nil {
		return ip.String()
	}
	return r.RemoteAddr
}

// ClientIP returns the address recorded by RealIP, falling back to the
// peer address.
func ClientIP(r *http.Request) string {
	if ip, ok := r.Context().Value(keyClientIP).(string); ok && ip != "" {
		return ip
	}
	if ip := peerIP(r.RemoteAddr); ip != nil {
		return ip.String()
	}
	return r.RemoteAddr
}

// LogFields is filled by inner middleware (auth sets org, project and
// user) and read by the request logger when the response is done.
type LogFields struct {
	mu    sync.Mutex
	attrs []slog.Attr
}

// AddLogFields attaches attributes to the request log line.
func AddLogFields(ctx context.Context, attrs ...slog.Attr) {
	if f, ok := ctx.Value(keyLogFields).(*LogFields); ok {
		f.mu.Lock()
		f.attrs = append(f.attrs, attrs...)
		f.mu.Unlock()
	}
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Logger writes one line per request.
func Logger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			startAt := time.Now()
			fields := &LogFields{}
			sw := &statusWriter{ResponseWriter: w}
			next.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), keyLogFields, fields)))
			if sw.status == 0 {
				sw.status = http.StatusOK
			}
			attrs := []slog.Attr{
				slog.String("method", r.Method), slog.String("path", r.URL.Path), slog.Int("status", sw.status),
				slog.Int64("dur_ms", time.Since(startAt).Milliseconds()), slog.String("req_id", GetRequestID(r.Context())),
				slog.String("ip", ClientIP(r)), slog.Int("bytes", sw.bytes),
			}
			fields.mu.Lock()
			attrs = append(attrs, fields.attrs...)
			fields.mu.Unlock()
			level := slog.LevelInfo
			if sw.status >= 500 {
				level = slog.LevelError
			}
			log.LogAttrs(r.Context(), level, "request", attrs...)
		})
	}
}

// Recover turns a panic into a 500 and logs the stack.
func Recover(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error("panic", "err", rec, "req_id", GetRequestID(r.Context()), "path", r.URL.Path, "stack", string(debug.Stack()))
					http.Error(w, "internal server error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
