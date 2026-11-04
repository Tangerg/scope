package tools_test

import (
	"testing"

	"github.com/Tangerg/scope/core/tool"

	"github.com/Tangerg/scope/tools/fs"
	"github.com/Tangerg/scope/tools/httpreq"
	"github.com/Tangerg/scope/tools/shell"
	"github.com/Tangerg/scope/tools/web"
)

func TestRequestAdmissionRejectsInvalidInput(t *testing.T) {
	for _, test := range []struct {
		name     string
		validate func([]byte) error
		valid    string
		invalid  string
	}{
		{"write", admission[fs.WriteRequest](t), `{"path":"file.txt","content":"text"}`, `{"path":"changed.txt","content":"binary\u0000text"}`},
		{"edit", admission[fs.EditRequest](t), `{"path":"file.txt","old_string":"old","new_string":"new"}`, `{"path":"changed.txt","old_string":"same","new_string":"same"}`},
		{"patch", admission[fs.ApplyPatchRequest](t), `{"patch":"diff --git a/file.txt b/file.txt\n--- a/file.txt\n+++ b/file.txt\n@@ -1,1 +1,1 @@\n-old\n+new\n"}`, `{"patch":"not a diff"}`},
		{"glob", admission[fs.GlobRequest](t), `{"pattern":"**/*.go"}`, `{"pattern":"["}`},
		{"grep", admission[fs.GrepRequest](t), `{"pattern":"Scope"}`, `{"pattern":"Scope","output_mode":"unknown"}`},
		{"shell", admission[shell.Request](t), `{"command":"true"}`, `{"command":" "}`},
		{"shell negative timeout", admission[shell.Request](t), `{"command":"true"}`, `{"command":"true","timeout_ms":-1}`},
		{"shell excessive timeout", admission[shell.Request](t), `{"command":"true"}`, `{"command":"true","timeout_ms":9223372036854775807}`},
		{"http", admission[httpreq.Request](t), `{"url":"https://example.com"}`, `{"url":"https://example.com?a=1","query":{"a":"2"}}`},
		{"search", admission[web.SearchRequest](t), `{"query":"Scope"}`, `{"query":"Scope","allowed_domains":["example.com"],"blocked_domains":["example.org"]}`},
		{"fetch", admission[web.FetchRequest](t), `{"url":"https://example.com"}`, `{"url":"file:///etc/hosts"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.validate([]byte(test.valid)); err != nil {
				t.Fatalf("valid request: %v", err)
			}
			for _, invalid := range []string{"null", "true", "{", `{"unknown":true}`, test.invalid} {
				if err := test.validate([]byte(invalid)); err == nil {
					t.Fatalf("accepted %s", invalid)
				}
			}
		})
	}
}

// admission is the typed-input rule every Func-based tool applies before execution.
func admission[In any](t *testing.T) func([]byte) error {
	t.Helper()
	signature, err := tool.NewSignature[In, string]()
	if err != nil {
		t.Fatal(err)
	}
	return signature.InputValidator()
}
