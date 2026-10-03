# Changelog

User-facing changes per release, newest first. `scripts/release.sh` refuses to tag a version
that has no section here and uses the section as the GitHub release notes, so every release
updates this file first. Dates are the tag dates.

## Unreleased

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
