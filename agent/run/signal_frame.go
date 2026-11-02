package run

import (
	"bytes"
	"encoding/json"
)

// SignalFrame is the closed set of inbox bodies admitted by the runtime:
// ExternalInput, Settlement, WaitOpened, ChildStartResult, ChildControlResult,
// ChildWaitOpened, or ChildWaitSatisfied. A definite dispatcher Settlement
// retains its opaque payload; an unknown outcome never enters the inbox.
type SignalFrame interface{ signalFrame() }

// ExternalInput belongs to the sender; its wait address and delivery identity
// remain on the Signal.
type ExternalInput struct{ payload json.RawMessage }

func (ExternalInput) signalFrame() {}

func (e ExternalInput) Payload() json.RawMessage { return bytes.Clone(e.payload) }

type WaitOpened struct{ waitID WaitID }

func (WaitOpened) signalFrame() {}

func (w WaitOpened) WaitID() WaitID { return w.waitID }
