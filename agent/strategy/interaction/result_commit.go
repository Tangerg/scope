package interaction

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/core/chat"
)

// ResultCommitter owns host persistence of a complete model-call result batch.
// Results are already known; this boundary must never execute calls or rewrite
// their model-visible output. Return a receipt only after the entire batch is
// durable. An error or a mismatched receipt leaves this publication Effect
// unknown and prevents both model continuation and direct completion.
//
// CommitResults must be idempotent by EffectID: atomically store the receipt
// with the results, return the stored receipt for identical content, and reject
// different content. Before writing after an uncertain attempt, reconcile the
// authoritative transaction; if its outcome remains uncertain, return an error.
// Durable hosts must fence every transaction with the current TreeIncarnationID.
// Scope may replay a pending publication after restore. A settled Unknown is
// replayed only through Process.ReplayUnknownEffect. Both paths reconstruct the
// entire batch from the authoritative tree, retaining EffectID and exact results
// while supplying the current writer. Neither path executes Tools or Delegates.
type ResultCommitter interface {
	CommitResults(ctx context.Context, batch ResultBatch) (ResultReceipt, error)
}

// ResultBatch is immutable and covers every call in one model response, in
// original call order. Relation identifies the requesting Interaction, including
// when a Tool child or Delegate produced the result. Together with this Process,
// ModelCallSequence and the entry's ToolCallIndex identify a logical call even
// when the provider reuses its call ID in later responses.
type ResultBatch struct {
	request agent.EffectRequest
	commit  resultCommit
	receipt ResultReceipt
}

func newResultBatch(request agent.EffectRequest, commit resultCommit) (ResultBatch, error) {
	if !request.Valid() {
		return ResultBatch{}, errors.New("interaction: invalid result commit request")
	}
	digest, err := commit.digest()
	if err != nil {
		return ResultBatch{}, err
	}
	return ResultBatch{request: request, commit: commit, receipt: ResultReceipt{EffectID: request.ID(), Digest: digest}}, nil
}

func (r ResultBatch) Relation() agent.ProcessRelation { return r.request.Relation() }
func (r ResultBatch) ModelCallSequence() uint32       { return r.commit.ModelCallSequence }
func (r ResultBatch) TreeIncarnationID() (agent.TreeIncarnationID, bool) {
	return r.request.TreeIncarnationID()
}

// Receipt identifies the immutable publication. Obtaining it does not commit
// anything; the host must persist it with the batch before returning it.
func (r ResultBatch) Receipt() ResultReceipt { return r.receipt }

func (r ResultBatch) Entries() []ResultEntry {
	entries := make([]ResultEntry, len(r.commit.Results))
	for index, result := range r.commit.Results {
		disposition := ResultSucceeded
		if result.Rejected {
			disposition = ResultRejected
		} else if result.Result.IsError {
			disposition = ResultFailed
		}
		entries[index] = ResultEntry{
			ToolCallIndex: uint32(index), Call: r.commit.Calls[index],
			Result: result.Result.Clone(), Disposition: disposition,
		}
	}
	return entries
}

// ResultDisposition describes a definite outcome. Unknown execution outcomes
// and input checkpoints never enter a ResultBatch. Rejection does not assert
// that a Tool's business operation ran.
type ResultDisposition string

const (
	ResultInvalid   ResultDisposition = ""
	ResultSucceeded ResultDisposition = "succeeded"
	ResultFailed    ResultDisposition = "failed"
	ResultRejected  ResultDisposition = "rejected"
)

func (r ResultDisposition) Valid() bool {
	return r == ResultSucceeded || r == ResultFailed || r == ResultRejected
}

func (r ResultDisposition) String() string {
	if !r.Valid() {
		return "invalid"
	}
	return string(r)
}

// ResultEntry keeps exact provider attribution and output together. Entries
// returned by ResultBatch are independent copies and cannot alter its receipt.
type ResultEntry struct {
	ToolCallIndex uint32
	Call          chat.ToolCall
	Result        chat.ToolResult
	Disposition   ResultDisposition
}

// ResultReceipt binds one publication Effect to its complete canonical content.
// It can be persisted as data and converted to an explicit recovery settlement.
// A receipt is evidence only when read from the authoritative host transaction.
type ResultReceipt struct {
	EffectID agent.EffectID `json:"effect_id"`
	Digest   agent.Digest   `json:"digest"`
}

func (r ResultReceipt) Validate() error {
	if !r.EffectID.Valid() || !r.Digest.Valid() {
		return fmt.Errorf("%w: receipt requires an EffectID and digest", ErrInvalidResult)
	}
	return nil
}

func (r ResultReceipt) Settlement() (agent.Settlement, error) {
	if err := r.Validate(); err != nil {
		return agent.Settlement{}, err
	}
	payload, err := jsonv2.Marshal(signalEnvelope{Operation: operationResultCommit, Receipt: &r}, jsonv2.Deterministic(true))
	if err != nil {
		return agent.Settlement{}, err
	}
	return agent.NewSettlement(r.EffectID, agent.SettlementStatusSucceeded, payload)
}

type resultCommit struct {
	ModelCallSequence uint32           `json:"model_call_sequence"`
	Calls             []chat.ToolCall  `json:"calls"`
	Results           []toolCallResult `json:"results"`
}

func (r resultCommit) validate() error {
	if r.ModelCallSequence == 0 || len(r.Calls) == 0 || len(r.Calls) != len(r.Results) || uint64(len(r.Calls)) > uint64(^uint32(0)) {
		return errors.New("interaction: result commit requires a complete bounded call set")
	}
	seen := make(map[string]struct{}, len(r.Calls))
	for index, call := range r.Calls {
		if err := call.Validate(); err != nil {
			return err
		}
		if _, duplicate := seen[call.ID]; duplicate {
			return errors.New("interaction: result commit contains duplicate call IDs")
		}
		seen[call.ID] = struct{}{}
		if err := r.Results[index].validateCall(call); err != nil {
			return fmt.Errorf("interaction: result commit entry %d: %w", index, err)
		}
	}
	return nil
}

func (r resultCommit) digest() (agent.Digest, error) {
	if err := r.validate(); err != nil {
		return agent.Digest{}, err
	}
	payload, err := jsonv2.Marshal(r, jsonv2.Deterministic(true))
	if err != nil {
		return agent.Digest{}, err
	}
	return agent.ComputeDigest(payload), nil
}
