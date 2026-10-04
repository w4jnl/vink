# vink ping for Go

`github.com/w4jnl/vink/ping` sends heartbeat pings to a [vink](https://github.com/w4jnl/vink)
server from Go code. A job tells vink when it starts, how it ended and how far it got, and vink
alerts when a ping does not arrive or reports a failure.

It is the library form of `vink ping` and `vink run`. It depends on the standard library only,
works with Go 1.22 and later, and needs the project's ping key, never an API key.

```sh
go get github.com/w4jnl/vink/ping@latest
```

## Quick start

```go
import "github.com/w4jnl/vink/ping"

c, err := ping.FromEnv() // reads VINK_PING_URL and VINK_PING_KEY
if err != nil {
	log.Fatal(err)
}
err = c.Monitor("nightly-backup").Run(ctx, func(ctx context.Context) error {
	return backup(ctx)
})
```

`Run` sends a start, calls the function, then reports a success, or a failure carrying the
error's text. vink shows the run's duration and alerts on the failure. `Run` returns the
function's error unchanged; a ping that cannot be sent never fails the job.

Three things identify a monitor:

| What | Where to find it | Example |
| --- | --- | --- |
| Ping URL up to the key | the start of every ping URL vink shows | `https://vink.example.com/ping/`, or `https://www.example.com/vink/ping/` under a path |
| Ping key | Settings › Keys in vink, or the part after `/ping/` in a ping URL | `7jnmu7j5wjxovaewk4lee5` |
| Slug | the last part of the monitor's ping URL | `nightly-backup` |

Pass the URL and the key in the environment (`VINK_PING_URL`, `VINK_PING_KEY`), the same
variables the vink CLI reads, or to `ping.New(url, key)`. Keep the key out of source code: it
lets anyone ping every monitor of the project.

## API at a glance

| Call | Sends | Effect in vink |
| --- | --- | --- |
| `m.Success(ctx)` | `…/<slug>` | up; the next deadline counts from now |
| `m.Start(ctx)` | `…/<slug>/start` | a run opens; failed when `max_runtime` passes first |
| `m.Fail(ctx)` | `…/<slug>/fail` | down (at the failure threshold), alerts |
| `m.Exit(ctx, code)` | `…/<slug>/<code>` | 0 is a success, anything else a failure with the code |
| `m.Log(ctx, "text")` | `…/<slug>/log` | a note in the history, nothing else changes |
| `m.Run(ctx, fn)` | start, then the outcome | wraps a job; panics are reported, then re-raised |
| `m.NewRun()` | nothing yet | a `*Run` whose pings share one run id |
| `run.Finish(ctx, err)` | success, exit code, or fail | by `err`: nil, an `ExitCode()` above 0, or anything else |

`m` is `c.Monitor(slug)`, or `c.MonitorID(id)` to ping one monitor by its id without the
project key. Every call takes options:

- `ping.Msg("disk full")` adds a line shown on the observation and in the alert, up to 2,000 bytes.
- `ping.Body(output)` adds a stored body, cut to its last 64 kB.
- `ping.ContentType("application/json")` sets the body's type when it is not text.
- `ping.RunID(id)` pairs a start with its finish by hand. A `Run` does this for you.
- `ping.Create()` makes the monitor from its first ping, as a heartbeat with a one-day period
  and a one-hour grace.

Client options: `WithUserAgent("nas-backup")` names the sender in vink's observation panel,
`WithAttempts`, `WithTimeout`, `WithBodyLimit`, `WithHTTPClient` for a proxy or a private CA,
and `WithErrorHandler` for the ping errors `Run` swallows.

## Recipes

**A command's output, its exit code and progress notes**

```go
run := c.Monitor("nightly-backup").NewRun()
_ = run.Start(ctx)

var out bytes.Buffer
cmd := exec.CommandContext(ctx, "restic", "backup", "/home")
cmd.Stdout, cmd.Stderr = &out, &out
err := cmd.Run()
_ = run.Log(ctx, "backup done, pruning")
if err == nil {
	err = prune(ctx)
}
// nil reports a success, an *exec.ExitError its exit code, anything else a failure
_ = run.Finish(ctx, err, ping.Body(out.Bytes()))
```

**A service that should check in every few minutes**

Set the monitor's period to the interval and let a missing ping raise the alert:

```go
m := c.Monitor("queue-worker")
t := time.NewTicker(5 * time.Minute)
defer t.Stop()
for {
	select {
	case <-ctx.Done():
		return
	case <-t.C:
		if err := w.healthy(); err != nil {
			_ = m.Fail(ctx, ping.Msg(err.Error()))
			continue
		}
		_ = m.Success(ctx)
	}
}
```

**Logging ping failures without failing the job**

```go
c, err := ping.FromEnv(ping.WithErrorHandler(func(err error) {
	slog.Warn("vink ping failed", "err", err)
}))
```

**Telling a wrong key from a server that is away**

```go
switch err := m.Success(ctx); {
case errors.Is(err, ping.ErrNotFound): // wrong key, slug or id: fix the configuration
case err != nil: // unreachable, 5xx or rate limited after the retries: log and carry on
}
```

## Behaviour

- **Retries.** A ping is tried 3 times when vink is unreachable, answers 5xx or rate-limits it.
  The pauses are 0.5 s and then 1 s, or the server's `Retry-After` when that is 30 s or less.
  A 404 is not retried. Each attempt has a 10 s timeout, and the context bounds the whole ping.
- **Limits.** A message is cut to 2,000 bytes. A body is cut to its last 64 kB, the end being
  where a log says why it failed. A monitor with a lower limit answers 413 with that limit, and
  the ping is sent once more, cut to it.
- **Errors.** `ErrNotFound`, `ErrRateLimited` and `*StatusError` describe server answers.
  Errors never contain the ping key or a monitor id, so they are safe to log.
- **Concurrency.** A `Client` is safe for concurrent use. Make one per process and share it.
- **Run ids.** A `Run` sends the same run id on every ping, so vink pairs a start with its
  finish even when runs of one monitor overlap.

## For coding agents

- Make one `*ping.Client` at start-up with `ping.FromEnv()`, or `ping.New(url, key)`. Fail
  start-up on its error, since that means the configuration is missing.
- Wrap a job with `m.Run(ctx, fn)`, or use `m.NewRun()` with `Start` and `Finish`. Do not
  pair a start and a finish by hand.
- Never return or exit on a ping error inside a job. Log it and carry on, or let `Run` handle
  it with `WithErrorHandler`.
- Put a command's output in `ping.Body`, not `ping.Msg`. The message is one line for the alert,
  and the body is the detail.
- Never put the ping key in code or in a URL literal. Read it from `VINK_PING_KEY`.
- `go doc github.com/w4jnl/vink/ping` has the full reference, with runnable examples in
  `example_test.go`.

## Versions

The module is versioned on its own, with tags named `ping/vX.Y.Z` in the vink repository. It
works with every vink server from 0.1.0 on. A vink deployed under a path needs a server with path
support, the first release after 0.1.3. The ping protocol is described in
[docs/heartbeats.md](https://github.com/w4jnl/vink/blob/main/docs/heartbeats.md). MIT
licensed, like vink.
