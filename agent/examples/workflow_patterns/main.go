// Command workflow_patterns demonstrates prompt chaining, routing, parallel
// sectioning, and parallel voting through one managed Workflow. It uses
// deterministic local workers and requires no credentials or network access.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/workflow"
)

const (
	patternChildBudgetSteps   = 16
	patternChildBudgetEffects = 8
	patternChildBudgetSignals = 16
	sectionWorkerCount        = 2
	sectionWindowSize         = 2
	voteWindowSize            = 2
	urgentRouteID             = "urgent"
	standardRouteID           = "standard"
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
		evidence.ProcessCount,
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

type finding struct {
	Normalized string `json:"normalized"`
	Summary    string `json:"summary"`
	Route      string `json:"route"`
	Section    string `json:"section"`
	Content    string `json:"content"`
}

type findingBundle struct {
	Normalized string    `json:"normalized"`
	Summary    string    `json:"summary"`
	Route      string    `json:"route"`
	Findings   []finding `json:"findings"`
}

type ballot struct {
	Normalized string   `json:"normalized"`
	Summary    string   `json:"summary"`
	Route      string   `json:"route"`
	Sections   []string `json:"sections"`
	Evidence   []string `json:"evidence"`
	Choice     string   `json:"choice"`
}

type patternReport struct {
	Normalized    string   `json:"normalized"`
	Summary       string   `json:"summary"`
	Route         string   `json:"route"`
	Sections      []string `json:"sections"`
	Decision      string   `json:"decision"`
	DecisionVotes int      `json:"decision_votes"`
	TotalVotes    int      `json:"total_votes"`
}

type executionEvidence struct {
	ProcessCount int
	Deployments  map[string]int
}

func execute(
	ctx context.Context,
	request patternRequest,
) (_ patternReport, _ executionEvidence, err error) {
	root, resolver, err := newWorkflowPatterns()
	if err != nil {
		return patternReport{}, executionEvidence{}, err
	}
	engine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: resolver})
	if err != nil {
		return patternReport{}, executionEvidence{}, err
	}
	defer func() {
		err = errors.Join(err, engine.Close())
	}()
	input, err := agent.EncodeInput(request)
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
	tree, err := engine.CaptureTree(ctx, process.ID())
	if err != nil {
		return patternReport{}, executionEvidence{}, err
	}
	snapshots := tree.ProcessSnapshots()
	evidence := executionEvidence{ProcessCount: len(snapshots), Deployments: make(map[string]int)}
	for _, snapshot := range snapshots {
		evidence.Deployments[snapshot.DeploymentRef().Name()]++
	}
	return report, evidence, nil
}

func newWorkflowPatterns() (agent.Deployment, deploymentResolver, error) {
	children, err := newPatternChildren()
	if err != nil {
		return agent.Deployment{}, nil, err
	}
	budget, err := agent.NewBudget(agent.BudgetConfig{
		Steps: patternChildBudgetSteps, Effects: patternChildBudgetEffects,
		Signals: patternChildBudgetSignals,
	})
	if err != nil {
		return agent.Deployment{}, nil, err
	}
	stages, err := newPatternStages(children, budget)
	if err != nil {
		return agent.Deployment{}, nil, err
	}
	root, err := newPatternRoot(stages, children.deployments(), budget)
	if err != nil {
		return agent.Deployment{}, nil, err
	}
	return root, children.resolver(), nil
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
		func(request patternRequest) (chainState, error) {
			text := strings.ToUpper(strings.TrimSpace(request.Text))
			if text == "" {
				return chainState{}, errors.New("request text must not be empty")
			}
			return chainState{Normalized: text, Urgent: request.Urgent}, nil
		},
	)
	if err != nil {
		return patternChildren{}, err
	}
	children.summarizer, err = transformDeployment(
		"example.workflow_patterns.summarize",
		"Summarize the normalized result from the previous prompt-chain stage.",
		struct{}{},
		func(state chainState) (chainState, error) {
			if state.Normalized == "" || state.Summary != "" {
				return chainState{}, errors.New("summarizer received an invalid chain state")
			}
			state.Summary = "summary: " + state.Normalized
			return state, nil
		},
	)
	if err != nil {
		return patternChildren{}, err
	}
	children.urgent, err = routeDeployment("urgent")
	if err != nil {
		return patternChildren{}, err
	}
	children.standard, err = routeDeployment("standard")
	if err != nil {
		return patternChildren{}, err
	}
	children.facts, err = findingDeployment("facts")
	if err != nil {
		return patternChildren{}, err
	}
	children.risks, err = findingDeployment("risks")
	if err != nil {
		return patternChildren{}, err
	}
	children.approveFirst, err = ballotDeployment("approve_first", "approve")
	if err != nil {
		return patternChildren{}, err
	}
	children.rejectFirst, err = ballotDeployment("reject_first", "reject")
	if err != nil {
		return patternChildren{}, err
	}
	children.rejectSecond, err = ballotDeployment("reject_second", "reject")
	if err != nil {
		return patternChildren{}, err
	}
	children.approveSecond, err = ballotDeployment("approve_second", "approve")
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
		ID: "route",
		Select: func(state chainState) (string, error) {
			if state.Normalized == "" || state.Summary == "" {
				return "", errors.New("router received an incomplete chain state")
			}
			if state.Urgent {
				return urgentRouteID, nil
			}
			return standardRouteID, nil
		},
		Cases: []workflow.SwitchCase{
			{ID: urgentRouteID, Deployment: children.urgent, Budget: budget},
			{ID: standardRouteID, Deployment: children.standard, Budget: budget},
		},
	})
	if err != nil {
		return nil, err
	}
	section, err := workflow.Fork(workflow.ForkConfig[routedState, finding, findingBundle]{
		ID: "section",
		Branches: []workflow.ForkBranch{
			{ID: "facts", Deployment: children.facts, Budget: budget},
			{ID: "risks", Deployment: children.risks, Budget: budget},
		},
		WindowSize: sectionWindowSize,
		Reduce: func(findings []finding) (findingBundle, error) {
			if len(findings) != sectionWorkerCount || findings[0].Normalized != findings[1].Normalized ||
				findings[0].Summary != findings[1].Summary || findings[0].Route != findings[1].Route {
				return findingBundle{}, errors.New("parallel sections returned inconsistent context")
			}
			return findingBundle{
				Normalized: findings[0].Normalized, Summary: findings[0].Summary,
				Route: findings[0].Route, Findings: slices.Clone(findings),
			}, nil
		},
	})
	if err != nil {
		return nil, err
	}
	vote, err := workflow.Fork(workflow.ForkConfig[findingBundle, ballot, patternReport]{
		ID: "vote",
		Branches: []workflow.ForkBranch{
			{ID: "approve_first", Deployment: children.approveFirst, Budget: budget},
			{ID: "reject_first", Deployment: children.rejectFirst, Budget: budget},
			{ID: "reject_second", Deployment: children.rejectSecond, Budget: budget},
			{ID: "approve_second", Deployment: children.approveSecond, Budget: budget},
		},
		WindowSize: voteWindowSize,
		Reduce:     reduceBallots,
	})
	if err != nil {
		return nil, err
	}
	return []workflow.Stage{normalize, summarize, route, section, vote}, nil
}

func newPatternRoot(
	stages []workflow.Stage,
	children []agent.Deployment,
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
		Children      []string     `json:"children"`
		Budget        agent.Budget `json:"budget"`
		SectionWindow uint32       `json:"section_window"`
		VoteWindow    uint32       `json:"vote_window"`
	}{Budget: budget, SectionWindow: sectionWindowSize, VoteWindow: voteWindowSize}
	for _, child := range children {
		configuration.Children = append(configuration.Children, child.DeploymentRef().Digest().String())
	}
	configurationJSON, err := json.Marshal(configuration)
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

func (p patternChildren) deployments() []agent.Deployment {
	return []agent.Deployment{
		p.normalizer, p.summarizer, p.urgent, p.standard, p.facts, p.risks,
		p.approveFirst, p.rejectFirst, p.rejectSecond, p.approveSecond,
	}
}

func (p patternChildren) resolver() deploymentResolver {
	children := p.deployments()
	resolver := make(deploymentResolver, len(children))
	for _, child := range children {
		resolver[child.DeploymentRef()] = child
	}
	return resolver
}

func routeDeployment(route string) (agent.Deployment, error) {
	if route != "urgent" && route != "standard" {
		return agent.Deployment{}, errors.New("route must be urgent or standard")
	}
	return transformDeployment(
		"example.workflow_patterns.route_"+route,
		"Apply the selected "+route+" route.",
		struct {
			Route string `json:"route"`
		}{Route: route},
		func(state chainState) (routedState, error) {
			if state.Normalized == "" || state.Summary == "" {
				return routedState{}, errors.New("route worker received an incomplete chain state")
			}
			return routedState{Normalized: state.Normalized, Summary: state.Summary, Route: route}, nil
		},
	)
}

func findingDeployment(section string) (agent.Deployment, error) {
	if section != "facts" && section != "risks" {
		return agent.Deployment{}, errors.New("section must be facts or risks")
	}
	return transformDeployment(
		"example.workflow_patterns.section_"+section,
		"Produce the "+section+" parallel section.",
		struct {
			Section string `json:"section"`
		}{Section: section},
		func(state routedState) (finding, error) {
			if state.Route == "" || state.Summary == "" {
				return finding{}, errors.New("section worker received incomplete routed state")
			}
			return finding{
				Normalized: state.Normalized, Summary: state.Summary, Route: state.Route,
				Section: section, Content: section + " for " + state.Summary,
			}, nil
		},
	)
}

func ballotDeployment(name, choice string) (agent.Deployment, error) {
	if choice != "approve" && choice != "reject" {
		return agent.Deployment{}, errors.New("ballot choice must be approve or reject")
	}
	return transformDeployment(
		"example.workflow_patterns.vote_"+name,
		"Return one deterministic "+choice+" ballot.",
		struct {
			Choice string `json:"choice"`
		}{Choice: choice},
		func(bundle findingBundle) (ballot, error) {
			if len(bundle.Findings) != 2 || bundle.Findings[0].Content == "" || bundle.Findings[1].Content == "" {
				return ballot{}, errors.New("voter requires both parallel sections")
			}
			sections := []string{bundle.Findings[0].Section, bundle.Findings[1].Section}
			evidence := []string{bundle.Findings[0].Content, bundle.Findings[1].Content}
			return ballot{
				Normalized: bundle.Normalized, Summary: bundle.Summary, Route: bundle.Route,
				Sections: sections, Evidence: evidence, Choice: choice,
			}, nil
		},
	)
}

func reduceBallots(ballots []ballot) (patternReport, error) {
	if len(ballots) == 0 {
		return patternReport{}, errors.New("parallel vote returned no ballots")
	}
	counts := make(map[string]int)
	first := ballots[0]
	for index, ballot := range ballots {
		if ballot.Choice != "approve" && ballot.Choice != "reject" {
			return patternReport{}, fmt.Errorf("ballot %d has invalid choice", index)
		}
		if ballot.Normalized != first.Normalized || ballot.Summary != first.Summary ||
			ballot.Route != first.Route || !slices.Equal(ballot.Sections, first.Sections) ||
			!slices.Equal(ballot.Evidence, first.Evidence) {
			return patternReport{}, errors.New("parallel ballots returned inconsistent context")
		}
		counts[ballot.Choice]++
	}
	winner := ""
	winnerVotes := 0
	for _, ballot := range ballots {
		if counts[ballot.Choice] > winnerVotes {
			winner = ballot.Choice
			winnerVotes = counts[ballot.Choice]
		}
	}
	return patternReport{
		Normalized: first.Normalized, Summary: first.Summary, Route: first.Route,
		Sections: slices.Clone(first.Sections), Decision: winner,
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
