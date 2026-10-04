# Heartbeat monitors

A heartbeat monitor expects a job to check in on a schedule. The job does that by requesting a
URL when it runs; vink does the rest: it knows when the next check-in is due, turns the monitor
`late` when the deadline passes by more than the tolerance, `down` when the grace is over or the
job reports a failure, and
opens an incident and alerts the routes that match. This page is for the person writing the job.

## The ping URL

Every project has a ping key, and every monitor has a slug. Together they make the URL:

```
https://vink.w4j.nl/ping/<ping key>/<slug>
```

The drawer of a monitor shows it with a Copy button, `vink get <slug>` prints it, and the
project's Settings › Keys tab shows the key. The key is an address, not a secret: whoever has it
can ping any monitor of the project, so rotate it there if it ends up in a public log.

A request to the plain URL means "the job finished and all is well". Nothing else is needed:

```cron
0 3 * * * /usr/local/bin/backup.sh && curl -fsS -m 10 --retry 3 https://vink.w4j.nl/ping/<key>/nightly-backup
```

`&&` keeps the ping from being sent when the script fails; `-m 10 --retry 3` keeps a slow
network from breaking the job. vink answers `200 OK` with the body `OK`.

## Signals

Append one segment to the URL to say more than "done":

| URL | Meaning | Effect on the state |
| --- | --- | --- |
| `…/<slug>` | finished, all well | up; the next deadline moves on |
| `…/<slug>/start` | the job started | none; opens a run (see below) |
| `…/<slug>/<exit code>` | finished with this exit code | `/0` is up; anything else is a failure |
| `…/<slug>/fail` | finished badly | a failure |
| `…/<slug>/log` | a note from a running job | none; the note is stored |

Each one with curl, with `URL=https://vink.w4j.nl/ping/<key>/<slug>`:

```sh
# done, all well: GET is enough
curl -fsS -m 10 --retry 3 "$URL"

# the job started
curl -fsS -m 10 --retry 3 "$URL/start"

# finished with the exit code of the command that just ran ($? in a shell)
curl -fsS -m 10 --retry 3 "$URL/$?"

# finished badly, with a one-line reason on the row
curl -fsS -m 10 "$URL/fail?msg=disk%20full%20on%20%2Fmnt%2Fbackup"

# finished badly, with the last lines of the log as the stored body
tail -c 16000 /tmp/backup.log | curl -fsS -m 10 -X POST --data-binary @- "$URL/1"

# a progress note while running: GET with ?msg= for a line, POST with a body for more
curl -fsS -m 10 "$URL/log?msg=step%202%20of%205%20done"
curl -fsS -m 10 -X POST --data-binary "step 2 of 5 done, 1203 files so far" "$URL/log"

# the same signals by HEAD (no body stored) or PUT (for clients that cannot POST a body)
curl -fsS -m 10 -I "$URL"
curl -fsS -m 10 -X PUT --data-binary "ok" "$URL/0"
```

`-f` makes curl exit non-zero on a 4xx or 5xx answer, `-s -S` keeps it quiet except for errors,
`-m 10` caps the request at ten seconds, `--retry 3` rides out a short network blip.

A failure turns the monitor `down` at once (with the default `failure_threshold` of 1; a
threshold of 2 lets a job retry once quietly). Every ping is stored as an observation and shown
in the drawer with its time, signal, exit code, source address and user agent, so the job's
history reads as a log.

## A run: start, progress, finish

For a job that takes a while, send `/start` first and the finish ping at the end. vink then
knows the job is running, shows its duration when it finishes, and can catch a job that never
finishes:

```sh
#!/bin/sh
URL=https://vink.w4j.nl/ping/<key>/nightly-backup
curl -fsS -m 10 --retry 3 "$URL/start" >/dev/null
restic backup /home
curl -fsS -m 10 --retry 3 "$URL/$?" >/dev/null
```

`$?` is the exit code of the previous command, so the last line reports success as `/0` and any
failure with the code restic gave. With `max_runtime` set on the monitor (for example `2h`), a
`/start` that is not followed by a finish within that time counts as a failure with the reason
`run_timeout`, which is how a hung job is caught.

**Progress** goes through `/log`. It changes nothing in the state and leaves the deadline and
the open run as they are; it adds an observation with the message you send, visible in the
drawer. Like every ping URL it accepts GET, POST, HEAD and PUT; what differs is where the text
travels:

- GET with `?msg=`, URL-encoded, up to 2,000 characters. The drawer shows it inline on the row,
  which suits a one-liner such as a progress count:

  ```sh
  curl -fsS "$URL/log?msg=step%202%20of%205%20done"
  ```

- POST (or PUT) with the message as the body, up to 64 kB, stored as it is and opened from the
  row's `body` link. This is the one for anything longer than a line, such as a chunk of output:

  ```sh
  curl -fsS -X POST --data-binary "step 2 of 5 done, 1203 files so far" "$URL/log"
  ```

The two can go together on one request, the msg for the row and the body for the detail. With
the CLI, `vink ping <slug> --log --msg "step 2 of 5 done"` sends the first and
`… | vink ping <slug> --log --body -` the second.

**Overlapping runs** are told apart with a run id. Pass the same `?rid=` on the start and the
finishing ping, and vink pairs those two whatever else arrives in between:

```sh
RID=$(date +%s)-$$
curl -fsS "$URL/start?rid=$RID"
…
curl -fsS "$URL/$??rid=$RID"
```

## Carrying information

Two ways to attach text to a ping:

- **The body.** A POST or PUT body is stored as it is, up to 64 kB (`ping.body_limit`), and the
  drawer shows a `body` link on that observation that opens it as plain text. Larger bodies are
  cut at the limit and marked truncated. Send the last lines of the job's output on a failure:

  ```sh
  restic backup /home > /tmp/backup.log 2>&1
  code=$?
  curl -fsS -X POST --data-binary @/tmp/backup.log "$URL/$code"
  ```

- **`?msg=`.** For clients that cannot send a body, a message of up to 2,000 characters in the
  query string. The drawer shows it inline on the row (the first 80 characters), so it suits a
  one-line reason:

  ```sh
  curl -fsS "$URL/1?msg=$(python3 -c 'import urllib.parse,sys; print(urllib.parse.quote(sys.argv[1]))' 'repository is already locked by PID 4120')"
  ```

Both work on every signal, including `/log`. When a ping takes the monitor `down`, what it
carried goes into the alert: the `?msg=` as it is, or, without one, the last 20 lines of a text
body (at most 2,000 bytes), plus the exit code. A deadline that passes has nothing to say beyond
its reason.

## From a script: the vink CLI

The vink binary sends pings itself, so a script needs no curl line to build. `vink run` wraps a
command, and `vink ping` sends one signal. Both work on any host with vink installed, from the
release archive, Homebrew or the container image, and need only the project's ping key.

### Setting up a job host

Give the CLI the address in front of the key and the key itself. The key is the same one every
ping URL of the project contains, and Settings › Keys shows it.

```sh
# /etc/vink/ping.env, owned by root, mode 600
# the ping URL up to the key, and the project's ping key
VINK_PING_URL=https://vink.example.com/ping/
VINK_PING_KEY=7jnmu7j5wjxovaewk4lee5
```

Plain assignments without `export` and without trailing comments suit both readers. A systemd
unit loads the file with `EnvironmentFile=/etc/vink/ping.env`. A shell loads it with
`set -a; . /etc/vink/ping.env; set +a`, where `set -a` exports what the file assigns, so vink
sees it.

With a ping key the CLI only sends pings and never calls the API, so the host holds nothing a
curl line would not. The address is found in this order:

1. `--ping-url` or `VINK_PING_URL`, a ping URL up to the key. Set it when pings have their own
   address (`ping.base_url`).
2. `VINK_SERVER` or the context's server, with `/ping/` added.

`--ping-key` and `--ping-url` set the same per command. Without a ping key, the context's API key
looks the ping key up once and the context keeps it. That takes a read-write key, since a
read-only key cannot see the ping key.

### Wrapping a command: `vink run`

```sh
vink run nightly-backup -- /usr/local/bin/backup.sh --full
```

It sends `/start`, runs the command with its output passed through, then sends `/<exit code>`
with the last 16 kB of the output as the body (`--tail` changes how much). It exits with the
command's own code and passes Ctrl-C and `SIGTERM` on to the command. When vink cannot be
reached it prints a warning and the command runs all the same. That makes it a drop-in for the
command in a crontab line or a unit file:

```cron
0 3 * * * set -a && . /etc/vink/ping.env && vink run nightly-backup -- /usr/local/bin/backup.sh
```

```ini
[Service]
Type=oneshot
EnvironmentFile=/etc/vink/ping.env
ExecStart=/usr/local/bin/vink run nightly-backup -- /usr/local/bin/backup.sh
```

### One signal: `vink ping`

| Command | Sends |
| --- | --- |
| `vink ping <slug>` | a success |
| `vink ping <slug> --start` | the job began; a run opens |
| `vink ping <slug> --fail` | a failure |
| `vink ping <slug> --exit N` | an exit code, 0 is a success |
| `vink ping <slug> --log --msg "…"` | a progress note; it changes nothing else |

One of `--start`, `--fail`, `--exit` and `--log` at a time. Each goes with these:

- `--msg "…"` adds a line for the observation's row and the alert.
- `--body -` reads the body from stdin and sends its first 64 kB. Pipe through `tail -c` to keep
  the end instead.
- `--rid ID` pairs a start with its finish.
- `--create` makes the monitor from its first ping.
- `--quiet` prints nothing on success.

`vink ping` makes one attempt of at most ten seconds and exits with one of these codes:

| Exit | Meaning |
| --- | --- |
| 0 | the ping was recorded |
| 1 | it was refused: a wrong key, slug or address, or flags that do not go together |
| 2 | vink could not be reached, answered with a server error, or rate-limited the ping |

### In a bash script

A script that reports its own start, its outcome and the end of its log:

```bash
#!/usr/bin/env bash
set -euo pipefail
set -a; . /etc/vink/ping.env; set +a

SLUG=nightly-backup
RID="$(date +%s)-$$"          # pairs the start with its finish
LOG=$(mktemp)

# monitoring must never stop the job: a failed ping is ignored
hb() { vink ping "$SLUG" --rid "$RID" --quiet "$@" || true; }

hb --start
trap 'code=$?; tail -c 16000 "$LOG" | hb --exit "$code" --body -; rm -f "$LOG"' EXIT

{
  restic backup /home
  hb --log --msg "backup done, pruning"
  restic forget --keep-daily 7 --prune
} >>"$LOG" 2>&1
```

- **On success**, vink gets `/start`, the note, and `/0` with the log as the body, and the run's
  duration.
- **On a failure**, `set -e` ends the script and the trap sends the failing command's exit code
  with the end of the log, which the alert quotes.
- **When vink is down**, every `hb` fails quietly and the job finishes as it would without it.

`|| true` matters because `vink ping` exits non-zero when it cannot deliver. Under `set -e`
that would end the job. `vink run` needs no such guard.

A note can carry more than a line. Pipe it in as the body:

```bash
restic stats --json | hb --log --body -
```

**One monitor per step.** When the steps of a script deserve their own alerts, wrap each:

```bash
vink run db-dump -- sh -c 'pg_dump app > /backup/app.sql'
vink run offsite-copy -- rclone sync /backup remote:backup
```

The redirect sits inside `sh -c`, so the dump goes to its file, and only what the command prints
on stderr ends up in the body.

## From Go: the ping module

A Go program pings through `github.com/w4jnl/vink/ping`, a module with no dependencies beyond
the standard library that does what `vink ping` and `vink run` do:

```go
c, err := ping.FromEnv() // VINK_PING_URL and VINK_PING_KEY, as above
if err != nil {
	log.Fatal(err)
}
err = c.Monitor("nightly-backup").Run(ctx, backup)
```

Its [README](../ping/README.md) and `go doc github.com/w4jnl/vink/ping` cover every signal,
runs, notes, bodies, retries and errors.

## Methods, answers and limits

- `GET`, `POST`, `HEAD` and `PUT` are accepted. A monitor can restrict itself to `POST`
  (Advanced › Methods) so link previewers and crawlers cannot ping it by following a URL.
- `200 OK` on success. `404` for an unknown key or slug, with the same body for both. `405` for
  a method the monitor refuses. `413` when the `Content-Length` is over the limit; the answer
  header `Ping-Body-Limit` says what the limit is. `429` with `Retry-After` when a monitor gets
  more than 10 pings a minute or an address more than 300; a rate-limited ping is dropped.
- A ping never redirects and never answers with HTML, so `curl -f` is enough to notice a problem.

## Creating a monitor from the first ping

`?create=1` on the plain URL creates an unknown slug as a heartbeat with a one-day period and
a one-hour grace, then records the ping:

```cron
0 3 * * * backup.sh && curl -fsS https://vink.w4j.nl/ping/<key>/nightly-backup?create=1
```

This is the quickest way to cover many jobs; edit the schedule afterwards in the drawer. A slug
is lower-case letters, digits and dashes.

## Schedules, tolerance, grace and the states

A heartbeat is either **periodic** (`period: 1h`, 60 s or more; the clock restarts at each
ping, so the deadline is the last ping plus the period) or **cron** (`0 3 * * *`, five fields
with names, ranges and steps, in the monitor's timezone, which defaults to the project's; the
deadline is the next occurrence after the last ping). The create form shows the next three runs
as you type.

| State | Glyph | When |
| --- | --- | --- |
| new | ◌ | created, no ping yet |
| up | ● | pinged in time |
| late | ◐ | the deadline passed and the tolerance ran out; nothing is sent to channels unless a route asks for `late` |
| down | ◆ | the grace after the deadline is over, or a failure arrived; an incident opens and the routes alert |
| paused | ‖ | paused by hand; nothing is expected |

The **tolerance** (`30s` by default, under Advanced in the form) is how long after the deadline a
ping still counts as on time. A cron job that starts exactly at its deadline and pings a few
seconds later never goes `late`; a periodic job whose pings drift by a few seconds is fine too.
Set it to the job's normal jitter, never more than the grace.

The **grace** is how long after the deadline `late` becomes `down`. Size it for the job's
normal variance, not for the schedule: a nightly backup that sometimes runs twenty minutes long
gets `30m`; a cron job that fires every minute gets the minimum of `60s`. Daylight-saving
changes are handled in the monitor's timezone: a 02:30 run that does not exist on the
spring-forward night is skipped, one that exists twice in autumn fires once.

The next successful ping brings a `down` monitor back to `up`, closes the incident and sends the
recovery to the same channels. **Maintenance windows** (Settings › Maintenance) hold a monitor
back: inside a window it records pings and events but never goes `down` and never alerts, and a
missed deadline is looked at again when the window ends. **Pause** stops expecting anything until
you resume.

## Recipes

**systemd timer.** Put the ping in the service, not the timer; `ExecStopPost=` runs after the
job with its result available:

```ini
[Service]
Type=oneshot
ExecStartPre=/usr/bin/curl -fsS -m 10 https://vink.w4j.nl/ping/<key>/nightly-backup/start
ExecStart=/usr/local/bin/backup.sh
ExecStopPost=/usr/bin/curl -fsS -m 10 "https://vink.w4j.nl/ping/<key>/nightly-backup/$EXIT_STATUS?msg=$SERVICE_RESULT"
```

**GitHub Actions.** One step at the end of the workflow, which also runs when a step failed:

```yaml
      - if: always()
        run: curl -fsS -m 10 "https://vink.w4j.nl/ping/${{ secrets.VINK_PING_KEY }}/deploy/${{ job.status == 'success' && 0 || 1 }}"
```

**Python.**

```python
import requests, sys
url = "https://vink.w4j.nl/ping/<key>/report"
requests.get(f"{url}/start", timeout=10)
try:
    run_report()
    requests.get(url, timeout=10)
except Exception as e:
    requests.post(f"{url}/fail", data=str(e)[:4000], timeout=10)
    sys.exit(1)
```

**PowerShell.**

```powershell
Invoke-WebRequest -UseBasicParsing -TimeoutSec 10 "https://vink.w4j.nl/ping/<key>/sync/$LASTEXITCODE" | Out-Null
```

**Docker container that runs a job:** the ping must come from inside the job or from its
wrapper; a container's exit code is not seen by vink. `vink run` inside the image, or the shell
recipe above, both work.

## Reading what happened

The monitor's drawer lists the last observations newest first: `start`, `ok`, `exit 1 ·
<message>`, `log · <message>`, each with where it came from and, on a finish that followed a
start, how long the run took. The 24-hour bar above it shows the worst state of each hour, and
the figure under it the share of the day the monitor was up, weighted by time. Events, the
state flips, are below with their reason: `deadline passed`, `grace over`, `exit 1`,
`run_timeout`, `recovered`. The same facts are in the API at
`/api/v1/monitors/<slug>/observations` and `/events`, and `vink logs <slug>` prints them.

## When something does not add up

- **`404`**: the key or the slug is wrong, or the monitor belongs to another project. Both
  cases look the same on purpose. Check the URL in the drawer.
- **The monitor goes `late` for a minute at every run**: the job pings later than the deadline
  plus the tolerance. A periodic monitor's deadline is the last ping plus the period, so a job
  that runs every five minutes with a `5m` period is late by its own run time at every cycle;
  give it a `6m` period or a tolerance that covers the run. A cron monitor's deadline is the
  next occurrence; raise the tolerance to the job's start-up jitter.
- **`from` shows the proxy, or the Docker host**: `from` is the client as the last trusted hop
  reported it. With `[server] trusted_proxies` naming the proxy's network, vink takes the client
  from `X-Forwarded-For` and the panel adds `via` with the proxy's own address; without it, the
  proxy is all vink sees. An address ending in `.1` on a Docker network is the bridge gateway,
  the host itself: a job on the Docker host that pings a published port arrives from there, and
  no hop can recover more. Name such jobs in the ping instead (`curl -A nas-backup …`).
- **A `/start` without a finish** stays open until the next ping; with `max_runtime` it turns
  into a failure at the limit.
- **`429`**: the job pings faster than ten times a minute; batch the progress into fewer `/log`
  calls.
- **The body is cut**: it was over 64 kB; the observation is marked truncated. Send the tail,
  not the whole log.
