package run

import "time"

type PendingEffect struct {
	Request EffectRequest
	Policy  EffectPolicy
	Outcome Settlement
	Failure *Failure
}

type ProcessInspection struct {
	ID             ProcessID
	Program        ProgramRef
	Status         Status
	Termination    Termination
	Usage          Usage
	StartedAt      time.Time
	FinishedAt     time.Time
	Waits          []Wait
	PendingEffects []PendingEffect
}

type TreeInspection struct {
	Root      ProcessID
	Head      LSN
	Epoch     Epoch
	Processes []ProcessInspection
	receipts  map[ProcessID][]SignalReceipt
}

func (t TreeInspection) Process(id ProcessID) (ProcessInspection, bool) {
	for _, p := range t.Processes {
		if p.ID == id {
			return p, true
		}
	}
	return ProcessInspection{}, false
}
func (t TreeInspection) Answered(process ProcessID, wait WaitKey, payload Payload) (bool, error) {
	for _, receipt := range t.receipts[process] {
		if receipt.Wait != wait {
			continue
		}
		signal := Signal{ID: receipt.ID, Wait: wait, Payload: payload}
		if signal.digest() != receipt.Digest {
			return false, ErrSignalConflict
		}
		return true, nil
	}
	return false, nil
}
