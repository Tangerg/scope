package agent

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/core/jsonschema"
)

var ErrInvalidExternalWait = errors.New("agent: invalid external wait")

// ExternalWaitSpec is the immutable, persistable acceptance contract of one
// Host-answered wait. The mailbox that owns the wait validates every external
// answer against it at admission, so a rejected answer never advances or closes
// the wait and a correct answer can still arrive. Recovery rebuilds the contract
// from the persisted opening, so the rule a restored wait enforces is the rule it
// opened with.
//
// AnswerSchema, when valid, is the schema the answer payload must satisfy; an
// invalid (zero) schema accepts any RFC 7493 JSON value. AnswerIsOutput marks a
// wait whose admitted answer Signal is delivered verbatim as the Execution's
// output, so its deliverable encoding must also fit the payload size limit:
// without this the accept set would exceed the deliverable set and a schema-valid
// answer could be admitted yet fail to deliver.
type ExternalWaitSpec struct {
	key            WaitKey
	answerSchema   jsonschema.Schema
	answerIsOutput bool
}

type ExternalWaitSpecConfig struct {
	Key            WaitKey
	AnswerSchema   jsonschema.Schema
	AnswerIsOutput bool
}

func NewExternalWaitSpec(config ExternalWaitSpecConfig) (ExternalWaitSpec, error) {
	if !config.Key.Valid() {
		return ExternalWaitSpec{}, fmt.Errorf("%w: key: %w", ErrInvalidExternalWait, ErrInvalidIdentity)
	}
	return ExternalWaitSpec{
		key:            config.Key,
		answerSchema:   config.AnswerSchema,
		answerIsOutput: config.AnswerIsOutput,
	}, nil
}

func (e ExternalWaitSpec) Key() WaitKey { return e.key }

func (e ExternalWaitSpec) Valid() bool { return e.key.Valid() }

// admits reports whether signal satisfies this wait's acceptance contract. A
// failure is an admission rejection: the wait stays open for a later answer.
func (e ExternalWaitSpec) admits(signal Signal) error {
	if e.answerSchema.Valid() {
		if err := e.answerSchema.Validate(signal.payload); err != nil {
			return fmt.Errorf("%w: answer schema: %w", ErrSignalRejected, err)
		}
	}
	if e.answerIsOutput {
		if _, err := EncodePayload(signal); err != nil {
			return fmt.Errorf("%w: answer is not deliverable: %w", ErrSignalRejected, err)
		}
	}
	return nil
}

type externalWaitSpecWire struct {
	Key            WaitKey         `json:"key"`
	AnswerSchema   json.RawMessage `json:"answer_schema,omitzero"`
	AnswerIsOutput bool            `json:"answer_is_output,omitzero"`
}

func (e ExternalWaitSpec) wire() externalWaitSpecWire {
	wire := externalWaitSpecWire{Key: e.key, AnswerIsOutput: e.answerIsOutput}
	if e.answerSchema.Valid() {
		wire.AnswerSchema = e.answerSchema.JSON()
	}
	return wire
}

func (e externalWaitSpecWire) value() (ExternalWaitSpec, error) {
	config := ExternalWaitSpecConfig{Key: e.Key, AnswerIsOutput: e.AnswerIsOutput}
	if len(e.AnswerSchema) > 0 {
		schema, err := jsonschema.Parse(e.AnswerSchema)
		if err != nil {
			return ExternalWaitSpec{}, fmt.Errorf("%w: answer schema: %w", ErrInvalidExternalWait, err)
		}
		config.AnswerSchema = schema
	}
	return NewExternalWaitSpec(config)
}
