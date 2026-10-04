package ping

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strconv"
)

// Run is one run of a job: a start, any number of notes, and a finish,
// all carrying the same run id, so vink pairs them even when runs of the
// monitor overlap and shows the run's duration. Make one with
// [Monitor.NewRun] per execution of the job. A Run sends nothing until
// asked and is safe for concurrent use.
type Run struct {
	m  *Monitor
	id string
}

// NewRun prepares a run with a fresh run id. Nothing is sent yet.
func (m *Monitor) NewRun() *Run {
	// a run id tells runs apart; it grants nothing, so it need not be unguessable
	return &Run{m: m, id: fmt.Sprintf("%016x%016x", rand.Uint64(), rand.Uint64())} //nolint:gosec // G404
}

// ID is the run id every ping of this run carries.
func (r *Run) ID() string { return r.id }

func (r *Run) req(opts []PingOption) request {
	q := build(opts)
	q.runID = r.id
	return q
}

// Start sends the run's start. See [Monitor.Start].
func (r *Run) Start(ctx context.Context, opts ...PingOption) error {
	return r.m.send(ctx, "start", r.req(opts))
}

// Log sends a progress note within the run. See [Monitor.Log].
func (r *Run) Log(ctx context.Context, msg string, opts ...PingOption) error {
	q := r.req(opts)
	if msg != "" {
		q.msg = msg
	}
	if q.msg == "" && len(q.body) == 0 {
		return ErrEmptyNote
	}
	return r.m.send(ctx, "log", q)
}

// Success ends the run as a success. See [Monitor.Success].
func (r *Run) Success(ctx context.Context, opts ...PingOption) error {
	return r.m.send(ctx, "", r.req(opts))
}

// Fail ends the run as a failure. See [Monitor.Fail].
func (r *Run) Fail(ctx context.Context, opts ...PingOption) error {
	return r.m.send(ctx, "fail", r.req(opts))
}

// Exit ends the run with a process's exit code. See [Monitor.Exit].
func (r *Run) Exit(ctx context.Context, code int, opts ...PingOption) error {
	if err := checkExit(code); err != nil {
		return err
	}
	return r.m.send(ctx, strconv.Itoa(code), r.req(opts))
}

// Finish ends the run by its outcome: a success when err is nil, and
// otherwise a failure whose message is err's text (a [Msg] among opts
// replaces it). An err with an ExitCode() method above 0, such as an
// *exec.ExitError, is reported as that exit code.
func (r *Run) Finish(ctx context.Context, err error, opts ...PingOption) error {
	if err == nil {
		return r.Success(ctx, opts...)
	}
	opts = append([]PingOption{Msg(err.Error())}, opts...)
	var ec interface{ ExitCode() int }
	if errors.As(err, &ec) && ec.ExitCode() > 0 {
		return r.Exit(ctx, ec.ExitCode(), opts...)
	}
	return r.Fail(ctx, opts...)
}

// Run wraps a job: it sends a start, calls fn, and ends the run by fn's
// outcome as [Run.Finish] does. A panic in fn is reported as a failure
// and then re-raised. Run returns fn's error unchanged: a ping that
// cannot be sent never fails the job, and goes to the client's
// [WithErrorHandler] instead. The finishing ping is sent even when ctx
// was cancelled while fn ran, within the client's attempts and timeout.
func (m *Monitor) Run(ctx context.Context, fn func(ctx context.Context) error) (err error) {
	run := m.NewRun()
	report := func(perr error) {
		if perr != nil && m.c.onError != nil {
			m.c.onError(perr)
		}
	}
	report(run.Start(ctx))
	done := context.WithoutCancel(ctx)
	defer func() {
		if p := recover(); p != nil {
			report(run.Fail(done, Msg(fmt.Sprintf("panic: %v", p))))
			panic(p)
		}
	}()
	err = fn(ctx)
	report(run.Finish(done, err))
	return err
}
