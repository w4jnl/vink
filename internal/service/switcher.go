package service

import "context"

// ProjectCounts are monitors per state of one project, for the switcher.
type ProjectCounts struct {
	Down, Late, Up, Paused, New int
}

// ProjectProblems returns monitor counts per "org/project" slug pair for
// the whole instance in one query; callers show only the projects the
// viewer may see.
func (s *Service) ProjectProblems(ctx context.Context) (map[string]ProjectCounts, error) {
	rows, err := s.db.Read().ListMonitorsForMetrics(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]ProjectCounts{}
	for _, r := range rows {
		key := r.OrgSlug + "/" + r.ProjectSlug
		c := out[key]
		n := int(r.N)
		switch {
		case r.Paused:
			c.Paused += n
		case r.State == "down":
			c.Down += n
		case r.State == "late":
			c.Late += n
		case r.State == "up":
			c.Up += n
		default:
			c.New += n
		}
		out[key] = c
	}
	return out, nil
}
