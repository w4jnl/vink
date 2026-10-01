package importer

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/w4jnl/vink/internal/apply"
	"github.com/w4jnl/vink/internal/domain"
)

// kumaBackup is the JSON Uptime Kuma 1.x writes from Settings → Backup.
type kumaBackup struct {
	Version          string             `json:"version"`
	MonitorList      []kumaMonitor      `json:"monitorList"`
	NotificationList []kumaNotification `json:"notificationList"`
}

type kumaMonitor struct {
	ID                 int             `json:"id"`
	Name               string          `json:"name"`
	Type               string          `json:"type"`
	URL                string          `json:"url"`
	Method             string          `json:"method"`
	Hostname           string          `json:"hostname"`
	Port               int             `json:"port"`
	Interval           float64         `json:"interval"`
	RetryInterval      float64         `json:"retryInterval"`
	Timeout            float64         `json:"timeout"`
	MaxRetries         int             `json:"maxretries"`
	Active             *bool           `json:"active"`
	Keyword            string          `json:"keyword"`
	InvertKeyword      bool            `json:"invertKeyword"`
	JSONPath           string          `json:"jsonPath"`
	ExpectedValue      string          `json:"expectedValue"`
	IgnoreTLS          bool            `json:"ignoreTls"`
	UpsideDown         bool            `json:"upsideDown"`
	MaxRedirects       *int            `json:"maxredirects"`
	AcceptedStatus     []string        `json:"accepted_statuscodes"`
	DNSResolveType     string          `json:"dns_resolve_type"`
	DNSResolveServer   string          `json:"dns_resolve_server"`
	Headers            string          `json:"headers"`
	Body               string          `json:"body"`
	BasicAuthUser      string          `json:"basic_auth_user"`
	BasicAuthPass      string          `json:"basic_auth_pass"`
	ExpiryNotification bool            `json:"expiryNotification"`
	Tags               []kumaTag       `json:"tags"`
	NotificationIDList json.RawMessage `json:"notificationIDList"`
}

type kumaTag struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type kumaNotification struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Config string `json:"config"`
	Active *bool  `json:"active"`
}

// Kuma converts an Uptime Kuma backup: http, keyword, json-query, port,
// ping, dns and push monitors, and the notifications vink has a channel
// kind for. Routes are not imported: vink's first channel gets a route
// that sends every down and up.
func Kuma(data []byte) (*Result, error) {
	var b kumaBackup
	if err := json.Unmarshal(data, &b); err != nil || b.MonitorList == nil {
		return nil, errors.New("not an Uptime Kuma backup: want the JSON from Settings → Backup → Export")
	}
	r := &Result{File: &apply.File{Version: 1}}
	for _, n := range b.NotificationList {
		r.kumaChannel(n)
	}
	taken := slugs{}
	for _, m := range b.MonitorList {
		r.kumaMonitor(m, taken)
	}
	if len(r.File.Monitors) == 0 && len(r.File.Channels) == 0 {
		return r, errors.New("nothing to import; every monitor was skipped")
	}
	return r, nil
}

func (r *Result) kumaMonitor(k kumaMonitor, taken slugs) {
	slug := taken.take(k.Name, "monitor-"+strconv.Itoa(k.ID))
	what := describe(k.Name, slug)
	m := apply.Monitor{Slug: slug, Name: k.Name}
	for _, t := range k.Tags {
		tag := t.Name
		if t.Value != "" {
			tag += "-" + t.Value
		}
		m.Tags = append(m.Tags, tag)
	}
	m.Tags = domain.NormalizeTags(m.Tags)
	pull := func(kind domain.Kind) {
		m.Kind = kind
		m.Interval = seconds(k.Interval, domain.MinInterval.Std())
		if k.Timeout > 0 {
			m.Timeout = seconds(k.Timeout, time.Second)
			if m.Timeout >= m.Interval {
				m.Timeout = m.Interval / 2
			}
		}
		if k.MaxRetries > 0 {
			retries := k.MaxRetries
			if retries > domain.MaxConfirmRetries {
				retries = domain.MaxConfirmRetries
				r.note(what, fmt.Sprintf("retries capped at %d", retries))
			}
			delay := seconds(k.RetryInterval, time.Second)
			if delay > domain.MaxConfirmDelay {
				delay = domain.MaxConfirmDelay
			}
			m.Confirm = &domain.Confirm{Retries: retries, Delay: delay}
		}
	}
	switch k.Type {
	case "http", "keyword", "json-query":
		pull(domain.KindHTTP)
		h := &domain.HTTPCheck{URL: k.URL, Method: strings.ToUpper(k.Method), Body: k.Body}
		if k.Headers != "" {
			var headers map[string]any
			if err := json.Unmarshal([]byte(k.Headers), &headers); err != nil {
				r.note(what, "headers were not JSON and were left out")
			} else {
				h.Headers = map[string]string{}
				for name, v := range headers {
					h.Headers[name] = fmt.Sprint(v)
				}
			}
		}
		if k.BasicAuthUser != "" {
			if h.Headers == nil {
				h.Headers = map[string]string{}
			}
			h.Headers["Authorization"] = "Basic " + base64.StdEncoding.EncodeToString([]byte(k.BasicAuthUser+":"+k.BasicAuthPass))
			r.note(what, "basic auth became an Authorization header; the file now holds that secret")
		}
		for _, s := range k.AcceptedStatus {
			rg, err := domain.ParseStatusRange(s)
			if err != nil {
				r.note(what, "accepted status "+s+" was not understood and was left out")
				continue
			}
			h.ExpectStatus = append(h.ExpectStatus, rg)
		}
		if k.MaxRedirects != nil && *k.MaxRedirects == 0 {
			no := false
			h.FollowRedirects = &no
		}
		if k.IgnoreTLS {
			no := false
			h.VerifyTLS = &no
		}
		switch k.Type {
		case "keyword":
			if k.InvertKeyword {
				h.ExpectBody = &domain.ExpectBody{NotContains: k.Keyword}
			} else {
				h.ExpectBody = &domain.ExpectBody{Contains: k.Keyword}
			}
		case "json-query":
			path := strings.TrimSpace(k.JSONPath)
			if !strings.HasPrefix(path, "$") {
				path = "$." + path
			}
			h.ExpectBody = &domain.ExpectBody{JSONPath: &domain.JSONPathExpect{Path: path, Equals: k.ExpectedValue}}
			r.note(what, "the JSON query compares as a string; adjust equals if the value is a number or a boolean")
		}
		m.HTTP = h
		if k.ExpiryNotification {
			r.note(what, "certificate expiry alerts need a tls monitor on the host; add one")
		}
	case "port":
		pull(domain.KindTCP)
		m.TCP = &domain.TCPCheck{Host: k.Hostname, Port: k.Port}
	case "ping":
		pull(domain.KindICMP)
		m.ICMP = &domain.ICMPCheck{Host: k.Hostname}
	case "dns":
		pull(domain.KindDNS)
		d := &domain.DNSCheck{Name: k.Hostname, Type: strings.ToUpper(k.DNSResolveType)}
		if k.DNSResolveServer != "" {
			port := k.Port
			if port == 0 {
				port = 53
			}
			d.Resolver = k.DNSResolveServer + ":" + strconv.Itoa(port)
		}
		m.DNS = d
	case "push":
		m.Kind = domain.KindHeartbeat
		m.Schedule = &domain.Schedule{Period: seconds(k.Interval, time.Minute)}
		grace := seconds(k.RetryInterval*float64(k.MaxRetries), time.Minute)
		m.Grace = grace
		r.note(what, "a push monitor becomes a heartbeat with a new ping URL; point the job at it")
	default:
		r.skip(what, "vink has no kind for a "+k.Type+" monitor")
		return
	}
	if k.UpsideDown {
		r.note(what, "upside down mode does not exist in vink; the monitor is imported the normal way round")
	}
	if k.Active != nil && !*k.Active {
		r.note(what, "paused in Kuma; vink imports it running, pause it after the apply")
	}
	r.File.Monitors = append(r.File.Monitors, m)
}

// kumaChannel maps a notification to a channel when vink has the kind.
func (r *Result) kumaChannel(n kumaNotification) {
	var cfg map[string]any
	if err := json.Unmarshal([]byte(n.Config), &cfg); err != nil {
		r.skip("notification "+n.Name, "config was not JSON")
		return
	}
	str := func(k string) string { v, _ := cfg[k].(string); return strings.TrimSpace(v) }
	typ := str("type")
	name := n.Name
	if name == "" {
		name = str("name")
	}
	if name == "" {
		name = typ + "-" + strconv.Itoa(n.ID)
	}
	ch := apply.Channel{Name: name, Config: map[string]any{}}
	switch typ {
	case "gotify":
		ch.Kind = "gotify"
		ch.Config["url"], ch.Config["token"] = str("gotifyserverurl"), str("gotifyapplicationToken")
		if p, ok := cfg["gotifyPriority"].(float64); ok && p > 0 {
			ch.Config["priority"] = int(p)
		}
	case "slack":
		ch.Kind = "slackhook"
		ch.Config["url"] = str("slackwebhookURL")
	case "webhook":
		ch.Kind = "webhook"
		ch.Config["url"] = str("webhookURL")
	case "matrix":
		ch.Kind = "matrix"
		ch.Config["homeserver"], ch.Config["room_id"], ch.Config["access_token"] = str("homeserverUrl"), str("internalRoomId"), str("accessToken")
	case "smtp":
		ch.Kind = "smtp"
		to := []string{}
		for _, a := range strings.Split(str("smtpTo"), ",") {
			if a = strings.TrimSpace(a); a != "" {
				to = append(to, a)
			}
		}
		ch.Config["to"] = to
		r.note("notification "+name, "the SMTP relay itself is instance config in vink.toml")
	default:
		r.skip("notification "+name, "vink has no channel kind for "+typ)
		return
	}
	if n.Active != nil && !*n.Active {
		off := false
		ch.Enabled = &off
	}
	r.File.Channels = append(r.File.Channels, ch)
}
