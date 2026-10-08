// Command evaluator_optimizer demonstrates bounded evaluator-optimizer
// composition with exact managed child Processes. It uses deterministic local
// workers and requires no credentials or network access.
package main

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strings"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/workflow"
)

const (
	acceptanceThreshold    = 0.9
	workerBudgetSteps      = 8
	workerBudgetEffects    = 4
	workerBudgetSignals    = 8
	iterationBudgetSteps   = 64
	iterationBudgetEffects = 32
	iterationBudgetSignals = 64
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
	)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(
		output,
		"objective: %s\nbest: %s\nscore: %.2f\nattempts: %d\naccepted: %t\nprocesses: %d\n",
		report.Objective,
		report.best().Candidate.Content,
		report.best().Assessment.Score,
		len(report.History),
		report.Accepted,
		evidence.ProcessCount(),
	)
	return err
}

type optimizationRequest struct {
	Objective string `json:"objective"`
}

// candidate is identified by its position: revision n is History[n-1], or
// Current while it awaits evaluation.
type candidate struct {
	Content string `json:"content"`
}

type assessment struct {
	Score    float64 `json:"score"`
	Feedback string  `json:"feedback"`
}

type attempt struct {
	Candidate  candidate  `json:"candidate"`
	Assessment assessment `json:"assessment"`
}

func (a attempt) valid() bool {
	return strings.TrimSpace(a.Candidate.Content) != "" &&
		validScore(a.Assessment.Score) && strings.TrimSpace(a.Assessment.Feedback) != ""
}

type optimizationState struct {
	Objective string     `json:"objective"`
	History   []attempt  `json:"history"`
	Current   *candidate `json:"current,omitzero"`
}

func (o optimizationState) validatePending() error {
	if err := o.validateHistory(); err != nil {
		return err
	}
	if o.Current == nil || strings.TrimSpace(o.Current.Content) == "" {
		return errors.New("optimizer did not produce the next complete revision")
	}
	return nil
}

func (o optimizationState) validateSettled() error {
	if err := o.validateHistory(); err != nil {
		return err
	}
	if o.Current != nil {
		return errors.New("evaluated candidate must belong only to history")
	}
	return nil
}

func (o optimizationState) validateHistory() error {
	if strings.TrimSpace(o.Objective) == "" || o.Objective != strings.TrimSpace(o.Objective) {
		return errors.New("optimization objective must be non-empty and trimmed")
	}
	if o.History == nil {
		return errors.New("optimization history must be initialized")
	}
	for index, recorded := range o.History {
		if !recorded.valid() {
			return fmt.Errorf("attempt %d is invalid", index)
		}
	}
	return nil
}

func (o optimizationState) earliestBest() (attempt, bool) {
	if len(o.History) == 0 {
		return attempt{}, false
	}
	best := o.History[0]
	for _, recorded := range o.History[1:] {
		if recorded.Assessment.Score > best.Assessment.Score {
			best = recorded
		}
	}
	return best, true
}

func (o optimizationState) accepted(threshold float64) bool {
	best, present := o.earliestBest()
	return present && best.Assessment.Score >= threshold
}

// optimizationReport keeps one attempt per iteration; the best attempt and the
// iteration count follow from that history.
type optimizationReport struct {
	Objective string    `json:"objective"`
	History   []attempt `json:"history"`
	Accepted  bool      `json:"accepted"`
}

func (o optimizationReport) best() attempt {
	best, _ := optimizationState{History: o.History}.earliestBest()
	return best
}

type executionEvidence struct {
	Deployments map[string]int
}

// ProcessCount totals the Processes the Deployment counts already record.
func (e executionEvidence) ProcessCount() int {
	count := 0
	for _, processes := range e.Deployments {
		count += processes
	}
	return count
}

func execute(
	ctx context.Context,
	request optimizationRequest,
	scores []float64,
	threshold float64,
) (_ optimizationReport, _ executionEvidence, err error) {
	root, err := newEvaluatorOptimizer(scores, threshold)
	if err != nil {
		return optimizationReport{}, executionEvidence{}, err
	}
	engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: agent.NewMemoryTreeCommitter()})
	if err != nil {
		return optimizationReport{}, executionEvidence{}, err
	}
	defer func() {
		err = errors.Join(err, engine.Close(context.WithoutCancel(ctx)))
	}()

	input, err := agent.EncodePayload(request)
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
		Deployments: make(map[string]int),
	}
	for _, snapshot := range snapshots {
		evidence.Deployments[snapshot.DeploymentRef().Name()]++
	}
	return report, evidence, nil
}

// newEvaluatorOptimizer runs one iteration per scheduled score: the schedule
// owns the iteration bound.
func newEvaluatorOptimizer(scores []float64, threshold float64) (agent.Deployment, error) {
	frozenScores, err := validateScoreSchedule(scores, threshold)
	if err != nil {
		return agent.Deployment{}, err
	}
	optimizer, err := newOptimizerDeployment()
	if err != nil {
		return agent.Deployment{}, err
	}
	evaluator, err := newEvaluatorDeployment(frozenScores)
	if err != nil {
		return agent.Deployment{}, err
	}
	iteration, err := newIterationDeployment(optimizer, evaluator)
	if err != nil {
		return agent.Deployment{}, err
	}
	root, err := newOptimizationRoot(iteration, threshold, uint64(len(frozenScores)))
	if err != nil {
		return agent.Deployment{}, err
	}
	return root, nil
}

func validateScoreSchedule(scores []float64, threshold float64) ([]float64, error) {
	if len(scores) == 0 {
		return nil, errors.New("score schedule must contain at least one iteration")
	}
	if !validScore(threshold) || threshold == 0 {
		return nil, errors.New("acceptance threshold must be within (0, 1]")
	}
	frozenScores := slices.Clone(scores)
	for index, score := range frozenScores {
		if !validScore(score) {
			return nil, fmt.Errorf("score schedule entry %d must be within [0, 1]", index)
		}
	}
	return frozenScores, nil
}

func newOptimizerDeployment() (agent.Deployment, error) {
	return transformDeployment(
		"example.evaluator_optimizer.optimizer",
		"Produce one revised candidate from the objective and latest evaluator feedback.",
		struct{}{},
		func(_ context.Context, state optimizationState) (optimizationState, error) {
			if err := state.validateSettled(); err != nil {
				return optimizationState{}, err
			}
			revision := uint32(len(state.History) + 1)
			content := fmt.Sprintf("draft %d", revision)
			if len(state.History) > 0 {
				content += "; addressed: " + state.History[len(state.History)-1].Assessment.Feedback
			}
			state.Current = &candidate{Content: content}
			return state, nil
		},
	)
}

// newEvaluatorDeployment scores candidates; the root Loop owns the acceptance
// threshold, so feedback never decides acceptance.
func newEvaluatorDeployment(scores []float64) (agent.Deployment, error) {
	return transformDeployment(
		"example.evaluator_optimizer.evaluator",
		"Score one candidate, provide revision feedback, and retain the stable best attempt.",
		struct {
			Scores []float64 `json:"scores"`
		}{Scores: scores},
		func(_ context.Context, state optimizationState) (optimizationState, error) {
			if validatePendingStateErr := state.validatePending(); validatePendingStateErr != nil {
				return optimizationState{}, validatePendingStateErr
			}
			index := len(state.History)
			score := scores[index]
			feedback := fmt.Sprintf("raise quality after revision %d", index+1)
			latest := attempt{
				Candidate:  *state.Current,
				Assessment: assessment{Score: score, Feedback: feedback},
			}
			state.History = append(slices.Clone(state.History), latest)
			state.Current = nil
			if validateSettledStateErr := state.validateSettled(); validateSettledStateErr != nil {
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
	workerBudget := agent.Budget{
		Steps: agent.NewQuota(workerBudgetSteps), Effects: agent.NewQuota(workerBudgetEffects), Signals: agent.NewQuota(workerBudgetSignals),
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
			WorkerBudget agent.Budget `json:"worker_budget"`
		}{WorkerBudget: workerBudget},
	)
}

func initializeOptimization(_ context.Context, request optimizationRequest) (optimizationState, error) {
	objective := strings.TrimSpace(request.Objective)
	if objective == "" || objective != request.Objective {
		return optimizationState{}, errors.New("objective must be non-empty and trimmed")
	}
	return optimizationState{Objective: objective, History: []attempt{}}, nil
}

func finalizeOptimization(result workflow.LoopResult[optimizationState]) (optimizationReport, error) {
	state := result.Value
	if !result.Valid() {
		return optimizationReport{}, errors.New("loop result is invalid")
	}
	if err := state.validateSettled(); err != nil {
		return optimizationReport{}, err
	}
	if _, present := state.earliestBest(); !present {
		return optimizationReport{}, errors.New("loop result has no attempt")
	}
	return optimizationReport{
		Objective: state.Objective, History: slices.Clone(state.History), Accepted: result.Satisfied,
	}, nil
}

func newOptimizationRoot(
	iteration agent.Deployment,
	threshold float64,
	maxIterations uint64,
) (agent.Deployment, error) {
	initialize, err := workflow.Transform("initialize", initializeOptimization)
	if err != nil {
		return agent.Deployment{}, err
	}
	iterationBudget := agent.Budget{
		Steps: agent.NewQuota(iterationBudgetSteps), Effects: agent.NewQuota(iterationBudgetEffects), Signals: agent.NewQuota(iterationBudgetSignals),
	}
	refine, err := workflow.Loop(workflow.LoopConfig[optimizationState]{
		ID: "refine", Body: iteration, Budget: iterationBudget,
		MaxIterations: agent.NewQuota(uint64(maxIterations)),
		Predicate: func(_ context.Context, state optimizationState) (bool, error) {
			if validateSettledStateErr := state.validateSettled(); validateSettledStateErr != nil {
				return false, validateSettledStateErr
			}
			return state.accepted(threshold), nil
		},
	})
	if err != nil {
		return agent.Deployment{}, err
	}
	finalize, err := workflow.Transform("finalize", func(_ context.Context,
		result workflow.LoopResult[optimizationState],
	) (optimizationReport, error) {
		return finalizeOptimization(result)
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
			IterationBudget agent.Budget `json:"iteration_budget"`
			Threshold       float64      `json:"threshold"`
			MaxIterations   agent.Quota  `json:"max_iterations"`
		}{
			IterationBudget: iterationBudget,
			Threshold:       threshold,
			MaxIterations:   agent.NewQuota(uint64(maxIterations)),
		},
	)
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
	configurationJSON, err := jsonv2.Marshal(configuration)
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
	configurationJSON, err := jsonv2.Marshal(configuration)
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
	output, present := result.Termination().Output()
	if !present {
		return zero, errors.New("completed Process has no Output")
	}
	return output.Decode[T]()
}
