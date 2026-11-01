package run_test

import (
	"github.com/Tangerg/scope/agent/run"
)

type episodeInputDisposition string

const (
	episodeInputPending     episodeInputDisposition = "pending"
	episodeInputConsumed    episodeInputDisposition = "consumed"
	episodeInputRetained    episodeInputDisposition = "retained_at_predecessor"
	episodeInputNotAdmitted episodeInputDisposition = "not_admitted"
)

type episodeInput struct {
	root         run.ProcessID
	recipient    run.ProcessID
	request      run.SignalRequest
	acknowledged bool
	disposition  episodeInputDisposition
}
