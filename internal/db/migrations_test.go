package db

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func openTemp(t *testing.T) *DB {
	t.Helper()
	d, err := Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func TestMigrateAppliesOnceAndIsIdempotent(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()
	first, err := Migrate(ctx, d.Writer, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if len(first) == 0 {
		t.Fatal("expected at least one migration applied")
	}
	second, err := Migrate(ctx, d.Writer, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 0 {
		t.Fatalf("second run applied %v", second)
	}
	list, err := List(ctx, d.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range list {
		if !m.Applied {
			t.Errorf("%s not applied", m.Version)
		}
	}
	var n int
	if err := d.Reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name <> 'schema_migrations'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 24 {
		t.Errorf("expected 24 tables, got %d", n)
	}
}

func TestPragmas(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()
	var mode string
	if err := d.Writer.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q", mode)
	}
	var fk int
	if err := d.Writer.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Error("foreign_keys must be on")
	}
	if _, err := d.Reader.ExecContext(ctx, `CREATE TABLE nope (x)`); err == nil {
		t.Error("reader pool must be query_only")
	}
}

func TestForeignKeysCascadeAfterMigrate(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()
	if _, err := Migrate(ctx, d.Writer, quiet()); err != nil {
		t.Fatal(err)
	}
	// foreign_keys was toggled around the migration; it must be on again.
	var fk int
	if err := d.Writer.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Fatal("foreign_keys off after migrate")
	}
	if _, err := d.Writer.ExecContext(ctx, `INSERT INTO projects (id, org_id, slug, name, ping_key, created_at) VALUES ('p', 'missing', 's', 'n', 'k', 0)`); err == nil {
		t.Error("expected foreign key violation")
	}
}

func TestRollbackAndDump(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()
	if _, err := Migrate(ctx, d.Writer, quiet()); err != nil {
		t.Fatal(err)
	}
	dump, err := Dump(ctx, d.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s := string(dump)
	if !strings.Contains(s, "CREATE TABLE monitors") || !strings.Contains(s, "Dbmate schema migrations") || !strings.Contains(s, "('20260927000000')") {
		t.Errorf("dump missing parts:\n%s", s)
	}
	if !strings.Contains(s, "('20260930000000')") || !strings.Contains(s, "CREATE TABLE route_channels") {
		t.Errorf("dump missing the route_channels migration:\n%s", s)
	}
	// rolling back walks the migrations newest first, down to nothing
	for _, want := range []string{"20261006000000", "20261005000000", "20261004000000", "20261003000000", "20261002000000", "20261001000000", "20260930000000", "20260927000000"} {
		v, err := Rollback(ctx, d.Writer, quiet())
		if err != nil {
			t.Fatal(err)
		}
		if v != want {
			t.Errorf("rolled back %s, want %s", v, want)
		}
	}
	var n int
	if err := d.Reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='monitors'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("monitors table still present after rollback")
	}
	if _, err := Rollback(ctx, d.Writer, quiet()); err == nil {
		t.Error("rollback on empty schema must fail")
	}
}

// TestOrgStatusPagesMigration: the rebuild keeps a project's page and
// fills its org; rolling back keeps project pages and drops org pages,
// which have no project to go back to.
func TestOrgStatusPagesMigration(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()
	if _, err := Migrate(ctx, d.Writer, quiet()); err != nil {
		t.Fatal(err)
	}
	if v, err := Rollback(ctx, d.Writer, quiet()); err != nil || v != "20261006000000" {
		t.Fatalf("rollback: %s %v", v, err)
	}
	exec := func(q string) {
		t.Helper()
		if _, err := d.Writer.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	exec(`INSERT INTO orgs (id, slug, name, created_at) VALUES ('o', 'o', 'O', 0)`)
	exec(`INSERT INTO projects (id, org_id, slug, name, ping_key, created_at) VALUES ('p', 'o', 'p', 'P', 'k', 0)`)
	exec(`INSERT INTO status_pages (id, project_id, slug, title, match_tags, public, created_at, updated_at) VALUES ('sp', 'p', 'home', 'Home', '["prod"]', 1, 0, 0)`)
	if _, err := Migrate(ctx, d.Writer, quiet()); err != nil {
		t.Fatal(err)
	}
	var org, project, tags, projects, groupBy, incidents string
	err := d.Reader.QueryRowContext(ctx, `SELECT org_id, project_id, match_tags, projects, group_by, incidents FROM status_pages WHERE id = 'sp'`).
		Scan(&org, &project, &tags, &projects, &groupBy, &incidents)
	if err != nil || org != "o" || project != "p" || tags != `["prod"]` || projects != "[]" || groupBy != "tag" || incidents != "open" {
		t.Fatalf("page after the rebuild: %v %s %s %s %s %s %s", err, org, project, tags, projects, groupBy, incidents)
	}
	exec(`INSERT INTO status_pages (id, org_id, slug, title, projects, group_by, incidents, created_at, updated_at) VALUES ('op', 'o', 'all', 'All', '["p"]', 'project', '30d', 0, 0)`)
	if _, err := d.Writer.ExecContext(ctx, `INSERT INTO status_pages (id, org_id, project_id, slug, title, group_by, created_at, updated_at) VALUES ('bad', 'o', 'p', 'bad', 'Bad', 'project', 0, 0)`); err == nil {
		t.Error("a project page grouped by project must be refused")
	}
	if _, err := Rollback(ctx, d.Writer, quiet()); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := d.Reader.QueryRowContext(ctx, `SELECT COUNT(*) FROM status_pages`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("pages after rolling back: %d %v", n, err)
	}
}

func TestMigrateRefusesNewerDatabase(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()
	if _, err := Migrate(ctx, d.Writer, quiet()); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Writer.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES ('99990101000000')`); err != nil {
		t.Fatal(err)
	}
	if _, err := Migrate(ctx, d.Writer, quiet()); err == nil || !strings.Contains(err.Error(), "newer than the binary") {
		t.Fatalf("expected newer-database error, got %v", err)
	}
}

func TestExtractSection(t *testing.T) {
	cases := []struct {
		name, in, marker, want string
		err                    bool
	}{
		{"up", "-- migrate:up\nCREATE TABLE a (x);\n-- migrate:down\nDROP TABLE a;\n", upMarker, "CREATE TABLE a (x);", false},
		{"down", "-- migrate:up\nCREATE TABLE a (x);\n-- migrate:down\nDROP TABLE a;\n", downMarker, "DROP TABLE a;", false},
		{"no markers", "CREATE TABLE a (x);", upMarker, "CREATE TABLE a (x);", false},
		{"no markers down", "CREATE TABLE a (x);", downMarker, "", true},
		{"empty up", "-- migrate:up\n\n-- migrate:down\nDROP TABLE a;", upMarker, "", true},
		{"missing down", "-- migrate:up\nCREATE TABLE a (x);", downMarker, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := extractSection(c.in, c.marker)
			if c.err != (err != nil) {
				t.Fatalf("err = %v, want error %v", err, c.err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestNewMigration(t *testing.T) {
	dir := t.TempDir()
	path, err := NewMigration(dir, "Add Widgets!", time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "20260927120000_add_widgets.sql" {
		t.Errorf("path = %s", path)
	}
	if _, err := NewMigration(dir, "!!!", time.Now()); err == nil {
		t.Error("expected error for empty name")
	}
}

func TestTxCommitsAndRollsBack(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()
	if _, err := Migrate(ctx, d.Writer, quiet()); err != nil {
		t.Fatal(err)
	}
	err := d.Tx(ctx, func(q *Queries) error {
		_, err := q.CreateOrg(ctx, CreateOrgParams{ID: "o1", Slug: "one", Name: "One", CreatedAt: 1})
		if err != nil {
			return err
		}
		return context.Canceled
	})
	if err == nil {
		t.Fatal("expected fn error")
	}
	if _, err := d.Read().GetOrg(ctx, "o1"); !IsNotFound(err) {
		t.Errorf("row must not exist after rollback, got %v", err)
	}
	if err := d.Tx(ctx, func(q *Queries) error {
		_, err := q.CreateOrg(ctx, CreateOrgParams{ID: "o1", Slug: "one", Name: "One", CreatedAt: 1})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Read().GetOrg(ctx, "o1"); err != nil {
		t.Errorf("row must exist after commit: %v", err)
	}
	err = d.Tx(ctx, func(q *Queries) error {
		_, err := q.CreateOrg(ctx, CreateOrgParams{ID: "o2", Slug: "one", Name: "Dup", CreatedAt: 1})
		return err
	})
	if !IsUniqueViolation(err) {
		t.Errorf("expected unique violation, got %v", err)
	}
}
