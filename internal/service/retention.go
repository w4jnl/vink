package service

import (
	"context"
	"time"

	"github.com/w4jnl/vink/internal/domain"
)

// Pruned says what a retention pass removed.
type Pruned struct {
	Observations int64
	Bodies       int64
}

// Prune deletes observations older than observationDays and bodies
// older than bodyDays. Events and incidents are kept.
func (s *Service) Prune(ctx context.Context, now time.Time, observationDays, bodyDays int) (Pruned, error) {
	var out Pruned
	if bodyDays > 0 {
		n, err := s.db.Write().DeleteBodiesBefore(ctx, domain.Millis(now.Add(-time.Duration(bodyDays)*24*time.Hour)))
		if err != nil {
			return out, err
		}
		out.Bodies = n
	}
	if observationDays > 0 {
		n, err := s.db.Write().DeleteObservationsBefore(ctx, domain.Millis(now.Add(-time.Duration(observationDays)*24*time.Hour)))
		if err != nil {
			return out, err
		}
		out.Observations = n
	}
	if out.Observations > 0 || out.Bodies > 0 {
		s.log.Info("retention", "observations_deleted", out.Observations, "bodies_deleted", out.Bodies)
	}
	return out, nil
}

// RunRetention prunes at start and then every interval until ctx ends.
func (s *Service) RunRetention(ctx context.Context, every time.Duration, observationDays, bodyDays int) error {
	if every <= 0 {
		every = 6 * time.Hour
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		if _, err := s.Prune(ctx, s.now(), observationDays, bodyDays); err != nil && ctx.Err() == nil {
			s.log.Error("retention failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
