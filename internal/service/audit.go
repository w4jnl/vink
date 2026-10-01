package service

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/w4jnl/vink/internal/apply"
	"github.com/w4jnl/vink/internal/audit"
	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
)

// record writes one audit row through q, the transaction of the action,
// so a rolled-back change leaves no trace and a committed one always has one.
func (s *Service) record(ctx context.Context, q *db.Queries, sc domain.Scope, e audit.Entry) error {
	if q == nil {
		return errNoTx
	}
	if s.noAudit {
		return nil
	}
	req := audit.RequestFrom(ctx)
	actor, kind, actorID := actorOf(sc)
	via := req.Via
	if kind == audit.KindKey && via == audit.ViaAPI {
		via = "api vk_" + actor
	}
	detail := "{}"
	if len(e.Detail) > 0 {
		b, err := json.Marshal(e.Detail)
		if err != nil {
			return err
		}
		detail = string(b)
	}
	return q.InsertAudit(ctx, db.InsertAuditParams{
		ID: domain.NewID(), At: domain.Millis(s.now()), Actor: actor, ActorKind: kind, ActorID: ptrs(actorID),
		OrgID: ptrs(e.OrgID), ProjectID: ptrs(e.ProjectID), Act: e.Action, Target: e.Target, TargetID: ptrs(e.TargetID),
		SpecBefore: ptrs(e.Before), SpecAfter: ptrs(e.After), Detail: detail, Via: via, RequestID: ptrs(req.RequestID), RemoteAddr: ptrs(req.RemoteAddr),
	})
}

// actorOf names the caller the way the log shows it: the subject for a
// person, the key prefix for an API key, the command for the server host.
func actorOf(sc domain.Scope) (actor, kind, id string) {
	switch {
	case sc.IsKey():
		return strings.TrimPrefix(sc.Actor, "key:"), audit.KindKey, sc.KeyID
	case sc.UserID != "":
		return strings.TrimPrefix(sc.Actor, "user:"), audit.KindUser, sc.UserID
	case strings.HasPrefix(sc.Actor, "cli:"):
		return "vink admin", audit.KindUser, ""
	case sc.Actor == "":
		return "vink", audit.KindSystem, ""
	}
	return strings.TrimPrefix(sc.Actor, "user:"), audit.KindUser, ""
}

// projectEntry is an action inside the scope's project.
func projectEntry(sc domain.Scope, action, target, targetID string) audit.Entry {
	return audit.Entry{Action: action, Target: target, TargetID: targetID, OrgID: sc.OrgID, ProjectID: sc.ProjectID}
}

// orgEntry is an action at org level, without a project.
func orgEntry(orgID, action, target, targetID string) audit.Entry {
	return audit.Entry{Action: action, Target: target, TargetID: targetID, OrgID: orgID}
}

// yamlOf renders a snapshot; a failure to render is an empty snapshot,
// never a failed action.
func yamlOf(v any) string {
	b, err := yaml.Marshal(v)
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(b), "\n")
}

// changedFields lists the top-level keys whose value differs between two
// YAML snapshots, for "changed monitor x: interval, confirm".
func changedFields(before, after string) []string {
	var a, b map[string]any
	_ = yaml.Unmarshal([]byte(before), &a)
	_ = yaml.Unmarshal([]byte(after), &b)
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	var out []string
	for k := range keys {
		if !reflect.DeepEqual(a[k], b[k]) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Snapshots of the stored specs, secrets as ***.

func channelSnapshot(c *domain.Channel) string {
	m := map[string]any{}
	_ = json.Unmarshal(RedactConfig(c.Kind, c.Config), &m)
	m["name"], m["kind"] = c.Name, string(c.Kind)
	if !c.Enabled {
		m["enabled"] = false
	}
	return yamlOf(m)
}

func routeSnapshot(r *domain.Route) string {
	return yamlOf(apply.Route{MatchTags: r.MatchTags, Channels: r.ChannelNames(), On: r.On, RepeatEvery: domain.Duration(r.RepeatEvery), Priority: r.Priority})
}

func maintenanceSnapshot(w *domain.Maintenance) string { return yamlOf(apply.MaintenanceFrom(w)) }

func pageSnapshot(p *domain.StatusPage) string {
	sp := apply.StatusPage{Slug: p.Slug, Title: p.Title, MatchTags: p.MatchTags, CustomDomain: p.CustomDomain}
	if !p.Public {
		private := false
		sp.Public = &private
	}
	return yamlOf(sp)
}

func projectSnapshot(p *domain.Project) string {
	return yamlOf(map[string]any{"slug": p.Slug, "name": p.Name, "timezone": p.Timezone})
}

func agentSnapshot(a *domain.Agent) string {
	return yamlOf(map[string]any{"name": a.Name, "labels": a.Labels})
}

func orgSnapshot(o *domain.Org) string {
	m := map[string]any{"slug": o.Slug, "name": o.Name}
	if o.QuotaMonitors != nil {
		m["quota_monitors"] = *o.QuotaMonitors
	}
	if o.QuotaAgents != nil {
		m["quota_agents"] = *o.QuotaAgents
	}
	return yamlOf(m)
}

// MetaLastBackup is the instance_meta name vink admin backup writes.
const MetaLastBackup = "last_backup_at"

// RecordSignIn writes a sign-in (method password, totp, recovery, oidc)
// or a failed one, which carries the subject tried and no user.
func (s *Service) RecordSignIn(ctx context.Context, u *domain.User, subject, method string, ok bool) error {
	sc := domain.Scope{Actor: "user:" + subject}
	action := "user.signin"
	if u != nil {
		sc.UserID = u.ID
	}
	if !ok {
		action = "user.signin_failed"
	}
	return s.db.Tx(ctx, func(q *db.Queries) error {
		return s.record(ctx, q, sc, audit.Entry{Action: action, Target: subject, Detail: map[string]any{"method": method}})
	})
}

// RecordSignOut writes a sign-out.
func (s *Service) RecordSignOut(ctx context.Context, u *domain.User) error {
	sc := domain.Scope{UserID: u.ID, Actor: "user:" + u.Subject}
	return s.db.Tx(ctx, func(q *db.Queries) error {
		return s.record(ctx, q, sc, audit.Entry{Action: "user.signout", Target: u.Subject, TargetID: u.ID})
	})
}

// AuditFilter narrows the log. Zero means everything the scope may see
// over the last seven days.
type AuditFilter struct {
	OrgID     string
	ProjectID string
	Actor     string
	// Changes, Access and State pick the kinds; none set means all three.
	Changes, Access, State bool
	Since                  time.Time
	// BeforeAt and BeforeID are the cursor of the next older page.
	BeforeAt time.Time
	BeforeID string
}

// AuditEntry is one row of the log, from audit or from events.
type AuditEntry struct {
	ID        string
	At        time.Time
	Source    string // audit or event
	Actor     string
	ActorKind string
	ActorID   string
	OrgID     string
	ProjectID string
	Action    string
	Target    string
	TargetID  string
	Before    string
	After     string
	Detail    map[string]any
	Via       string
	RequestID string
	Remote    string
}

// AuditPage is one page of the log, newest first, with the cursor of
// the next older page when there is one.
type AuditPage struct {
	Entries []AuditEntry
	More    bool
	NextAt  time.Time
	NextID  string
}

// AuditCounts are the chip counts for a filter's org, project and period.
type AuditCounts struct{ Changes, Access, State int }

// auditScope decides what a scope may see of the log: instance admins any
// org or all, org admins and owners their org, members and viewers their
// org's project rows only. Keys see nothing.
func (s *Service) auditScope(ctx context.Context, sc domain.Scope, f *AuditFilter) (orgOnly, projectOnly bool, err error) {
	if sc.IsKey() {
		return false, false, domain.ErrForbidden
	}
	switch {
	case sc.InstanceAdmin:
		// any org, or all of them
	case sc.OrgID == "":
		return false, false, domain.ErrForbidden
	case sc.CanAdminOrg():
		f.OrgID = sc.OrgID
	default:
		f.OrgID = sc.OrgID
		projectOnly = true
	}
	if f.ProjectID != "" {
		p, err := s.ProjectByID(ctx, f.ProjectID)
		if err != nil || (f.OrgID != "" && p.OrgID != f.OrgID) {
			return false, false, domain.NotFound("project")
		}
		if f.OrgID == "" {
			f.OrgID = p.OrgID
		}
	}
	return orgOnly, projectOnly, nil
}

func (f *AuditFilter) kinds() (changes, access, state int64) {
	if !f.Changes && !f.Access && !f.State {
		return 1, 1, 1
	}
	return b2i(f.Changes), b2i(f.Access), b2i(f.State)
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// AuditLog returns a page of the log for the scope and filter.
func (s *Service) AuditLog(ctx context.Context, sc domain.Scope, f AuditFilter) (*AuditPage, error) {
	orgOnly, projectOnly, err := s.auditScope(ctx, sc, &f)
	if err != nil {
		return nil, err
	}
	if f.Since.IsZero() {
		f.Since = s.now().Add(-7 * 24 * time.Hour)
	}
	beforeAt := f.BeforeAt
	beforeID := f.BeforeID
	if beforeAt.IsZero() {
		beforeAt = s.now().Add(24 * time.Hour)
		beforeID = "~"
	}
	changes, access, state := f.kinds()
	rows, err := s.db.Read().ListAudit(ctx, db.ListAuditParams{
		OrgID: f.OrgID, ProjectID: f.ProjectID, OrgOnly: b2i(orgOnly), ProjectOnly: b2i(projectOnly), Actor: f.Actor,
		Since: domain.Millis(f.Since), BeforeAt: domain.Millis(beforeAt), BeforeID: beforeID, Changes: changes, Access: access, State: state,
	})
	if err != nil {
		return nil, err
	}
	page := &AuditPage{Entries: []AuditEntry{}}
	for i, r := range rows {
		if i == 50 {
			page.More = true
			break
		}
		e := AuditEntry{
			ID: r.ID, At: domain.FromMillis(r.At), Source: r.Source, Actor: r.Actor, ActorKind: r.ActorKind, ActorID: strp(r.ActorID),
			OrgID: strp(r.OrgID), ProjectID: strp(r.ProjectID), Action: r.Act, Target: r.Target, TargetID: strp(r.TargetID),
			Before: strp(r.SpecBefore), After: strp(r.SpecAfter), Via: r.Via, RequestID: strp(r.RequestID), Remote: strp(r.RemoteAddr),
		}
		_ = json.Unmarshal([]byte(r.Detail), &e.Detail)
		page.Entries = append(page.Entries, e)
	}
	if page.More {
		last := page.Entries[len(page.Entries)-1]
		page.NextAt, page.NextID = last.At, last.ID
	}
	return page, nil
}

// AuditCounts counts the kinds in the filter's org, project and period,
// for the chips.
func (s *Service) AuditCounts(ctx context.Context, sc domain.Scope, f AuditFilter) (AuditCounts, error) {
	orgOnly, projectOnly, err := s.auditScope(ctx, sc, &f)
	if err != nil {
		return AuditCounts{}, err
	}
	if f.Since.IsZero() {
		f.Since = s.now().Add(-7 * 24 * time.Hour)
	}
	c, err := s.db.Read().CountAudit(ctx, db.CountAuditParams{OrgID: f.OrgID, ProjectID: f.ProjectID, OrgOnly: b2i(orgOnly), ProjectOnly: b2i(projectOnly), Since: domain.Millis(f.Since)})
	if err != nil {
		return AuditCounts{}, err
	}
	out := AuditCounts{Changes: int(c.Changes), Access: int(c.Access)}
	if !orgOnly {
		n, err := s.db.Read().CountStateEvents(ctx, db.CountStateEventsParams{OrgID: f.OrgID, ProjectID: f.ProjectID, Since: domain.Millis(f.Since)})
		if err != nil {
			return AuditCounts{}, err
		}
		out.State = int(n)
	}
	return out, nil
}

// AuditActors lists who acted in the filter's org, project and period,
// for the "who" select.
func (s *Service) AuditActors(ctx context.Context, sc domain.Scope, f AuditFilter) ([]string, error) {
	if _, _, err := s.auditScope(ctx, sc, &f); err != nil {
		return nil, err
	}
	if f.Since.IsZero() {
		f.Since = s.now().Add(-7 * 24 * time.Hour)
	}
	return s.db.Read().ListAuditActors(ctx, db.ListAuditActorsParams{OrgID: f.OrgID, ProjectID: f.ProjectID, Since: domain.Millis(f.Since)})
}

// LastBackupAt is when vink admin backup last ran, if ever.
func (s *Service) LastBackupAt(ctx context.Context) (time.Time, bool) {
	row, err := s.db.Read().GetInstanceMeta(ctx, MetaLastBackup)
	if err != nil {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, row.Value)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}
