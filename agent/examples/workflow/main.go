// Command workflow demonstrates an ordered managed Workflow whose Call and
// Fork Stages create independently recoverable child Processes. It uses only
// deterministic local components and requires no network access.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/workflow"
)

const (
	workflowChildBudgetUnits = 16
	reviewerCount            = 2
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, output io.Writer) (err error) {
	root, resolver, err := newManagedWorkflow()
	if err != nil {
		return err
	}
	definition, ok := root.Definition().(*workflow.Definition)
	if !ok {
		return errors.New("root does not contain a Workflow Definition")
	}
	topology := definition.Topology()
	engine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: resolver})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, engine.Close(context.WithoutCancel(ctx))) }()

	input, err := root.Descriptor().EncodeInput(request{Text: "  ship managed workflow  "})
	if err != nil {
		return err
	}
	process, err := engine.Start(ctx, root, input)
	if err != nil {
		return err
	}
	result, err := process.Await(ctx)
	if err != nil {
		return err
	}
	erased, present := result.Output()
	if !present {
		return fmt.Errorf("workflow Process ended with %s", result.Status())
	}
	report, err := root.Descriptor().DecodeOutput[reviewReport](erased)
	if err != nil {
		return err
	}
	tree, err := engine.CaptureTree(ctx, process.ID())
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(
		output,
		"request: %s\nreviews: %s=%s, %s=%s\nstages: %d\nprocesses: %d\n",
		report.Request,
		report.Reviews[0].Reviewer,
		report.Reviews[0].Verdict,
		report.Reviews[1].Reviewer,
		report.Reviews[1].Verdict,
		len(topology.Stages),
		len(tree.ProcessSnapshots()),
	)
	return err
}

type request struct {
	Text string `json:"text"`
}

type normalizedRequest struct {
	Text string `json:"text"`
}

type review struct {
	Request  string `json:"request"`
	Reviewer string `json:"reviewer"`
	Verdict  string `json:"verdict"`
}

type reviewReport struct {
	Request string   `json:"request"`
	Reviews []review `json:"reviews"`
}

func newManagedWorkflow() (agent.Deployment, deploymentResolver, error) {
	normalizer, err := transformDeployment(
		"example.workflow.normalizer",
		"Normalize one review request.",
		func(_ context.Context, input request) (normalizedRequest, error) {
			return normalizedRequest{Text: strings.TrimSpace(input.Text)}, nil
		},
	)
	if err != nil {
		return agent.Deployment{}, nil, err
	}
	clarity, err := reviewerDeployment("clarity")
	if err != nil {
		return agent.Deployment{}, nil, err
	}
	safety, err := reviewerDeployment("safety")
	if err != nil {
		return agent.Deployment{}, nil, err
	}
	budget := agent.Budget{
		Steps: workflowChildBudgetUnits, Effects: workflowChildBudgetUnits, Signals: workflowChildBudgetUnits,
	}
	normalize, err := workflow.Call(workflow.CallConfig{
		ID: "normalize", Deployment: normalizer, Budget: budget,
	})
	if err != nil {
		return agent.Deployment{}, nil, err
	}
	reviewers, err := workflow.Fork(workflow.ForkConfig[normalizedRequest, review, reviewReport]{
		ID: "review",
		Branches: []workflow.ForkBranch{
			{ID: "clarity", Deployment: clarity, Budget: budget},
			{ID: "safety", Deployment: safety, Budget: budget},
		},
		WindowSize: reviewerCount,
		Reduce: func(_ context.Context, reviews []review) (reviewReport, error) {
			if len(reviews) != reviewerCount || reviews[0].Request != reviews[1].Request {
				return reviewReport{}, errors.New("review branches returned inconsistent results")
			}
			return reviewReport{Request: reviews[0].Request, Reviews: reviews}, nil
		},
	})
	if err != nil {
		return agent.Deployment{}, nil, err
	}
	definition, err := workflow.NewDefinition(workflow.DefinitionConfig{
		Name: "example.workflow.review", Description: "Normalize and review one request with managed child Processes.",
		Stages: []workflow.Stage{normalize, reviewers},
	})
	if err != nil {
		return agent.Deployment{}, nil, err
	}
	root, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition:           definition,
		ImplementationDigest: agent.ComputeDigest([]byte("example-workflow-review-implementation")),
		ConfigurationDigest: agent.ComputeDigest([]byte(
			"example-workflow-review:" + normalizer.DeploymentRef().Digest().String() + ":" +
				clarity.DeploymentRef().Digest().String() + ":" + safety.DeploymentRef().Digest().String(),
		)),
	})
	if err != nil {
		return agent.Deployment{}, nil, err
	}
	return root, deploymentResolver{
		normalizer.DeploymentRef(): normalizer,
		clarity.DeploymentRef():    clarity,
		safety.DeploymentRef():     safety,
	}, nil
}

func reviewerDeployment(reviewer string) (agent.Deployment, error) {
	return transformDeployment(
		"example.workflow.reviewer_"+reviewer,
		"Return one deterministic "+reviewer+" review.",
		func(_ context.Context, input normalizedRequest) (review, error) {
			return review{Request: input.Text, Reviewer: reviewer, Verdict: "ready"}, nil
		},
	)
}

func transformDeployment[I, O any](
	name string,
	description string,
	transform workflow.TransformFunc[I, O],
) (agent.Deployment, error) {
	stage, err := workflow.Transform("apply", transform)
	if err != nil {
		return agent.Deployment{}, err
	}
	definition, err := workflow.NewDefinition(workflow.DefinitionConfig{
		Name: name, Description: description, Stages: []workflow.Stage{stage},
	})
	if err != nil {
		return agent.Deployment{}, err
	}
	return agent.NewDeployment(agent.DeploymentConfig{
		Definition:           definition,
		ImplementationDigest: agent.ComputeDigest([]byte(name + "-implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte(name + "-configuration")),
	})
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
