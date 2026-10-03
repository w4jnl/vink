package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/domain"
)

func (s *Service) channelFromRow(r db.Channel) (*domain.Channel, error) {
	cfg, err := s.keyring.Open(r.Config)
	if err != nil {
		return nil, fmt.Errorf("channel %s: decrypt config: %w", r.Name, err)
	}
	return &domain.Channel{
		ID: r.ID, ProjectID: r.ProjectID, OrgID: r.OrgID, Name: r.Name, Kind: domain.ChannelKind(r.Kind), Config: cfg, Enabled: r.Enabled,
		CreatedAt: domain.FromMillis(r.CreatedAt), UpdatedAt: domain.FromMillis(r.UpdatedAt),
	}, nil
}

func (s *Service) checkChannel(c *domain.Channel) error {
	c.Name = strings.TrimSpace(c.Name)
	if err := c.Validate(); err != nil {
		return err
	}
	if s.validateChannel != nil {
		if err := s.validateChannel(c.Kind, c.Config); err != nil {
			if _, ok := domain.AsValidation(err); ok {
				return err
			}
			return (&domain.ValidationError{Errors: []domain.FieldError{{Field: "config", Msg: err.Error()}}}).OrNil()
		}
	}
	return nil
}

// CreateChannel stores a channel with its config encrypted. The first
// channel of a project also gets the default catch-all route for down
// and up, so alerts work without a second step.
func (s *Service) CreateChannel(ctx context.Context, sc domain.Scope, c *domain.Channel) (*domain.Channel, error) {
	if err := requireEdit(sc); err != nil {
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
			ID: domain.NewID(), ProjectID: sc.ProjectID, OrgID: sc.OrgID, Name: c.Name, Kind: string(c.Kind), Config: sealed, Enabled: c.Enabled,
			CreatedAt: domain.Millis(now), UpdatedAt: domain.Millis(now),
		})
		if err != nil {
			return conflictIfUnique(err, "a channel named "+c.Name+" exists")
		}
		out, err = s.channelFromRow(row)
		if err != nil {
			return err
		}
		routes, err := q.ListRoutes(ctx, sc.ProjectID)
		if err != nil {
			return err
		}
		if len(routes) == 0 {
			route, err := q.CreateRoute(ctx, db.CreateRouteParams{
				ID: domain.NewID(), ProjectID: sc.ProjectID, MatchTags: "[]",
				OnStates: statesJSON([]domain.State{domain.StateDown, domain.StateUp}), RepeatEveryS: 0, Priority: 0,
				CreatedAt: domain.Millis(now), UpdatedAt: domain.Millis(now),
			})
			if err != nil {
				return err
			}
			if err := q.InsertRouteChannel(ctx, db.InsertRouteChannelParams{RouteID: route.ID, ChannelID: out.ID, ProjectID: sc.ProjectID}); err != nil {
				return err
			}
		}
		e := projectEntry(sc, "channel.create", out.Name, out.ID)
		e.After = channelSnapshot(out)
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return nil, err
	}
	s.log.Info("channel created", "project_id", sc.ProjectID, "channel", out.Name, "kind", out.Kind, "actor", sc.Actor)
	return out, nil
}

// Channel returns one channel with its decrypted config.
func (s *Service) Channel(ctx context.Context, sc domain.Scope, id string) (*domain.Channel, error) {
	if err := requireProject(sc); err != nil {
		return nil, err
	}
	row, err := s.db.Read().GetChannel(ctx, db.GetChannelParams{ProjectID: sc.ProjectID, ID: id})
	if err != nil {
		return nil, notFoundIfNoRows(err, "channel")
	}
	return s.channelFromRow(row)
}

// ChannelByID loads a channel without a scope, for the dispatcher.
func (s *Service) ChannelByID(ctx context.Context, projectID, id string) (*domain.Channel, error) {
	row, err := s.db.Read().GetChannel(ctx, db.GetChannelParams{ProjectID: projectID, ID: id})
	if err != nil {
		return nil, notFoundIfNoRows(err, "channel")
	}
	return s.channelFromRow(row)
}

// ListChannels lists the project's channels by name.
func (s *Service) ListChannels(ctx context.Context, sc domain.Scope) ([]*domain.Channel, error) {
	if err := requireProject(sc); err != nil {
		return nil, err
	}
	rows, err := s.db.Read().ListChannels(ctx, sc.ProjectID)
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

// UpdateChannel replaces name, kind, config and enabled. A secret field
// sent as "***" keeps its stored value.
func (s *Service) UpdateChannel(ctx context.Context, sc domain.Scope, id string, upd *domain.Channel) (*domain.Channel, error) {
	if err := requireEdit(sc); err != nil {
		return nil, err
	}
	cur, err := s.Channel(ctx, sc, id)
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
		row, err := q.UpdateChannel(ctx, db.UpdateChannelParams{
			Name: next.Name, Kind: string(next.Kind), Config: sealed, Enabled: next.Enabled, UpdatedAt: domain.Millis(s.now()), ProjectID: sc.ProjectID, ID: id,
		})
		if err != nil {
			return conflictIfUnique(err, "a channel named "+next.Name+" exists")
		}
		out, err = s.channelFromRow(row)
		if err != nil {
			return err
		}
		e := projectEntry(sc, "channel.update", out.Name, out.ID)
		e.Before, e.After = channelSnapshot(cur), channelSnapshot(out)
		e.Detail = map[string]any{"fields": changedFields(e.Before, e.After)}
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return nil, err
	}
	s.log.Info("channel updated", "project_id", sc.ProjectID, "channel", next.Name, "actor", sc.Actor)
	return out, nil
}

// SetChannelEnabled flips a channel on or off. The config is neither
// decrypted for validation nor re-sealed: a toggle must work even when
// the instance can no longer validate the kind (an SMTP channel on a
// server without [smtp] host, say), since a disabled channel is exactly
// what such a setup wants.
func (s *Service) SetChannelEnabled(ctx context.Context, sc domain.Scope, id string, enabled bool) (*domain.Channel, error) {
	if err := requireEdit(sc); err != nil {
		return nil, err
	}
	cur, err := s.Channel(ctx, sc, id)
	if err != nil {
		return nil, err
	}
	var out *domain.Channel
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		row, err := q.SetChannelEnabled(ctx, db.SetChannelEnabledParams{Enabled: enabled, UpdatedAt: domain.Millis(s.now()), ProjectID: sc.ProjectID, ID: id})
		if err != nil {
			return notFoundIfNoRows(err, "channel")
		}
		out, err = s.channelFromRow(row)
		if err != nil {
			return err
		}
		e := projectEntry(sc, "channel.update", out.Name, out.ID)
		e.Before, e.After = channelSnapshot(cur), channelSnapshot(out)
		e.Detail = map[string]any{"fields": changedFields(e.Before, e.After)}
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return nil, err
	}
	s.log.Info("channel toggled", "project_id", sc.ProjectID, "channel", out.Name, "enabled", enabled, "actor", sc.Actor)
	return out, nil
}

// DeleteChannel removes a channel and, by cascade, its routes.
func (s *Service) DeleteChannel(ctx context.Context, sc domain.Scope, id string) error {
	if err := requireEdit(sc); err != nil {
		return err
	}
	cur, err := s.Channel(ctx, sc, id)
	if err != nil {
		return err
	}
	err = s.db.Tx(ctx, func(q *db.Queries) error {
		n, err := q.DeleteChannel(ctx, db.DeleteChannelParams{ProjectID: sc.ProjectID, ID: id})
		if err != nil {
			return err
		}
		if n == 0 {
			return domain.NotFound("channel")
		}
		// A route whose last channel went away has nowhere to send.
		if _, err := q.DeleteOrphanRoutes(ctx, sc.ProjectID); err != nil {
			return err
		}
		e := projectEntry(sc, "channel.delete", cur.Name, cur.ID)
		e.Before = channelSnapshot(cur)
		return s.record(ctx, q, sc, e)
	})
	if err != nil {
		return err
	}
	s.log.Info("channel deleted", "project_id", sc.ProjectID, "channel_id", id, "actor", sc.Actor)
	return nil
}

// RedactConfig replaces secret fields with "***" for responses.
func RedactConfig(kind domain.ChannelKind, cfg json.RawMessage) json.RawMessage {
	var m map[string]any
	if err := json.Unmarshal(cfg, &m); err != nil || m == nil {
		return cfg
	}
	for _, f := range domain.ChannelSecretFields[kind] {
		if v, ok := m[f]; ok && v != nil && v != "" {
			m[f] = "***"
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return cfg
	}
	return out
}

// KeepSecrets copies stored secret fields into an update whose secret
// fields are "***".
func KeepSecrets(kind domain.ChannelKind, updated, stored json.RawMessage) (json.RawMessage, error) {
	var upd, cur map[string]any
	if err := json.Unmarshal(updated, &upd); err != nil || upd == nil {
		return updated, nil
	}
	if err := json.Unmarshal(stored, &cur); err != nil {
		return updated, nil
	}
	for _, f := range domain.ChannelSecretFields[kind] {
		if v, ok := upd[f]; ok && v == "***" {
			if old, ok := cur[f]; ok {
				upd[f] = old
			} else {
				delete(upd, f)
			}
		}
	}
	return json.Marshal(upd)
}
