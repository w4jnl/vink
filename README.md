# vink

Self-hosted heartbeat and uptime monitor. Jobs ping it, and it probes services. One Go binary with an embedded SQLite database, multi-tenant (orgs, projects, roles), deployable air-gapped. The name is Dutch: *vink* is a finch, and *vinkje* is the checkmark you tick off a list.

Phase 1 is done: heartbeat monitors (period or cron, grace, start/fail/exit signals, run ids) and pull checks (HTTP with status, body and JSON path matching, TCP with banners, DNS, TLS expiry, ICMP); one state machine with incidents, confirm retries and maintenance windows; notifications by mail, webhook, ntfy, Gotify, Matrix, Slack-compatible hooks and Alertmanager with retries and repeats; public status pages with badges; a declarative `apply` file with `export`; Prometheus metrics; a server-rendered web UI; a REST API; a CLI. Probe agents for closed networks come in phase 2. `docs/design.md` is the specification.

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

## Pull checks

A pull monitor is created like a heartbeat, with its kind's block instead of a schedule. The server runs it every `interval` with `timeout` per attempt, retries a failed attempt `confirm.retries` times before it counts, turns the monitor `late` on the first counted failure and `down` after `failure_threshold`.

```sh
curl -sS -H "Authorization: Bearer vk_…" -H 'Content-Type: application/json' \
  -d '{"slug":"api","kind":"http","interval":"30s","tags":["prod"],"http":{"url":"https://api.example.com/healthz","expect_body":{"jsonpath":{"path":"$.status","equals":"ok"}}}}' \
  http://localhost:8080/api/v1/monitors
```

| kind | block | what counts as up |
| --- | --- | --- |
| `http` | `url`, `method`, `headers`, `body`, `expect_status` (`[200-299]`), `expect_body` (`contains`, `not_contains` or `jsonpath: {path, equals}`), `follow_redirects`, `verify_tls`, `ca_pem` | the status is expected and the body matches |
| `tcp` | `host`, `port`, `send`, `expect` | the port accepts and the banner contains `expect` |
| `dns` | `name`, `type`, `resolver`, `expect` | the name resolves and every expected answer is present |
| `tls` | `host`, `port`, `servername`, `warn_days`, `crit_days` | the handshake verifies; `late` inside `warn_days`, `down` inside `crit_days` |
| `icmp` | `host`, `count`, `loss_threshold` | fewer packets are lost than the threshold (needs `CAP_NET_RAW` or `net.ipv4.ping_group_range` on Linux) |

`vink check <slug>` runs a check now; the drawer has the same button and shows every attempt with its latency and reason. List rows carry a 24-hour latency sparkline.

## Maintenance windows

Settings › Maintenance takes one-off windows (from, to) and weekly ones (days, from, to) in a timezone, each with tags. While a window is active, the monitors carrying its tags keep recording but never go `down` and never alert; a heartbeat held back is looked at again when the window ends. End now cuts the running occurrence short.

## Status pages

Settings › Status pages publishes the monitors with any of a page's tags at `/s/<slug>`: a banner, one group per tag with a state and a 90-day uptime bar, open incidents. The page ships no script, is cacheable for 30 seconds, can ask for a password, and can be served on a custom domain routed to vink. Badges live at `/s/<slug>/badge/<monitor>.svg` and `.json` (Shields schema).

## Declarative configuration

`vink apply -f vink.yaml` brings a project to a file: channels, routes, maintenance windows, monitors and status pages, applied in one transaction, with the diff printed. `--dry-run` shows the diff without applying, `--prune` deletes what the file does not name. `${VAR}` is expanded from the environment before sending, so tokens stay out of the file; a secret written as `***` keeps the stored value. `vink export -o vink.yaml` writes the project back in the same form, secrets redacted (`--secrets` includes them). `docs/apply-schema.json` is the JSON Schema the CLI validates against; the same file goes to `PUT /api/v1/apply`.

A whole org fits in one file too. `vink admin org key create --org homelab --access rw` (on the server host) issues an org key; with it in a context, `vink export --org homelab -o homelab.yaml` writes every project under `org:` and `projects:`, and `vink apply -f homelab.yaml` brings them all back in one transaction, creating a project the file names and the org lacks, never deleting one. An org key does only that: it cannot touch a project's monitors or agents, and a project key cannot act for the org.

```yaml
version: 1
channels:
  - {name: ntfy, kind: ntfy, url: https://ntfy.example.com, topic: vink, token: ${NTFY_TOKEN}}
routes:
  - {match_tags: [prod], channels: [ntfy], on: [down, up], repeat_every: 4h}
maintenance:
  - {name: weekly patching, match_tags: [prod], rrule: "FREQ=WEEKLY;BYDAY=SU", from: "02:00", to: "04:00"}
monitors:
  - {slug: nightly-backup, kind: heartbeat, schedule: {cron: "0 3 * * *"}, grace: 30m, tags: [backup, prod]}
  - {slug: api, kind: http, interval: 30s, tags: [prod], http: {url: https://api.example.com/healthz}}
status_pages:
  - {slug: homelab, title: Homelab status, match_tags: [prod], public: true}
```

## Notifications

Channels: `smtp`, `webhook`, `ntfy`, `gotify`, `matrix`, `slackhook` (Slack, Mattermost, Rocket.Chat) and `alertmanager` (fires `MonitorDown`, resolves on up). Routes send the events of the monitors that carry all of a route's tags to its channels, with an optional repeat while an incident stays unacknowledged. A webhook with a body template covers PagerDuty, Opsgenie, Discord, Telegram and ilert; the templates are in `docs/webhook-templates/`.

## Signals

| URL | meaning |
| --- | --- |
| `/ping/<key>/<slug>` | ok (GET, POST, HEAD or PUT; the body up to 64 kB is stored) |
| `/ping/<key>/<slug>/start` | the job started; pairs with the next ok or fail through `?rid=` |
| `/ping/<key>/<slug>/fail` | the job failed |
| `/ping/<key>/<slug>/<exit code>` | 0 is ok, anything else is a fail with the code stored |
| `/ping/<key>/<slug>/log` | store a message without touching the state |
| `/ping/id/<monitor id>` | the same by id |

## Agents

A probe agent runs pull checks from a network vink cannot reach: a DMZ, a site behind NAT, a lab. It is the same binary, dials out to `wss://vink.example.com/agent/v1` with its token, runs the checks it is given with the same checkers the server uses, and keeps nothing on disk.

1. Org settings → Agents → Add agent. The token and the full command show once.
2. On the host: `vink agent --server wss://vink.example.com --token vat_… --labels site=dc2,zone=dmz` (or `VINK_AGENT_TOKEN`, `--token-file`; `docs/deploy/vink-agent.service` is a hardened unit). `--ca` trusts a private CA, `--pin` a certificate by its SHA-256, `--proxy` an http proxy.
3. On a monitor, Advanced → Run from: an agent by name, or agents with labels (`site=dc2`; the least loaded one runs it). In `vink.yaml`: `location: agent:dc2-probe` or `location: site=dc2`.

An agent quiet for `[agents] offline_after` (2 min) turns its monitors late with reason agent offline; they never go down for lack of an agent. Revoking the agent disconnects it at once.

## Operating

- People and orgs: `vink admin org create acme --name Acme`, `vink admin user create bob --org acme --role member --password-stdin`, and for someone who already has an account `vink admin user grant bob --org acme --role admin` or `vink admin user revoke bob --org acme`. The last owner of an org stays. In proxy mode a group named `vink:<org>:<role>` does the same on its own.
- `vink serve --config vink.toml`, or `VINK_CONFIG_FILE=/etc/vink/vink.toml` for every command; `--print-config` shows the effective configuration. `docs/deploy/vink.toml.example` lists every key; each is also an environment variable, `VINK_SERVER_LISTEN` for `[server] listen`, and the environment wins over the file.
- `docs/deploy/` holds a hardened systemd unit (`vink.service`, `DynamicUser` and `StateDirectory=vink`), the agent's unit (`vink-agent.service`), `compose.yaml`, a Nomad job (`vink.nomad.hcl`), and the reverse-proxy snippets `traefik-authelia.yaml` and `apache-kerberos.conf`. The container image is `ghcr.io/w4jnl/vink`, built `FROM scratch` with the binary and CA certificates; it serves, runs an agent or acts as the CLI by its arguments.
- Behind a reverse proxy that authenticates people, turn on `[auth.proxy]` and map groups named `vink:<org>:<role>` to roles; the deploy snippets show the headers, the shared secret and which paths stay open.
- Air-gapped: vink makes no connection you did not configure (no telemetry, update checks or CDN assets). Set `[outbound] egress_log` to a file and it records every connection the server opens; an idle install leaves it empty. `make lint` runs the same gate over the source.
- Moving in: `vink import healthchecks -f checks.json` (the API listing) or `vink import kuma -f backup.json` writes an apply file and lists what could not carry over; `--apply` sends it to the current context.
- The database file and the secret key file are the state. `vink admin backup --out vink-backup.db` writes a consistent snapshot while the server runs; copy the key file (`secret_key_file` in the config) alongside.
- `GET /metrics` serves Prometheus metrics: monitors by state, pings, checks with latency, deliveries, open incidents, scheduler lag. Set `[metrics] token` to require a bearer token.
- Observations are pruned after `retention.observations_days` (90) and stored ping bodies after `retention.bodies_days` (14); events and incidents are kept.
- Outbound connections (checks, notifications) honour `[outbound]`: a proxy, an extra CA bundle, and whether private addresses may be targeted (on by default, for a homelab).
- `/api/v1/openapi.yaml` documents the API. Errors are RFC 7807 problems, listed in `docs/errors.md`.
- `-d` on any command switches to debug logging with colour.

## Developing

```sh
make tools       # pinned sqlc, golangci-lint, air, goreleaser and govulncheck into ./bin
make dev         # live reload of vink serve -d on data/vink.db; uses data/vink.toml when that file exists
make generate    # sqlc
make migrate     # apply migrations and dump db/schema.sql
make lint        # gofmt, vet, golangci-lint and the tenancy gate
make test        # go test -race ./...
make e2e         # builds the binary and runs the heartbeat and homelab smoke tests (about four minutes)
make golden      # regenerate the UI golden files from the design system with node
```

Every project-scoped query filters by `project_id`; a request for another tenant's resource is a 404, and `internal/http/crosstenant_test.go` checks every route.
