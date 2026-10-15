//go:build integration

package treecommitterconformance

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Tangerg/scope/agent"
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
	descriptor agent.Descriptor
}

func (p probeDefinition) Descriptor() agent.Descriptor { return p.descriptor }

func (probeDefinition) ChildDeployments() []agent.Deployment { return nil }

func (p probeDefinition) Start(input agent.Payload) (agent.Execution, error) {
	if err := p.descriptor.ValidateInput(input); err != nil {
		return nil, err
	}
	value, err := input.Decode[probeValue]()
	if err != nil {
		return nil, err
	}
	return &probeExecution{state: probeState{Phase: probeReady, Value: value.Value}}, nil
}

func (probeDefinition) Restore(ctx context.Context, state agent.ExecutionState) (agent.Execution, error) {
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
		return nil, agent.ErrInvalidExecutionState
	}
}

type probeExecution struct {
	state probeState
}

func (p *probeExecution) Step(ctx context.Context, signals []agent.Signal) (agent.Transition, error) {
	if err := ctx.Err(); err != nil {
		return agent.Transition{}, err
	}
	switch p.state.Phase {
	case probeReady:
		if len(signals) != 0 {
			return agent.Transition{}, agent.ErrInvalidSignal
		}
		payload, err := jsonv2.Marshal(probeValue{Value: p.state.Value})
		if err != nil {
			return agent.Transition{}, err
		}
		effect, err := agent.NewDispatcherEffect(payload)
		if err != nil {
			return agent.Transition{}, err
		}
		p.state.Phase = probeAwaiting
		return agent.Continue(0, effect)
	case probeAwaiting:
		if len(signals) != 1 {
			return agent.Transition{}, agent.ErrInvalidSignal
		}
		settlement, err := agent.ParseSettlement(signals[0])
		if err != nil || settlement.Status() != agent.SettlementStatusSucceeded {
			return agent.Transition{}, agent.ErrInvalidSignal
		}
		var value probeValue
		if decodeErr := jsonv2.Unmarshal(signals[0].Payload(), &value, jsonv2.RejectUnknownMembers(true)); decodeErr != nil {
			return agent.Transition{}, decodeErr
		}
		output, err := agent.EncodePayload(value)
		if err != nil {
			return agent.Transition{}, err
		}
		p.state.Phase = probeCompleted
		return agent.Complete(1, output)
	default:
		return agent.Transition{}, agent.ErrInvalidExecutionState
	}
}

func (p *probeExecution) Snapshot() (agent.ExecutionState, error) {
	encoded, err := jsonv2.Marshal(p.state)
	if err != nil {
		return agent.ExecutionState{}, err
	}
	return agent.ParseExecutionState(probeStateKind, encoded)
}

type probeDispatcher struct {
	pool *pgxpool.Pool
	gate func(context.Context, agent.EffectRequest) error
}

func (p probeDispatcher) Dispatch(ctx context.Context, request agent.EffectRequest, _ agent.DeltaEmitter) (agent.Settlement, error) {
	attempt, dispatched := request.AttemptID()
	if !dispatched {
		return agent.Settlement{}, errors.New("probe: missing physical dispatch identity")
	}
	if _, err := p.pool.Exec(ctx, "INSERT INTO external_calls(effect_id,attempt_id,payload) VALUES($1,$2,$3)",
		request.ID().String(), attempt.String(), request.Effect().Payload()); err != nil {
		return agent.Settlement{}, err
	}
	if p.gate != nil {
		if err := p.gate(ctx, request); err != nil {
			return agent.Settlement{}, err
		}
	}
	return agent.NewSettlement(agent.SettlementStatusSucceeded, request.Effect().Payload())
}

func (probeDispatcher) Policy(agent.Effect) agent.EffectPolicy {
	return agent.EffectPolicy{Replay: agent.ReplayPolicyNever}
}

func probeDeployment(dispatcher probeDispatcher) (agent.Deployment, error) {
	schema, err := agent.SchemaFor[probeValue]()
	if err != nil {
		return agent.Deployment{}, err
	}
	descriptor, err := agent.NewDescriptor(agent.DescriptorConfig{
		Name: probeStateKind, Description: "Persist an external receipt through a managed Effect.", InputSchema: schema, OutputSchema: schema,
	})
	if err != nil {
		return agent.Deployment{}, err
	}
	return agent.NewDeployment(agent.DeploymentConfig{
		Definition: probeDefinition{descriptor: descriptor}, Dispatcher: dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("postgres-crash-probe")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("one-effect-replay-never")),
	})
}

func reconcileProbe(ctx context.Context, pool *pgxpool.Pool, id agent.EffectID) (agent.Settlement, error) {
	rows, err := pool.Query(ctx, "SELECT payload FROM external_calls WHERE effect_id=$1", id.String())
	if err != nil {
		return agent.Settlement{}, err
	}
	defer rows.Close()
	var payload []byte
	if !rows.Next() {
		return agent.Settlement{}, errors.Join(errors.New("probe: no external receipt"), rows.Err())
	}
	if err := rows.Scan(&payload); err != nil {
		return agent.Settlement{}, err
	}
	if rows.Next() {
		return agent.Settlement{}, errors.New("probe: more than one physical dispatch")
	}
	if err := rows.Err(); err != nil {
		return agent.Settlement{}, err
	}
	return agent.NewSettlement(agent.SettlementStatusSucceeded, payload)
}
