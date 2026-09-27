# vink

Self-hosted heartbeat and uptime monitor. Jobs ping it, and it probes services. One Go binary with an embedded SQLite database, multi-tenant (orgs, projects, roles), deployable air-gapped. The name is Dutch: *vink* is a finch, and *vinkje* is the checkmark you tick off a list.

Phase 0 is done: heartbeat monitors with period or cron schedules, grace, start/fail/exit signals and run ids; a state machine with incidents; notifications by webhook, ntfy and mail with retries and repeats; a server-rendered web UI; a REST API; a CLI. Pull checks (HTTP, TCP, DNS, TLS, ICMP), status pages and `apply` come in phase 1. `docs/design.md` is the specification.

## Quick start

Build once (Go 1.27 or newer; the binary needs no cgo):

```sh
make build            # ./bin/vink
```

Bootstrap an empty database. This creates the instance admin, the first org, the first project, and prints the project's ping key and a one-time read-write API key:

```sh
printf 'a-long-password\n' | ./bin/vink admin init --org homelab --user j --password-stdin --timezone Europe/Amsterdam --db data/vink.db
```

Start the server (defaults: `:8080`, database `vink.db`, local sign-in on):

```sh
VINK_DB_PATH=data/vink.db ./bin/vink serve -d
```

Sign in at http://localhost:8080 with the user and password from `admin init`. Point the CLI at the server with the API key it printed:

```sh
./bin/vink ctx add local --server http://localhost:8080 --key vk_…
```

Create a heartbeat that expects a ping every minute and goes down one minute after a missed deadline. The web form does the same at Create monitor.

```sh
curl -sS -H "Authorization: Bearer vk_…" -H 'Content-Type: application/json' \
  -d '{"slug":"backup","name":"Nightly backup","schedule":{"period":"60s"},"grace":"60s","tags":["backup"]}' \
  http://localhost:8080/api/v1/monitors
```

Ping it the way a cron job would, with the ping key from `admin init`:

```sh
curl -fsS http://localhost:8080/ping/<ping key>/backup
```

The monitor is `up`. Wait: after 60 seconds without a ping it turns `late`, and 60 seconds later `down`, which opens an incident. Watch it in the UI, or from the shell:

```sh
./bin/vink ls
./bin/vink status          # exits 3 while anything is down
./bin/vink logs backup
```

To receive the alert, add a channel and let the default route send every down and up to it. A webhook to any URL:

```sh
curl -sS -H "Authorization: Bearer vk_…" -H 'Content-Type: application/json' \
  -d '{"name":"hook","kind":"webhook","config":{"url":"https://hooks.example.com/vink"}}' \
  http://localhost:8080/api/v1/channels
```

Or mail, after setting `[smtp] host` in `vink.toml`:

```sh
curl -sS -H "Authorization: Bearer vk_…" -H 'Content-Type: application/json' \
  -d '{"name":"mail","kind":"smtp","config":{"to":["ops@example.com"]}}' \
  http://localhost:8080/api/v1/channels
```

Settings › Channels has a Test button that sends a synthetic notification and shows the error verbatim. Ping again to bring the monitor `up`; the recovery is sent too.

In a crontab, the whole thing is one line:

```cron
0 3 * * * restic backup && curl -fsS http://localhost:8080/ping/<ping key>/backup?create=1
```

`?create=1` creates an unknown monitor with a one-day period and one-hour grace. For jobs on hosts with the CLI, `vink run backup -- restic backup` sends a start ping, runs the command, and sends its exit code and the output tail as the finish ping.

## Signals

| URL | meaning |
| --- | --- |
| `/ping/<key>/<slug>` | ok (GET, POST, HEAD or PUT; the body up to 64 kB is stored) |
| `/ping/<key>/<slug>/start` | the job started; pairs with the next ok or fail through `?rid=` |
| `/ping/<key>/<slug>/fail` | the job failed |
| `/ping/<key>/<slug>/<exit code>` | 0 is ok, anything else is a fail with the code stored |
| `/ping/<key>/<slug>/log` | store a message without touching the state |
| `/ping/id/<monitor id>` | the same by id |

## Operating

- `vink serve --print-config` shows the effective configuration. `docs/deploy/vink.toml.example` lists every key; each is also an environment variable, `VINK_SERVER_LISTEN` for `[server] listen`.
- `docs/deploy/vink.service` is a hardened systemd unit with `DynamicUser` and `StateDirectory=vink`.
- Behind a reverse proxy that authenticates people, turn on `[auth.proxy]` and map groups named `vink:<org>:<role>` to roles; see the auth section of `docs/design.md` for Traefik + Authelia and Apache + Kerberos.
- The database file is the only state. Back it up with `sqlite3 vink.db ".backup vink.bak"`.
- `/api/v1/openapi.yaml` documents the API. Errors are RFC 7807 problems, listed in `docs/errors.md`.
- `-d` on any command switches to debug logging with colour.

## Developing

```sh
make tools       # pinned sqlc, golangci-lint, air, goreleaser and govulncheck into ./bin
make dev         # live reload of vink serve -d
make generate    # sqlc
make migrate     # apply migrations and dump db/schema.sql
make lint        # gofmt, vet, golangci-lint and the tenancy gate
make test        # go test -race ./...
make e2e         # builds the binary and runs the three-minute heartbeat smoke test
make golden      # regenerate the UI golden files from the design system with node
```

Every project-scoped query filters by `project_id`; a request for another tenant's resource is a 404, and `internal/http/crosstenant_test.go` checks every route.
