// Command workflow_patterns demonstrates prompt chaining, routing, parallel
// sectioning, and parallel voting through one managed Workflow. It uses
// deterministic local workers and requires no credentials or network access.
package main

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/workflow"
)

const (
	patternChildBudgetSteps   = 16
	patternChildBudgetEffects = 8
	patternChildBudgetSignals = 16
	sectionWindowSize         = 2
	voteWindowSize            = 2
	urgentRouteID             = "urgent"
	standardRouteID           = "standard"
	factsSectionID            = "facts"
	risksSectionID            = "risks"
	approveFirstBallotID      = "approve_first"
	rejectFirstBallotID       = "reject_first"
	rejectSecondBallotID      = "reject_second"
	approveSecondBallotID     = "approve_second"
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
	request patternRequest,
) (_ patternReport, _ executionEvidence, err error) {
	root, err := newWorkflowPatterns()
	if err != nil {
		return patternReport{}, executionEvidence{}, err
	}
	engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: agent.NewMemoryTreeCommitter()})
	if err != nil {
		return patternReport{}, executionEvidence{}, err
	}
	defer func() {
		err = errors.Join(err, engine.Close(context.WithoutCancel(ctx)))
	}()
	input, err := agent.EncodePayload(request)
	if err != nil {
		return patternReport{}, executionEvidence{}, err
	}
	process, err := engine.Start(ctx, root, input)
	if err != nil {
		return patternReport{}, executionEvidence{}, err
	}
	result, err := process.Await(ctx)
	if err != nil {
		return patternReport{}, executionEvidence{}, err
	}
	report, err := decodeCompleted[patternReport](result)
	if err != nil {
		return patternReport{}, executionEvidence{}, err
	}
	tree, err := engine.CaptureTree(ctx, process.Relation().ProcessID())
	if err != nil {
		return patternReport{}, executionEvidence{}, err
	}
	snapshots := tree.ProcessSnapshots()
	evidence := executionEvidence{Deployments: make(map[string]int)}
	for _, snapshot := range snapshots {
		evidence.Deployments[snapshot.DeploymentRef().Name()]++
	}
	return report, evidence, nil
}

func newWorkflowPatterns() (agent.Deployment, error) {
	children, err := newPatternChildren()
	if err != nil {
		return agent.Deployment{}, err
	}
	budget := agent.Budget{
		Steps: agent.NewQuota(patternChildBudgetSteps), Effects: agent.NewQuota(patternChildBudgetEffects),
		Signals: agent.NewQuota(patternChildBudgetSignals),
	}
	stages, err := newPatternStages(children, budget)
	if err != nil {
		return agent.Deployment{}, err
	}
	root, err := newPatternRoot(stages, budget)
	if err != nil {
		return agent.Deployment{}, err
	}
	return root, nil
}

type patternChildren struct {
	normalizer    agent.Deployment
	summarizer    agent.Deployment
	urgent        agent.Deployment
	standard      agent.Deployment
	facts         agent.Deployment
	risks         agent.Deployment
	approveFirst  agent.Deployment
	rejectFirst   agent.Deployment
	rejectSecond  agent.Deployment
	approveSecond agent.Deployment
}

func newPatternChildren() (patternChildren, error) {
	var children patternChildren
	var err error
	children.normalizer, err = transformDeployment(
		"example.workflow_patterns.normalize",
		"Normalize one request for the following prompt-chain stage.",
		struct{}{},
		normalizePatternRequest,
	)
	if err != nil {
		return patternChildren{}, err
	}
	children.summarizer, err = transformDeployment(
		"example.workflow_patterns.summarize",
		"Summarize the normalized result from the previous prompt-chain stage.",
		struct{}{},
		summarizeChain,
	)
	if err != nil {
		return patternChildren{}, err
	}
	children.urgent, err = routeDeployment(urgentRouteID)
	if err != nil {
		return patternChildren{}, err
	}
	children.standard, err = routeDeployment(standardRouteID)
	if err != nil {
		return patternChildren{}, err
	}
	children.facts, err = findingDeployment(factsSectionID)
	if err != nil {
		return patternChildren{}, err
	}
	children.risks, err = findingDeployment(risksSectionID)
	if err != nil {
		return patternChildren{}, err
	}
	children.approveFirst, err = ballotDeployment(approveFirstBallotID, ballotApprove)
	if err != nil {
		return patternChildren{}, err
	}
	children.rejectFirst, err = ballotDeployment(rejectFirstBallotID, ballotReject)
	if err != nil {
		return patternChildren{}, err
	}
	children.rejectSecond, err = ballotDeployment(rejectSecondBallotID, ballotReject)
	if err != nil {
		return patternChildren{}, err
	}
	children.approveSecond, err = ballotDeployment(approveSecondBallotID, ballotApprove)
	if err != nil {
		return patternChildren{}, err
	}
	return children, nil
}

func newPatternStages(children patternChildren, budget agent.Budget) ([]workflow.Stage, error) {
	normalize, err := workflow.Call(workflow.CallConfig{
		ID: "normalize", Deployment: children.normalizer, Budget: budget,
	})
	if err != nil {
		return nil, err
	}
	summarize, err := workflow.Call(workflow.CallConfig{
		ID: "summarize", Deployment: children.summarizer, Budget: budget,
	})
	if err != nil {
		return nil, err
	}
	route, err := workflow.Switch(workflow.SwitchConfig[chainState]{
		ID:     "route",
		Select: selectPatternRoute,
		Cases: []workflow.SwitchCase{
			{ID: urgentRouteID, Deployment: children.urgent, Budget: budget},
			{ID: standardRouteID, Deployment: children.standard, Budget: budget},
		},
	})
	if err != nil {
		return nil, err
	}
	sections := []workflow.ForkBranch{
		{ID: factsSectionID, Deployment: children.facts, Budget: budget},
		{ID: risksSectionID, Deployment: children.risks, Budget: budget},
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
			{ID: approveFirstBallotID, Deployment: children.approveFirst, Budget: budget},
			{ID: rejectFirstBallotID, Deployment: children.rejectFirst, Budget: budget},
			{ID: rejectSecondBallotID, Deployment: children.rejectSecond, Budget: budget},
			{ID: approveSecondBallotID, Deployment: children.approveSecond, Budget: budget},
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

func newPatternRoot(
	stages []workflow.Stage,
	budget agent.Budget,
) (agent.Deployment, error) {
	definition, err := workflow.NewDefinition(workflow.DefinitionConfig{
		Name:        "example.workflow_patterns",
		Description: "Chain, route, section, and vote through exact managed child Processes.",
		Stages:      stages,
	})
	if err != nil {
		return agent.Deployment{}, err
	}
	configuration := struct {
		Budget        agent.Budget `json:"budget"`
		SectionWindow uint32       `json:"section_window"`
		VoteWindow    uint32       `json:"vote_window"`
	}{Budget: budget, SectionWindow: sectionWindowSize, VoteWindow: voteWindowSize}
	configurationJSON, err := jsonv2.Marshal(configuration)
	if err != nil {
		return agent.Deployment{}, err
	}
	root, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition:           definition,
		ImplementationDigest: agent.ComputeDigest([]byte("workflow-patterns-root")),
		ConfigurationDigest:  agent.ComputeDigest(configurationJSON),
	})
	if err != nil {
		return agent.Deployment{}, err
	}
	return root, nil
}

func routeDeployment(route string) (agent.Deployment, error) {
	if route != urgentRouteID && route != standardRouteID {
		return agent.Deployment{}, errors.New("route must be urgent or standard")
	}
	return transformDeployment(
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

func findingDeployment(section string) (agent.Deployment, error) {
	if section != factsSectionID && section != risksSectionID {
		return agent.Deployment{}, errors.New("section must be facts or risks")
	}
	return transformDeployment(
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

func ballotDeployment(name string, choice ballotChoice) (agent.Deployment, error) {
	if !choice.valid() {
		return agent.Deployment{}, errors.New("ballot choice must be approve or reject")
	}
	return transformDeployment(
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

func decodeCompleted[T any](result agent.Result) (T, error) {
	var zero T
	if result.Termination().Status() != agent.StatusCompleted {
		return zero, fmt.Errorf("process ended with %s: %#v", result.Termination().Status(), result.Termination())
	}
	output, present := result.Termination().Output()
	if !present {
		return zero, errors.New("completed Process has no Output")
	}
	return output.Decode[T]()
}
