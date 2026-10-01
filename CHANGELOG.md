# Changelog

User-facing changes per release, newest first. `scripts/release.sh` refuses to tag a version
that has no section here and uses the section as the GitHub release notes, so every release
updates this file first. Dates are the tag dates.

## Unreleased

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
