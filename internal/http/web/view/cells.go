package view

import (
	"time"

	"github.com/w4jnl/vink/internal/domain"
)

// HourCells returns 24 cells, oldest first, with the worst state seen in
// each hour of the last day: up, late, down, or none when the monitor
// did not exist or was paused or new.
func HourCells(now, createdAt time.Time, current domain.State, events []*domain.Event) []string {
	const hours = 24
	start := now.Add(-hours * time.Hour)
	// state at the start of the window
	state := current
	for _, e := range events {
		if !e.At.Before(start) {
			state = e.From
			break
		}
	}
	cells := make([]string, hours)
	ei := 0
	for i := range hours {
		bucketStart := start.Add(time.Duration(i) * time.Hour)
		bucketEnd := bucketStart.Add(time.Hour)
		if !bucketEnd.After(createdAt) {
			cells[i] = "none"
			for ei < len(events) && events[ei].At.Before(bucketEnd) {
				state = events[ei].To
				ei++
			}
			continue
		}
		worst := rank(state)
		for ei < len(events) && events[ei].At.Before(bucketEnd) {
			if events[ei].At.Before(bucketStart) {
				ei++
				continue
			}
			state = events[ei].To
			if r := rank(state); r > worst {
				worst = r
			}
			ei++
		}
		cells[i] = word(worst)
	}
	return cells
}

func rank(s domain.State) int {
	switch s {
	case domain.StateUp:
		return 1
	case domain.StateLate:
		return 2
	case domain.StateDown:
		return 3
	}
	return 0
}

func word(r int) string {
	switch r {
	case 1:
		return "up"
	case 2:
		return "late"
	case 3:
		return "down"
	}
	return "none"
}

// UpShare renders the share of cells that are up as "98.6% up".
func UpShare(cells []string) string {
	known, up := 0, 0
	for _, c := range cells {
		if c == "none" {
			continue
		}
		known++
		if c == "up" {
			up++
		}
	}
	if known == 0 {
		return "no data"
	}
	pct := float64(up) * 100 / float64(known)
	return formatPct(pct) + " up"
}

func formatPct(p float64) string {
	s := time.Duration(0)
	_ = s
	whole := int(p)
	tenth := int(p*10+0.5) % 10
	if p >= 100 {
		return "100%"
	}
	return itoa(whole) + "." + itoa(tenth) + "%"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
