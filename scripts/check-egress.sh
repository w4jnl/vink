#!/bin/sh
# Fails when Go code outside internal/outbound opens network connections
# on its own: every dial, HTTP client and resolver must come from the
# outbound environment, so proxy, CA, the private-target guard and the
# egress log apply everywhere. The allowlist names the few places that
# are the environment itself or talk to vink's own server.
set -eu

cd "$(dirname "$0")/.."

pattern='http\.Client{|http\.Get\(|http\.Post\(|http\.Head\(|http\.DefaultClient|http\.DefaultTransport|net\.Dial\(|net\.DialTimeout\(|tls\.Dial\(|tls\.Dialer\{|net\.Dialer\{|net\.LookupHost|net\.LookupIP|net\.LookupAddr|net\.DefaultResolver|smtp\.Dial\(|icmp\.ListenPacket|websocket\.Dial\('

# file:reason — the code the gate accepts
allow='
internal/outbound/outbound.go:the environment itself
internal/checks/http.go:a client on the environment transport, per check for its TLS settings
internal/checks/icmp.go:ICMP has no dial; the send is reported through Observe
internal/notify/httpclient.go:a client on the environment transport
internal/agent/agent.go:the agent dials its own vink server through the environment transport
internal/cli/client.go:the CLI talks to the vink server the user configured
internal/auth/oidc.go:a client on the environment transport, to the issuer the operator configured
'

status=0
for f in $(grep -rlE "$pattern" --include='*.go' internal cmd 2>/dev/null | grep -v '_test\.go$' | sort); do
  if ! printf '%s\n' "$allow" | grep -q "^$f:"; then
    echo "egress gate: $f opens connections outside internal/outbound:" >&2
    grep -nE "$pattern" "$f" >&2
    status=1
  fi
done
for entry in $(printf '%s\n' "$allow" | grep -v '^$' | cut -d: -f1); do
  if ! grep -qE "$pattern" "$entry" 2>/dev/null; then
    echo "egress gate: allowlist entry $entry no longer matches; remove it" >&2
    status=1
  fi
done
if [ "$status" -ne 0 ]; then
  echo "egress gate failed: route the connection through internal/outbound or add a reasoned allowlist entry" >&2
  exit 1
fi
echo "egress gate ok"
