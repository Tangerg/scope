package run

import (
	"errors"
	"fmt"
	"slices"
)

const invalidEnumName = "invalid"

var ErrInvalidStatus = errors.New("run: invalid status")

// Status is the complete common lifecycle state of a Process. Strategy-specific
// conditions such as a Planning no-plan result do not add common statuses.
type Status string

const (
	StatusInvalid   Status = ""
	StatusRunning   Status = "running"
	StatusWaiting   Status = "waiting"
	StatusPaused    Status = "paused"
	StatusDraining  Status = "draining"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCanceled  Status = "canceled"
	StatusTimedOut  Status = "timed_out"
	StatusKilled    Status = "killed"
)

func (s Status) String() string {
	if !s.Valid() {
		return invalidEnumName
	}
	return string(s)
}

func parseStatus(value string) (Status, error) {
	status := Status(value)
	if !status.Valid() {
		return StatusInvalid, fmt.Errorf("%w: unknown value %q", ErrInvalidStatus, value)
	}
	return status, nil
}

func (s Status) Valid() bool {
	return slices.Contains(s.JSONSchemaEnum(), string(s))
}

func (Status) JSONSchemaEnum() []string {
	return []string{string(StatusRunning), string(StatusWaiting), string(StatusPaused), string(StatusDraining),
		string(StatusCompleted), string(StatusFailed), string(StatusCanceled), string(StatusTimedOut), string(StatusKilled)}
}

// Terminal reports whether the Process may never transition again.
func (s Status) Terminal() bool {
	switch s {
	case StatusCompleted, StatusFailed, StatusCanceled, StatusTimedOut, StatusKilled:
		return true
	default:
		return false
	}
}

func (s Status) MarshalText() ([]byte, error) {
	if !s.Valid() {
		return nil, ErrInvalidStatus
	}
	return []byte(s), nil
}
func (s *Status) UnmarshalText(text []byte) error {
	if s == nil {
		return ErrInvalidStatus
	}
	value, err := parseStatus(string(text))
	if err != nil {
		return err
	}
	*s = value
	return nil
}
