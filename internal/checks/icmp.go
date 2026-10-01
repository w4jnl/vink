package checks

import (
	"context"
	"fmt"
	"math"
	"net"
	"os"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"

	"github.com/w4jnl/vink/internal/domain"
)

// ICMP pings a host over an unprivileged datagram socket. Where the
// kernel forbids that (Linux without CAP_NET_RAW and outside
// net.ipv4.ping_group_range) the attempt fails with a reason that says so.
type ICMP struct{}

func (ICMP) Kind() domain.Kind { return domain.KindICMP }

func (ICMP) Check(ctx context.Context, spec *domain.PullSpec, env Env) Result {
	i := spec.ICMP
	if i == nil {
		return fail("no icmp block")
	}
	ips, err := env.Out.CheckTarget(ctx, i.Host)
	if err != nil {
		return fail(reasonFor(err, spec.Timeout.Std()))
	}
	if len(ips) == 0 {
		return fail("no addresses for " + i.Host)
	}
	ip := ips[0].IP
	for _, cand := range ips {
		if cand.IP.To4() != nil {
			ip = cand.IP
			break
		}
	}
	network, listen, proto := "udp4", "0.0.0.0", ipv4.ICMPTypeEcho.Protocol()
	var echoType icmp.Type = ipv4.ICMPTypeEcho
	if ip.To4() == nil {
		network, listen, proto = "udp6", "::", ipv6.ICMPTypeEchoRequest.Protocol()
		echoType = ipv6.ICMPTypeEchoRequest
	}
	pc, err := icmp.ListenPacket(network, listen)
	if err != nil {
		return fail("icmp unavailable: " + err.Error() + "; needs CAP_NET_RAW or net.ipv4.ping_group_range")
	}
	defer func() { _ = pc.Close() }()
	dst := &net.UDPAddr{IP: ip}
	env.Out.Observe("icmp", ip.String())
	id := os.Getpid() & 0xffff
	perPacket := spec.Timeout.Std() / time.Duration(i.Count)
	if perPacket < 200*time.Millisecond {
		perPacket = 200 * time.Millisecond
	}
	var rtts []float64
	sent, received := 0, 0
	buf := make([]byte, 1500)
	for seq := 1; seq <= i.Count && ctx.Err() == nil; seq++ {
		msg := icmp.Message{Type: echoType, Code: 0, Body: &icmp.Echo{ID: id, Seq: seq, Data: []byte("vink")}}
		wire, err := msg.Marshal(nil)
		if err != nil {
			return fail("icmp: " + err.Error())
		}
		start := time.Now()
		if _, err := pc.WriteTo(wire, dst); err != nil {
			return fail("send: " + reasonFor(err, spec.Timeout.Std()))
		}
		sent++
		deadline := start.Add(perPacket)
		if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
			deadline = dl
		}
		_ = pc.SetReadDeadline(deadline)
		for {
			n, _, err := pc.ReadFrom(buf)
			if err != nil {
				break // lost
			}
			rm, err := icmp.ParseMessage(proto, buf[:n])
			if err != nil {
				continue
			}
			echo, ok := rm.Body.(*icmp.Echo)
			if ok && (rm.Type == ipv4.ICMPTypeEchoReply || rm.Type == ipv6.ICMPTypeEchoReply) && echo.Seq == seq {
				received++
				rtts = append(rtts, float64(time.Since(start).Microseconds())/1000)
				break
			}
		}
	}
	res := Result{Detail: map[string]any{"addr": ip.String(), "sent": sent, "received": received}}
	loss := 1.0
	if sent > 0 {
		loss = float64(sent-received) / float64(sent)
	}
	res.Detail["loss"] = math.Round(loss*100) / 100
	if len(rtts) > 0 {
		sum := 0.0
		for _, r := range rtts {
			sum += r
		}
		res.LatencyMs = int64(math.Round(sum / float64(len(rtts))))
		res.Detail["rtts_ms"] = rtts
	}
	switch {
	case received == 0:
		res.Reason = "no reply from " + ip.String()
	case loss >= i.LossThreshold:
		res.Reason = fmt.Sprintf("%d of %d packets lost", sent-received, sent)
	default:
		res.OK = true
	}
	return res
}
