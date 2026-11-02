// Command workflow_patterns demonstrates prompt chaining, routing, parallel
// sectioning, and parallel voting through one managed Workflow. It uses
// deterministic local workers and requires no credentials or network access.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	agentrun "github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/workflow"
)

const (
	patternChildBudgetSteps    = 16
	patternChildBudgetEffects  = 8
	patternChildBudgetMessages = 16
	sectionWindowSize          = 2
	voteWindowSize             = 2
	urgentRouteID              = "urgent"
	standardRouteID            = "standard"
	factsSectionID             = "facts"
	risksSectionID             = "risks"
	approveFirstBallotID       = "approve_first"
	rejectFirstBallotID        = "reject_first"
	rejectSecondBallotID       = "reject_second"
	approveSecondBallotID      = "approve_second"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, output io.Writer) error {
	report, evidence, err := execute(ctx, patternRequest{Text: "  release agent  ", Urgent: true})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(
		output,
		"chain: %s -> %s\nroute: %s\nsections: %s, %s\nvote: %s %d/%d\nprocesses: %d\n",
		report.Normalized,
		report.Summary,
		report.Route,
		report.Sections[0],
		report.Sections[1],
		report.Decision,
		report.DecisionVotes,
		report.TotalVotes,
		evidence.ProcessCount(),
	)
	return err
}

type patternRequest struct {
	Text   string `json:"text"`
	Urgent bool   `json:"urgent"`
}

type chainState struct {
	Normalized string `json:"normalized"`
	Summary    string `json:"summary"`
	Urgent     bool   `json:"urgent"`
}

type routedState struct {
	Normalized string `json:"normalized"`
	Summary    string `json:"summary"`
	Route      string `json:"route"`
}

// sectionContent and ballot carry only what each parallel worker adds; the
// Fork reducer joins them with their branch and the state the Fork received.
type sectionContent struct {
	Content string `json:"content"`
}

type finding struct {
	Section string `json:"section"`
	Content string `json:"content"`
}

type findingBundle struct {
	Normalized string    `json:"normalized"`
	Summary    string    `json:"summary"`
	Route      string    `json:"route"`
	Findings   []finding `json:"findings"`
}

type ballotChoice string

const (
	ballotApprove ballotChoice = "approve"
	ballotReject  ballotChoice = "reject"
)

func (b ballotChoice) valid() bool { return b == ballotApprove || b == ballotReject }

type ballot struct {
	Choice ballotChoice `json:"choice"`
}

type patternReport struct {
	Normalized    string       `json:"normalized"`
	Summary       string       `json:"summary"`
	Route         string       `json:"route"`
	Sections      []string     `json:"sections"`
	Decision      ballotChoice `json:"decision"`
	DecisionVotes int          `json:"decision_votes"`
	TotalVotes    int          `json:"total_votes"`
}

type executionEvidence struct {
	Programs map[string]int
}

// ProcessCount totals the Processes the Program counts already record.
func (e executionEvidence) ProcessCount() int {
	count := 0
	for _, processes := range e.Programs {
		count += processes
	}
	return count
}

func execute(
	ctx context.Context,
	request patternRequest,
) (_ patternReport, _ executionEvidence, err error) {
	runtime, err := agentrun.New(ctx, agentrun.Config{Build: "example-workflow-patterns", Store: agentrun.NewMemoryStore()})
	if err != nil {
		return patternReport{}, executionEvidence{}, err
	}
	defer func() {
		err = errors.Join(err, runtime.Close(context.WithoutCancel(ctx)))
	}()
	root, err := newWorkflowPatterns(runtime.Compiler())
	if err != nil {
		return patternReport{}, executionEvidence{}, err
	}
	input, err := agentrun.EncodePayload(request)
	if err != nil {
		return patternReport{}, executionEvidence{}, err
	}
	handle, err := runtime.Start(ctx, root, agentrun.StartRequest[agentrun.Payload]{ID: agentrun.StartID{Scope: "example", Key: "run"}, Input: input})
	if err != nil {
		return patternReport{}, executionEvidence{}, err
	}
	result, err := handle.Await(ctx)
	if err != nil {
		return patternReport{}, executionEvidence{}, err
	}
	report, err := decodeCompleted[patternReport](result)
	if err != nil {
		return patternReport{}, executionEvidence{}, err
	}
	log, err := runtime.History(ctx, result.ProcessID())
	if err != nil {
		return patternReport{}, executionEvidence{}, err
	}
	evidence := executionEvidence{Programs: make(map[string]int)}
	for _, entry := range log.Entries {
		if started, ok := entry.Record.(agentrun.Started); ok {
			evidence.Programs[started.Program.Name()]++
		}
	}
	return report, evidence, nil
}

func newWorkflowPatterns(compiler *agentrun.Compiler) (agentrun.Program[agentrun.Payload, agentrun.Payload], error) {
	children, err := newPatternChildren(compiler)
	if err != nil {
		return agentrun.Program[agentrun.Payload, agentrun.Payload]{}, err
	}
	budget := agentrun.Budget{
		Steps: agentrun.NewQuota(patternChildBudgetSteps), Effects: agentrun.NewQuota(patternChildBudgetEffects),
		Messages: agentrun.NewQuota(patternChildBudgetMessages),
	}
	stages, err := newPatternStages(children, budget)
	if err != nil {
		return agentrun.Program[agentrun.Payload, agentrun.Payload]{}, err
	}
	root, err := newPatternRoot(compiler, stages)
	if err != nil {
		return agentrun.Program[agentrun.Payload, agentrun.Payload]{}, err
	}
	return root, nil
}

type patternChildren struct {
	normalizer    agentrun.Program[agentrun.Payload, agentrun.Payload]
	summarizer    agentrun.Program[agentrun.Payload, agentrun.Payload]
	urgent        agentrun.Program[agentrun.Payload, agentrun.Payload]
	standard      agentrun.Program[agentrun.Payload, agentrun.Payload]
	facts         agentrun.Program[agentrun.Payload, agentrun.Payload]
	risks         agentrun.Program[agentrun.Payload, agentrun.Payload]
	approveFirst  agentrun.Program[agentrun.Payload, agentrun.Payload]
	rejectFirst   agentrun.Program[agentrun.Payload, agentrun.Payload]
	rejectSecond  agentrun.Program[agentrun.Payload, agentrun.Payload]
	approveSecond agentrun.Program[agentrun.Payload, agentrun.Payload]
}

func newPatternChildren(compiler *agentrun.Compiler) (patternChildren, error) {
	var children patternChildren
	var err error
	children.normalizer, err = transformProgram(compiler,
		"example.workflow_patterns.normalize",
		"Normalize one request for the following prompt-chain stage.",
		struct{}{},
		normalizePatternRequest,
	)
	if err != nil {
		return patternChildren{}, err
	}
	children.summarizer, err = transformProgram(compiler,
		"example.workflow_patterns.summarize",
		"Summarize the normalized result from the previous prompt-chain stage.",
		struct{}{},
		summarizeChain,
	)
	if err != nil {
		return patternChildren{}, err
	}
	children.urgent, err = routeProgram(compiler, urgentRouteID)
	if err != nil {
		return patternChildren{}, err
	}
	children.standard, err = routeProgram(compiler, standardRouteID)
	if err != nil {
		return patternChildren{}, err
	}
	children.facts, err = findingProgram(compiler, factsSectionID)
	if err != nil {
		return patternChildren{}, err
	}
	children.risks, err = findingProgram(compiler, risksSectionID)
	if err != nil {
		return patternChildren{}, err
	}
	children.approveFirst, err = ballotProgram(compiler, approveFirstBallotID, ballotApprove)
	if err != nil {
		return patternChildren{}, err
	}
	children.rejectFirst, err = ballotProgram(compiler, rejectFirstBallotID, ballotReject)
	if err != nil {
		return patternChildren{}, err
	}
	children.rejectSecond, err = ballotProgram(compiler, rejectSecondBallotID, ballotReject)
	if err != nil {
		return patternChildren{}, err
	}
	children.approveSecond, err = ballotProgram(compiler, approveSecondBallotID, ballotApprove)
	if err != nil {
		return patternChildren{}, err
	}
	return children, nil
}

func newPatternStages(children patternChildren, budget agentrun.Budget) ([]workflow.Stage, error) {
	normalize, err := workflow.Call(workflow.CallConfig{
		ID: "normalize", Program: children.normalizer, Budget: budget,
	})
	if err != nil {
		return nil, err
	}
	summarize, err := workflow.Call(workflow.CallConfig{
		ID: "summarize", Program: children.summarizer, Budget: budget,
	})
	if err != nil {
		return nil, err
	}
	route, err := workflow.Switch(workflow.SwitchConfig[chainState]{
		ID:     "route",
		Select: selectPatternRoute,
		Cases: []workflow.SwitchCase{
			{ID: urgentRouteID, Program: children.urgent, Budget: budget},
			{ID: standardRouteID, Program: children.standard, Budget: budget},
		},
	})
	if err != nil {
		return nil, err
	}
	sections := []workflow.ForkBranch{
		{ID: factsSectionID, Program: children.facts, Budget: budget},
		{ID: risksSectionID, Program: children.risks, Budget: budget},
	}
	section, err := workflow.Fork(workflow.ForkConfig[routedState, sectionContent, findingBundle]{
		ID: "section", Branches: sections, WindowSize: sectionWindowSize,
		// Fork outputs arrive in branch declaration order.
		Reduce: func(_ context.Context, state routedState, contents []sectionContent) (findingBundle, error) {
			bundle := findingBundle{Normalized: state.Normalized, Summary: state.Summary, Route: state.Route}
			for index, content := range contents {
				bundle.Findings = append(bundle.Findings, finding{Section: sections[index].ID, Content: content.Content})
			}
			return bundle, nil
		},
	})
	if err != nil {
		return nil, err
	}
	vote, err := workflow.Fork(workflow.ForkConfig[findingBundle, ballot, patternReport]{
		ID: "vote",
		Branches: []workflow.ForkBranch{
			{ID: approveFirstBallotID, Program: children.approveFirst, Budget: budget},
			{ID: rejectFirstBallotID, Program: children.rejectFirst, Budget: budget},
			{ID: rejectSecondBallotID, Program: children.rejectSecond, Budget: budget},
			{ID: approveSecondBallotID, Program: children.approveSecond, Budget: budget},
		},
		WindowSize: voteWindowSize,
		Reduce:     reduceBallots,
	})
	if err != nil {
		return nil, err
	}
	return []workflow.Stage{normalize, summarize, route, section, vote}, nil
}

func normalizePatternRequest(_ context.Context, request patternRequest) (chainState, error) {
	text := strings.ToUpper(strings.TrimSpace(request.Text))
	if text == "" {
		return chainState{}, errors.New("request text must not be empty")
	}
	return chainState{Normalized: text, Urgent: request.Urgent}, nil
}

func summarizeChain(_ context.Context, state chainState) (chainState, error) {
	if state.Normalized == "" || state.Summary != "" {
		return chainState{}, errors.New("summarizer received an invalid chain state")
	}
	state.Summary = "summary: " + state.Normalized
	return state, nil
}

func selectPatternRoute(_ context.Context, state chainState) (string, error) {
	if state.Normalized == "" || state.Summary == "" {
		return "", errors.New("router received an incomplete chain state")
	}
	if state.Urgent {
		return urgentRouteID, nil
	}
	return standardRouteID, nil
}

func newPatternRoot(compiler *agentrun.Compiler, stages []workflow.Stage) (agentrun.Program[agentrun.Payload, agentrun.Payload], error) {
	return workflow.Compile[agentrun.Payload, agentrun.Payload](compiler, workflow.Config{
		Name: "example.workflow_patterns", Description: "Chain, route, section, and vote through exact managed child Processes.", Revision: "patterns-v1", Stages: stages,
	})
}

func routeProgram(compiler *agentrun.Compiler, route string) (agentrun.Program[agentrun.Payload, agentrun.Payload], error) {
	if route != urgentRouteID && route != standardRouteID {
		return agentrun.Program[agentrun.Payload, agentrun.Payload]{}, errors.New("route must be urgent or standard")
	}
	return transformProgram(compiler,
		"example.workflow_patterns.route_"+route,
		"Apply the selected "+route+" route.",
		struct {
			Route string `json:"route"`
		}{Route: route},
		func(_ context.Context, state chainState) (routedState, error) {
			if state.Normalized == "" || state.Summary == "" {
				return routedState{}, errors.New("route worker received an incomplete chain state")
			}
			return routedState{Normalized: state.Normalized, Summary: state.Summary, Route: route}, nil
		},
	)
}

func findingProgram(compiler *agentrun.Compiler, section string) (agentrun.Program[agentrun.Payload, agentrun.Payload], error) {
	if section != factsSectionID && section != risksSectionID {
		return agentrun.Program[agentrun.Payload, agentrun.Payload]{}, errors.New("section must be facts or risks")
	}
	return transformProgram(compiler,
		"example.workflow_patterns.section_"+section,
		"Produce the "+section+" parallel section.",
		struct {
			Section string `json:"section"`
		}{Section: section},
		func(_ context.Context, state routedState) (sectionContent, error) {
			if state.Route == "" || state.Summary == "" {
				return sectionContent{}, errors.New("section worker received incomplete routed state")
			}
			return sectionContent{Content: section + " for " + state.Summary}, nil
		},
	)
}

func ballotProgram(compiler *agentrun.Compiler, name string, choice ballotChoice) (agentrun.Program[agentrun.Payload, agentrun.Payload], error) {
	if !choice.valid() {
		return agentrun.Program[agentrun.Payload, agentrun.Payload]{}, errors.New("ballot choice must be approve or reject")
	}
	return transformProgram(compiler,
		"example.workflow_patterns.vote_"+name,
		"Return one deterministic "+string(choice)+" ballot.",
		ballot{Choice: choice},
		func(_ context.Context, bundle findingBundle) (ballot, error) {
			for _, finding := range bundle.Findings {
				if finding.Content == "" {
					return ballot{}, errors.New("voter requires every parallel section")
				}
			}
			return ballot{Choice: choice}, nil
		},
	)
}

func reduceBallots(_ context.Context, bundle findingBundle, ballots []ballot) (patternReport, error) {
	if len(ballots) == 0 {
		return patternReport{}, errors.New("parallel vote returned no ballots")
	}
	counts := make(map[ballotChoice]int)
	for index, ballot := range ballots {
		if !ballot.Choice.valid() {
			return patternReport{}, fmt.Errorf("ballot %d has invalid choice", index)
		}
		counts[ballot.Choice]++
	}
	var winner ballotChoice
	winnerVotes := 0
	for _, ballot := range ballots {
		if counts[ballot.Choice] > winnerVotes {
			winner = ballot.Choice
			winnerVotes = counts[ballot.Choice]
		}
	}
	sections := make([]string, len(bundle.Findings))
	for index, finding := range bundle.Findings {
		sections[index] = finding.Section
	}
	return patternReport{
		Normalized: bundle.Normalized, Summary: bundle.Summary, Route: bundle.Route,
		Sections: sections, Decision: winner,
		DecisionVotes: winnerVotes, TotalVotes: len(ballots),
	}, nil
}

func transformProgram[I, O any](
	compiler *agentrun.Compiler,
	name, description string,
	configuration any,
	transform workflow.TransformFunc[I, O],
) (agentrun.Program[agentrun.Payload, agentrun.Payload], error) {
	stage, err := workflow.Transform("transform", transform)
	if err != nil {
		return agentrun.Program[agentrun.Payload, agentrun.Payload]{}, err
	}
	return workflow.Compile[agentrun.Payload, agentrun.Payload](compiler, workflow.Config{
		Name: name, Description: description, Revision: name, Stages: []workflow.Stage{stage}, Configuration: configuration,
	})
}

func decodeCompleted[T any](result agentrun.Result[agentrun.Payload]) (T, error) {
	var zero T
	if result.Termination().Status() != agentrun.StatusCompleted {
		return zero, fmt.Errorf("process ended with %s: %#v", result.Termination().Status(), result.Termination())
	}
	output, present := result.Termination().Output()
	if !present {
		return zero, errors.New("completed Process has no Output")
	}
	return output.Decode[T]()
}
