package agent

import (
	"fmt"
)

// signalAdmission holds only new facts. Historical records remain authoritative
// throughout preflight, and applying an admitted batch cannot fail.
type signalAdmission struct {
	records   []signalRecord
	status    Status
	duplicate bool
}

func newAdmissionRecord(signal Signal, source signalSource) (signalRecord, error) {
	if !signal.Valid() || (source != signalSourceExternal && source != signalSourceChildWait) {
		return signalRecord{}, fmt.Errorf("%w: %w", ErrSignalRejected, ErrInvalidSignal)
	}
	return newSignalRecord(signal, false), nil
}
