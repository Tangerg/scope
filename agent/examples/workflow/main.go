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

	agentrun "github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/workflow"
)

const (
	workflowChildBudgetUnits = 16
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, output io.Writer) (err error) {
	root, err := newManagedWorkflow(ctx)
	if err != nil {
		return err
	}
	definition, ok := root.Definition().(*workflow.Definition)
	if !ok {
		return errors.New("root does not contain a Workflow Definition")
	}
	topology := definition.Topology()
	engine, err := agentrun.New(ctx, agentrun.Config{Store: agentrun.NewMemoryStore()})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, engine.Close(context.WithoutCancel(ctx))) }()

	resultHandle, err := engine.Start(ctx, root, agentrun.StartRequest[request]{ID: agentrun.StartID{Scope: "example", Key: "review"}, Input: request{Text: "  ship managed workflow  "}})
	if err != nil {
		return err
	}
	if err = resultHandle.Join(ctx); err != nil {
		return err
	}
	result, err := resultHandle.Await(ctx)
	if err != nil {
		return err
	}
	report, err := result.Output()
	if err != nil {
		return err
	}
	tree, err := engine.CaptureTree(ctx, result.ProcessID())
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

// verdict is all a reviewer branch adds; the Fork reducer pairs it with the
// branch that returned it and with the request the Fork received.
type verdict struct {
	Verdict string `json:"verdict"`
}

type review struct {
	Reviewer string `json:"reviewer"`
	Verdict  string `json:"verdict"`
}

type reviewReport struct {
	Request string   `json:"request"`
	Reviews []review `json:"reviews"`
}

func newManagedWorkflow(ctx context.Context) (agentrun.Program[request, reviewReport], error) {
	normalizer, err := transformDeployment(
		"example.workflow.normalizer",
		"Normalize one review request.",
		func(_ context.Context, input request) (normalizedRequest, error) {
			return normalizedRequest{Text: strings.TrimSpace(input.Text)}, nil
		},
	)
	if err != nil {
		return agentrun.Program[request, reviewReport]{}, err
	}
	// Fork outputs arrive in branch declaration order.
	reviewers := []string{"clarity", "safety"}
	budget := agentrun.Budget{
		Steps: agentrun.NewQuota(workflowChildBudgetUnits), Effects: agentrun.NewQuota(workflowChildBudgetUnits), Signals: agentrun.NewQuota(workflowChildBudgetUnits),
	}
	normalize, err := workflow.Call(workflow.CallConfig{
		ID: "normalize", Program: normalizer, Budget: budget,
	})
	if err != nil {
		return agentrun.Program[request, reviewReport]{}, err
	}
	branches := make([]workflow.ForkBranch, 0, len(reviewers))
	for _, reviewer := range reviewers {
		deployment, reviewerErr := reviewerDeployment(reviewer)
		if reviewerErr != nil {
			return agentrun.Program[request, reviewReport]{}, reviewerErr
		}
		branches = append(branches, workflow.ForkBranch{ID: reviewer, Program: deployment, Budget: budget})
	}
	reviewStage, err := workflow.Fork(workflow.ForkConfig[normalizedRequest, verdict, reviewReport]{
		ID: "review", Branches: branches, WindowSize: uint32(len(reviewers)),
		Reduce: func(_ context.Context, request normalizedRequest, verdicts []verdict) (reviewReport, error) {
			report := reviewReport{Request: request.Text}
			for index, reviewer := range reviewers {
				report.Reviews = append(report.Reviews, review{Reviewer: reviewer, Verdict: verdicts[index].Verdict})
			}
			return report, nil
		},
	})
	if err != nil {
		return agentrun.Program[request, reviewReport]{}, err
	}
	return workflow.Compile[request, reviewReport](ctx, workflow.Config{BuildIdentity: agentrun.ComputeDigest([]byte("example-workflow-review-implementation")), CallbackIdentity: agentrun.ComputeDigest([]byte("example-workflow-review-configuration")),
		Name: "example.workflow.review", Description: "Normalize and review one request with managed child Processes.",
		Stages: []workflow.Stage{normalize, reviewStage},
	})
}

func reviewerDeployment(reviewer string) (agentrun.Program[agentrun.Payload, agentrun.Payload], error) {
	return transformDeployment(
		"example.workflow.reviewer_"+reviewer,
		"Return one deterministic "+reviewer+" review.",
		func(_ context.Context, _ normalizedRequest) (verdict, error) {
			return verdict{Verdict: "ready"}, nil
		},
	)
}

func transformDeployment[I, O any](
	name string,
	description string,
	transform workflow.TransformFunc[I, O],
) (agentrun.Program[agentrun.Payload, agentrun.Payload], error) {
	stage, err := workflow.Transform("apply", transform)
	if err != nil {
		return agentrun.Program[agentrun.Payload, agentrun.Payload]{}, err
	}
	definition, err := workflow.NewDefinition(workflow.DefinitionConfig{
		Name: name, Description: description, Stages: []workflow.Stage{stage},
	})
	if err != nil {
		return agentrun.Program[agentrun.Payload, agentrun.Payload]{}, err
	}
	return agentrun.Bind[agentrun.Payload, agentrun.Payload](agentrun.BindingConfig{
		Definition:           definition,
		ImplementationDigest: agentrun.ComputeDigest([]byte(name + "-implementation")),
		ConfigurationDigest:  agentrun.ComputeDigest([]byte(name + "-configuration")),
	})
}
