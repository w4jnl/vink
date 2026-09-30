# vink

Self-hosted heartbeat and uptime monitor: jobs ping it (push), and it probes services (pull). One Go binary with embedded SQLite, multi-tenant (orgs → projects → roles), deployable air-gapped. Repo: github.com/w4jnl/vink, sibling of github.com/w4jnl/flok.

## Read first
- `docs/design.md` is the spec: architecture, data model, state machine, ping ingress, API, CLI, auth, notifiers, config, security, and the phase task lists, with diagrams in `docs/diagrams/`. It is the source of truth; ask before deviating from it.
- `docs/design-system/README.md` is the brand and UI rules. `tokens.css` holds every colour, space and radius. `components/<Name>/README.md` says how each UI part behaves. `components/bundle.js` returns the exact HTML each part must render, and `components/bundle.css` styles it. `screens/README.md` lists every designed screen with its route and the behaviour the pictures can't show; `screens/*.html` hold the exact markup and `screens/*-dark.png` / `*-light.png` the look.
- `assets/brand/` holds logos, favicons and the state/kind icons.

## House conventions
- Go 1.23+, stdlib first, `internal/` packages, no global state, `context.Context` on every I/O path.
- Logging: `log/slog`. JSON by default; `-d`/`--debug` on every command switches to debug level with colour via `github.com/SladkyCitron/slogcolor` (the module formerly at MatusOllah/slogcolor) (off when not a TTY or `NO_COLOR` is set).
- HTTP: `net/http` ServeMux with method patterns; middleware as `func(http.Handler) http.Handler`.
- Database: SQLite via `modernc.org/sqlite` (no cgo), WAL, one writer connection. All SQL goes through `sqlc`, with queries in `internal/db/queries/`. Migrations are dbmate-format files (`-- migrate:up/down`), embedded and applied by our own runner in `internal/db/migrations.go`; the dbmate library is never imported (its sqlite driver needs cgo). `vink migrate dump` writes `db/schema.sql`, which is sqlc's schema input.
- Web UI: `html/template` + htmx 4 (vendored), no JS framework, no build step, no CDN or external URLs anywhere. Embed `static/` (tokens.css, bundle.css, htmx, fonts, icon sprite) with `embed.FS`.
- CLI: `spf13/cobra`; `--json` on every read command.
- Errors: wrap with `%w`; HTTP errors are RFC 7807 problem+json.
- Tests: table-driven, `httptest`, a temp SQLite per test, no DB mocks. Run `-race` in CI.

## Rules
- Build one phase at a time (phase 0 first) and stop at its gate for review. Commit after each task on the phase list, with a clear message.
- Tenancy is structural. Every project-scoped query filters by `project_id`, and a cross-tenant request returns 404. Keep the cross-tenant test suite green.
- UI markup must match what `docs/design-system/components/bundle.js` returns for that component; take styles from `bundle.css` and `tokens.css`. Do not add screens, modals, colours or components beyond the design system without asking.
- Never make outbound network calls unless configuration asks for them: no telemetry, update checks or CDN fonts.
- Before calling a task done: `make generate` (sqlc) is clean, `make lint` passes, `make test` passes.

## Commands (create these in phase 0)
- `make dev`: air live reload of `vink serve -d`
- `make generate`: sqlc
- `make migrate`: dbmate up + dump
- `make lint`: golangci-lint
- `make test`: go test -race ./...
- `make e2e`: heartbeat smoke test from docs/design.md
