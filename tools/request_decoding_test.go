package tools_test

import (
	"bytes"
	jsonv2 "encoding/json/v2"
	"testing"

	"github.com/Tangerg/scope/tools/fs"
	"github.com/Tangerg/scope/tools/httpreq"
	"github.com/Tangerg/scope/tools/shell"
	"github.com/Tangerg/scope/tools/web"
)

func TestRequestDecodingRejectsInvalidInputWithoutChangingTheReceiver(t *testing.T) {
	for _, test := range []struct {
		name    string
		request any
		valid   string
		invalid string
	}{
		{"write", &fs.WriteRequest{}, `{"path":"file.txt","content":"text"}`, `{"path":"changed.txt","content":"binary\u0000text"}`},
		{"edit", &fs.EditRequest{}, `{"path":"file.txt","old_string":"old","new_string":"new"}`, `{"path":"changed.txt","old_string":"same","new_string":"same"}`},
		{"patch", &fs.ApplyPatchRequest{}, `{"patch":"diff --git a/file.txt b/file.txt\n--- a/file.txt\n+++ b/file.txt\n@@ -1,1 +1,1 @@\n-old\n+new\n"}`, `{"patch":"not a diff"}`},
		{"glob", &fs.GlobRequest{}, `{"pattern":"**/*.go"}`, `{"pattern":"["}`},
		{"grep", &fs.GrepRequest{}, `{"pattern":"Scope"}`, `{"pattern":"Scope","output_mode":"unknown"}`},
		{"shell", &shell.Request{}, `{"command":"true"}`, `{"command":" "}`},
		{"shell negative timeout", &shell.Request{}, `{"command":"true"}`, `{"command":"true","timeout_ms":-1}`},
		{"shell excessive timeout", &shell.Request{}, `{"command":"true"}`, `{"command":"true","timeout_ms":9223372036854775807}`},
		{"http", &httpreq.Request{}, `{"url":"https://example.com"}`, `{"url":"https://example.com?a=1","query":{"a":"2"}}`},
		{"search", &web.SearchRequest{}, `{"query":"Scope"}`, `{"query":"Scope","allowed_domains":["example.com"],"blocked_domains":["example.org"]}`},
		{"fetch", &web.FetchRequest{}, `{"url":"https://example.com"}`, `{"url":"file:///etc/hosts"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := jsonv2.Unmarshal([]byte(test.valid), test.request); err != nil {
				t.Fatalf("valid request: %v", err)
			}
			before, err := jsonv2.Marshal(test.request)
			if err != nil {
				t.Fatal(err)
			}
			for _, invalid := range []string{"null", "true", "{", `{"unknown":true}`, test.invalid} {
				if decodeErr := jsonv2.Unmarshal([]byte(invalid), test.request); decodeErr == nil {
					t.Fatalf("accepted %s", invalid)
				}
				after, encodeErr := jsonv2.Marshal(test.request)
				if encodeErr != nil || !bytes.Equal(before, after) {
					t.Fatalf("rejected input changed the receiver: before=%s after=%s error=%v", before, after, encodeErr)
				}
			}
		})
	}
}
