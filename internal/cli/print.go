package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/fatih/color"
)

// Glyphs from the brand guide: every state has a shape as well as a word.
var glyphs = map[string]string{"up": "●", "late": "◐", "down": "◆", "paused": "‖", "new": "◌"}

var stateColors = map[string]*color.Color{
	"up":     color.RGB(0x5F, 0xC4, 0x7A),
	"late":   color.RGB(0xE8, 0x96, 0x3C),
	"down":   color.RGB(0xF0, 0x64, 0x5A),
	"paused": color.RGB(0x8A, 0x99, 0x9C),
	"new":    color.RGB(0x8A, 0x99, 0x9C),
}

// Printer writes tables, with colour only when asked.
type Printer struct {
	Out   io.Writer
	Color bool
}

// State renders a state with its glyph, coloured when enabled.
func (p *Printer) State(s string) string {
	g, ok := glyphs[s]
	if !ok {
		g = "?"
	}
	text := g + " " + s
	if p.Color {
		if c, ok := stateColors[s]; ok {
			return c.Sprint(text)
		}
	}
	return text
}

// Table prints aligned columns.
func (p *Printer) Table(header []string, rows [][]string) {
	tw := tabwriter.NewWriter(p.Out, 0, 4, 2, ' ', 0)
	if len(header) > 0 {
		fmt.Fprintln(tw, strings.Join(header, "\t"))
	}
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	_ = tw.Flush()
}

// KV prints aligned key-value lines.
func (p *Printer) KV(pairs [][2]string) {
	tw := tabwriter.NewWriter(p.Out, 0, 4, 2, ' ', 0)
	for _, kv := range pairs {
		fmt.Fprintf(tw, "%s\t%s\n", kv[0], kv[1])
	}
	_ = tw.Flush()
}

// JSON writes the raw API payload unchanged, with a trailing newline.
func (p *Printer) JSON(raw []byte) {
	_, _ = p.Out.Write(raw)
	if len(raw) == 0 || raw[len(raw)-1] != '\n' {
		_, _ = io.WriteString(p.Out, "\n")
	}
}
