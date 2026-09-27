package main

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/w4jnl/vink/internal/cli"
	"github.com/w4jnl/vink/internal/timefmt"
)

func newLsCmd(g *globals) *cobra.Command {
	f := &clientFlags{g: g}
	var tag, state string
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List monitors",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, _, _, p, err := f.connect(cmd)
			if err != nil {
				return err
			}
			q := url.Values{}
			if tag != "" {
				q.Set("tag", tag)
			}
			if state != "" {
				q.Set("state", state)
			}
			path := "/monitors?limit=200"
			if len(q) > 0 {
				path += "&" + q.Encode()
			}
			items, raw, err := listAll[monitor](cmd.Context(), c, path)
			if err != nil {
				return err
			}
			if f.asJSON {
				p.JSON(raw)
				return nil
			}
			now := time.Now()
			rows := make([][]string, 0, len(items))
			for _, m := range items {
				next := "-"
				switch {
				case m.Paused:
					next = "paused"
				case m.ExpectedAt != nil:
					next = timefmt.In(*m.ExpectedAt, now)
				}
				rows = append(rows, []string{m.Slug, m.Kind, p.State(m.State), timefmt.Span(now.Sub(m.StateSince)), next, fmtTime(m.LastObsAt, now, timefmt.Ago), strings.Join(m.Tags, ",")})
			}
			if len(rows) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "no monitors")
				return nil
			}
			p.Table([]string{"SLUG", "KIND", "STATE", "SINCE", "NEXT DUE", "LAST PING", "TAGS"}, rows)
			return nil
		},
	}
	f.add(cmd, true)
	cmd.Flags().StringVar(&tag, "tag", "", "only monitors with this tag")
	cmd.Flags().StringVar(&state, "state", "", "only monitors in this state")
	return cmd
}

type event struct {
	At     time.Time `json:"at"`
	From   string    `json:"from"`
	To     string    `json:"to"`
	Reason string    `json:"reason"`
}

func newGetCmd(g *globals) *cobra.Command {
	f := &clientFlags{g: g}
	cmd := &cobra.Command{
		Use:   "get <slug>",
		Short: "Show a monitor and its last ten events",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, p, err := f.connect(cmd)
			if err != nil {
				return err
			}
			var m monitor
			raw, err := c.DoRaw(cmd.Context(), "GET", "/monitors/"+url.PathEscape(args[0]), nil, &m)
			if err != nil {
				return err
			}
			if f.asJSON {
				p.JSON(raw)
				return nil
			}
			now := time.Now()
			since := timefmt.Span(now.Sub(m.StateSince))
			pairs := [][2]string{
				{"slug", m.Slug}, {"name", m.Name}, {"kind", m.Kind}, {"state", p.State(m.State) + " for " + since},
				{"schedule", m.schedule()}, {"grace", m.Grace}, {"timezone", m.Timezone},
				{"last ping", fmtTime(m.LastObsAt, now, timefmt.Ago)}, {"next due", fmtTime(m.ExpectedAt, now, timefmt.In)},
				{"tags", strings.Join(m.Tags, ", ")}, {"ping url", m.PingURL},
			}
			p.KV(pairs)
			var events pageOf[event]
			if err := c.Do(cmd.Context(), "GET", "/monitors/"+url.PathEscape(args[0])+"/events?limit=10", nil, &events); err != nil {
				return err
			}
			if len(events.Items) > 0 {
				fmt.Fprintln(cmd.OutOrStdout())
				rows := make([][]string, 0, len(events.Items))
				for _, e := range events.Items {
					rows = append(rows, []string{timefmt.Ago(e.At, now), e.From + " → " + p.State(e.To), e.Reason})
				}
				p.Table([]string{"WHEN", "CHANGE", "REASON"}, rows)
			}
			return nil
		},
	}
	f.add(cmd, true)
	return cmd
}

type observation struct {
	ID         string         `json:"id"`
	At         time.Time      `json:"at"`
	Signal     string         `json:"signal"`
	OK         bool           `json:"ok"`
	ExitCode   *int64         `json:"exit_code"`
	DurationMs *int64         `json:"duration_ms"`
	RemoteAddr string         `json:"remote_addr"`
	HasBody    bool           `json:"has_body"`
	Detail     map[string]any `json:"detail"`
}

func (o observation) text() string {
	s := o.Signal
	if o.Signal == "exit" && o.ExitCode != nil {
		s = "exit " + strconv.FormatInt(*o.ExitCode, 10)
	}
	if r, ok := o.Detail["reason"].(string); ok && r != "" {
		s += " · " + strings.ReplaceAll(r, "_", " ")
	}
	if msg, ok := o.Detail["msg"].(string); ok && msg != "" {
		s += " · " + msg
	}
	return s
}

func newLogsCmd(g *globals) *cobra.Command {
	f := &clientFlags{g: g}
	var n int
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs <slug>",
		Short: "Show observations, newest first; --follow polls every 5s",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, p, err := f.connect(cmd)
			if err != nil {
				return err
			}
			base := "/monitors/" + url.PathEscape(args[0]) + "/observations"
			var page pageOf[observation]
			raw, err := c.DoRaw(cmd.Context(), "GET", base+"?limit="+strconv.Itoa(n), nil, &page)
			if err != nil {
				return err
			}
			if f.asJSON && !follow {
				p.JSON(raw)
				return nil
			}
			print := func(items []observation, newestFirst bool) {
				rows := make([][]string, 0, len(items))
				for _, o := range items {
					state := "down"
					switch {
					case o.OK:
						state = "up"
					case o.Signal == "start" || o.Signal == "log":
						state = "new"
					}
					right := ""
					if o.DurationMs != nil {
						right = timefmt.RunDuration(*o.DurationMs)
					} else if o.HasBody {
						right = "body"
					}
					rows = append(rows, []string{o.At.Local().Format("2006-01-02 15:04:05"), p.State(state), o.text(), right, o.RemoteAddr})
				}
				if !newestFirst {
					for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
						rows[i], rows[j] = rows[j], rows[i]
					}
				}
				p.Table(nil, rows)
			}
			print(page.Items, true)
			if !follow {
				return nil
			}
			last := time.Time{}
			if len(page.Items) > 0 {
				last = page.Items[0].At
			}
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-cmd.Context().Done():
					return nil
				case <-ticker.C:
				}
				var fresh pageOf[observation]
				path := base + "?limit=50"
				if !last.IsZero() {
					path += "&since=" + url.QueryEscape(last.Add(time.Millisecond).Format(time.RFC3339Nano))
				}
				if err := c.Do(cmd.Context(), "GET", path, nil, &fresh); err != nil {
					return err
				}
				if len(fresh.Items) > 0 {
					print(fresh.Items, false)
					last = fresh.Items[0].At
				}
			}
		},
	}
	f.add(cmd, true)
	cmd.Flags().IntVarP(&n, "lines", "n", 50, "how many observations")
	cmd.Flags().BoolVar(&follow, "follow", false, "keep polling for new observations")
	return cmd
}

func newActionCmd(g *globals, use, short, verb string) *cobra.Command {
	f := &clientFlags{g: g}
	cmd := &cobra.Command{
		Use:   use + " <slug>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, _, err := f.connect(cmd)
			if err != nil {
				return err
			}
			var m monitor
			if err := c.Do(cmd.Context(), "POST", "/monitors/"+url.PathEscape(args[0])+"/"+verb, nil, &m); err != nil {
				return err
			}
			if !f.quiet {
				fmt.Fprintf(cmd.OutOrStdout(), "%s %s: %s\n", verb+"d", m.Slug, m.State)
			}
			return nil
		},
	}
	f.add(cmd, false)
	return cmd
}

func newAckCmd(g *globals) *cobra.Command {
	f := &clientFlags{g: g}
	cmd := &cobra.Command{
		Use:   "ack <incident id>",
		Short: "Acknowledge an incident, which silences repeat notifications",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, _, _, _, err := f.connect(cmd)
			if err != nil {
				return err
			}
			var inc struct {
				ID      string `json:"id"`
				Monitor string `json:"monitor"`
			}
			if err := c.Do(cmd.Context(), "POST", "/incidents/"+url.PathEscape(args[0])+"/ack", nil, &inc); err != nil {
				return err
			}
			if !f.quiet {
				fmt.Fprintf(cmd.OutOrStdout(), "acknowledged incident %s (%s)\n", inc.ID, inc.Monitor)
			}
			return nil
		},
	}
	f.add(cmd, false)
	return cmd
}

type statusPayload struct {
	Counts        map[string]int `json:"counts"`
	Total         int            `json:"total"`
	OpenIncidents []struct {
		ID       string    `json:"id"`
		Monitor  string    `json:"monitor"`
		OpenedAt time.Time `json:"opened_at"`
		AckedBy  string    `json:"acked_by"`
	} `json:"open_incidents"`
	OldestLate *time.Time `json:"oldest_late_since"`
}

func newStatusCmd(g *globals) *cobra.Command {
	f := &clientFlags{g: g}
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Project summary; exits 3 when any monitor is down",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, _, _, p, err := f.connect(cmd)
			if err != nil {
				return err
			}
			var st statusPayload
			raw, err := c.DoRaw(cmd.Context(), "GET", "/status", nil, &st)
			if err != nil {
				return err
			}
			down := st.Counts["down"] > 0
			if f.asJSON {
				p.JSON(raw)
			} else if !f.quiet {
				parts := []string{fmt.Sprintf("%d monitors", st.Total)}
				for _, s := range []string{"up", "late", "down", "paused", "new"} {
					if n := st.Counts[s]; n > 0 {
						parts = append(parts, p.State(s)+" "+strconv.Itoa(n))
					}
				}
				fmt.Fprintln(cmd.OutOrStdout(), "[✓▁] vink  ·  "+strings.Join(parts, "  ·  "))
				now := time.Now()
				for _, inc := range st.OpenIncidents {
					acked := ""
					if inc.AckedBy != "" {
						acked = "  acked by " + inc.AckedBy
					}
					fmt.Fprintf(cmd.OutOrStdout(), "  %s %s down for %s%s  (%s)\n", p.State("down"), inc.Monitor, timefmt.Span(now.Sub(inc.OpenedAt)), acked, inc.ID)
				}
			}
			if down {
				return &cli.ExitError{Code: cli.ExitDown, Err: fmt.Errorf("%d monitor(s) down", st.Counts["down"])}
			}
			return nil
		},
	}
	f.add(cmd, true)
	return cmd
}
