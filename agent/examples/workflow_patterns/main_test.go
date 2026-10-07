package main

import (
	"bytes"
	"context"
	"maps"
	"slices"
	"testing"
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
	if evidence.Deployments["example.workflow_patterns.route_standard"] != 1 ||
		evidence.Deployments["example.workflow_patterns.route_urgent"] != 0 {
		t.Fatalf("route deployments=%#v", evidence.Deployments)
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
	if !maps.Equal(evidence.Deployments, want) {
		t.Fatalf("deployments=%#v, want %#v", evidence.Deployments, want)
	}
}
