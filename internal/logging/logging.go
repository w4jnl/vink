// Package logging builds the process logger: JSON by default, a colourised
// text handler in debug mode, with colour decided by the --color flag, a TTY
// check and NO_COLOR.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/SladkyCitron/slogcolor"
	"github.com/fatih/color"
)

// ColorMode is the value of the --color flag.
type ColorMode int

const (
	ColorAuto ColorMode = iota
	ColorAlways
	ColorNever
)

// ParseColorMode parses auto, always or never.
func ParseColorMode(s string) (ColorMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "auto":
		return ColorAuto, nil
	case "always":
		return ColorAlways, nil
	case "never":
		return ColorNever, nil
	}
	return ColorAuto, fmt.Errorf("invalid --color %q: use auto, always or never", s)
}

// Options control the handler.
type Options struct {
	// Debug forces level debug and the colourised text handler.
	Debug bool
	// Level is the configured level when not in debug mode: debug, info, warn, error.
	Level string
	// Format is json or text when not in debug mode.
	Format string
	// Color decides whether the text handler colours output.
	Color ColorMode
	// Out defaults to os.Stderr.
	Out io.Writer
}

// New returns a logger built from opts.
func New(opts Options) *slog.Logger {
	out := opts.Out
	if out == nil {
		out = os.Stderr
	}
	level := parseLevel(opts.Level)
	if opts.Debug {
		level = slog.LevelDebug
	}
	if opts.Debug || strings.EqualFold(opts.Format, "text") {
		noColor := !ColorEnabled(opts.Color, out)
		// fatih/color decides globally from os.Stdout; our decision wins.
		color.NoColor = noColor
		o := slogcolor.DefaultOptions
		o.Level = level
		o.NoColor = noColor
		o.TimeFormat = "15:04:05.000"
		o.LevelTags = levelTags()
		if opts.Debug {
			o.SrcFileMode = slogcolor.ShortFile
		} else {
			o.SrcFileMode = slogcolor.Nop
		}
		return slog.New(slogcolor.NewHandler(out, o))
	}
	return slog.New(slog.NewJSONHandler(out, &slog.HandlerOptions{Level: level}))
}

// ColorEnabled reports whether output to w should be coloured under mode.
// Auto means: w is a terminal and NO_COLOR is unset.
func ColorEnabled(mode ColorMode, w io.Writer) bool {
	switch mode {
	case ColorAlways:
		return true
	case ColorNever:
		return false
	}
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return false
	}
	return IsTerminal(w)
}

// IsTerminal reports whether w is a character device such as a TTY.
func IsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

// levelTags maps levels to the brand palette: debug ink-muted, info accent,
// warn late, error down.
func levelTags() map[slog.Level]string {
	return map[slog.Level]string{
		slog.LevelDebug: color.RGB(0x8A, 0x99, 0x9C).Sprint("DEBUG"),
		slog.LevelInfo:  color.RGB(0x3F, 0xD0, 0xD4).Sprint("INFO "),
		slog.LevelWarn:  color.RGB(0xE8, 0x96, 0x3C).Sprint("WARN "),
		slog.LevelError: color.RGB(0xF0, 0x64, 0x5A).Sprint("ERROR"),
	}
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

type ctxKey struct{}

// WithLogger stores l in ctx.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// FromContext returns the logger stored by WithLogger, or slog.Default.
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}

// Sub returns a logger tagged with a subsystem name.
func Sub(l *slog.Logger, name string) *slog.Logger { return l.With("sys", name) }
