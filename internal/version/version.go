// Package version holds the build metadata injected at link time and the
// brand lockup printed by `vink version` and `vink doctor`.
package version

import (
	"fmt"
	"io"
	"runtime"
)

// Set with -ldflags "-X github.com/w4jnl/vink/internal/version.Version=…".
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// Tagline is the product line under the wordmark.
const Tagline = "heartbeat and uptime monitor"

// String returns the one-line version used in logs and User-Agent headers.
func String() string {
	return fmt.Sprintf("vink %s (%s, %s, %s)", Version, Commit, Date, runtime.Version())
}

// Lockup writes the ASCII lockup from the brand guide. The licence line is
// added once the repository has a licence.
func Lockup(w io.Writer) error {
	_, err := fmt.Fprintf(w, "  ╭─────────╮\n  │   ✓     │   vink %s\n  │   ▁     │   %s\n  ╰─────────╯   w4j.nl\n", Version, Tagline)
	return err
}
