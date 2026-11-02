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
	store := agentrun.NewMemoryStore()
	runtime, err := agentrun.New(ctx, agentrun.Config{Build: "example-workflow", Store: store})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, runtime.Close(context.WithoutCancel(ctx))) }()
	root, err := newManagedWorkflow(runtime.Compiler())
	if err != nil {
		return err
	}
	topology := root.Definition().(interface{ Topology() workflow.Topology }).Topology()

	resultHandle, err := runtime.Start(ctx, root, agentrun.StartRequest[request]{ID: agentrun.StartID{Scope: "example", Key: "review"}, Input: request{Text: "  ship managed workflow  "}})
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
	log, _, err := store.Load(ctx, result.ProcessID())
	if err != nil {
		return err
	}
	processes := 0
	for _, entry := range log.Entries {
		if _, ok := entry.Record.(agentrun.Started); ok {
			processes++
		}
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
		processes,
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

func newManagedWorkflow(compiler *agentrun.Compiler) (agentrun.Program[request, reviewReport], error) {
	normalizer, err := transformProgram(compiler,
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
		Steps: agentrun.NewQuota(workflowChildBudgetUnits), Effects: agentrun.NewQuota(workflowChildBudgetUnits), Messages: agentrun.NewQuota(workflowChildBudgetUnits),
	}
	normalize, err := workflow.Call(workflow.CallConfig{
		ID: "normalize", Program: normalizer, Budget: budget,
	})
	if err != nil {
		return agentrun.Program[request, reviewReport]{}, err
	}
	branches := make([]workflow.ForkBranch, 0, len(reviewers))
	for _, reviewer := range reviewers {
		program, reviewerErr := reviewerProgram(compiler, reviewer)
		if reviewerErr != nil {
			return agentrun.Program[request, reviewReport]{}, reviewerErr
		}
		branches = append(branches, workflow.ForkBranch{ID: reviewer, Program: program, Budget: budget})
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
	return workflow.Compile[request, reviewReport](compiler, workflow.Config{Revision: "review-v1",
		Name: "example.workflow.review", Description: "Normalize and review one request with managed child Processes.",
		Stages: []workflow.Stage{normalize, reviewStage},
	})
}

func reviewerProgram(compiler *agentrun.Compiler, reviewer string) (agentrun.Program[agentrun.Payload, agentrun.Payload], error) {
	return transformProgram(compiler,
		"example.workflow.reviewer_"+reviewer,
		"Return one deterministic "+reviewer+" review.",
		func(_ context.Context, _ normalizedRequest) (verdict, error) {
			return verdict{Verdict: "ready"}, nil
		},
	)
}

func transformProgram[I, O any](
	compiler *agentrun.Compiler,
	name, description string,

	transform workflow.TransformFunc[I, O],
) (agentrun.Program[agentrun.Payload, agentrun.Payload], error) {
	stage, err := workflow.Transform("apply", transform)
	if err != nil {
		return agentrun.Program[agentrun.Payload, agentrun.Payload]{}, err
	}
	return workflow.Compile[agentrun.Payload, agentrun.Payload](compiler, workflow.Config{
		Name: name, Description: description, Revision: name, Stages: []workflow.Stage{stage},
	})
}
