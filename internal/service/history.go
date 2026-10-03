package service

import (
	"context"
	"math"
	"time"

	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
)

// HistoryPage is a cursor over a monitor's timeline, newest first: the
// window, an optional kind, and the keyset position of the previous page.
type HistoryPage struct {
	Since, Until time.Time
	// Kind narrows the rows: "" for all, KindOK, KindFail, KindRun for
	// observations, KindChange for state changes only.
	Kind     string
	CursorAt time.Time
	CursorID string
	Limit    int
}

// Kinds of timeline rows.
const (
	KindOK     = "ok"     // successful pings and checks
	KindFail   = "fail"   // fail pings, non-zero exits, run timeouts, failed and confirming checks
	KindRun    = "run"    // start and log signals
	KindChange = "change" // state changes only
)

// ValidHistoryKind reports whether k is "" or one of the kinds.
func ValidHistoryKind(k string) bool {
	switch k {
	case "", KindOK, KindFail, KindRun, KindChange:
		return true
	}
	return false
}

// bounds turns the page into query arguments: a zero Until is the far
// future, a zero cursor sits above every row ("~" sorts after any ULID).
func (p HistoryPage) bounds() (since, until, cursorAt int64, cursorID string, limit int) {
	limit = p.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	until = int64(math.MaxInt64)
	if !p.Until.IsZero() {
		until = domain.Millis(p.Until)
	}
	cursorAt, cursorID = int64(math.MaxInt64), "~"
	if !p.CursorAt.IsZero() {
		cursorAt, cursorID = domain.Millis(p.CursorAt), p.CursorID
	}
	return domain.Millis(p.Since), until, cursorAt, cursorID, limit
}

// ListObservations returns a page of observations for a monitor.
func (s *Service) ListObservations(ctx context.Context, sc domain.Scope, slug string, p HistoryPage) ([]*domain.Observation, error) {
	m, err := s.MonitorBySlug(ctx, sc, slug)
	if err != nil {
		return nil, err
	}
	_, _, _, _, limit := p.bounds()
	return s.observationPage(ctx, sc.ProjectID, m.ID, p, limit)
}

func (s *Service) observationPage(ctx context.Context, projectID, monitorID string, p HistoryPage, limit int) ([]*domain.Observation, error) {
	since, until, cursorAt, cursorID, _ := p.bounds()
	kind := p.Kind
	if kind == KindChange {
		return nil, nil
	}
	rows, err := s.db.Read().ListObservations(ctx, db.ListObservationsParams{
		ProjectID: projectID, MonitorID: monitorID, Since: since, Until: until,
		CursorAt: cursorAt, CursorID: cursorID, Kind: kind, PageSize: int64(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Observation, 0, len(rows))
	for _, r := range rows {
		out = append(out, observationFromRow(r))
	}
	return out, nil
}

// ListEventsPage returns a page of a monitor's state flips, newest first,
// with the same window and cursor as ListObservations.
func (s *Service) ListEventsPage(ctx context.Context, sc domain.Scope, slug string, p HistoryPage) ([]*domain.Event, error) {
	m, err := s.MonitorBySlug(ctx, sc, slug)
	if err != nil {
		return nil, err
	}
	_, _, _, _, limit := p.bounds()
	return s.eventPage(ctx, sc.ProjectID, m.ID, p, limit)
}

func (s *Service) eventPage(ctx context.Context, projectID, monitorID string, p HistoryPage, limit int) ([]*domain.Event, error) {
	since, until, cursorAt, cursorID, _ := p.bounds()
	rows, err := s.db.Read().ListEventsPage(ctx, db.ListEventsPageParams{
		ProjectID: projectID, MonitorID: monitorID, Since: since, Until: until,
		CursorAt: cursorAt, CursorID: cursorID, PageSize: int64(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Event, 0, len(rows))
	for _, r := range rows {
		out = append(out, eventFromRow(r))
	}
	return out, nil
}

// HistoryItem is one row of the timeline: an observation or an event.
type HistoryItem struct {
	At    time.Time
	ID    string
	Obs   *domain.Observation
	Event *domain.Event
}

// HistoryResult is one page of the timeline and where the next one starts.
type HistoryResult struct {
	Items  []HistoryItem
	More   bool
	NextAt time.Time
	NextID string
}

// History returns one page of a monitor's observations and state changes
// merged newest first. Both tables are keyed by (at, id) with ULIDs, so
// one cursor serves both: each table is asked for limit+1 rows after it,
// the two lists are merged and cut at limit, and the last row emitted is
// the next cursor. An event written in the same transaction as its
// observation has the larger id and sits above it.
func (s *Service) History(ctx context.Context, sc domain.Scope, slug string, p HistoryPage) (HistoryResult, error) {
	m, err := s.MonitorBySlug(ctx, sc, slug)
	if err != nil {
		return HistoryResult{}, err
	}
	if !ValidHistoryKind(p.Kind) {
		return HistoryResult{}, &domain.ValidationError{Errors: []domain.FieldError{{Field: "kind", Msg: "must be ok, fail, run or change"}}}
	}
	_, _, _, _, limit := p.bounds()
	var obs []*domain.Observation
	var events []*domain.Event
	if p.Kind != KindChange {
		if obs, err = s.observationPage(ctx, sc.ProjectID, m.ID, p, limit+1); err != nil {
			return HistoryResult{}, err
		}
	}
	if p.Kind == "" || p.Kind == KindChange {
		if events, err = s.eventPage(ctx, sc.ProjectID, m.ID, p, limit+1); err != nil {
			return HistoryResult{}, err
		}
	}
	res := HistoryResult{Items: make([]HistoryItem, 0, limit)}
	i, j := 0, 0
	for len(res.Items) < limit && (i < len(obs) || j < len(events)) {
		takeObs := j >= len(events)
		if !takeObs && i < len(obs) {
			oa, ea := domain.Millis(obs[i].At), domain.Millis(events[j].At)
			takeObs = oa > ea || (oa == ea && obs[i].ID > events[j].ID)
		}
		if takeObs {
			o := obs[i]
			res.Items = append(res.Items, HistoryItem{At: o.At, ID: o.ID, Obs: o})
			i++
		} else {
			e := events[j]
			res.Items = append(res.Items, HistoryItem{At: e.At, ID: e.ID, Event: e})
			j++
		}
	}
	res.More = i < len(obs) || j < len(events)
	if n := len(res.Items); n > 0 {
		res.NextAt, res.NextID = res.Items[n-1].At, res.Items[n-1].ID
	}
	return res, nil
}

// ObservationsSince returns observations from since on, oldest first, for
// timelines.
func (s *Service) ObservationsSince(ctx context.Context, sc domain.Scope, m *domain.Monitor, since time.Time) ([]*domain.Observation, error) {
	if err := requireProject(sc); err != nil {
		return nil, err
	}
	rows, err := s.db.Read().ListObservationsSince(ctx, db.ListObservationsSinceParams{ProjectID: sc.ProjectID, MonitorID: m.ID, At: domain.Millis(since)})
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Observation, 0, len(rows))
	for _, r := range rows {
		out = append(out, observationFromRow(r))
	}
	return out, nil
}

// Observation returns one observation of the scope's project.
func (s *Service) Observation(ctx context.Context, sc domain.Scope, id string) (*domain.Observation, error) {
	if err := requireProject(sc); err != nil {
		return nil, err
	}
	row, err := s.db.Read().GetObservation(ctx, db.GetObservationParams{ProjectID: sc.ProjectID, ID: id})
	if err != nil {
		return nil, notFoundIfNoRows(err, "observation")
	}
	return observationFromRow(row), nil
}

// ObservationBody returns the stored body and content type.
func (s *Service) ObservationBody(ctx context.Context, sc domain.Scope, id string) ([]byte, string, error) {
	if err := requireProject(sc); err != nil {
		return nil, "", err
	}
	row, err := s.db.Read().GetBody(ctx, db.GetBodyParams{ProjectID: sc.ProjectID, ObservationID: id})
	if err != nil {
		return nil, "", notFoundIfNoRows(err, "body")
	}
	return row.Content, row.ContentType, nil
}

// ListEvents returns a monitor's state flips, newest first.
func (s *Service) ListEvents(ctx context.Context, sc domain.Scope, slug string, limit int) ([]*domain.Event, error) {
	m, err := s.MonitorBySlug(ctx, sc, slug)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Read().ListEvents(ctx, db.ListEventsParams{ProjectID: sc.ProjectID, MonitorID: m.ID, Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Event, 0, len(rows))
	for _, r := range rows {
		out = append(out, eventFromRow(r))
	}
	return out, nil
}

// EventsSince returns a monitor's flips from since on, oldest first.
func (s *Service) EventsSince(ctx context.Context, sc domain.Scope, m *domain.Monitor, since time.Time) ([]*domain.Event, error) {
	if err := requireProject(sc); err != nil {
		return nil, err
	}
	rows, err := s.db.Read().ListEventsSince(ctx, db.ListEventsSinceParams{ProjectID: sc.ProjectID, MonitorID: m.ID, At: domain.Millis(since)})
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Event, 0, len(rows))
	for _, r := range rows {
		out = append(out, eventFromRow(r))
	}
	return out, nil
}

// LatencyPoint is one timed latency sample.
type LatencyPoint struct {
	At time.Time
	Ms int64
}

// LatenciesSince returns every latency sample of the project since a
// time, grouped by monitor id, oldest first. The list rows draw their
// sparklines from it in one query.
func (s *Service) LatenciesSince(ctx context.Context, sc domain.Scope, since time.Time) (map[string][]LatencyPoint, error) {
	if err := requireProject(sc); err != nil {
		return nil, err
	}
	rows, err := s.db.Read().ListLatenciesSince(ctx, db.ListLatenciesSinceParams{ProjectID: sc.ProjectID, At: domain.Millis(since)})
	if err != nil {
		return nil, err
	}
	out := map[string][]LatencyPoint{}
	for _, r := range rows {
		if r.LatencyMs == nil {
			continue
		}
		out[r.MonitorID] = append(out[r.MonitorID], LatencyPoint{At: domain.FromMillis(r.At), Ms: *r.LatencyMs})
	}
	return out, nil
}
