package trajectory

// EvidenceGaps records known losses in observation, independently of runtime
// outcomes. A lost callback cannot establish success, failure, or Unknown.
// Restored history and RuntimeStopped are retained as Agent events and do not
// increment these counters. Zero counters do not certify unobserved history.
type EvidenceGaps struct {
	DroppedEvents           uint64 `json:"dropped_events,omitzero"`
	DroppedCallObservations uint64 `json:"dropped_call_observations,omitzero"`
	UnpairedCalls           uint64 `json:"unpaired_calls,omitzero"`
}

func (e EvidenceGaps) IsZero() bool {
	return e == (EvidenceGaps{})
}

func incrementGap(count *uint64) {
	if *count < ^uint64(0) {
		*count++
	}
}
