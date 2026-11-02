//go:build integration

package postgres_test

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

type probeValue struct {
	Value string `json:"value"`
}

type probePhase string

const (
	probeReady     probePhase = "ready"
	probeAwaiting  probePhase = "awaiting"
	probeCompleted probePhase = "completed"
	probeStateKind            = "treecommitter.probe"
)

type probeState struct {
	Phase probePhase `json:"phase"`
	Value string     `json:"value"`
}

type probeDefinition struct {
	descriptor run.Descriptor
}

func (p probeDefinition) Descriptor() run.Descriptor { return p.descriptor }

func (probeDefinition) ChildPrograms() []run.Program[run.Payload, run.Payload] { return nil }

func (p probeDefinition) Start(input run.Payload) (run.Execution, error) {
	if err := p.descriptor.ValidateInput(input); err != nil {
		return nil, err
	}
	value, err := input.Decode[probeValue]()
	if err != nil {
		return nil, err
	}
	return &probeExecution{state: probeState{Phase: probeReady, Value: value.Value}}, nil
}

func (probeDefinition) Restore(ctx context.Context, state run.ExecutionState) (run.Execution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	value, err := state.Decode[probeState](probeStateKind)
	if err != nil {
		return nil, err
	}
	switch value.Phase {
	case probeReady, probeAwaiting, probeCompleted:
		return &probeExecution{state: value}, nil
	default:
		return nil, run.ErrInvalidExecutionState
	}
}

type probeExecution struct {
	state probeState
}

func (p *probeExecution) Step(ctx context.Context, signals []run.Signal) (run.Transition, error) {
	if err := ctx.Err(); err != nil {
		return run.Transition{}, err
	}
	switch p.state.Phase {
	case probeReady:
		if len(signals) != 0 {
			return run.Transition{}, run.ErrInvalidSignal
		}
		payload, err := jsonv2.Marshal(probeValue{Value: p.state.Value})
		if err != nil {
			return run.Transition{}, err
		}
		effect, err := run.NewDispatcherEffect(payload)
		if err != nil {
			return run.Transition{}, err
		}
		p.state.Phase = probeAwaiting
		return run.Continue(0, effect)
	case probeAwaiting:
		if len(signals) != 1 {
			return run.Transition{}, run.ErrInvalidSignal
		}
		settlement, err := signals[0].Frame[run.Settlement]()
		if err != nil || settlement.Status() != run.SettlementStatusSucceeded {
			return run.Transition{}, run.ErrInvalidSignal
		}
		var value probeValue
		if decodeErr := jsonv2.Unmarshal(signals[0].Payload(), &value, jsonv2.RejectUnknownMembers(true)); decodeErr != nil {
			return run.Transition{}, decodeErr
		}
		output, err := run.EncodePayload(value)
		if err != nil {
			return run.Transition{}, err
		}
		p.state.Phase = probeCompleted
		return run.Complete(1, output)
	default:
		return run.Transition{}, run.ErrInvalidExecutionState
	}
}

func (p *probeExecution) Snapshot() (run.ExecutionState, error) {
	encoded, err := jsonv2.Marshal(p.state)
	if err != nil {
		return run.ExecutionState{}, err
	}
	return run.ParseExecutionState(probeStateKind, encoded)
}

type probeDispatcher struct {
	pool *pgxpool.Pool
	gate func(context.Context, run.EffectRequest) error
}

func (p probeDispatcher) Dispatch(ctx context.Context, request run.EffectRequest, _ run.DeltaEmitter) (run.Settlement, error) {
	attempt, dispatched := request.AttemptID()
	if !dispatched {
		return run.Settlement{}, errors.New("probe: missing physical dispatch identity")
	}
	if _, err := p.pool.Exec(ctx, "INSERT INTO external_calls(effect_id,attempt_id,payload) VALUES($1,$2,$3)",
		request.ID().String(), attempt.String(), request.Effect().Payload()); err != nil {
		return run.Settlement{}, err
	}
	if p.gate != nil {
		if err := p.gate(ctx, request); err != nil {
			return run.Settlement{}, err
		}
	}
	return run.NewSettlement(run.SettlementStatusSucceeded, request.Effect().Payload())
}

func (probeDispatcher) Policy(run.Effect) run.EffectPolicy {
	return run.EffectPolicy{Replay: run.ReplayPolicyNever}
}

func probeDeployment(dispatcher probeDispatcher) (run.Program[run.Payload, run.Payload], error) {
	schema, err := jsonschema.For[probeValue]()
	if err != nil {
		return run.Program[run.Payload, run.Payload]{}, err
	}
	descriptor, err := run.NewDescriptor(run.DescriptorConfig{
		Name: probeStateKind, Description: "Persist an external receipt through a managed Effect.", InputSchema: schema, OutputSchema: schema,
	})
	if err != nil {
		return run.Program[run.Payload, run.Payload]{}, err
	}
	return run.Bind[run.Payload, run.Payload](run.BindingConfig{
		Definition: probeDefinition{descriptor: descriptor}, Dispatcher: dispatcher,
		ImplementationDigest: run.ComputeDigest([]byte("postgres-crash-probe")),
		ConfigurationDigest:  run.ComputeDigest([]byte("one-effect-replay-never")),
	})
}

func reconcileProbe(ctx context.Context, pool *pgxpool.Pool, id run.EffectID) (run.Settlement, error) {
	rows, err := pool.Query(ctx, "SELECT payload FROM external_calls WHERE effect_id=$1", id.String())
	if err != nil {
		return run.Settlement{}, err
	}
	defer rows.Close()
	var payload []byte
	if !rows.Next() {
		return run.Settlement{}, errors.Join(errors.New("probe: no external receipt"), rows.Err())
	}
	if err := rows.Scan(&payload); err != nil {
		return run.Settlement{}, err
	}
	if rows.Next() {
		return run.Settlement{}, errors.New("probe: more than one physical dispatch")
	}
	if err := rows.Err(); err != nil {
		return run.Settlement{}, err
	}
	return run.NewSettlement(run.SettlementStatusSucceeded, payload)
}
