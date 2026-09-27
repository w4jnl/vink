// Package dbtest opens a migrated temporary database for tests.
package dbtest

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/w4jnl/vink/internal/db"
	"github.com/w4jnl/vink/internal/secrets"
)

// Open returns a migrated database in a temp directory, closed on cleanup.
func Open(t testing.TB) *db.DB {
	t.Helper()
	secrets.FastParamsForTests()
	path := filepath.Join(t.TempDir(), "vink.db")
	d, err := db.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := db.Migrate(context.Background(), d.Writer, quiet); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return d
}
