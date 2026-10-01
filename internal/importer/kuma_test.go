package importer

import (
	"strings"
	"testing"

	"github.com/w4jnl/vink/internal/apply"
	"github.com/w4jnl/vink/internal/domain"
)

const kumaSample = `{"version":"1.23.3","notificationList":[
 {"id":1,"name":"Phone","config":"{\"name\":\"Phone\",\"type\":\"gotify\",\"gotifyserverurl\":\"https://gotify.lan\",\"gotifyapplicationToken\":\"AbC\",\"gotifyPriority\":5}","active":true},
 {"id":2,"name":"Chat","config":"{\"type\":\"slack\",\"slackwebhookURL\":\"https://hooks.slack.com/x\"}","active":false},
 {"id":3,"name":"Mail","config":"{\"type\":\"smtp\",\"smtpTo\":\"a@example.com, b@example.com\"}"},
 {"id":4,"name":"Tg","config":"{\"type\":\"telegram\"}"}
],"monitorList":[
 {"id":1,"name":"Home page","type":"http","url":"https://example.com/","method":"GET","interval":60,"retryInterval":30,"maxretries":2,"active":true,"accepted_statuscodes":["200-299","301"],"maxredirects":0,"ignoreTls":true,"headers":"{\"Accept\":\"text/html\"}","tags":[{"name":"prod","value":""},{"name":"site","value":"Web"}]},
 {"id":2,"name":"API says ok","type":"keyword","url":"https://api.example.com/health","keyword":"ok","interval":20,"maxretries":0,"basic_auth_user":"u","basic_auth_pass":"p"},
 {"id":3,"name":"Status JSON","type":"json-query","url":"https://api.example.com/status","jsonPath":"status","expectedValue":"green","interval":120,"timeout":48},
 {"id":4,"name":"LDAP","type":"port","hostname":"ldap.lan","port":389,"interval":60,"maxretries":9,"retryInterval":120},
 {"id":5,"name":"Router","type":"ping","hostname":"10.0.0.1","interval":30,"upsideDown":true},
 {"id":6,"name":"Zone","type":"dns","hostname":"example.com","dns_resolve_type":"a","dns_resolve_server":"1.1.1.1","port":53,"interval":300},
 {"id":7,"name":"Backup job","type":"push","interval":86400,"retryInterval":60,"maxretries":5,"active":false},
 {"id":8,"name":"Docker","type":"docker","interval":60},
 {"id":9,"name":"Home page","type":"http","url":"https://example.org/","interval":5}
]}`

func TestKuma(t *testing.T) {
	r, err := Kuma([]byte(kumaSample))
	if err != nil {
		t.Fatal(err)
	}
	f := r.File
	if len(f.Monitors) != 8 || len(f.Channels) != 3 {
		t.Fatalf("monitors %d channels %d", len(f.Monitors), len(f.Channels))
	}
	by := map[string]apply.Monitor{}
	for _, m := range f.Monitors {
		by[m.Slug] = m
	}
	home := by["home-page"]
	if home.Kind != domain.KindHTTP || home.HTTP.URL != "https://example.com/" || home.Interval != domain.MustDuration("1m") || home.Confirm == nil || home.Confirm.Retries != 2 || home.Confirm.Delay != domain.MustDuration("30s") ||
		len(home.HTTP.ExpectStatus) != 2 || home.HTTP.ExpectStatus[1].Lo != 301 || home.HTTP.FollowRedirects == nil || *home.HTTP.FollowRedirects || home.HTTP.VerifyTLS == nil || *home.HTTP.VerifyTLS ||
		home.HTTP.Headers["Accept"] != "text/html" || strings.Join(home.Tags, ",") != "prod,site-web" {
		t.Errorf("http: %+v %+v", home, home.HTTP)
	}
	kw := by["api-says-ok"]
	if kw.HTTP.ExpectBody == nil || kw.HTTP.ExpectBody.Contains != "ok" || kw.Interval != domain.MustDuration("20s") || kw.Confirm != nil || !strings.HasPrefix(kw.HTTP.Headers["Authorization"], "Basic dTpw") {
		t.Errorf("keyword: %+v %+v", kw, kw.HTTP)
	}
	jq := by["status-json"]
	if jq.HTTP.ExpectBody == nil || jq.HTTP.ExpectBody.JSONPath == nil || jq.HTTP.ExpectBody.JSONPath.Path != "$.status" || jq.HTTP.ExpectBody.JSONPath.Equals != "green" || jq.Timeout != domain.MustDuration("48s") {
		t.Errorf("json-query: %+v", jq.HTTP.ExpectBody)
	}
	ldap := by["ldap"]
	if ldap.Kind != domain.KindTCP || ldap.TCP.Host != "ldap.lan" || ldap.TCP.Port != 389 || ldap.Confirm.Retries != 5 || ldap.Confirm.Delay != domain.MustDuration("60s") {
		t.Errorf("tcp: %+v %+v", ldap, ldap.TCP)
	}
	if r := by["router"]; r.Kind != domain.KindICMP || r.ICMP.Host != "10.0.0.1" {
		t.Errorf("icmp: %+v", r)
	}
	if z := by["zone"]; z.Kind != domain.KindDNS || z.DNS.Name != "example.com" || z.DNS.Type != "A" || z.DNS.Resolver != "1.1.1.1:53" {
		t.Errorf("dns: %+v", z.DNS)
	}
	if p := by["backup-job"]; p.Kind != domain.KindHeartbeat || p.Schedule.Period != domain.MustDuration("1d") || p.Grace != domain.MustDuration("5m") {
		t.Errorf("push: %+v", p)
	}
	if d := by["home-page-2"]; d.Interval != domain.MinInterval {
		t.Errorf("duplicate name and interval floor: %+v", d)
	}
	ch := map[string]apply.Channel{}
	for _, c := range f.Channels {
		ch[c.Name] = c
	}
	if ch["Phone"].Kind != "gotify" || ch["Phone"].Config["url"] != "https://gotify.lan" || ch["Phone"].Config["token"] != "AbC" || ch["Phone"].Config["priority"] != 5 {
		t.Errorf("gotify: %+v", ch["Phone"])
	}
	if ch["Chat"].Kind != "slackhook" || ch["Chat"].Enabled == nil || *ch["Chat"].Enabled {
		t.Errorf("slack: %+v", ch["Chat"])
	}
	if to, _ := ch["Mail"].Config["to"].([]string); ch["Mail"].Kind != "smtp" || len(to) != 2 {
		t.Errorf("smtp: %+v", ch["Mail"])
	}
	joined := strings.Join(r.Skipped, "\n")
	if len(r.Skipped) != 2 || !strings.Contains(joined, "telegram") || !strings.Contains(joined, "docker") {
		t.Errorf("skipped: %v", r.Skipped)
	}
	notes := strings.Join(r.Notes, "\n")
	for _, want := range []string{"basic auth", "JSON query", "retries capped", "upside down", "push monitor", "paused in Kuma", "SMTP relay"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes lack %q: %v", want, r.Notes)
		}
	}
	out, err := apply.Encode(f)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := apply.Parse(out, true); err != nil {
		t.Fatalf("apply file invalid: %v\n%s", err, out)
	}
	for _, bad := range []string{`{}`, `nope`, `{"monitorList":[{"name":"x","type":"docker"}]}`} {
		if _, err := Kuma([]byte(bad)); err == nil {
			t.Errorf("%s must fail", bad)
		}
	}
}
