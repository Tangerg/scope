package agent_test

import (
	"github.com/Tangerg/scope/agent"
)

type episodeInputDisposition string

const (
	episodeInputPending     episodeInputDisposition = "pending"
	episodeInputConsumed    episodeInputDisposition = "consumed"
	episodeInputRetained    episodeInputDisposition = "retained_at_predecessor"
	episodeInputNotAdmitted episodeInputDisposition = "not_admitted"
)

type episodeInput struct {
	root         agent.ProcessID
	recipient    agent.ProcessID
	request      agent.SignalRequest
	acknowledged bool
	disposition  episodeInputDisposition
}
