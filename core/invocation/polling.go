package invocation

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"time"
)

var ErrInvalidPolling = errors.New("invocation: invalid polling policy")

// Polling bounds how a synchronous call waits for an asynchronous provider
// job. A zero field selects the provider's default through [Polling.Or].
type Polling struct {
	Interval time.Duration
	Timeout  time.Duration
}

func (p Polling) Validate() error {
	if p.Interval < 0 || p.Timeout < 0 {
		return fmt.Errorf("%w: interval and timeout must not be negative", ErrInvalidPolling)
	}
	return nil
}

// Or fills each zero field from defaults.
func (p Polling) Or(defaults Polling) Polling {
	return Polling{Interval: cmp.Or(p.Interval, defaults.Interval), Timeout: cmp.Or(p.Timeout, defaults.Timeout)}
}

// Poll fetches at once, because short jobs often finish before the first
// interval, and then once per Interval until fetch reports a terminal value.
// The Timeout bounds the whole wait, including every fetch. fetch owns the
// provider's status vocabulary and rejects a status it does not recognize.
func (p Polling) Poll[T any](ctx context.Context, fetch func(context.Context) (T, bool, error)) (T, error) {
	var zero T
	if p.Interval <= 0 || p.Timeout <= 0 {
		return zero, fmt.Errorf("%w: Poll requires a positive interval and timeout", ErrInvalidPolling)
	}
	deadline, cancel := context.WithTimeout(ctx, p.Timeout)
	defer cancel()
	ticker := time.NewTicker(p.Interval)
	defer ticker.Stop()
	for {
		value, terminal, err := fetch(deadline)
		if err != nil {
			return zero, err
		}
		if terminal {
			return value, nil
		}
		select {
		case <-deadline.Done():
			return zero, deadline.Err()
		case <-ticker.C:
		}
	}
}
