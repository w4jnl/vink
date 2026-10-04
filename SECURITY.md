# Security policy

vink is a network service. It listens for pings from your jobs, probes the targets you
configure, sends notifications to the channels you configure, serves a web UI, a REST API and
public status pages, and signs people in itself or through a reverse proxy or an OpenID Connect
provider. What an attacker can reach, and what stands in the way:

- **Ping ingress** (`/ping/…`) is unauthenticated by design. A ping key is an address, not a
  secret: whoever has it can mark a monitor up or down, create one with `?create=1`, and store
  a body up to `ping.body_limit`. Rate limits apply per monitor and per address; bodies are
  stored as bytes and rendered as escaped text. Rotate a key with `POST /api/v1/ping-key/rotate`.
- **Proxy-header sign-in** trusts `Remote-User` and friends only from a peer inside
  `auth.proxy.trusted_cidrs` that also sends the shared secret in `auth.proxy.secret_header`.
  Those two settings are the whole boundary; a wide CIDR or a leaked secret lets anyone sign in
  as anyone.
- **OIDC** uses the authorization-code flow with PKCE; the callback checks the state against a
  signed cookie, refuses a state seen before, and verifies the ID token's signature and nonce.
- **Local accounts** hold argon2id password hashes. TOTP secrets are encrypted under the
  instance key; recovery codes are hashed and work once; invite and password-reset links are
  256-bit one-time tokens that expire.
- **Outbound requests** (HTTP checks, webhooks, OIDC discovery, SMTP) go through one outbound
  environment with the configured proxy and CA bundle. `outbound.allow_private_targets = false`
  refuses loopback, link-local and RFC 1918 targets after resolution, which is the control
  against SSRF from a monitor or channel someone with a member role creates.
- **Channel secrets** (tokens, passwords, webhook headers) are encrypted at rest under
  `secrets.key_file`; the API returns them as `***`.
- **API keys** are per project, `ro` or `rw`, shown once and stored hashed; **org keys** act on
  export and apply only. **Agent tokens** are shown once and stored hashed; an agent only ever
  dials out.
- **Public status pages** carry no session and no script; a page can take a password.

## Supported versions

Only the latest release gets fixes. Update from the
[releases page](https://github.com/w4jnl/vink/releases) or pull the next image.

## Reporting a vulnerability

Use GitHub's private vulnerability reporting for this repository:
<https://github.com/w4jnl/vink/security/advisories/new>. Describe what the attacker controls (a
ping, a request to the API or the UI, a check target, a channel, a proxy header, an ID token) and
what they gain. You will get an answer within a week; fixes ship as a patch release with a note in
`CHANGELOG.md`. Please do not open a public issue for something exploitable before it is fixed.

## Deployment advice

- Terminate TLS at your proxy and set `server.base_url` to the public address, path included when vink lives under one, so cookies are
  `Secure` and links are right.
- Keep `auth.proxy.trusted_cidrs` to the proxy's own address, and the proxy secret out of the
  config file (`secret = "env:VINK_PROXY_SECRET"`).
- Back up `secret.key` with the database: without it, channel secrets and two-factor secrets in
  a restored database cannot be read.
