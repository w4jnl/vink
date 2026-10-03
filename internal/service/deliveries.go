package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/notify"
)

// SetNotifier installs the registry used to send and to validate channel
// configs.
func (s *Service) SetNotifier(r *notify.Registry) {
	s.notifier = r
	s.SetChannelValidator(r.Validate)
}

func deliveryFromRow(r db.Delivery) domain.Delivery {
	return domain.Delivery{
		ID: r.ID, EventID: r.EventID, ChannelID: r.ChannelID, ProjectID: r.ProjectID, MonitorID: r.MonitorID, RouteID: strp(r.RouteID),
		Kind: r.Kind, Repeat: r.Repeat, Attempt: int(r.Attempt), NextAttemptAt: domain.FromMillis(r.NextAttemptAt),
		DeliveredAt: domain.FromMillisPtr(r.DeliveredAt), FailedAt: domain.FromMillisPtr(r.FailedAt), Error: strp(r.Error), CreatedAt: domain.FromMillis(r.CreatedAt),
	}
}

// DueDeliveries lists outbox rows whose next attempt is due.
func (s *Service) DueDeliveries(ctx context.Context, now time.Time, limit int) ([]domain.Delivery, error) {
	rows, err := s.db.Read().ListDueDeliveries(ctx, db.ListDueDeliveriesParams{NextAttemptAt: domain.Millis(now), Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]domain.Delivery, 0, len(rows))
	for _, r := range rows {
		out = append(out, deliveryFromRow(r))
	}
	return out, nil
}

// EarlierPending counts undelivered rows for the same monitor and channel
// created before `before`, so an up never overtakes its down.
func (s *Service) EarlierPending(ctx context.Context, d domain.Delivery) (int, error) {
	n, err := s.db.Read().CountEarlierPendingDeliveries(ctx, db.CountEarlierPendingDeliveriesParams{MonitorID: d.MonitorID, ChannelID: d.ChannelID, CreatedAt: domain.Millis(d.CreatedAt)})
	return int(n), err
}

// MarkDelivered records success.
func (s *Service) MarkDelivered(ctx context.Context, id string, attempt int, now time.Time) error {
	return s.db.Write().MarkDeliveryDelivered(ctx, db.MarkDeliveryDeliveredParams{DeliveredAt: ptri(domain.Millis(now)), Attempt: int64(attempt), ID: id})
}

// RescheduleDelivery records a failed attempt and the next try.
func (s *Service) RescheduleDelivery(ctx context.Context, id string, attempt int, next time.Time, errMsg string) error {
	return s.db.Write().RescheduleDelivery(ctx, db.RescheduleDeliveryParams{Attempt: int64(attempt), NextAttemptAt: domain.Millis(next), Error: ptrs(errMsg), ID: id})
}

// FailDelivery gives up on a row.
func (s *Service) FailDelivery(ctx context.Context, id string, attempt int, now time.Time, errMsg string) error {
	return s.db.Write().MarkDeliveryFailed(ctx, db.MarkDeliveryFailedParams{Attempt: int64(attempt), FailedAt: ptri(domain.Millis(now)), Error: ptrs(errMsg), ID: id})
}

// Deliver builds the notification for a delivery and sends it through the
// channel's notifier.
func (s *Service) Deliver(ctx context.Context, d domain.Delivery) error {
	if s.notifier == nil {
		return errors.New("no notifier registry configured")
	}
	ch, err := s.ChannelByID(ctx, d.ProjectID, d.ChannelID)
	if err != nil {
		return err
	}
	if !ch.Enabled {
		return errChannelDisabled
	}
	n, err := s.BuildNotification(ctx, d)
	if err != nil {
		return err
	}
	if err := s.notifier.Send(ctx, ch.Kind, ch.Config, *n); err != nil {
		if s.metrics != nil {
			s.metrics.Deliveries.WithLabelValues(string(ch.Kind), "error").Inc()
		}
		return err
	}
	if s.metrics != nil {
		s.metrics.Deliveries.WithLabelValues(string(ch.Kind), "ok").Inc()
	}
	s.log.Info("notification sent", "project_id", d.ProjectID, "channel", ch.Name, "kind", ch.Kind, "event", n.Kind(), "monitor", n.Monitor.Slug, "repeat", d.Repeat)
	return nil
}

var errChannelDisabled = errors.New("channel is disabled")

// BuildNotification assembles event, monitor, project, incident and links.
func (s *Service) BuildNotification(ctx context.Context, d domain.Delivery) (*notify.Notification, error) {
	q := s.db.Read()
	evRow, err := q.GetEvent(ctx, db.GetEventParams{ProjectID: d.ProjectID, ID: d.EventID})
	if err != nil {
		return nil, notFoundIfNoRows(err, "event")
	}
	mRow, err := q.GetMonitor(ctx, db.GetMonitorParams{ProjectID: d.ProjectID, ID: d.MonitorID})
	if err != nil {
		return nil, notFoundIfNoRows(err, "monitor")
	}
	m, err := monitorFromRow(mRow)
	if err != nil {
		return nil, err
	}
	pRow, err := q.GetProjectByID(ctx, d.ProjectID)
	if err != nil {
		return nil, notFoundIfNoRows(err, "project")
	}
	project := projectFromRow(pRow)
	org, err := q.GetOrg(ctx, project.OrgID)
	if err != nil {
		return nil, notFoundIfNoRows(err, "org")
	}
	project.OrgSlug = org.Slug
	n := &notify.Notification{Event: *eventFromRow(evRow), Monitor: *m, Project: *project, Repeat: d.Repeat}
	if err := s.attachObservation(ctx, q, d.ProjectID, n); err != nil {
		return nil, err
	}
	if inc, err := q.GetOpenIncidentForMonitor(ctx, db.GetOpenIncidentForMonitorParams{ProjectID: d.ProjectID, MonitorID: d.MonitorID}); err == nil {
		n.Incident = incidentFrom(incidentRow{inc.ID, inc.MonitorID, inc.ProjectID, inc.OpenedAt, inc.ResolvedAt, inc.AckedBy, inc.AckedAt, inc.OpenEventID, inc.CloseEventID, m.Slug, m.Name, m.TagsJSON(), n.Event.Reason})
	} else if !db.IsNotFound(err) {
		return nil, err
	}
	n.Links = s.links(project, m, n.Incident)
	return n, nil
}

// attachObservation copies what the ping or check behind the event said
// into the notification: the exit code and the ?msg=, or the tail of a
// text body when there was no msg. An observation the retention job has
// already removed leaves the notification as it is.
func (s *Service) attachObservation(ctx context.Context, q *db.Queries, projectID string, n *notify.Notification) error {
	if n.Event.ObservationID == "" {
		return nil
	}
	row, err := q.GetObservation(ctx, db.GetObservationParams{ProjectID: projectID, ID: n.Event.ObservationID})
	if db.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	obs := observationFromRow(row)
	n.ExitCode = obs.ExitCode
	n.Message, _ = obs.Detail["msg"].(string)
	if n.Message != "" || !obs.HasBody {
		return nil
	}
	body, err := q.GetBody(ctx, db.GetBodyParams{ProjectID: projectID, ObservationID: obs.ID})
	if db.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	n.Message = notify.Excerpt(body.Content)
	return nil
}

// links builds the UI URLs for a notification.
func (s *Service) links(p *domain.Project, m *domain.Monitor, inc *domain.Incident) notify.Links {
	base := s.cfg.BaseURL
	if base == "" {
		base = s.cfg.PingBaseURL
	}
	l := notify.Links{Monitor: fmt.Sprintf("%s/o/%s/p/%s/m/%s", base, p.OrgSlug, p.Slug, m.Slug)}
	if inc != nil && inc.Open() {
		l.Incident = fmt.Sprintf("%s/o/%s/p/%s/incidents", base, p.OrgSlug, p.Slug)
		if inc.AckedAt == nil {
			l.Ack = base + "/a/" + s.AckToken(p.ID, inc.ID)
		}
	}
	return l
}

// EnqueueRepeats adds a repeat delivery for every open, unacknowledged
// incident whose route has repeat_every set and whose last delivery on
// that route is older than the interval.
func (s *Service) EnqueueRepeats(ctx context.Context, now time.Time) (int, error) {
	incidents, err := s.db.Read().ListOpenUnackedIncidents(ctx)
	if err != nil {
		return 0, err
	}
	added := 0
	for _, inc := range incidents {
		mRow, err := s.db.Read().GetMonitor(ctx, db.GetMonitorParams{ProjectID: inc.ProjectID, ID: inc.MonitorID})
		if err != nil {
			continue
		}
		m, err := monitorFromRow(mRow)
		if err != nil || m.State != domain.StateDown {
			continue
		}
		if _, covered, err := s.coverage(ctx, s.db.Read(), m, now); err != nil {
			return added, err
		} else if covered {
			continue // maintenance silences repeats too
		}
		routes, err := s.listRoutes(ctx, s.db.Read(), inc.ProjectID)
		if err != nil {
			return added, err
		}
		for _, rt := range routes {
			if rt.RepeatEvery <= 0 || !rt.Fires(domain.StateDown) || !m.HasAllTags(rt.MatchTags) {
				continue
			}
			last, err := s.db.Read().LastDeliveryForRoute(ctx, db.LastDeliveryForRouteParams{MonitorID: m.ID, RouteID: &rt.ID})
			if err != nil && !db.IsNotFound(err) {
				return added, err
			}
			if err == nil && now.Sub(domain.FromMillis(last.CreatedAt)) < rt.RepeatEvery {
				continue
			}
			if err == nil && last.DeliveredAt == nil && last.FailedAt == nil {
				continue // the previous one is still in flight
			}
			for _, ch := range rt.Channels {
				if !ch.Enabled {
					continue
				}
				if err := s.db.Write().InsertDelivery(ctx, db.InsertDeliveryParams{
					ID: domain.NewID(), EventID: inc.OpenEventID, ChannelID: ch.ID, ProjectID: inc.ProjectID, MonitorID: m.ID, RouteID: &rt.ID,
					Kind: string(domain.StateDown), Repeat: true, Attempt: 0, NextAttemptAt: domain.Millis(now), CreatedAt: domain.Millis(now),
				}); err != nil {
					return added, err
				}
				added++
			}
		}
	}
	return added, nil
}

// TestChannel sends a synthetic notification and returns the notifier's
// error verbatim.
func (s *Service) TestChannel(ctx context.Context, sc domain.Scope, id string) error {
	if err := requireEdit(sc); err != nil {
		return err
	}
	ch, err := s.Channel(ctx, sc, id)
	if err != nil {
		return err
	}
	return s.TestChannelConfig(ctx, sc, ch.Kind, ch.Config)
}

// LastSentForChannel returns when a channel last delivered, or nil.
func (s *Service) LastSentForChannel(ctx context.Context, sc domain.Scope, channelID string) (*time.Time, error) {
	if err := requireProject(sc); err != nil {
		return nil, err
	}
	ms, err := s.db.Read().LastSentForChannel(ctx, db.LastSentForChannelParams{ProjectID: sc.ProjectID, ChannelID: channelID})
	if err != nil || ms == 0 {
		return nil, err
	}
	t := domain.FromMillis(ms)
	return &t, nil
}

// TestChannelConfig sends a synthetic notification through an unsaved
// config, so a panel can test before saving.
func (s *Service) TestChannelConfig(ctx context.Context, sc domain.Scope, kind domain.ChannelKind, cfg []byte) error {
	if err := requireEdit(sc); err != nil {
		return err
	}
	probe := &domain.Channel{Name: "test", Kind: kind, Config: cfg, Enabled: true}
	if err := s.checkChannel(probe); err != nil {
		return err
	}
	project, err := s.Project(ctx, sc)
	if err != nil {
		return err
	}
	if s.notifier == nil {
		return errors.New("no notifier registry configured")
	}
	if org, err := s.OrgByID(ctx, project.OrgID); err == nil {
		project.OrgSlug = org.Slug
	}
	now := s.now()
	n := notify.Notification{
		Test:    true,
		Event:   domain.Event{ID: domain.NewID(), At: now, From: domain.StateUp, To: domain.StateDown, Reason: "channel test"},
		Monitor: domain.Monitor{ID: "test", Slug: "channel-test", Name: "Channel test", Kind: domain.KindHeartbeat, State: domain.StateDown, StateSince: now, Tags: []string{}},
		Project: *project,
	}
	n.Links = s.links(project, &n.Monitor, nil)
	return s.notifier.Send(ctx, kind, cfg, n)
}

// RecentDeliveries lists the project's latest outbox rows with channel names.
func (s *Service) RecentDeliveries(ctx context.Context, sc domain.Scope, limit int) ([]domain.Delivery, error) {
	if err := requireProject(sc); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Read().ListRecentDeliveries(ctx, db.ListRecentDeliveriesParams{ProjectID: sc.ProjectID, Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	out := make([]domain.Delivery, 0, len(rows))
	for _, r := range rows {
		out = append(out, deliveryFromRow(db.Delivery{
			ID: r.ID, EventID: r.EventID, ChannelID: r.ChannelID, ProjectID: r.ProjectID, MonitorID: r.MonitorID, RouteID: r.RouteID, Kind: r.Kind, Repeat: r.Repeat,
			Attempt: r.Attempt, NextAttemptAt: r.NextAttemptAt, DeliveredAt: r.DeliveredAt, FailedAt: r.FailedAt, Error: r.Error, CreatedAt: r.CreatedAt,
		}))
	}
	return out, nil
}
