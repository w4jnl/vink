package service

import (
	"context"
	"errors"
	"time"

	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
	"github.com/w4jnl/vink/internal/notify"
)

// Org channels: notifiers of the org itself, set up once for many
// projects. An org channel belongs to no project and only the org's routes
// send to it. Org admins and owners manage them, as they manage org status
// pages.

// CreateOrgChannel stores a channel of the org with its config encrypted.
// Unlike a project's first channel it gets no default route: which
// projects an org channel serves is a choice the org route makes.
func (s *Service) CreateOrgChannel(ctx context.Context, sc domain.Scope, c *domain.Channel) (*domain.Channel, error) {
	if err := requireOrgAdmin(sc); err != nil {
		return nil, err
	}
	if err := s.checkChannel(c); err != nil {
		return nil, err
	}
	sealed, err := s.keyring.Seal(c.Config)
	if err != nil {
		return nil, err
	}
	now := s.now()
	var out *domain.Channel
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		row, err := q.CreateChannel(ctx, db.CreateChannelParams{
			ID: domain.NewID(), ProjectID: nil, OrgID: sc.OrgID, Name: c.Name, Kind: string(c.Kind), Config: sealed, Enabled: c.Enabled,
			CreatedAt: domain.Millis(now), UpdatedAt: domain.Millis(now),
		})
		if err != nil {
			return conflictIfUnique(err, "a channel named "+c.Name+" exists")
		}
		out, err = s.channelFromRow(row)
		if err != nil {
			return err
		}
		e := orgEntry(sc.OrgID, "channel.create", out.Name, out.ID)
		e.After = channelSnapshot(out)
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return nil, err
	}
	s.log.Info("org channel created", "org_id", sc.OrgID, "channel", out.Name, "kind", out.Kind, "actor", sc.Actor)
	return out, nil
}

// OrgChannel returns one channel of the org with its decrypted config.
func (s *Service) OrgChannel(ctx context.Context, sc domain.Scope, id string) (*domain.Channel, error) {
	if err := requireOrgAdmin(sc); err != nil {
		return nil, err
	}
	row, err := s.db.Read().GetOrgChannel(ctx, db.GetOrgChannelParams{OrgID: sc.OrgID, ID: id})
	if err != nil {
		return nil, notFoundIfNoRows(err, "channel")
	}
	return s.channelFromRow(row)
}

// ListOrgChannels lists the org's own channels by name.
func (s *Service) ListOrgChannels(ctx context.Context, sc domain.Scope) ([]*domain.Channel, error) {
	if err := requireOrgAdmin(sc); err != nil {
		return nil, err
	}
	rows, err := s.db.Read().ListOrgChannels(ctx, sc.OrgID)
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Channel, 0, len(rows))
	for _, r := range rows {
		c, err := s.channelFromRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// UpdateOrgChannel replaces name, kind, config and enabled. A secret field
// sent as "***" keeps its stored value.
func (s *Service) UpdateOrgChannel(ctx context.Context, sc domain.Scope, id string, upd *domain.Channel) (*domain.Channel, error) {
	cur, err := s.OrgChannel(ctx, sc, id)
	if err != nil {
		return nil, err
	}
	next := *upd
	next.ID = cur.ID
	if next.Kind == "" {
		next.Kind = cur.Kind
	}
	if next.Name == "" {
		next.Name = cur.Name
	}
	if len(next.Config) == 0 {
		next.Config = cur.Config
	} else if next.Kind == cur.Kind {
		merged, err := KeepSecrets(next.Kind, next.Config, cur.Config)
		if err != nil {
			return nil, err
		}
		next.Config = merged
	}
	if err := s.checkChannel(&next); err != nil {
		return nil, err
	}
	sealed, err := s.keyring.Seal(next.Config)
	if err != nil {
		return nil, err
	}
	var out *domain.Channel
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		row, err := q.UpdateOrgChannel(ctx, db.UpdateOrgChannelParams{
			Name: next.Name, Kind: string(next.Kind), Config: sealed, Enabled: next.Enabled, UpdatedAt: domain.Millis(s.now()), OrgID: sc.OrgID, ID: id,
		})
		if err != nil {
			return conflictIfUnique(err, "a channel named "+next.Name+" exists")
		}
		out, err = s.channelFromRow(row)
		if err != nil {
			return err
		}
		e := orgEntry(sc.OrgID, "channel.update", out.Name, out.ID)
		e.Before, e.After = channelSnapshot(cur), channelSnapshot(out)
		e.Detail = map[string]any{"fields": changedFields(e.Before, e.After)}
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return nil, err
	}
	s.log.Info("org channel updated", "org_id", sc.OrgID, "channel", next.Name, "actor", sc.Actor)
	return out, nil
}

// SetOrgChannelEnabled flips a channel of the org on or off, without
// decrypting its config (see SetChannelEnabled).
func (s *Service) SetOrgChannelEnabled(ctx context.Context, sc domain.Scope, id string, enabled bool) (*domain.Channel, error) {
	cur, err := s.OrgChannel(ctx, sc, id)
	if err != nil {
		return nil, err
	}
	var out *domain.Channel
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		row, err := q.SetOrgChannelEnabled(ctx, db.SetOrgChannelEnabledParams{Enabled: enabled, UpdatedAt: domain.Millis(s.now()), OrgID: sc.OrgID, ID: id})
		if err != nil {
			return notFoundIfNoRows(err, "channel")
		}
		out, err = s.channelFromRow(row)
		if err != nil {
			return err
		}
		e := orgEntry(sc.OrgID, "channel.update", out.Name, out.ID)
		e.Before, e.After = channelSnapshot(cur), channelSnapshot(out)
		e.Detail = map[string]any{"fields": changedFields(e.Before, e.After)}
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return nil, err
	}
	s.log.Info("org channel toggled", "org_id", sc.OrgID, "channel", out.Name, "enabled", enabled, "actor", sc.Actor)
	return out, nil
}

// DeleteOrgChannel removes a channel of the org and, by cascade, its place
// in org routes; a route left with no channel goes too.
func (s *Service) DeleteOrgChannel(ctx context.Context, sc domain.Scope, id string) error {
	cur, err := s.OrgChannel(ctx, sc, id)
	if err != nil {
		return err
	}
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		n, err := q.DeleteOrgChannel(ctx, db.DeleteOrgChannelParams{OrgID: sc.OrgID, ID: id})
		if err != nil {
			return err
		}
		if n == 0 {
			return domain.NotFound("channel")
		}
		if _, err := q.DeleteOrphanOrgRoutes(ctx, sc.OrgID); err != nil {
			return err
		}
		e := orgEntry(sc.OrgID, "channel.delete", cur.Name, cur.ID)
		e.Before = channelSnapshot(cur)
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return err
	}
	s.log.Info("org channel deleted", "org_id", sc.OrgID, "channel_id", id, "actor", sc.Actor)
	return nil
}

// TestOrgChannel sends a synthetic notification through a channel of the
// org and returns the notifier's error verbatim.
func (s *Service) TestOrgChannel(ctx context.Context, sc domain.Scope, id string) error {
	ch, err := s.OrgChannel(ctx, sc, id)
	if err != nil {
		return err
	}
	return s.TestOrgChannelConfig(ctx, sc, ch.Kind, ch.Config)
}

// TestOrgChannelConfig sends a synthetic notification through an unsaved
// config. With no project to speak of, the test names a stand-in one.
func (s *Service) TestOrgChannelConfig(ctx context.Context, sc domain.Scope, kind domain.ChannelKind, cfg []byte) error {
	if err := requireOrgAdmin(sc); err != nil {
		return err
	}
	probe := &domain.Channel{Name: "test", Kind: kind, Config: cfg, Enabled: true}
	if err := s.checkChannel(probe); err != nil {
		return err
	}
	org, err := s.OrgByID(ctx, sc.OrgID)
	if err != nil {
		return err
	}
	if s.notifier == nil {
		return errors.New("no notifier registry configured")
	}
	now := s.now()
	project := domain.Project{OrgID: org.ID, OrgSlug: org.Slug, Slug: "channel-test", Name: "Channel test", Timezone: "UTC"}
	n := notify.Notification{
		Test:    true,
		Event:   domain.Event{ID: domain.NewID(), At: now, From: domain.StateUp, To: domain.StateDown, Reason: "channel test"},
		Monitor: domain.Monitor{ID: "test", Slug: "channel-test", Name: "Channel test", Kind: domain.KindHeartbeat, State: domain.StateDown, StateSince: now, Tags: []string{}},
		Project: project,
	}
	n.Links = s.links(&project, &n.Monitor, nil)
	return s.notifier.Send(ctx, kind, cfg, n)
}

// LastSentForOrgChannel returns when a channel of the org last delivered,
// for any of the org's projects, or nil.
func (s *Service) LastSentForOrgChannel(ctx context.Context, sc domain.Scope, channelID string) (*time.Time, error) {
	if err := requireOrgAdmin(sc); err != nil {
		return nil, err
	}
	ms, err := s.db.Read().LastSentForOrgChannel(ctx, db.LastSentForOrgChannelParams{OrgID: sc.OrgID, ChannelID: channelID})
	if err != nil || ms == 0 {
		return nil, err
	}
	t := domain.FromMillis(ms)
	return &t, nil
}

// OrgRouteCountForChannel says how many org routes send to a channel.
func (s *Service) OrgRouteCountForChannel(ctx context.Context, sc domain.Scope, channelID string) (int, error) {
	if err := requireOrgAdmin(sc); err != nil {
		return 0, err
	}
	n, err := s.db.Read().CountOrgRoutesForChannel(ctx, db.CountOrgRoutesForChannelParams{OrgID: sc.OrgID, ChannelID: channelID})
	return int(n), err
}
