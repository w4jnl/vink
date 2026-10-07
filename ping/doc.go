// Package ping sends heartbeat pings to a vink server from Go code: the
// job that runs tells vink that it started, how it went, and what it has
// done so far, and vink alerts when a ping does not come or reports a
// failure.
//
// It is the library form of the vink CLI's `vink ping` and `vink run`,
// with no dependencies beyond the standard library. It needs only the
// project's ping key, never an API key, so a job holds nothing more than
// a curl line would. vink itself is at https://github.com/w4jnl/vink;
// the ping protocol is in its docs/heartbeats.md.
//
// # What you need
//
//   - The ping URL up to the key: https://vink.example.com/ping/, or
//     https://www.example.com/vink/ping/ when vink lives under a path.
//     Every ping URL vink shows starts with it.
//   - The project's ping key: Settings › Keys in vink, or the part after
//     /ping/ in any of the project's ping URLs. Keep it out of source code;
//     pass it in the environment.
//   - The monitor's slug: the last part of its ping URL. With [Create]
//     the first ping makes the monitor.
//
// # Quick start
//
//	c, err := ping.FromEnv() // VINK_PING_URL and VINK_PING_KEY
//	if err != nil {
//		log.Fatal(err)
//	}
//	err = c.Monitor("nightly-backup").Run(ctx, func(ctx context.Context) error {
//		return backup(ctx)
//	})
//
// [Monitor.Run] sends a start, runs the function, and reports a success,
// or a failure with the error's text, and vink shows the run's duration.
// A ping that cannot be sent never changes what Run returns.
//
// # Signals
//
// Each method sends one ping; the monitor's state follows from it.
//
//	m := c.Monitor("nightly-backup")
//	m.Success(ctx)                          // ran and succeeded: up, next deadline from now
//	m.Start(ctx)                            // began: a run opens, nothing else changes
//	m.Fail(ctx, ping.Msg("disk full"))      // failed: down, alerts carry the message
//	m.Exit(ctx, 3, ping.Body(output))       // exit code: 0 succeeds, anything else fails
//	m.Log(ctx, "step 2 of 5 done")          // a note in the history; changes nothing
//
// Every method takes options: [Msg] for a line shown on the observation and
// in alerts, [Body] for a stored body such as a command's output (with
// [ContentType] when it is not text), [RunID] to pair a start with its
// finish, and [Create] to make the monitor from its first ping, set up by
// [CreateOption]s such as [Cron], [Grace] and [Tags] (on create only).
//
// # Runs
//
// A run is a start and its finish. vink measures the time between them,
// shows the job as running meanwhile, and fails it when the monitor's
// max_runtime passes without a finish. [Monitor.NewRun] gives every ping
// of one execution the same run id, so overlapping runs stay apart:
//
//	run := m.NewRun()
//	_ = run.Start(ctx)
//	_ = run.Log(ctx, "dump done, uploading")
//	err := upload(ctx)
//	_ = run.Finish(ctx, err, ping.Body(logTail))
//
// [Run.Finish] reports a success for a nil error, the exit code for an
// error that has one (such as *exec.ExitError), and a failure with the
// error's text otherwise.
//
// # Messages and bodies
//
// A message is cut to [MaxMsgLen] bytes. A body is cut to the client's
// limit, [DefaultBodyLimit] unless [WithBodyLimit] says otherwise, keeping
// its end, which is where a log says why a job failed. When a monitor
// accepts less, the server answers 413 with its limit and the ping is sent
// once more with the body cut to it. On a failure, an alert carries the
// message, or without one the last lines of a text body.
//
// # Errors and retries
//
// A ping is tried [DefaultAttempts] times when the server cannot be
// reached, answers 5xx, or rate-limits it (after its Retry-After, when that
// is at most 30 seconds); each attempt is bounded by [DefaultTimeout]. The
// options [WithAttempts] and [WithTimeout] change both, and the caller's
// context ends the ping at any point. A 404 is not retried: errors.Is(err,
// [ErrNotFound]) means the key, the slug or the id is wrong. Other answers
// are a [*StatusError]. Errors never contain the ping key or a monitor id.
//
// Monitoring must not break the job it watches: treat a ping error as
// something to log, not to stop for. [Monitor.Run] does this for you and
// hands ping errors to [WithErrorHandler].
//
// # By id
//
// [Client.MonitorID] pings one monitor by its id, without the project's
// key, for a host that should be able to ping that monitor only. Make the
// client with an empty key:
//
//	c, _ := ping.New("https://vink.example.com/ping/", "")
//	_ = c.MonitorID("01J9Z3K6V4W8X2Y5Z7A9B1C3D5").Success(ctx)
//
// # Concurrency
//
// A [Client] is safe for concurrent use; make one per process and share
// it. [Monitor] and [Run] values are immutable and may be shared too.
package ping
