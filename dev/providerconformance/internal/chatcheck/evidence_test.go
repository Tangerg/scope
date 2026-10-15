package chatcheck

import (
	"strings"
	"testing"
)

func TestEvidenceNeverPromotesMissingSkippedOrFailedTests(t *testing.T) {
	for _, test := range []struct {
		name    string
		events  string
		status  string
		support string
	}{
		{"passed", `{"Action":"run","Test":"TestProof"}
{"Action":"pass","Test":"TestProof/child"}
{"Action":"pass","Test":"TestProof"}`, "verified", "unsupported"},
		{"missing", `{"Action":"pass"}`, "unverified", "unknown"},
		{"skipped", `{"Action":"skip","Test":"TestProof"}`, "unverified", "unknown"},
		{"skipped child", `{"Action":"skip","Test":"TestProof/child"}
{"Action":"pass","Test":"TestProof"}`, "unverified", "unknown"},
		{"failed child", `{"Action":"fail","Test":"TestProof/child"}
{"Action":"fail","Test":"TestProof"}`, "failed", "unknown"},
		{"truncated", `{"Action":"run","Test":"TestProof/child"}
{"Action":"pass","Test":"TestProof"}`, "unverified", "unknown"},
		{"similar name", `{"Action":"pass","Test":"TestProofOther"}`, "unverified", "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			results, err := Read(strings.NewReader(test.events))
			if err != nil {
				t.Fatal(err)
			}
			evidence := results.Evidence("binding", "offline_protocol", "", Case{Test: "TestProof", Support: "unsupported"})
			if evidence.Status != test.status || evidence.Support != test.support {
				t.Fatalf("evidence = %+v", evidence)
			}
		})
	}
}

func TestEvidenceRejectsMalformedArtifacts(t *testing.T) {
	if _, err := Read(strings.NewReader(`{"Action":`)); err == nil {
		t.Fatal("accepted truncated event")
	}
}

func TestLiveSelectionIsExplicitAndExact(t *testing.T) {
	for _, invalid := range []string{"all", "openai-chat,", "openai-chat,openai-chat", " openai-chat"} {
		if _, err := Select(invalid); err == nil {
			t.Fatalf("accepted %q", invalid)
		}
	}
	selected, err := Select("")
	if err != nil || len(selected) != 0 {
		t.Fatal("empty selection enabled live calls")
	}
	selected, err = Select("openai-chat,google-generate-content")
	if err != nil || len(selected) != 2 || !selected["openai-chat"] || !selected["google-generate-content"] {
		t.Fatalf("selection = %v, %v", selected, err)
	}
}
