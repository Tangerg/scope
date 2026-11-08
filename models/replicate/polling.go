package replicate

import (
	"cmp"
	"errors"
	"time"
)

type pollingPolicy struct {
	interval time.Duration
	timeout  time.Duration
}

func (p pollingPolicy) validate() error {
	if p.interval < 0 {
		return errors.New("replicate: PollInterval must not be negative")
	}
	if p.timeout < 0 {
		return errors.New("replicate: PollTimeout must not be negative")
	}
	return nil
}

func (p pollingPolicy) resolve(defaultTimeout time.Duration) pollingPolicy {
	p.interval = cmp.Or(p.interval, DefaultPollInterval)
	p.timeout = cmp.Or(p.timeout, defaultTimeout)
	return p
}
