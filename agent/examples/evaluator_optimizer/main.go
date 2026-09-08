// Command evaluator_optimizer demonstrates bounded evaluator-optimizer
// composition with exact managed child Processes. It uses deterministic local
// workers and requires no credentials or network access.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strings"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/workflow"
)

const (
	acceptanceThreshold       = 0.9
	maximumOptimizationRounds = 3
	workerBudgetSteps         = 8
	workerBudgetEffects       = 4
	workerBudgetSignals       = 8
	iterationBudgetSteps      = 64
	iterationBudgetEffects    = 32
	iterationBudgetSignals    = 64
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, output io.Writer) error {
	report, evidence, err := execute(
		ctx,
		optimizationRequest{Objective: "improve the release draft"},
		[]float64{0.4, 0.7, 0.95},
		acceptanceThreshold,
		maximumOptimizationRounds,
	)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(
		output,
		"objective: %s\nbest: %s\nscore: %.2f\nattempts: %d\naccepted: %t\niterations: %d\nprocesses: %d\n",
		report.Objective,
		report.Best.Candidate.Content,
		report.Best.Assessment.Score,
		len(report.History),
		report.Accepted,
		report.Iterations,
		evidence.ProcessCount,
	)
	return err
}

type optimizationRequest struct {
	Objective string `json:"objective"`
}

type candidate struct {
	Revision uint32 `json:"revision"`
	Content  string `json:"content"`
}

type assessment struct {
	Score    float64 `json:"score"`
	Feedback string  `json:"feedback"`
}

type attempt struct {
	Candidate  candidate  `json:"candidate"`
	Assessment assessment `json:"assessment"`
}

type optimizationState struct {
	Objective string    `json:"objective"`
	History   []attempt `json:"history"`
	Current   candidate `json:"current"`
	Best      attempt   `json:"best"`
	HasBest   bool      `json:"has_best"`
	Accepted  bool      `json:"accepted"`
}

type optimizationReport struct {
	Objective  string    `json:"objective"`
	History    []attempt `json:"history"`
	Best       attempt   `json:"best"`
	Accepted   bool      `json:"accepted"`
	Iterations uint32    `json:"iterations"`
}

type executionEvidence struct {
	ProcessCount int
	Deployments  map[string]int
}

func execute(
	ctx context.Context,
	request optimizationRequest,
	scores []float64,
	threshold float64,
	maxIterations uint32,
) (_ optimizationReport, _ executionEvidence, err error) {
	root, resolver, err := newEvaluatorOptimizer(scores, threshold, maxIterations)
	if err != nil {
		return optimizationReport{}, executionEvidence{}, err
	}
	engine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: resolver})
	if err != nil {
		return optimizationReport{}, executionEvidence{}, err
	}
	defer func() {
		err = errors.Join(err, engine.Close())
	}()

	input, err := agent.EncodeInput(request)
	if err != nil {
		return optimizationReport{}, executionEvidence{}, err
	}
	process, err := engine.Start(ctx, root, input)
	if err != nil {
		return optimizationReport{}, executionEvidence{}, err
	}
	result, err := process.Await(ctx)
	if err != nil {
		return optimizationReport{}, executionEvidence{}, err
	}
	report, err := decodeCompleted[optimizationReport](result)
	if err != nil {
		return optimizationReport{}, executionEvidence{}, err
	}
	tree, err := engine.CaptureTree(ctx, process.ID())
	if err != nil {
		return optimizationReport{}, executionEvidence{}, err
	}
	snapshots := tree.ProcessSnapshots()
	evidence := executionEvidence{
		ProcessCount: len(snapshots),
		Deployments:  make(map[string]int),
	}
	for _, snapshot := range snapshots {
		evidence.Deployments[snapshot.DeploymentRef().Name()]++
	}
	return report, evidence, nil
}

func newEvaluatorOptimizer(
	scores []float64,
	threshold float64,
	maxIterations uint32,
) (agent.Deployment, deploymentResolver, error) {
	frozenScores, err := validateScoreSchedule(scores, threshold, maxIterations)
	if err != nil {
		return agent.Deployment{}, nil, err
	}
	optimizer, err := newOptimizerDeployment(threshold)
	if err != nil {
		return agent.Deployment{}, nil, err
	}
	evaluator, err := newEvaluatorDeployment(frozenScores, threshold)
	if err != nil {
		return agent.Deployment{}, nil, err
	}
	iteration, err := newIterationDeployment(optimizer, evaluator)
	if err != nil {
		return agent.Deployment{}, nil, err
	}
	root, err := newOptimizationRoot(iteration, threshold, maxIterations)
	if err != nil {
		return agent.Deployment{}, nil, err
	}
	return root, deploymentResolver{
		optimizer.DeploymentRef(): optimizer,
		evaluator.DeploymentRef(): evaluator,
		iteration.DeploymentRef(): iteration,
	}, nil
}

func validateScoreSchedule(
	scores []float64,
	threshold float64,
	maxIterations uint32,
) ([]float64, error) {
	if maxIterations == 0 || len(scores) != int(maxIterations) {
		return nil, errors.New("score schedule must contain exactly one score per configured iteration")
	}
	if !validScore(threshold) || threshold == 0 {
		return nil, errors.New("acceptance threshold must be within (0, 1]")
	}
	frozenScores := slices.Clone(scores[:maxIterations])
	for index, score := range frozenScores {
		if !validScore(score) {
			return nil, fmt.Errorf("score schedule entry %d must be within [0, 1]", index)
		}
	}
	return frozenScores, nil
}

func newOptimizerDeployment(threshold float64) (agent.Deployment, error) {
	return transformDeployment(
		"example.evaluator_optimizer.optimizer",
		"Produce one revised candidate from the objective and latest evaluator feedback.",
		struct {
			Threshold float64 `json:"threshold"`
		}{Threshold: threshold},
		func(_ context.Context, state optimizationState) (optimizationState, error) {
			if err := validateSettledState(state, threshold); err != nil {
				return optimizationState{}, err
			}
			revision := uint32(len(state.History) + 1)
			content := fmt.Sprintf("draft %d", revision)
			if len(state.History) > 0 {
				content += "; addressed: " + state.History[len(state.History)-1].Assessment.Feedback
			}
			state.Current = candidate{Revision: revision, Content: content}
			return state, nil
		},
	)
}

func newEvaluatorDeployment(scores []float64, threshold float64) (agent.Deployment, error) {
	return transformDeployment(
		"example.evaluator_optimizer.evaluator",
		"Score one candidate, provide revision feedback, and retain the stable best attempt.",
		struct {
			Scores    []float64 `json:"scores"`
			Threshold float64   `json:"threshold"`
		}{Scores: scores, Threshold: threshold},
		func(_ context.Context, state optimizationState) (optimizationState, error) {
			if validatePendingStateErr := validatePendingState(state, threshold); validatePendingStateErr != nil {
				return optimizationState{}, validatePendingStateErr
			}
			index := len(state.History)
			score := scores[index]
			feedback := fmt.Sprintf("raise quality after revision %d", state.Current.Revision)
			if score >= threshold {
				feedback = "accept this revision"
			}
			latest := attempt{
				Candidate:  state.Current,
				Assessment: assessment{Score: score, Feedback: feedback},
			}
			state.History = append(slices.Clone(state.History), latest)
			if !state.HasBest || score > state.Best.Assessment.Score {
				state.Best = latest
				state.HasBest = true
			}
			state.Accepted = state.Best.Assessment.Score >= threshold
			if validateSettledStateErr := validateSettledState(state, threshold); validateSettledStateErr != nil {
				return optimizationState{}, validateSettledStateErr
			}
			return state, nil
		},
	)
}

func newIterationDeployment(
	optimizer agent.Deployment,
	evaluator agent.Deployment,
) (agent.Deployment, error) {
	workerBudget, err := agent.NewBudget(agent.BudgetConfig{
		Steps: workerBudgetSteps, Effects: workerBudgetEffects, Signals: workerBudgetSignals,
	})
	if err != nil {
		return agent.Deployment{}, err
	}
	optimize, err := workflow.Call(workflow.CallConfig{
		ID: "optimize", Deployment: optimizer, Budget: workerBudget,
	})
	if err != nil {
		return agent.Deployment{}, err
	}
	evaluate, err := workflow.Call(workflow.CallConfig{
		ID: "evaluate", Deployment: evaluator, Budget: workerBudget,
	})
	if err != nil {
		return agent.Deployment{}, err
	}
	iterationDefinition, err := workflow.NewDefinition(workflow.DefinitionConfig{
		Name:        "example.evaluator_optimizer.iteration",
		Description: "Run one exact optimizer child followed by one exact evaluator child.",
		Stages:      []workflow.Stage{optimize, evaluate},
	})
	if err != nil {
		return agent.Deployment{}, err
	}
	return newWorkflowDeployment(
		iterationDefinition,
		"evaluator-optimizer-iteration",
		struct {
			Optimizer    string       `json:"optimizer"`
			Evaluator    string       `json:"evaluator"`
			WorkerBudget agent.Budget `json:"worker_budget"`
		}{
			Optimizer:    optimizer.DeploymentRef().Digest().String(),
			Evaluator:    evaluator.DeploymentRef().Digest().String(),
			WorkerBudget: workerBudget,
		},
	)
}

func newOptimizationRoot(
	iteration agent.Deployment,
	threshold float64,
	maxIterations uint32,
) (agent.Deployment, error) {
	initialize, err := workflow.Transform("initialize", func(_ context.Context, request optimizationRequest) (optimizationState, error) {
		objective := strings.TrimSpace(request.Objective)
		if objective == "" || objective != request.Objective {
			return optimizationState{}, errors.New("objective must be non-empty and trimmed")
		}
		return optimizationState{Objective: objective, History: []attempt{}}, nil
	})
	if err != nil {
		return agent.Deployment{}, err
	}
	iterationBudget, err := agent.NewBudget(agent.BudgetConfig{
		Steps: iterationBudgetSteps, Effects: iterationBudgetEffects, Signals: iterationBudgetSignals,
	})
	if err != nil {
		return agent.Deployment{}, err
	}
	refine, err := workflow.Loop(workflow.LoopConfig[optimizationState]{
		ID: "refine", Body: iteration, Budget: iterationBudget,
		MaxIterations: maxIterations,
		Predicate: func(_ context.Context, state optimizationState) (bool, error) {
			if validateSettledStateErr := validateSettledState(state, threshold); validateSettledStateErr != nil {
				return false, validateSettledStateErr
			}
			return state.Accepted, nil
		},
	})
	if err != nil {
		return agent.Deployment{}, err
	}
	finalize, err := workflow.Transform("finalize", func(_ context.Context,
		result workflow.LoopResult[optimizationState],
	) (optimizationReport, error) {
		state := result.Value
		if !result.Valid() || result.Satisfied != state.Accepted {
			return optimizationReport{}, errors.New("loop result and acceptance state disagree")
		}
		if validateSettledStateErr := validateSettledState(state, threshold); validateSettledStateErr != nil {
			return optimizationReport{}, validateSettledStateErr
		}
		if !state.HasBest || uint32(len(state.History)) != result.Iterations {
			return optimizationReport{}, errors.New("loop result has incomplete attempt history")
		}
		return optimizationReport{
			Objective: state.Objective, History: slices.Clone(state.History), Best: state.Best,
			Accepted: state.Accepted, Iterations: result.Iterations,
		}, nil
	})
	if err != nil {
		return agent.Deployment{}, err
	}
	rootDefinition, err := workflow.NewDefinition(workflow.DefinitionConfig{
		Name:        "example.evaluator_optimizer",
		Description: "Refine candidates through bounded exact optimizer and evaluator child Processes.",
		Stages:      []workflow.Stage{initialize, refine, finalize},
	})
	if err != nil {
		return agent.Deployment{}, err
	}
	return newWorkflowDeployment(
		rootDefinition,
		"evaluator-optimizer-root",
		struct {
			Iteration       string       `json:"iteration"`
			IterationBudget agent.Budget `json:"iteration_budget"`
			Threshold       float64      `json:"threshold"`
			MaxIterations   uint32       `json:"max_iterations"`
		}{
			Iteration:       iteration.DeploymentRef().Digest().String(),
			IterationBudget: iterationBudget,
			Threshold:       threshold,
			MaxIterations:   maxIterations,
		},
	)
}

func validatePendingState(state optimizationState, threshold float64) error {
	if err := validateHistory(state, threshold); err != nil {
		return err
	}
	wantRevision := uint32(len(state.History) + 1)
	if state.Current.Revision != wantRevision || strings.TrimSpace(state.Current.Content) == "" {
		return errors.New("optimizer did not produce the next complete revision")
	}
	return nil
}

func validateSettledState(state optimizationState, threshold float64) error {
	if err := validateHistory(state, threshold); err != nil {
		return err
	}
	if len(state.History) == 0 {
		if state.Current != (candidate{}) {
			return errors.New("initial state contains a current candidate")
		}
		return nil
	}
	if state.Current != state.History[len(state.History)-1].Candidate {
		return errors.New("current candidate is not the latest evaluated revision")
	}
	return nil
}

func validateHistory(state optimizationState, threshold float64) error {
	if strings.TrimSpace(state.Objective) == "" || state.Objective != strings.TrimSpace(state.Objective) {
		return errors.New("optimization objective must be non-empty and trimmed")
	}
	if state.History == nil {
		return errors.New("optimization history must be initialized")
	}
	if len(state.History) == 0 {
		if state.HasBest || state.Best != (attempt{}) || state.Accepted {
			return errors.New("empty history contains derived result state")
		}
		return nil
	}
	best := state.History[0]
	for index, recorded := range state.History {
		if recorded.Candidate.Revision != uint32(index+1) ||
			strings.TrimSpace(recorded.Candidate.Content) == "" ||
			!validScore(recorded.Assessment.Score) ||
			strings.TrimSpace(recorded.Assessment.Feedback) == "" {
			return fmt.Errorf("attempt %d is invalid", index)
		}
		if recorded.Assessment.Score > best.Assessment.Score {
			best = recorded
		}
	}
	if !state.HasBest || state.Best != best {
		return errors.New("best attempt is not the earliest highest-scoring attempt")
	}
	if state.Accepted != (best.Assessment.Score >= threshold) {
		return errors.New("acceptance state does not match the configured threshold")
	}
	return nil
}

func validScore(score float64) bool {
	return !math.IsNaN(score) && !math.IsInf(score, 0) && score >= 0 && score <= 1
}

func transformDeployment[I, O any](
	name string,
	description string,
	configuration any,
	transform workflow.TransformFunc[I, O],
) (agent.Deployment, error) {
	stage, err := workflow.Transform("transform", transform)
	if err != nil {
		return agent.Deployment{}, err
	}
	definition, err := workflow.NewDefinition(workflow.DefinitionConfig{
		Name: name, Description: description, Stages: []workflow.Stage{stage},
	})
	if err != nil {
		return agent.Deployment{}, err
	}
	configurationJSON, err := json.Marshal(configuration)
	if err != nil {
		return agent.Deployment{}, fmt.Errorf("encode %s configuration: %w", name, err)
	}
	return agent.NewDeployment(agent.DeploymentConfig{
		Definition:           definition,
		ImplementationDigest: agent.ComputeDigest([]byte(name + "-transform")),
		ConfigurationDigest:  agent.ComputeDigest(configurationJSON),
	})
}

func newWorkflowDeployment(
	definition *workflow.Definition,
	implementationIdentity string,
	configuration any,
) (agent.Deployment, error) {
	configurationJSON, err := json.Marshal(configuration)
	if err != nil {
		return agent.Deployment{}, fmt.Errorf("encode %s configuration: %w", definition.Descriptor().Name(), err)
	}
	return agent.NewDeployment(agent.DeploymentConfig{
		Definition:           definition,
		ImplementationDigest: agent.ComputeDigest([]byte(implementationIdentity)),
		ConfigurationDigest:  agent.ComputeDigest(configurationJSON),
	})
}

func decodeCompleted[T any](result agent.Result) (T, error) {
	var zero T
	if result.Status() != agent.StatusCompleted {
		return zero, fmt.Errorf("process ended with %s: %#v", result.Status(), result.Termination())
	}
	output, present := result.Output()
	if !present {
		return zero, errors.New("completed Process has no Output")
	}
	return output.Decode[T]()
}

type deploymentResolver map[agent.DeploymentRef]agent.Deployment

func (d deploymentResolver) Resolve(
	reference agent.DeploymentRef,
) (agent.Deployment, error) {
	deployment, found := d[reference]
	if !found {
		return agent.Deployment{}, fmt.Errorf("deployment %s is not registered", reference.Digest())
	}
	return deployment, nil
}
