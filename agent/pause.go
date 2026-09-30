package agent

import (
	"fmt"
)

const maxPauseReasonBytes = 4096

// pause is one explicit scheduling pause. Validation happens only at
// construction, so a present pause always carries an admissible reason.
type pause struct{ reason string }

func newPause(reason string) (pause, error) {
	if !validBoundedText(reason, maxPauseReasonBytes) {
		return pause{}, fmt.Errorf("pause reason must be non-empty, trimmed UTF-8, and at most %d bytes", maxPauseReasonBytes)
	}
	return pause{reason: reason}, nil
}

// parsePause restores an optional pause; an empty wire reason means none.
func parsePause(reason string) (pause, error) {
	if reason == "" {
		return pause{}, nil
	}
	return newPause(reason)
}

func (p pause) valid() bool { return p.reason != "" }
