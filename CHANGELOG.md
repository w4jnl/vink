# Changelog

User-facing changes per release, newest first. `scripts/release.sh` refuses to tag a version
that has no section here and uses the section as the GitHub release notes, so every release
updates this file first. Dates are the tag dates.

## Unreleased

- A Go module for sending pings, `github.com/w4jnl/vink/ping`, in `ping/` with its own versions
  (`ping/vX.Y.Z`). It does what `vink ping` and `vink run` do: every signal, progress notes,
  messages and bodies, runs paired by id, `Run` to wrap a job, pings by id and creating a
  monitor from its first ping, with retries, the server's limits respected, and errors that
  never contain the ping key. It needs Go 1.22 or later and nothing beyond the standard
  library; its README and `go doc` are written for people and coding agents alike.
- A monitor's own body limit, when lower than the server's, is now the one the
  `Ping-Body-Limit` header reports on a 413, so a client can cut its body to it and try again.
- `vink ping` and `vink run` work with the project's ping key alone, so a job host needs no API
  key: `VINK_PING_KEY` (or `--ping-key`) and `VINK_PING_URL` (or `--ping-url`, a ping URL up to
  the key), or `VINK_SERVER` and the context's server for the address. With a ping key the CLI
  never calls the API. A read-only API key without a ping key now says how to get one.
- `vink ping --log` sends a progress note, with `--msg`, `--body -` or both, which shows in the
  monitor's history and changes nothing else. `--start`, `--fail`, `--exit` and `--log` now
  refuse to be combined; before, a second one was silently dropped.
- vink can be deployed under a path on a shared host, `https://www.example.com/vink`, as well as
  at its own hostname: the path of `server.base_url` is the prefix, and a deployment lives at
  one or the other, never both. Every link, redirect, htmx attribute, static asset, cookie, the
  API's `Location` and the OpenAPI `servers` entry carry it; the proxy forwards the path
  unchanged. `docs/deploy.md` is the new deployment guide, with the proxy rules, a verification
  checklist, a troubleshooting table and the map of every URL vink serves; `docs/deploy/` gains
  an nginx example and prefixed variants of the Traefik and Apache ones. A `base_url` that
  already carried a path while vink served at the root now moves vink under that path.
- A ping that came through a trusted proxy records the proxy as `via` next to the client it
  reported, so the observation panel shows which hop an address came from.

## 0.1.3 (2026-10-03)

- A history page per monitor at `…/m/{slug}/history`, reached by a History button in the
  drawer: the drawer's head over the 24-hour and 90-day bars, then one timeline of observations
  and state changes newest first, grouped by day, with chips for ok, failures, runs and changes,
  a period of 24 h to 90 d or an exact window, day headings that link to their day, and an
  Older link that loads the next 50 rows as it scrolls into view. The head polls and offers a
  reload when newer rows exist. Pause, Resume and Check now work from the page.
- Observation rows open in place, in the drawer and on the history page, like the audit log's:
  a chevron at the left, and the panel shows where the ping came from (address, agent, method),
  the run, its duration and exit code, a check's detail, the whole message when the row cut it,
  and the stored body in a code box with Copy, loaded when the panel opens. The body link no
  longer leaves the page; the bare body URL still serves it as text.
- The web UI works on phones and tablets. Under 960px the drawer is the page (Close or Escape
  brings the list back); under 640px the top bar wraps into two rows, monitor and incident rows
  go two-line, settings rows wrap, forms stack their fields with the kind picker two across,
  tabs scroll sideways and the uptime bars fit; touch screens get 40px controls and 16px inputs,
  so Safari no longer zooms on focus. Wide screens show one 1120px column. The project switcher
  now sits before the section links in the top bar. Observation panels lead with the absolute
  time, and the agent drawer has a Close button.
- Alerts link to the monitor's history page instead of the drawer, so the failing ping, its
  body and the flips are one click from the email or message.
- The create form's ping URL follows the slug as you type (derived from the name when the slug
  is empty) instead of showing the example until the monitor exists.
- The history page's head shares the timeline's width, so a wide screen shows one column.
- The API takes `kind=ok|fail|run` on a monitor's observations, and its events page with
  `since`, `until` and `cursor` like observations do, answering with `next_cursor`.
- Switching a channel on or off no longer re-validates its configuration, so an SMTP channel
  can be toggled on a server that has no `[smtp] host` set; it failed with a 500.
- The Traefik and Apache deploy examples open `/static/` and `/a/` next to the status pages:
  a status page's stylesheet and the one-click acknowledgement links in alerts are anonymous
  routes too. Without them a status page renders unstyled for anyone without a session and
  the ack link bounces through the sign-in.

## 0.1.2 (2026-10-03)

- Alerts say why. A `down` caused by a ping carries the ping's `?msg=` (or, without one, the
  last 20 lines of a text body) and its exit code: as `exit` and `message` lines in every
  channel's text, as `message` and `exit_code` in the webhook payload, and as Alertmanager
  annotations. Repeats carry them too. The PagerDuty and Opsgenie templates include the message.
- Heartbeats have a `tolerance` (default `30s`, at most the grace): a ping this long after the
  deadline is still on time, and `late` starts when it runs out. A cron job that fires at its
  deadline and pings a second later no longer flaps late and back every run. The field sits
  under Advanced in the form, in the API, in `vink.yaml` and in `vink get`; the grace
  hint reads "Late at 03:00:30, down at 03:05." Existing monitors get the default.
- The drawer's 24-hour legend shows the share of the day the monitor was up, weighted by time
  like the status page. It counted cells before, so one late minute in an hour cost the whole
  hour and a monitor that was late once an hour read 0.0% up under a bar that was mostly fine.
- modernc.org/sqlite 1.60.1; the release workflow runs on actions/checkout 7 and
  docker/setup-qemu-action 4.

## 0.1.1 (2026-10-02)

- SQLite 3.53.4 through modernc.org/sqlite 1.60.0, which carries upstream's fix for a
  journal-rollback corruption after a crash during a multi-database commit.
- The release notes land on the GitHub release again; 0.1.0 came out with an empty body and
  had them set by hand.
- Build and release actions on their current majors; the CA certificates in the image come
  from Alpine 3.24.

## 0.1.0 (2026-10-02)

The first public release. Everything below exists and is covered by tests; `docs/design.md` is
the design it was built from.

- **Heartbeats**: a period or a 5-field cron schedule in the monitor's timezone, grace, a
  maximum runtime, `start`, `fail`, `log` and exit-code signals with run ids, the body of a
  ping stored up to 64 kB, `?create=1` to make a monitor from its first ping, and `vink run` to
  wrap a command between a start ping and a finish ping carrying its exit code and output.
- **Checks**: HTTP (status, body keyword or JSON path, redirects, private CA), TCP with a
  banner, DNS, TLS expiry and ICMP, on an interval with a timeout, confirm retries and
  thresholds, from the server or from a probe agent in another network.
- **One state machine** for both: new, up, late, down and paused, with incidents opened on down
  and closed on up, acknowledgements, and maintenance windows (one-off and weekly) that hold
  alerts back.
- **Alerts**: routes match tags and send down, up and late to SMTP, webhooks with templates,
  ntfy, Gotify, Matrix, Slack-compatible hooks and Alertmanager, with repeats while an incident
  stays unacknowledged, six delivery attempts with backoff, and one-click acknowledgement links.
- **Status pages** per project at `/s/<slug>`: a banner, groups by tag, 90-day bars, open
  incidents, an optional password, a custom domain, and SVG and JSON badges.
- **Teams**: orgs, projects and the roles viewer, member, admin and owner; invites by one-time
  link; quotas; an instance admin page for orgs, users and the server; sign-in with local
  accounts (two-factor with recovery codes, optional or required), trusted reverse-proxy headers
  or OpenID Connect, with groups mapped to roles; an audit log per org and for the instance.
- **Config as code**: `vink apply` and `vink export` for a project or a whole org, with a JSON
  Schema, diff, dry run and prune; importers for Healthchecks and Uptime Kuma.
- **Operations**: one binary with SQLite, `vink admin backup`, retention, Prometheus metrics,
  `/healthz` and `/readyz`, an outbound proxy and CA bundle, no outbound connection unless
  configured and an egress log to prove it, a systemd unit, a compose file, Traefik with Authelia
  and Apache with Kerberos snippets, shell completion.
