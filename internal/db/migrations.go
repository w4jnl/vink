package db

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Migrations are dbmate-format files: a timestamped name and
// "-- migrate:up" / "-- migrate:down" sections. The dbmate library is not
// used because its sqlite driver needs cgo; the dbmate binary still works
// on these files for anyone who prefers it.
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// MigrationsDir is the source directory `vink migrate new` writes into.
const MigrationsDir = "internal/db/migrations"

const (
	upMarker   = "-- migrate:up"
	downMarker = "-- migrate:down"
)

var versionRe = regexp.MustCompile(`^(\d+)_[^/]*\.sql$`)

// Migration is one embedded migration file.
type Migration struct {
	Version string
	Name    string
	Path    string
	Applied bool
}

// Migrate applies every pending migration in version order and returns the
// versions it applied. It fails if the database holds a version this binary
// does not know, so an older binary never runs against a newer schema.
func Migrate(ctx context.Context, conn *sql.DB, log *slog.Logger) ([]string, error) {
	if log == nil {
		log = slog.Default()
	}
	if err := ensureTable(ctx, conn); err != nil {
		return nil, err
	}
	list, err := List(ctx, conn)
	if err != nil {
		return nil, err
	}
	applied, err := appliedVersions(ctx, conn)
	if err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(list))
	for _, m := range list {
		known[m.Version] = true
	}
	for v := range applied {
		if !known[v] {
			return nil, fmt.Errorf("database has migration %s which this binary does not know: the database is newer than the binary", v)
		}
	}
	var done []string
	for _, m := range list {
		if m.Applied {
			continue
		}
		raw, err := migrationsFS.ReadFile(m.Path)
		if err != nil {
			return done, fmt.Errorf("read %s: %w", m.Path, err)
		}
		up, err := extractSection(string(raw), upMarker)
		if err != nil {
			return done, fmt.Errorf("migration %s: %w", m.Path, err)
		}
		if err := applyOne(ctx, conn, m.Version, up, true); err != nil {
			return done, fmt.Errorf("apply %s: %w", m.Path, err)
		}
		log.Info("migration applied", "version", m.Version, "name", m.Name)
		done = append(done, m.Version)
	}
	return done, nil
}

// Rollback reverts the most recently applied migration using its down section.
func Rollback(ctx context.Context, conn *sql.DB, log *slog.Logger) (string, error) {
	if log == nil {
		log = slog.Default()
	}
	if err := ensureTable(ctx, conn); err != nil {
		return "", err
	}
	list, err := List(ctx, conn)
	if err != nil {
		return "", err
	}
	var last *Migration
	for i := range list {
		if list[i].Applied {
			last = &list[i]
		}
	}
	if last == nil {
		return "", errors.New("no applied migrations")
	}
	raw, err := migrationsFS.ReadFile(last.Path)
	if err != nil {
		return "", err
	}
	down, err := extractSection(string(raw), downMarker)
	if err != nil {
		return "", fmt.Errorf("migration %s: %w", last.Path, err)
	}
	if err := applyOne(ctx, conn, last.Version, down, false); err != nil {
		return "", fmt.Errorf("rollback %s: %w", last.Path, err)
	}
	log.Info("migration rolled back", "version", last.Version, "name", last.Name)
	return last.Version, nil
}

// List returns every embedded migration with its applied flag.
func List(ctx context.Context, conn *sql.DB) ([]Migration, error) {
	files, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return nil, fmt.Errorf("glob migrations: %w", err)
	}
	sort.Strings(files)
	applied, err := appliedVersions(ctx, conn)
	if err != nil {
		return nil, err
	}
	out := make([]Migration, 0, len(files))
	for _, path := range files {
		base := filepath.Base(path)
		m := versionRe.FindStringSubmatch(base)
		if m == nil {
			return nil, fmt.Errorf("migration %s: name must be <version>_<name>.sql", base)
		}
		name := strings.TrimSuffix(strings.TrimPrefix(base, m[1]+"_"), ".sql")
		out = append(out, Migration{Version: m[1], Name: name, Path: path, Applied: applied[m[1]]})
	}
	return out, nil
}

// NewMigration writes an empty migration file into dir and returns its path.
func NewMigration(dir, name string, now time.Time) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	name = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(name, "_")
	name = strings.Trim(name, "_")
	if name == "" {
		return "", errors.New("migration name must not be empty")
	}
	path := filepath.Join(dir, now.UTC().Format("20060102150405")+"_"+name+".sql")
	body := upMarker + "\n\n\n" + downMarker + "\n\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// Dump renders the schema the way `dbmate dump` does: every CREATE statement
// in creation order, then the schema_migrations rows. The output is sqlc's
// schema input.
func Dump(ctx context.Context, conn *sql.DB) ([]byte, error) {
	rows, err := conn.QueryContext(ctx, `SELECT sql FROM sqlite_master WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%' ORDER BY rowid`)
	if err != nil {
		return nil, fmt.Errorf("read sqlite_master: %w", err)
	}
	defer rows.Close()
	var buf bytes.Buffer
	for rows.Next() {
		var stmt string
		if err := rows.Scan(&stmt); err != nil {
			return nil, err
		}
		buf.WriteString(stmt)
		buf.WriteString(";\n")
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	applied, err := appliedVersions(ctx, conn)
	if err != nil {
		return nil, err
	}
	versions := make([]string, 0, len(applied))
	for v := range applied {
		versions = append(versions, v)
	}
	sort.Strings(versions)
	if len(versions) > 0 {
		buf.WriteString("-- Dbmate schema migrations\nINSERT INTO \"schema_migrations\" (version) VALUES\n")
		for i, v := range versions {
			sep := ","
			if i == len(versions)-1 {
				sep = ";"
			}
			fmt.Fprintf(&buf, "  ('%s')%s\n", v, sep)
		}
	}
	return buf.Bytes(), nil
}

func ensureTable(ctx context.Context, conn *sql.DB) error {
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS "schema_migrations" (version varchar(128) primary key)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	return nil
}

// appliedVersions works on a read-only connection: a missing tracking table
// means nothing has been applied.
func appliedVersions(ctx context.Context, conn *sql.DB) (map[string]bool, error) {
	var exists int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&exists); err != nil {
		return nil, fmt.Errorf("check schema_migrations: %w", err)
	}
	if exists == 0 {
		return map[string]bool{}, nil
	}
	rows, err := conn.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = true
	}
	return out, rows.Err()
}

// applyOne runs one migration section in a transaction. foreign_keys is
// switched off around it, as SQLite's table-rebuild recipe requires, and
// foreign_key_check must come back empty before the commit.
func applyOne(ctx context.Context, conn *sql.DB, version, body string, up bool) error {
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable foreign_keys: %w", err)
	}
	defer func() { _, _ = conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`) }()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after commit
	if _, err := tx.ExecContext(ctx, body); err != nil {
		return err
	}
	var violations int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil {
		return fmt.Errorf("foreign_key_check: %w", err)
	}
	if violations > 0 {
		return fmt.Errorf("foreign_key_check found %d violation(s)", violations)
	}
	if up {
		_, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES (?)`, version)
	} else {
		_, err = tx.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = ?`, version)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

// extractSection returns the SQL of one section. A file without markers is
// treated as an up section only; an empty section is an error.
func extractSection(content, marker string) (string, error) {
	if !strings.Contains(content, upMarker) {
		if marker == downMarker {
			return "", errors.New("no down section")
		}
		body := strings.TrimSpace(content)
		if body == "" {
			return "", errors.New("empty migration")
		}
		return body, nil
	}
	i := strings.Index(content, marker)
	if i < 0 {
		return "", fmt.Errorf("no %q section", strings.TrimPrefix(marker, "-- migrate:"))
	}
	body := content[i+len(marker):]
	other := downMarker
	if marker == downMarker {
		other = upMarker
	}
	if j := strings.Index(body, other); j >= 0 {
		body = body[:j]
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return "", fmt.Errorf("empty %q section", strings.TrimPrefix(marker, "-- migrate:"))
	}
	return body, nil
}
