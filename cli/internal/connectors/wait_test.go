package connectors

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pubnub/blocks-sdk/cli/internal/blocksapi"
)

func TestConnectDone(t *testing.T) {
	own := AccountConnection{ID: "own", Status: "active", IsMine: true, UpdatedAt: "t1"}
	ownDead := AccountConnection{ID: "own", Status: "dead", IsMine: true, UpdatedAt: "t1"}
	other := AccountConnection{ID: "other", Status: "active", UpdatedAt: "t1"}

	cases := []struct {
		name    string
		before  []AccountConnection
		current []AccountConnection
		wantID  string
	}{
		{"unchanged own row", []AccountConnection{own}, []AccountConnection{own}, ""},
		{"new connection by someone else", []AccountConnection{own}, []AccountConnection{own, {ID: "n", Status: "active", UpdatedAt: "t2"}}, ""},
		{"new own active row", []AccountConnection{other}, []AccountConnection{other, {ID: "n", Status: "active", IsMine: true, UpdatedAt: "t2"}}, "n"},
		{"own row dead to active, same updatedAt", []AccountConnection{ownDead}, []AccountConnection{own}, "own"},
		{"own active row server updatedAt moved", []AccountConnection{own}, []AccountConnection{{ID: "own", Status: "active", IsMine: true, UpdatedAt: "t2"}}, "own"},
		{"own row updated but now dead", []AccountConnection{own}, []AccountConnection{{ID: "own", Status: "dead", IsMine: true, UpdatedAt: "t2"}}, ""},
	}
	for _, tc := range cases {
		if got, ok := ConnectDone(NewSnapshot(tc.before), tc.current); ok != (tc.wantID != "") || got.ID != tc.wantID {
			t.Errorf("%s: got %+v %v", tc.name, got, ok)
		}
	}
}

type fakeClock struct{ now time.Time }

func (f *fakeClock) Now() time.Time { return f.now }
func (f *fakeClock) Sleep(_ context.Context, d time.Duration) error {
	f.now = f.now.Add(d)
	return nil
}

func TestWaiterRetriesTransientPollErrorsAndStopsOnOthers(t *testing.T) {
	own := AccountConnection{ID: "n", Status: "active", IsMine: true, UpdatedAt: "t1"}
	apiErr := func(status int) error { return &blocksapi.APIError{StatusCode: status} }
	transient := []error{apiErr(503), apiErr(429), errors.New("connection reset")}
	for _, failure := range transient {
		clock := &fakeClock{now: time.Unix(0, 0)}
		polls := 0
		w := Waiter{Interval: time.Second, Timeout: time.Minute, Now: clock.Now, Sleep: clock.Sleep,
			Poll: func(context.Context) ([]AccountConnection, error) {
				polls++
				if polls == 1 {
					return nil, failure
				}
				return []AccountConnection{own}, nil
			}}
		if got, err := w.Wait(context.Background()); err != nil || got.ID != "n" || polls != 2 {
			t.Fatalf("%v: got %+v err %v after %d polls", failure, got, err, polls)
		}
	}
	for _, status := range []int{401, 403, 404} {
		polls := 0
		w := Waiter{Poll: func(context.Context) ([]AccountConnection, error) { polls++; return nil, apiErr(status) }}
		var got *blocksapi.APIError
		if _, err := w.Wait(context.Background()); !errors.As(err, &got) || got.StatusCode != status || polls != 1 {
			t.Fatalf("status %d: err %v after %d polls", status, err, polls)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := Waiter{Interval: time.Hour, Poll: func(ctx context.Context) ([]AccountConnection, error) { return nil, ctx.Err() }}
	if _, err := w.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestWaiterTimeoutCarriesTheLastPollError(t *testing.T) {
	clock := &fakeClock{now: time.Unix(0, 0)}
	down := &blocksapi.APIError{StatusCode: 503, Message: "backend down"}
	polls := 0
	w := Waiter{Interval: time.Second, Timeout: 3 * time.Second, Now: clock.Now, Sleep: clock.Sleep,
		Poll: func(context.Context) ([]AccountConnection, error) { polls++; return nil, down }}
	_, err := w.Wait(context.Background())
	var timeout *WaitTimeoutError
	var last *blocksapi.APIError
	if !errors.As(err, &timeout) || timeout.After != 3*time.Second || !errors.As(err, &last) || last != down {
		t.Fatalf("err = %v", err)
	}
	if polls != 4 || clock.now.Sub(time.Unix(0, 0)) != 3*time.Second {
		t.Fatalf("polls %d, elapsed %v", polls, clock.now.Sub(time.Unix(0, 0)))
	}
}

func TestWaiterDeadlineEndsAStalledPoll(t *testing.T) {
	w := Waiter{
		Timeout: 50 * time.Millisecond,
		Poll: func(ctx context.Context) ([]AccountConnection, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	done := make(chan error, 1)
	go func() { _, err := w.Wait(context.Background()); done <- err }()
	select {
	case err := <-done:
		var timeout *WaitTimeoutError
		if !errors.As(err, &timeout) {
			t.Fatalf("err = %v, want a WaitTimeoutError", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a stalled poll outlived the wait's deadline")
	}
}
