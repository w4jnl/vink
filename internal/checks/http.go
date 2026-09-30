package checks

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/domain"
)

// HTTP requests a URL and judges the status and the body.
type HTTP struct{}

func (HTTP) Kind() domain.Kind { return domain.KindHTTP }

func (HTTP) Check(ctx context.Context, spec *domain.PullSpec, env Env) Result {
	h := spec.HTTP
	if h == nil {
		return fail("no http block")
	}
	tr := env.Out.Transport
	if !h.Verify() || h.CAPem != "" {
		tr = tr.Clone()
		cfg := tr.TLSClientConfig.Clone()
		if !h.Verify() {
			// verify_tls: false is the spec's explicit choice; the UI flags it.
			cfg.InsecureSkipVerify = true
		}
		if h.CAPem != "" {
			pool := env.Out.RootCAs
			if pool == nil {
				if sys, err := x509.SystemCertPool(); err == nil && sys != nil {
					pool = sys
				}
			}
			if pool == nil {
				pool = x509.NewCertPool()
			} else {
				pool = pool.Clone()
			}
			pool.AppendCertsFromPEM([]byte(h.CAPem))
			cfg.RootCAs = pool
		}
		tr.TLSClientConfig = cfg
		defer tr.CloseIdleConnections()
	}
	client := &http.Client{Transport: tr}
	if h.Redirects() {
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("more than 10 redirects")
			}
			return nil
		}
	} else {
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	var body io.Reader
	if h.Body != "" {
		body = strings.NewReader(h.Body)
	}
	req, err := http.NewRequestWithContext(ctx, h.Method, h.URL, body)
	if err != nil {
		return fail(err.Error())
	}
	req.Header.Set("User-Agent", env.UserAgent)
	for k, v := range h.Headers {
		req.Header.Set(k, v)
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return fail(reasonFor(err, spec.Timeout.Std()))
	}
	defer func() { _ = resp.Body.Close() }()
	res := Result{LatencyMs: ms(start), Detail: map[string]any{"status": resp.StatusCode}}
	if resp.Request != nil && resp.Request.URL.String() != h.URL {
		res.Detail["final_url"] = resp.Request.URL.String()
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		res.Detail["content_type"] = ct
	}
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		res.Detail["tls_days_left"] = daysUntil(resp.TLS.PeerCertificates[0].NotAfter, env.Now())
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, domain.MaxCheckBody))
	if err != nil {
		res.Reason = "reading body: " + reasonFor(err, spec.Timeout.Std())
		return res
	}
	res.Detail["bytes"] = len(data)
	statusOK := false
	for _, r := range h.ExpectStatus {
		statusOK = statusOK || r.Contains(resp.StatusCode)
	}
	if !statusOK {
		res.Reason = strconv.Itoa(resp.StatusCode) + " " + http.StatusText(resp.StatusCode)
		if want := statusWant(h.ExpectStatus); want != "" {
			res.Reason += ", want " + want
		}
		return res
	}
	if eb := h.ExpectBody; eb != nil {
		if reason := checkBody(data, eb); reason != "" {
			res.Reason = reason
			return res
		}
		res.Detail["matched"] = true
	}
	res.OK = true
	return res
}

func statusWant(ranges []domain.StatusRange) string {
	if len(ranges) == 1 && ranges[0].Lo == 200 && ranges[0].Hi == 299 {
		return ""
	}
	parts := make([]string, 0, len(ranges))
	for _, r := range ranges {
		parts = append(parts, r.String())
	}
	return strings.Join(parts, " or ")
}

// checkBody applies the body expectations and returns the first reason
// that fails, or "".
func checkBody(data []byte, eb *domain.ExpectBody) string {
	text := string(data)
	if eb.Contains != "" && !strings.Contains(text, eb.Contains) {
		return fmt.Sprintf("body lacks %q", eb.Contains)
	}
	if eb.NotContains != "" && strings.Contains(text, eb.NotContains) {
		return fmt.Sprintf("body contains %q", eb.NotContains)
	}
	if jp := eb.JSONPath; jp != nil {
		var doc any
		if err := json.Unmarshal(data, &doc); err != nil {
			return "body is not JSON"
		}
		v, found, err := jsonPath(doc, jp.Path)
		if err != nil {
			return err.Error()
		}
		if !found {
			return jp.Path + " not found"
		}
		if fmt.Sprint(v) != fmt.Sprint(jp.Equals) {
			return fmt.Sprintf("%s is %s, want %s", jp.Path, quote(v), quote(jp.Equals))
		}
	}
	return ""
}

// daysUntil floors the days between now and t; negative once past.
func daysUntil(t, now time.Time) int {
	h := t.Sub(now).Hours()
	d := int(h / 24)
	if h < 0 && float64(d)*24 != h {
		d--
	}
	return d
}
