package connectors

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

var (
	DefaultWaitInterval = 2 * time.Second
	DefaultWaitTimeout  = 10 * time.Minute
)

// TimeoutHint is the likely cause of a connect wait that never ends.
const TimeoutHint = "the browser must be signed in as the same user as the CLI, in the same org, and that user must hold the PubNub Connectors permission"

// Snapshot is the org's connections as they stood before the browser opened.
type Snapshot map[string]AccountConnection

func NewSnapshot(conns []AccountConnection) Snapshot {
	s := make(Snapshot, len(conns))
	for _, c := range conns {
		s[c.ID] = c
	}
	return s
}

// ConnectDone reports the connection the caller just made. Only the caller's own active rows count: a
// connection id absent from the snapshot, a snapshot row that went dead to active, or an active row
// whose server updatedAt changed. Local time is never consulted.
func ConnectDone(snap Snapshot, current []AccountConnection) (AccountConnection, bool) {
	for _, c := range current {
		if !c.IsMine || c.Status != ConnectionActive {
			continue
		}
		before, seen := snap[c.ID]
		if !seen || before.Status == ConnectionDead || before.UpdatedAt != c.UpdatedAt {
			return c, true
		}
	}
	return AccountConnection{}, false
}

// WaitTimeoutError is returned when no connection appeared in time.
type WaitTimeoutError struct {
	After   time.Duration
	LastErr error
}

func (e *WaitTimeoutError) Error() string {
	if e.LastErr != nil {
		return fmt.Sprintf("no PubNub account was connected within %s (last check failed: %v)", e.After, e.LastErr)
	}
	return fmt.Sprintf("no PubNub account was connected within %s", e.After)
}

func (e *WaitTimeoutError) Unwrap() error { return e.LastErr }

// Waiter polls for the caller's new connection. Zero Interval, Timeout, Now and Sleep take their defaults.
type Waiter struct {
	Snapshot Snapshot
	Interval time.Duration
	Timeout  time.Duration
	Now      func() time.Time
	Sleep    func(ctx context.Context, d time.Duration) error
	Poll     func(ctx context.Context) ([]AccountConnection, error)
}

func (w Waiter) Wait(ctx context.Context) (AccountConnection, error) {
	interval, timeout, now, sleep := w.Interval, w.Timeout, w.Now, w.Sleep
	if interval <= 0 {
		interval = DefaultWaitInterval
	}
	if timeout <= 0 {
		timeout = DefaultWaitTimeout
	}
	if now == nil {
		now = time.Now
	}
	if sleep == nil {
		sleep = sleepContext
	}
	deadline := now().Add(timeout)
	var lastErr error
	for {
		pollCtx, cancel := context.WithTimeout(ctx, deadline.Sub(now()))
		current, err := w.Poll(pollCtx)
		expired := errors.Is(pollCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil
		cancel()
		if expired {
			return AccountConnection{}, &WaitTimeoutError{After: timeout, LastErr: lastErr}
		}
		lastErr = err
		switch {
		case err == nil:
			if c, ok := ConnectDone(w.Snapshot, current); ok {
				return c, nil
			}
		case !transient(ctx, err):
			return AccountConnection{}, err
		}
		if !now().Before(deadline) {
			return AccountConnection{}, &WaitTimeoutError{After: timeout, LastErr: lastErr}
		}
		if err := sleep(ctx, interval); err != nil {
			return AccountConnection{}, err
		}
	}
}

func transient(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	if e := apiError(err); e != nil {
		return e.StatusCode >= http.StatusInternalServerError || e.StatusCode == http.StatusTooManyRequests
	}
	return !errors.Is(err, context.Canceled)
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
