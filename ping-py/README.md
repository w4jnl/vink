# vink ping for Python

`vink_ping` sends heartbeat pings to a [vink](https://github.com/w4jnl/vink) server from Python.
A job tells vink when it starts, how it ended and how far it got, and vink alerts when a ping does
not arrive or reports a failure.

It is the Python form of `vink ping` and `vink run`. It is one file, uses only the standard
library, works with Python 3.9 and later, and needs the project's ping key, never an API key.

## Adding it to a project

It is not on PyPI. Install it from the vink repository, pinned to a tag:

```sh
pip install "vink-ping @ git+ssh://git@github.com/w4jnl/vink.git@ping-py/v0.2.0#subdirectory=ping-py"
```

The same requirement works elsewhere:

| Where | How |
| --- | --- |
| `requirements.txt` | `vink-ping @ git+ssh://git@github.com/w4jnl/vink.git@ping-py/v0.2.0#subdirectory=ping-py` |
| `pyproject.toml` | the same string in `dependencies` |
| uv | `uv add "vink-ping @ git+ssh://git@github.com/w4jnl/vink.git@ping-py/v0.2.0#subdirectory=ping-py"` |
| Poetry | `poetry add "git+ssh://git@github.com/w4jnl/vink.git@ping-py/v0.2.0#subdirectory=ping-py"` |

The repository is public, so `git+https://github.com/w4jnl/vink.git@…` works too, on hosts without
a GitHub key. Installing builds the package with setuptools, which pip and uv fetch for the build
only.

Where no package index or GitHub is reachable, copy the code instead. All of it is in
[`vink_ping/__init__.py`](vink_ping/__init__.py). Save that file in the project as
`vink_ping.py`, or copy the whole `vink_ping` folder to keep the typing marker.

## Quick start

```python
import vink_ping

client = vink_ping.Client.from_env()  # reads VINK_PING_URL and VINK_PING_KEY

with client.monitor("nightly-backup").run():
    backup()
```

The `with` block sends a start, then a success. If the block raises, it sends a failure carrying
the exception's text as the message and its traceback as the body, and the exception goes on as
usual. vink shows the run's duration and alerts on the failure. A ping that cannot be sent never
changes what the block does.

Three things identify a monitor:

| What | Where to find it | Example |
| --- | --- | --- |
| Ping URL up to the key | the start of every ping URL vink shows | `https://vink.example.com/ping/`, or `https://www.example.com/vink/ping/` under a path |
| Ping key | Settings › Keys in vink, or the part after `/ping/` in a ping URL | `7jnmu7j5wjxovaewk4lee5` |
| Slug | the last part of the monitor's ping URL | `nightly-backup` |

Pass the URL and the key in the environment (`VINK_PING_URL`, `VINK_PING_KEY`), the same
variables the vink CLI reads, or to `Client(url, key)`. Keep the key out of source code: it lets
anyone ping every monitor of the project.

## API at a glance

| Call | Sends | Effect in vink |
| --- | --- | --- |
| `m.success()` | `…/<slug>` | up; the next deadline counts from now |
| `m.start()` | `…/<slug>/start` | a run opens; failed when `max_runtime` passes first |
| `m.fail()` | `…/<slug>/fail` | down (at the failure threshold), alerts |
| `m.exit(code)` | `…/<slug>/<code>` | 0 is a success, anything else a failure with the code |
| `m.log("text")` | `…/<slug>/log` | a note in the history; nothing else changes |
| `with m.run() as run:` | start, then the outcome | wraps a block; `run.log(...)` adds notes inside it |
| `@m.job` | the same, per call | wraps a function |
| `m.new_run()` | nothing yet | a `Run` whose pings share one run id |
| `run.finish(exc)` | success, exit code or fail | by `exc`, see below |

`m` is `client.monitor(slug)`, or `client.monitor_id(id)` to ping one monitor by its id without the
project key. Every signal takes keyword arguments:

- `msg="disk full"` adds a line shown on the observation and in the alert, up to 2,000 bytes.
- `body=output` adds a stored body, `str` or `bytes`, cut to its last 64 kB.
- `content_type="application/json"` sets the body's type when it is not text.
- `run_id=...` pairs a start with its finish by hand. A run does this for you.
- `create=True` makes the monitor from its first ping, as a heartbeat with a one-day period and a
  one-hour grace. `create=Create(...)` sets it up instead: `name`, `period` or `cron`, `tz`,
  `grace`, `tolerance`, `max_runtime` (a `timedelta` or a string like `"30m"`) and `tags`. With
  `create`, a signal returns `True` when it made the monitor and `False` when it was already
  there; the settings apply on create only, and a ping never changes a monitor that exists.
  Settings vink refuses raise a `StatusError` with status 400, and nothing is recorded.

```python
from vink_ping import Create

m.success(create=Create(cron="0 3 * * *", tz="Europe/Amsterdam", grace="30m", tags=["backup"]))
```

`run.finish(exc)` and the `with` block report how a run ended:

| Outcome | Sent |
| --- | --- |
| no exception, `SystemExit(0)` or `sys.exit()` | a success |
| `SystemExit(n)` | exit code `n`; a message like `sys.exit("bye")` is code 1 |
| `subprocess.CalledProcessError` | its exit code, with the output it captured as the body |
| any other exception, `KeyboardInterrupt` included | a failure: `Type: text` as the message, the traceback as the body |

Client options, all keyword arguments of `Client` and `Client.from_env`:

- `user_agent="nas-backup"` names the sender in vink's observation panel.
- `attempts` and `timeout` tune retries and how long each attempt waits.
- `body_limit` changes where bodies are cut.
- `ssl_context` is for a private CA.
- `on_error` receives the ping errors that `run()` and `@job` swallow, for logging.

## Recipes

**A cron script**

```python
#!/usr/bin/env python3
import logging, subprocess, vink_ping

client = vink_ping.Client.from_env(on_error=lambda e: logging.warning("vink: %s", e))

with client.monitor("nightly-backup").run() as run:
    subprocess.run(["restic", "backup", "/home"], check=True, capture_output=True)
    run.log("backup done, pruning")
    subprocess.run(["restic", "forget", "--keep-daily", "7", "--prune"], check=True, capture_output=True)
```

If restic fails, vink gets its exit code and its output, and the script still fails with the same
`CalledProcessError`.

**A function on a schedule**

```python
monitor = client.monitor("report")

@monitor.job
def build_report(day):
    ...
```

**A worker that should check in every few minutes**

Set the monitor's period to the interval and let a missing ping raise the alert:

```python
def report(send):
    try:
        send()
    except vink_ping.PingError as e:
        log.warning("vink: %s", e)  # vink unreachable: keep working

m = client.monitor("queue-worker")
while True:
    try:
        worker.check()
    except Exception as e:
        report(lambda: m.fail(msg=str(e)))
    else:
        report(m.success)
    time.sleep(300)
```

**Telling a wrong key from a server that is away**

```python
try:
    m.success()
except vink_ping.NotFound:
    ...  # wrong key, slug or id: fix the configuration
except vink_ping.PingError:
    ...  # unreachable, 5xx or rate limited after the retries: log and carry on
```

## Behaviour

- **Retries.** A ping is tried 3 times when vink is unreachable, answers 5xx or rate-limits it.
  The pauses are 0.5 s and then 1 s, or the server's `Retry-After` when that is 30 s or less.
  A 404 is not retried. Each attempt waits at most 10 s for the connection and for each read.
- **Limits.** A message is cut to 2,000 bytes on a character boundary. A body is cut to its last
  64 kB. A monitor with a lower limit answers 413 with that limit, and the ping is sent once
  more, cut to it.
- **Errors.** `PingError` is the base. `NotFound`, `RateLimited` and `StatusError` describe
  server answers, and `Unreachable` covers no answer, with the original exception as its
  `__cause__`. Messages never contain the ping key or a monitor id, so they are safe to log.
  Mistakes in the arguments raise `ValueError` before anything is sent.
- **Threads.** A `Client` is safe to share between threads. Make one per process.
- **Proxies.** `HTTPS_PROXY` and `NO_PROXY` are honoured, as urllib does.

## For coding agents

- Make one `vink_ping.Client` at start-up with `Client.from_env()`, and let its `ValueError` stop
  the program, since that means the configuration is missing.
- Wrap a job with `with m.run():` or `@m.job`. Do not pair a start and a finish by hand.
- Never let a ping error stop a job. Catch `vink_ping.PingError` around single signals, and pass
  `on_error` to log the failures `run()` swallows.
- Put a command's output in `body`, not `msg`. The message is one line for the alert, and the
  body is the detail.
- Never put the ping key in code or in a URL literal. Read it from `VINK_PING_KEY`.
- When a job makes its own monitor, give `Create` the schedule it really runs on (`cron` with
  `tz`, or `period`) and a `grace` the job can keep. Changing them later is done in vink, not by
  the ping.
- `help(vink_ping)` and the docstrings are the full reference.

## Versions

The package is versioned on its own, with tags named `ping-py/vX.Y.Z` in the vink repository. It
works with every vink server from 0.1.0 on. A vink deployed under a path needs a server with path
support, the first release after 0.1.3. `Create(...)` settings need a server released after
0.2.1; an older one makes the monitor with its defaults and the signal returns `False`. The tests run against a fake endpoint and against a real
vink binary, on Python 3.9 and the current Python. The ping protocol is described in
[docs/heartbeats.md](https://github.com/w4jnl/vink/blob/main/docs/heartbeats.md). MIT licensed,
like vink.
