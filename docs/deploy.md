# Deploying vink

How to put vink behind a reverse proxy, at its own hostname or under a path on a shared host,
and how to prove the setup is right before anyone signs in. The example files under
`docs/deploy/` are the source of truth for proxy snippets; this page says what they do and why.
For the job that pings vink, read [heartbeats.md](heartbeats.md) instead.

## 1. Choose the address

vink lives at one address, and `server.base_url` is that address. A deployment is either at
the root of a hostname or under one path on a shared host, never both:

| Form | `base_url` | Pages, pings, API |
| --- | --- | --- |
| own hostname | `https://vink.example.com` | `https://vink.example.com/…` |
| a path on a shared host | `https://www.example.com/vink` | `https://www.example.com/vink/…` |

Everything vink writes follows `base_url`: the ping URLs in the drawer and the API, the
monitor, incident and acknowledgement links in alerts, invite and password-reset links, the
OIDC redirect URI, the status page addresses, the `vink ctx add` and `vink agent` commands it
prints, and the scope of its cookies. Change `base_url` and all of them change on the next
request; nothing in the database carries the old one.

The path is the only part with rules: it must be clean (`/vink`, `/it/monitoring`; no `..`,
`//`, query or fragment) and its first segment must not be one of vink's own top-level routes
(`o`, `s`, `a`, `api`, `ping`, `static`, `admin`, `account`, `login`, `logout`, `projects`,
`invite`, `reset`, `auth`, `agent`, `metrics`, `healthz`, `readyz`), which it would shadow.
`vink serve` refuses to start otherwise and says which rule it broke.

Under a path, vink answers under that path only. `https://www.example.com/healthz` is a 404;
`https://www.example.com/vink/healthz` is the health check. Health probes, the CLI's
`--server`, the agent's `--server` and anyone opening the container port directly all use the
path. The bare path (`/vink`) redirects to `/vink/`.

## 2. Configure vink

`vink.toml`, or the same keys as `VINK_<SECTION>_<KEY>` in the environment, which wins over
the file. The keys that matter for a deployment:

```toml
[server]
listen = ":8080"
base_url = "https://www.example.com/vink"   # or https://vink.example.com
trusted_proxies = ["172.16.0.0/12"]         # the proxy's network: X-Forwarded-For is believed from there

[ping]
listen = ""                                 # optional second listener for pings only
base_url = ""                               # optional separate address for ping URLs

[db]
path = "/data/vink.db"
[secrets]
key_file = "/data/secret.key"               # back it up with the database
```

- `trusted_proxies` is what makes an observation's `from` the real client instead of the
  proxy. Without it every ping shows the proxy's address. An address ending in `.1` on a Docker
  network is the Docker host itself: a job on that host that pings a published port arrives
  from there, and no hop can recover more. Name such jobs in the ping instead (`curl -A
  nas-backup …`).
- A separate ping listener (`ping.listen`) serves `/ping/…` on its own port under the path of
  `ping.base_url`, so a ping hostname can live at the root while the UI lives under a path.
  Pings on the shared listener use the server's path; a different path there is refused at
  start.
- **Pings around the proxy.** The same listener lets jobs reach vink without the proxy, when the
  proxy is slow or adds sign-in machinery pings do not need. It serves `/ping/…` and nothing
  else: no UI, no API, no identity headers, so opening it exposes only what a ping key already
  allows. Set `ping.listen = ":8081"` and `ping.base_url` to the address jobs use
  (`http://vink-host.corp.example:8081`), so the ping URLs vink shows point there, and allow the
  port only from the job hosts in the firewall. Pings there are rate-limited and logged as on
  the main listener; the client address is the job's own, so `trusted_proxies` is not involved.
- Proxy sign-in (`[auth.proxy]`) and OIDC (`[auth.oidc]`) are described in
  [design.md](design.md); the OIDC redirect URI is `base_url` + `/auth/oidc/callback`, so
  register that at the provider.
- Instance admin keys (`vka_…`, for `vink admin` through a context) can be limited to the
  networks people administer from with `[auth.admin_keys] allowed_cidrs`. vink checks the
  client address, which behind a proxy is only right when `trusted_proxies` names the proxy:
  without it every request seems to come from the proxy, so either every key is refused or
  the limit means nothing. `vink serve` warns at start about that combination.
- With Apache and Kerberos, where the AD groups cannot be shaped for vink, set
  `[auth.proxy] roles = "vink"` and list the first admins in `instance_admins`; org admins then
  add people by sign-in name on the Members tab. [design.md](design.md) has the details.
- Status pages can have their own hostname (`custom_domain` on the page). Route that host to
  vink as a whole; vink serves the page at that host's root whatever path the rest of it lives
  under.

## 3. Put the proxy in front

Three rules hold for every proxy:

1. **Forward the path unchanged.** Under `/vink`, the request that reaches vink must still say
   `/vink/…`. Do not strip the prefix; vink mounts itself under the path and writes every link
   with it.
2. **Leave the open paths open.** Pings, the API, status pages and their stylesheet, the
   acknowledgement links in alerts, the agent gateway, metrics and health carry their own
   credentials or none, and must not go through a browser sign-in. With an authenticating
   proxy (Authelia, Kerberos, oauth2-proxy) they bypass it; vink ignores identity headers on
   them anyway. At the root they are `/ping/`, `/api/`, `/s/`, `/static/`, `/a/`, `/agent/`,
   `/metrics`, `/healthz` and `/readyz`; under a path, each with the path in front.
3. **The agent gateway is a WebSocket.** `/agent/v1` needs the `Upgrade` and `Connection`
   headers passed through and a long read timeout.

Pass `X-Forwarded-For` and `X-Forwarded-Proto`, and keep `Host` as the client sent it.

**Traefik with Authelia**: [`deploy/traefik-authelia.yaml`](deploy/traefik-authelia.yaml). Two
routers on the host, the open one at a higher priority without forward auth and with any
identity header the client sent stripped, the UI one through Authelia; both add the shared
secret vink checks. The file shows the root form and, commented, the same two routers under a
path.

**Apache with Kerberos**: [`deploy/apache-kerberos.conf`](deploy/apache-kerberos.conf).
`ProxyPass` for the WebSocket first, then the rest; `<Location>` with SPNEGO and the group
lookup for the UI, `<LocationMatch>` opening the paths above. The commented variant moves both
under the path.

**nginx**: [`deploy/nginx.conf`](deploy/nginx.conf), one server block per form, with the
WebSocket upgrade map. nginx on its own does no sign-in; put vink's local accounts or OIDC in
front of the UI, or add `auth_request` for the UI locations only.

**Compose**: [`deploy/compose.yaml`](deploy/compose.yaml). The health check and the agent's
`--server` carry the path when there is one.

## 4. Verify

With `B` set to your `base_url`, every line must come back as stated. Run them from outside,
through the proxy.

```sh
B=https://www.example.com/vink          # or https://vink.example.com

curl -s  $B/healthz                       # ok <version>
curl -sI $B/ | grep -i '^location'        # a sign-in: /vink/login?next=… or your proxy's page
curl -s  $B/api/v1/openapi.yaml | grep 'url:'    # - url: /vink/api/v1  (or /api/v1)
curl -s -H "Authorization: Bearer $KEY" $B/api/v1/me   # the key's project; ping_base starts with $B
curl -s  "$B/ping/$PING_KEY/$SLUG"        # OK
curl -s  $B/s/$PAGE | grep -o '/[^"]*/bundle.css'   # /vink/static/<hash>/bundle.css (or /static/…)
curl -sI https://www.example.com/vink/static/<hash>/bundle.css | head -1   # 200, without a session
```

Under a path, two more at the host:

```sh
H=https://www.example.com
curl -sI $H/healthz | head -1                # 404: vink answers under the path only
curl -sI $H/vink | grep -i '^location'       # /vink/
```

Then in a browser: sign in, open a monitor, create one and read its ping URL, open the history
page, the settings tabs, the audit log and a status page. On the CLI:

```sh
vink ctx add corp --server $B --key vk_…
vink ls
```

## 5. When something does not add up

| Symptom | Cause | Fix |
| --- | --- | --- |
| A status page renders unstyled for visitors without a session | `/static/` is behind the proxy's sign-in | Add `/static/` (and `/a/`) to the open paths |
| The acknowledgement link in an alert lands on the sign-in page | `/a/` is behind the sign-in | Same |
| Every observation says `from <proxy address>` | `trusted_proxies` does not name the proxy's network | Set it; restart; new pings show the client, with `via` naming the proxy in the row's panel |
| `from 192.168.x.1` for a job on the Docker host | The Docker host arrives from the bridge gateway | Expected; name the job with `-A` or `?msg=` |
| `https://host/` or `/healthz` is a 404 under a path | One deployment, one mount | Use the path everywhere, health probes included |
| Signing in loops back to the form, or "The form expired" after every submit | The cookie is scoped to `base_url`'s path but the browser is on another: the proxy strips the prefix, or `base_url` differs from the public URL | Forward the path unchanged and make `base_url` the address people type |
| Links in alerts, invites or the ping URL point at the wrong host or path | `base_url` is wrong | Fix it; the Server tab in instance admin shows the value in use |
| "No identity from the proxy" on every page | `[auth.proxy] trusted_cidrs` does not include the proxy, or the secret differs | Match the proxy's network and the shared secret; `vink serve -d` logs why a header was refused |
| Behind Apache, requests now and then stall for seconds or answer 502 after a quiet spell | mod_proxy reuses idle connections to vink with no time limit; vink closes them after 120 s idle | Add `ttl=60` to the `ProxyPass` lines (see `docs/deploy/apache-kerberos.conf`); jobs can also ping the ping listener directly |
| `vink admin` says "admin keys are not accepted from 172.16.0.2" (the proxy's address) | `allowed_cidrs` is set but `trusted_proxies` does not name the proxy | Set `trusted_proxies`; the address in the message is the one vink checked |
| `vink admin` says "this context's key is not an instance admin key" | The context holds a project or org key (`vk_…`) | Make an instance admin key in Instance admin › API keys and add it as its own context; `vink ctx ls` shows each context's kind |
| `vink serve` stops at start with `server.base_url: the path "/api" starts with /api, which is a vink route` | The path shadows a vink route | Choose another path |
| Pings 404 on a separate ping listener | `ping.base_url` has a path and the proxy or the job does not use it | Use it, or drop the path from `ping.base_url` |

## 6. URL map

Every path vink serves, at the root form; under a path each is prefixed. "Open" means the
proxy must not put a browser sign-in in front of it. "Identity headers" says whether vink reads
the proxy's user headers there.

| Path | What | Who calls it | Credentials | Identity headers | Open at the proxy |
| --- | --- | --- | --- | --- | --- |
| `/` | redirect to the last project | browsers | session | yes | no |
| `/login`, `/login/code`, `/logout` | local sign-in, second factor, sign-out | browsers | none, then session | yes | no |
| `/auth/oidc/start`, `/auth/oidc/callback` | OIDC flow | browsers, the provider | signed flight cookie | yes | no |
| `/invite/{token}`, `/reset/{token}` | accept an invite, reset a password | browsers | the token in the link | yes | no |
| `/projects`, `/o/{org}/p/{project}/…` | the UI: list, drawer, history, incidents, settings | browsers | session | yes | no |
| `/o/{org}/admin/…`, `/admin/…`, `/account` | org settings, instance admin, the account page | browsers | session | yes | no |
| `/static/…` | stylesheet, fonts, icons, scripts, under a content hash | browsers, status page visitors | none | no | yes |
| `/s/{slug}`, `/s/{slug}/badge/…` | status pages and badges | anyone, or a page password | none or the page's cookie | no | yes |
| `/a/{token}` | one-click acknowledgement from an alert | the person who got the alert | signed token, 7 days | no | yes |
| `/ping/{key}/{slug}[/…]`, `/ping/id/{id}[/…]` | heartbeat pings | jobs | the project's ping key | no | yes |
| `/api/v1/…` | the API | the CLI, scripts, the UI | bearer API key, or a session with CSRF on the `/orgs/…` and `/me` routes | no | yes |
| `/api/v1/admin/…` | instance administration | `vink admin` through a context | instance admin key (`vka_…`, optionally from `allowed_cidrs` only), or an instance admin's session with CSRF | no | yes |
| `/api/v1/openapi.yaml` | the API description | people, tools | none | no | yes |
| `/agent/v1` | the probe agent gateway, WebSocket | `vink agent` | agent token | no | yes |
| `/metrics` | Prometheus metrics | the scraper | bearer `metrics.token` when set | no | yes |
| `/healthz`, `/readyz` | liveness, readiness | probes | none | no | yes |

A status page on its own hostname is served at that host's root with its stylesheet under the
same host, so a host routed wholly to vink needs nothing else open.
