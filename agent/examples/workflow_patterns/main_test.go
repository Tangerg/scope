package main

import (
	"bytes"
	"context"
	"maps"
	"slices"
	"strings"
	"testing"

	agentrun "github.com/Tangerg/scope/agent/run"
)

func TestRun(t *testing.T) {
	var output bytes.Buffer
	if err := run(context.Background(), &output); err != nil {
		t.Fatal(err)
	}
	const want = "chain: RELEASE AGENT -> summary: RELEASE AGENT\n" +
		"route: urgent\n" +
		"sections: facts, risks\n" +
		"vote: approve 2/4\n" +
		"processes: 10\n"
	if output.String() != want {
		t.Fatalf("output=%q, want %q", output.String(), want)
	}
}

func TestRoutingStartsOnlySelectedExactChild(t *testing.T) {
	report, evidence, err := execute(
		context.Background(),
		patternRequest{Text: "standard request", Urgent: false},
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.Route != "standard" || evidence.ProcessCount() != 10 {
		t.Fatalf("report=%#v evidence=%#v", report, evidence)
	}
	if evidence.Programs["example.workflow_patterns.route_standard"] != 1 ||
		evidence.Programs["example.workflow_patterns.route_urgent"] != 0 {
		t.Fatalf("route programs=%#v", evidence.Programs)
	}
}

func TestParallelSectionsAndVotingPreserveDeclarationOrder(t *testing.T) {
	report, evidence, err := execute(
		context.Background(),
		patternRequest{Text: "ordered request", Urgent: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(report.Sections, []string{"facts", "risks"}) {
		t.Fatalf("sections=%v, want declaration order", report.Sections)
	}
	if report.Decision != "approve" || report.DecisionVotes != 2 || report.TotalVotes != 4 {
		t.Fatalf("vote=%#v, want stable first-declared winner of 2-2 tie", report)
	}
	want := map[string]int{
		"example.workflow_patterns":                     1,
		"example.workflow_patterns.normalize":           1,
		"example.workflow_patterns.summarize":           1,
		"example.workflow_patterns.route_urgent":        1,
		"example.workflow_patterns.section_facts":       1,
		"example.workflow_patterns.section_risks":       1,
		"example.workflow_patterns.vote_approve_first":  1,
		"example.workflow_patterns.vote_reject_first":   1,
		"example.workflow_patterns.vote_reject_second":  1,
		"example.workflow_patterns.vote_approve_second": 1,
	}
	if !maps.Equal(evidence.Programs, want) {
		t.Fatalf("programs=%#v, want %#v", evidence.Programs, want)
	}
}

func TestIncompleteEvidenceCannotBeRoutedOrTurnedIntoAVote(t *testing.T) {
	ctx := t.Context()
	report, evidence, err := execute(ctx, patternRequest{Text: "  "})
	if err == nil || report.Normalized != "" || evidence.ProcessCount() != 0 {
		t.Fatal("empty request produced a partial report", report, evidence, err)
	}
	if summary, err := summarizeChain(ctx, chainState{Normalized: "X", Summary: "forged"}); summary.Summary != "" || err == nil {
		t.Fatal("overwrote an already-owned summary")
	}
	if route, err := selectPatternRoute(ctx, chainState{Normalized: "X"}); route != "" || err == nil {
		t.Fatal("routed incomplete evidence", route)
	}
	for _, votes := range [][]ballot{nil, {{Choice: ballotChoice("abstain")}}} {
		report, err := reduceBallots(ctx, findingBundle{}, votes)
		if err == nil || report.TotalVotes != 0 || report.DecisionVotes != 0 {
			t.Fatalf("invalid vote report=%v err=%v", report, err)
		}
	}
}

func TestWorkflowChildrenRejectInvalidPolicyAndIncompleteUpstreamRecords(t *testing.T) {
	ctx := t.Context()
	rt, err := agentrun.New(ctx, agentrun.Config{Build: "example-test", Store: agentrun.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if e := rt.Close(ctx); e != nil {
			t.Error(e)
		}
	}()
	if p, routeProgramErr := routeProgram(rt.Compiler(), "typo"); p.Valid() || routeProgramErr == nil {
		t.Fatal("unknown route accepted")
	}
	if p, findingProgramErr := findingProgram(rt.Compiler(), "unknown"); p.Valid() || findingProgramErr == nil {
		t.Fatal("unknown section accepted")
	}
	if p, ballotProgramErr := ballotProgram(rt.Compiler(), "invalid", ballotChoice("maybe")); p.Valid() || ballotProgramErr == nil {
		t.Fatal("unknown vote accepted")
	}
	route, err := routeProgram(rt.Compiler(), standardRouteID)
	if err != nil {
		t.Fatal(err)
	}
	section, err := findingProgram(rt.Compiler(), factsSectionID)
	if err != nil {
		t.Fatal(err)
	}
	ballotChild, err := ballotProgram(rt.Compiler(), approveFirstBallotID, ballotApprove)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		child agentrun.Program[agentrun.Payload, agentrun.Payload]
		input any
		want  string
	}{{route, chainState{Normalized: "X"}, "incomplete chain"}, {section, routedState{Summary: "Y"}, "incomplete routed"}, {ballotChild, findingBundle{Findings: []finding{{Section: factsSectionID, Content: ""}}}, "every parallel section"}} {
		payload, e := agentrun.EncodePayload(row.input)
		if e != nil {
			t.Fatal(e)
		}
		h, e := rt.Start(ctx, row.child, agentrun.StartRequest[agentrun.Payload]{ID: agentrun.StartID{Scope: "validation", Key: row.child.Descriptor().Name()}, Input: payload})
		if e != nil {
			t.Fatal(e)
		}
		result, e := h.Await(ctx)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = h.Await(ctx); e != nil {
			t.Fatal(e)
		}
		failure, ok := result.Termination().Failure()
		if !ok || !strings.Contains(failure.Message(), row.want) {
			t.Fatalf("child=%s failed as %v", row.child.Descriptor().Name(), failure)
		}
	}
}
