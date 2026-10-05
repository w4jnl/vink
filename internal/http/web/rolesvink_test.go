package web

import (
	"context"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/w4jnl/vink/internal/auth"
	"github.com/w4jnl/vink/internal/auth/oidctest"
	"github.com/w4jnl/vink/internal/config"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/service"
)

// proxyAuth is a proxy config beside local accounts, so the test's own
// session (j, an org admin of homelab) keeps working.
func proxyAuth(roles string, admins ...string) config.Auth {
	cfg := config.Default().Auth
	cfg.Proxy.Enabled = true
	cfg.Proxy.TrustedCIDRs = []string{"203.0.113.0/24"}
	cfg.Proxy.UserHeader = "X-User"
	cfg.Proxy.GroupsHeader = "X-Groups"
	cfg.Proxy.SecretHeader = "X-Proxy-Secret"
	cfg.Proxy.Secret = "s3cret"
	cfg.Proxy.StripRealm = true
	cfg.Proxy.Lowercase = true
	cfg.Proxy.Roles = roles
	cfg.Proxy.InstanceAdmins = admins
	return cfg
}

// proxyGet asks as a person the proxy signed in.
func (e *env) proxyGet(path, user, groups string) page {
	e.t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	req.RemoteAddr = "203.0.113.9:1"
	req.Header.Set("X-Proxy-Secret", "s3cret")
	req.Header.Set("X-User", user)
	req.Header.Set("X-Groups", groups)
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	return page{code: rec.Code, body: rec.Body.String(), hdr: rec.Header()}
}

// TestNoAccessByRoleSource: the no-access page says what is missing for
// the person's provider and where their roles come from.
func TestNoAccessByRoleSource(t *testing.T) {
	t.Run("proxy, groups, custom pattern and map", func(t *testing.T) {
		cfg := proxyAuth(config.RolesGroups)
		cfg.Proxy.GroupPattern = `^app-vink-(?P<org>[a-z0-9-]+)-(?P<role>owner|admin|member|viewer)$`
		cfg.Proxy.GroupMap = map[string]string{"cn=ops": "homelab:viewer"}
		e := newEnvAuth(t, cfg)
		p := e.proxyGet("/", "stranger", "staff")
		if p.code != 403 {
			t.Fatalf("%d", p.code)
		}
		p.has(t, "The proxy signed you in as stranger, but none of your groups gives access", "<dt>groups</dt><dd>staff</dd>",
			"<dt>needs</dt><dd>a group matching ^app-vink-", ", or a group in auth.proxy.group_map</dd>")
	})
	t.Run("proxy, vink", func(t *testing.T) {
		e := newEnvAuth(t, proxyAuth(config.RolesVink))
		p := e.proxyGet("/", "Stranger@CORP.EXAMPLE", "staff")
		if p.code != 403 {
			t.Fatalf("%d", p.code)
		}
		p.has(t, "The proxy signed you in as stranger, but no org has added you yet.", "Ask an org admin to add stranger on its Members tab, then reload.")
		if strings.Contains(p.body, "<dt>groups</dt>") || strings.Contains(p.body, "<dt>needs</dt>") {
			t.Error("groups do not matter while roles are set in vink")
		}
	})
	for _, roles := range []string{config.RolesGroups, config.RolesVink} {
		t.Run("oidc, "+roles, func(t *testing.T) {
			p := oidctest.New(t)
			p.Claims = map[string]any{"preferred_username": "alice", "groups": []string{"staff"}}
			cfg := config.Default().Auth
			p.Configure(&cfg)
			cfg.OIDC.Roles = roles
			e := newEnvAuth(t, cfg)
			st := e.doWith("GET", "/auth/oidc/start?next=/", nil)
			flight := cookieNamed(st.hdr, auth.OIDCCookie)
			done := e.doWith("GET", p.Visit(t, st.hdr.Get("Location")), nil, flight)
			sess := cookieNamed(done.hdr, auth.CookieName)
			if sess == nil {
				t.Fatalf("callback: %d %s", done.code, done.hdr.Get("Location"))
			}
			page := e.doWith("GET", "/", nil, sess)
			if page.code != 403 {
				t.Fatalf("%d", page.code)
			}
			if roles == config.RolesGroups {
				page.has(t, "You signed in with Keycloak as alice, but none of your groups gives access", "<dt>needs</dt><dd>vink:&lt;org&gt;:&lt;role&gt;", "sign out and in again after the change", ">Sign out<")
			} else {
				page.has(t, "You signed in with Keycloak as alice, but no org has added you yet.", "Ask an org admin to add alice on its Members tab")
			}
		})
	}
}

// TestAddMemberByName: with the proxy's roles set in vink, org admins add
// people by sign-in name, and the rows are theirs to change.
func TestAddMemberByName(t *testing.T) {
	e := newEnvAuth(t, proxyAuth(config.RolesVink))
	root := "/o/homelab/admin/members"
	tab := e.get(root, false)
	tab.has(t, `<a class="vk-btn" href="/o/homelab/admin/members?add=1">Add member</a>`)
	if strings.Contains(tab.body, `?invite=1">Invite</a>`) {
		t.Error("Invite is the tab-head button in vink mode")
	}
	e.get(root+"?add=1", false).has(t, `Add member</h2>`, `Sign-in name`, `Exactly as the proxy sends it, like jdoe.`, `<a class="vk-link" href="/o/homelab/admin/members?invite=1">Invite a local account</a>`,
		`A name vink has not seen yet becomes an account`)
	e.get(root+"?invite=1", false).has(t, `add them by sign-in name instead.`)

	r := e.post(root+"/add", url.Values{"add_name": {"JDoe@CORP.EXAMPLE"}, "add_role": {"member"}}, false)
	if r.code != 303 || !strings.Contains(r.hdr.Get("Location"), "flash=jdoe+added+as+member.") {
		t.Fatalf("add: %d %s", r.code, r.hdr.Get("Location"))
	}
	u, err := e.svc.UserBySubject(context.Background(), "jdoe")
	if err != nil || u.Source != "proxy" {
		t.Fatalf("account: %+v %v", u, err)
	}
	list := e.get(root, false)
	list.has(t, `jdoe · proxy account`)
	if sel := regexpFind(list.body, `<select[^>]*aria-label="Role for jdoe"[^>]*>`); sel == "" || strings.Contains(sel, "disabled") {
		t.Errorf("jdoe's role is not editable: %q", sel)
	}
	for _, tc := range []struct{ name, role, want string }{
		{"jdoe", "viewer", "Already a member here"},
		{"bob", "owner", "Only an owner can make someone an owner."},
		{"j", "member", "J is a local account here"},
		{"", "member", "required"},
	} {
		r := e.post(root+"/add", url.Values{"add_name": {tc.name}, "add_role": {tc.role}}, false)
		if r.code != 422 || !strings.Contains(strings.ToLower(r.body), strings.ToLower(tc.want)) {
			t.Errorf("%s as %s: %d, want %q", tc.name, tc.role, r.code, tc.want)
		}
	}
	// the person's first visit finds the role waiting
	if p := e.proxyGet("/o/homelab/p/prod", "jdoe", ""); p.code != 200 {
		t.Fatalf("jdoe's first visit: %d", p.code)
	}
}

// TestAddMemberNeedsVinkMode: while the groups decide, there is no Add
// member and the route refuses.
func TestAddMemberNeedsVinkMode(t *testing.T) {
	e := newEnvAuth(t, proxyAuth(config.RolesGroups))
	root := "/o/homelab/admin/members"
	tab := e.get(root+"?add=1", false)
	if strings.Contains(tab.body, "Add member") || !strings.Contains(tab.body, `?invite=1">Invite</a>`) {
		t.Errorf("groups mode offers Add member:\n%s", tab.body)
	}
	if r := e.post(root+"/add", url.Values{"add_name": {"jdoe"}, "add_role": {"member"}}, false); r.code != 422 || !strings.Contains(r.body, "auth.proxy.roles or auth.oidc.roles set to vink") {
		t.Fatalf("add in groups mode: %d", r.code)
	}
}

// TestInstanceAdminCheckboxLocks: the Users tab locks the flag only while
// the groups decide it, while the name is listed, or for yourself.
func TestInstanceAdminCheckboxLocks(t *testing.T) {
	ctx := context.Background()
	admin := domain.Scope{InstanceAdmin: true, Role: domain.RoleOwner, Actor: "test"}
	for _, tc := range []struct {
		name, roles, user string
		admins            []string
		locked            string
	}{
		{"groups", config.RolesGroups, "bob", nil, "Follows auth.proxy.instance_admin_group, since auth.proxy.roles is groups."},
		{"vink", config.RolesVink, "bob", nil, ""},
		{"vink, listed", config.RolesVink, "carol", []string{"carol"}, "Listed in auth.proxy.instance_admins in vink.toml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnvAuth(t, proxyAuth(tc.roles, tc.admins...))
			if err := e.svc.SetInstanceAdmin(ctx, admin, "j", true); err != nil {
				t.Fatal(err)
			}
			// the first visit makes the account; in vink mode nobody has
			// given bob a role yet, so it ends on the no-access page
			if p := e.proxyGet("/", tc.user, "vink:homelab:viewer"); p.code != 200 && p.code != 303 && p.code != 403 {
				t.Fatalf("first visit: %d", p.code)
			}
			u, err := e.svc.UserBySubject(ctx, tc.user)
			if err != nil {
				t.Fatal(err)
			}
			panel := e.get("/admin/users?edit="+u.ID, false)
			box := regexpFind(panel.body, `<input type="checkbox" name="is_admin"[^>]*>`)
			if (tc.locked != "") != strings.Contains(box, "disabled") {
				t.Fatalf("checkbox %s", box)
			}
			if tc.locked != "" {
				panel.has(t, tc.locked)
			}
			self := e.get("/admin/users?edit="+e.scope.UserID, false)
			self.has(t, "You cannot take instance admin from yourself.")
		})
	}
}

// TestAuditTextForAdminKeysAndRoles: the audit log says what happened to
// admin keys and role sources in words.
func TestAuditTextForAdminKeysAndRoles(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		e    service.AuditEntry
		want string
	}{
		{service.AuditEntry{Action: "adminkey.create", Target: "laptop", Detail: map[string]any{"access": "rw", "expires_at": "2027-01-03T12:00:00Z"}}, "created admin key <code>laptop</code> (rw, expires 3 Jan 2027)"},
		{service.AuditEntry{Action: "adminkey.revoke", Target: "laptop", Detail: map[string]any{"reason": "creator was disabled"}}, "revoked admin key <code>laptop</code>: creator was disabled"},
		{service.AuditEntry{Action: "adminkey.revoke", Target: "laptop"}, "revoked admin key <code>laptop</code>"},
		{service.AuditEntry{Action: "auth.roles", Target: "auth.proxy", Detail: map[string]any{"from": "groups", "to": "vink", "memberships": 3.0}}, "set the roles of <code>auth.proxy</code> in vink from now on; 3 roles from groups became ordinary ones"},
		{service.AuditEntry{Action: "auth.roles", Target: "auth.oidc", Detail: map[string]any{"from": "vink", "to": "groups", "memberships_set_in_vink": 1.0}}, "let the groups of <code>auth.oidc</code> decide roles again; 1 role set in vink stays and overrides them"},
		{service.AuditEntry{Action: "user.instance_admin", Target: "jdoe", Detail: map[string]any{"admin": true, "source": "config"}}, "made jdoe instance admin, listed in instance_admins"},
		{service.AuditEntry{Action: "user.instance_admin", Target: "jdoe", Detail: map[string]any{"admin": false, "source": "group"}}, "took instance admin from jdoe, by the provider’s groups"},
	} {
		if got := string(auditText(tc.e, now)); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.e.Action, got, tc.want)
		}
	}
}

func regexpFind(s, pattern string) string {
	return regexp.MustCompile(pattern).FindString(s)
}
