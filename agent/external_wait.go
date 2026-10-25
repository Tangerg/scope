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
// invalid (zero) schema accepts any RFC 7493 JSON value. MaxAnswerBytes bounds
// the canonical answer consumed by the Strategy. AnswerIsOutput marks a
// wait whose admitted answer Signal is delivered verbatim as the Execution's
// output, so its deliverable encoding must also fit the payload size limit:
// without this the accept set would exceed the deliverable set and a schema-valid
// answer could be admitted yet fail to deliver.
type ExternalWaitSpec struct {
	key            WaitKey
	answerSchema   jsonschema.Schema
	maxAnswerBytes int
	answerIsOutput bool
}

type ExternalWaitSpecConfig struct {
	Key          WaitKey
	AnswerSchema jsonschema.Schema
	// Zero uses MaxPayloadBytes; a smaller bound must match the answer consumer.
	MaxAnswerBytes int
	AnswerIsOutput bool
}

func NewExternalWaitSpec(config ExternalWaitSpecConfig) (ExternalWaitSpec, error) {
	if !config.Key.Valid() {
		return ExternalWaitSpec{}, fmt.Errorf("%w: key: %w", ErrInvalidExternalWait, ErrInvalidIdentity)
	}
	if config.MaxAnswerBytes < 0 || config.MaxAnswerBytes > MaxPayloadBytes {
		return ExternalWaitSpec{}, fmt.Errorf("%w: answer byte limit must be between 1 and %d", ErrInvalidExternalWait, MaxPayloadBytes)
	}
	if config.MaxAnswerBytes == 0 {
		config.MaxAnswerBytes = MaxPayloadBytes
	}
	return ExternalWaitSpec{
		key:            config.Key,
		answerSchema:   config.AnswerSchema,
		maxAnswerBytes: config.MaxAnswerBytes,
		answerIsOutput: config.AnswerIsOutput,
	}, nil
}

func (e ExternalWaitSpec) Key() WaitKey { return e.key }

func (e ExternalWaitSpec) Valid() bool {
	return e.key.Valid() && e.maxAnswerBytes > 0 && e.maxAnswerBytes <= MaxPayloadBytes
}

// admits reports whether signal satisfies this wait's acceptance contract. A
// failure is an admission rejection: the wait stays open for a later answer.
func (e ExternalWaitSpec) admits(signal Signal) error {
	if len(signal.payload) > e.maxAnswerBytes {
		return fmt.Errorf("%w: answer: %w: %d bytes exceeds %d", ErrSignalRejected, ErrPayloadTooLarge, len(signal.payload), e.maxAnswerBytes)
	}
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
	MaxAnswerBytes int             `json:"max_answer_bytes"`
	AnswerIsOutput bool            `json:"answer_is_output,omitzero"`
}

func (e ExternalWaitSpec) wire() externalWaitSpecWire {
	wire := externalWaitSpecWire{Key: e.key, MaxAnswerBytes: e.maxAnswerBytes, AnswerIsOutput: e.answerIsOutput}
	if e.answerSchema.Valid() {
		wire.AnswerSchema = e.answerSchema.JSON()
	}
	return wire
}

func (e externalWaitSpecWire) value() (ExternalWaitSpec, error) {
	if e.MaxAnswerBytes <= 0 {
		return ExternalWaitSpec{}, fmt.Errorf("%w: persisted answer byte limit must be positive", ErrInvalidExternalWait)
	}
	config := ExternalWaitSpecConfig{Key: e.Key, MaxAnswerBytes: e.MaxAnswerBytes, AnswerIsOutput: e.AnswerIsOutput}
	if len(e.AnswerSchema) > 0 {
		schema, err := jsonschema.Parse(e.AnswerSchema)
		if err != nil {
			return ExternalWaitSpec{}, fmt.Errorf("%w: answer schema: %w", ErrInvalidExternalWait, err)
		}
		config.AnswerSchema = schema
	}
	return NewExternalWaitSpec(config)
}
