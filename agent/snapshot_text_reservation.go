package agent

// Each optional text field charges its own worst-case JSON expansion when it
// enters the projection. The byte count cannot drift from a separate field count.
type snapshotTextReservation struct{ growth uint64 }

func (s *snapshotTextReservation) reason() string {
	s.growth += snapshotReasonGrowth
	return snapshotReservationText
}

func (s *snapshotTextReservation) failure() Failure {
	s.growth += snapshotFailureGrowth
	return Failure{kind: FailureKindExecution, code: snapshotReservationText, message: snapshotReservationText}
}
