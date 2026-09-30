package checks

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/domain"
)

// TCP connects, optionally sends a line and matches the banner.
type TCP struct{}

func (TCP) Kind() domain.Kind { return domain.KindTCP }

func (TCP) Check(ctx context.Context, spec *domain.PullSpec, env Env) Result {
	t := spec.TCP
	if t == nil {
		return fail("no tcp block")
	}
	addr := net.JoinHostPort(t.Host, strconv.Itoa(t.Port))
	start := time.Now()
	conn, err := env.Out.Dial(ctx, "tcp", addr)
	if err != nil {
		return fail(reasonFor(err, spec.Timeout.Std()))
	}
	defer func() { _ = conn.Close() }()
	res := Result{LatencyMs: ms(start), Detail: map[string]any{"addr": conn.RemoteAddr().String()}}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if t.Send != "" {
		if _, err := conn.Write([]byte(t.Send)); err != nil {
			res.Reason = "write: " + reasonFor(err, spec.Timeout.Std())
			return res
		}
	}
	if t.Expect != "" {
		banner := readBanner(conn, t.Expect)
		res.Detail["banner"] = truncate(strings.TrimSpace(banner), 200)
		if !strings.Contains(banner, t.Expect) {
			res.Reason = fmt.Sprintf("banner lacks %q", t.Expect)
			return res
		}
		res.Detail["matched"] = true
	}
	res.OK = true
	return res
}

// readBanner reads until expect appears, the peer stops sending or 4 KiB
// arrived. The first read waits for the connection deadline; later reads
// wait briefly so a banner that already arrived is judged at once.
func readBanner(conn net.Conn, expect string) string {
	var buf []byte
	chunk := make([]byte, 1024)
	for len(buf) < 4096 {
		n, err := conn.Read(chunk)
		buf = append(buf, chunk[:n]...)
		if strings.Contains(string(buf), expect) || err != nil {
			break
		}
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	}
	return string(buf)
}
