package ping_test

import (
	"bytes"
	"context"
	"errors"
	"log"
	"log/slog"
	"os"
	"os/exec"
	"time"

	"github.com/w4jnl/vink/ping"
)

// The usual setup: the address and the key come from the environment the
// vink CLI uses, VINK_PING_URL and VINK_PING_KEY, and Run wraps the job.
func Example() {
	c, err := ping.FromEnv()
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	// backup is the job: func(ctx context.Context) error
	if err := c.Monitor("nightly-backup").Run(ctx, backup); err != nil {
		log.Fatal(err) // the job's error; vink already has it
	}
}

// New takes the ping URL up to the key and the project's ping key.
// Options name the sender and tune retries.
func ExampleNew() {
	c, err := ping.New("https://www.example.com/vink/ping/", os.Getenv("VINK_PING_KEY"),
		ping.WithUserAgent("nas-backup"),
		ping.WithAttempts(5),
		ping.WithTimeout(5*time.Second),
	)
	if err != nil {
		log.Fatal(err)
	}
	_ = c.Monitor("nightly-backup").Success(context.Background())
}

// Run reports the job's outcome and hands pings it could not send to the
// error handler, so monitoring never fails the job.
func ExampleMonitor_Run() {
	c, err := ping.FromEnv(ping.WithErrorHandler(func(err error) {
		slog.Warn("vink ping failed", "err", err)
	}))
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	if err := c.Monitor("db-dump").Run(ctx, dump); err != nil {
		slog.Error("dump failed", "err", err)
	}
}

// A run by hand: start, progress notes, and a finish carrying the
// command's output, all paired by one run id.
func ExampleMonitor_NewRun() {
	c, err := ping.FromEnv()
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	run := c.Monitor("nightly-backup").NewRun()
	logPing := func(err error) {
		if err != nil {
			slog.Warn("vink ping failed", "err", err)
		}
	}

	logPing(run.Start(ctx))
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, "restic", "backup", "/home")
	cmd.Stdout, cmd.Stderr = &out, &out
	err = cmd.Run()
	logPing(run.Log(ctx, "backup done, pruning"))
	if err == nil {
		err = prune(ctx)
	}
	// nil: success; an *exec.ExitError: its exit code; otherwise a failure
	// with the error's text. The body keeps the end of the output.
	logPing(run.Finish(ctx, err, ping.Body(out.Bytes())))
}

// The single signals, each one ping.
func ExampleMonitor_Success() {
	c, _ := ping.FromEnv()
	ctx := context.Background()
	m := c.Monitor("nightly-backup")

	_ = m.Success(ctx)                                    // ran and succeeded
	_ = m.Start(ctx)                                      // began; vink shows it running
	_ = m.Fail(ctx, ping.Msg("disk full"))                // failed; the alert says why
	_ = m.Exit(ctx, 3, ping.Body([]byte("last lines\n"))) // an exit code, 0 is success
}

// A progress note changes nothing but the history: a line, a body, or both.
func ExampleMonitor_Log() {
	c, _ := ping.FromEnv()
	ctx := context.Background()
	m := c.Monitor("photo-sync")

	_ = m.Log(ctx, "1203 of 5000 files")
	_ = m.Log(ctx, "", ping.Body([]byte(`{"files":1203,"bytes":88213504}`)), ping.ContentType("application/json"))
}

// Create makes the monitor from its first ping, so a new job needs no
// setup in vink first. Its options set the monitor up; they are used on
// create only and never change a monitor that exists.
func ExampleCreate() {
	c, _ := ping.FromEnv()
	_ = c.Monitor("cache-warmup").Success(context.Background(), ping.Create())

	var created bool
	_ = c.Monitor("nightly-backup").Success(context.Background(), ping.Create(
		ping.Cron("0 3 * * *"), ping.Timezone("Europe/Amsterdam"),
		ping.Grace(30*time.Minute), ping.Tags("backup"), ping.WasCreated(&created),
	))
}

// A ping by id needs no project key: the id alone lets its holder ping
// that one monitor.
func ExampleClient_MonitorID() {
	c, err := ping.New("https://vink.example.com/ping/", "")
	if err != nil {
		log.Fatal(err)
	}
	_ = c.MonitorID(os.Getenv("VINK_MONITOR_ID")).Success(context.Background())
}

// Ping errors tell a wrong key or slug from a server that is away.
func ExampleErrNotFound() {
	c, _ := ping.FromEnv()
	err := c.Monitor("nightly-backup").Success(context.Background())
	var se *ping.StatusError
	switch {
	case err == nil:
	case errors.Is(err, ping.ErrNotFound):
		slog.Error("vink does not know this key or slug", "err", err)
	case errors.As(err, &se):
		slog.Warn("vink answered", "status", se.StatusCode)
	default:
		slog.Warn("vink unreachable", "err", err)
	}
}

func backup(context.Context) error { return nil }
func dump(context.Context) error   { return nil }
func prune(context.Context) error  { return nil }
