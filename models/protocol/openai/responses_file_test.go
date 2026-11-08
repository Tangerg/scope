package openai_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	corecontent "github.com/Tangerg/scope/core/content"
	"github.com/Tangerg/scope/core/media"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

func TestResponsesFileWirePreservesSourceAndMIME(t *testing.T) {
	for _, test := range []struct {
		name     string
		mime     string
		kind     media.SourceKind
		source   string
		fileData string
		fileID   string
		fileURL  string
	}{
		{name: "inline PDF", mime: "application/pdf", kind: media.SourceBytes, source: "pdf", fileData: "data:application/pdf;base64,cGRm"},
		{name: "inline text", mime: "text/plain", kind: media.SourceBytes, source: "text", fileData: "data:text/plain;base64,dGV4dA=="},
		{name: "reference", mime: "application/pdf", kind: media.SourceReference, source: "file-1", fileID: "file-1"},
		{name: "URI", mime: "application/pdf", kind: media.SourceURI, source: "https://example.com/report.pdf", fileURL: "https://example.com/report.pdf"},
	} {
		for _, role := range []string{"user", "tool"} {
			for _, operation := range []string{"call", "count"} {
				t.Run(test.name+"/"+role+"/"+operation, func(t *testing.T) {
					type fileInput struct {
						Type     string `json:"type"`
						Filename string `json:"filename"`
						FileData string `json:"file_data"`
						FileID   string `json:"file_id"`
						FileURL  string `json:"file_url"`
					}
					captured := make(chan fileInput, 1)
					server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
						var body struct {
							Input []struct {
								Content []fileInput `json:"content"`
								Output  []fileInput `json:"output"`
							} `json:"input"`
						}
						if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
							t.Error(err)
							http.Error(writer, "invalid request", http.StatusBadRequest)
							return
						}
						messageCount := 1
						if role == "tool" {
							messageCount = 3
						}
						if len(body.Input) != messageCount {
							t.Errorf("input count = %d, want %d", len(body.Input), messageCount)
							http.Error(writer, "invalid input items", http.StatusBadRequest)
							return
						}
						items := body.Input[0].Content
						if role == "tool" {
							items = body.Input[2].Output
						}
						if len(items) != 1 {
							t.Errorf("file items = %#v", items)
							http.Error(writer, "invalid file items", http.StatusBadRequest)
							return
						}
						captured <- items[0]
						if operation == "count" {
							if request.URL.Path != "/responses/input_tokens" {
								t.Errorf("count path = %q", request.URL.Path)
							}
							writer.Header().Set("Content-Type", "application/json")
							fmt.Fprint(writer, `{"object":"response.input_tokens","input_tokens":12}`)
							return
						}
						writeResponsesCompletion(writer)
					}))
					t.Cleanup(server.Close)
					var value *media.Media
					var err error
					switch test.kind {
					case media.SourceBytes:
						value, err = media.NewBytes(test.mime, []byte(test.source))
					case media.SourceReference:
						value, err = media.NewReference(test.mime, test.source)
					case media.SourceURI:
						value, err = media.NewURI(test.mime, test.source)
					}
					if err != nil {
						t.Fatal(err)
					}
					value.Name = "evidence"
					request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewMediaPart(value))}}
					if role == "tool" {
						request.Messages = []chat.Message{
							chat.NewUserMessage(chat.NewTextPart("read the file")),
							chat.NewAssistantMessage(chat.NewToolCallPart(chat.ToolCall{ID: "call-1", Name: "read", Arguments: `{}`})),
							chat.NewToolMessage(chat.ToolResult{ID: "call-1", Name: "read", Output: toolcontract.Output{
								Content: []corecontent.Block{{Kind: corecontent.Media, Media: value}},
							}}),
						}
						request.Tools = []toolcontract.Definition{{Name: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}}
					}
					model := newResponsesModel(t, server.URL, "gpt-5")
					if operation == "count" {
						count, countErr := model.CountInputTokens(t.Context(), request)
						if countErr != nil || count != 12 {
							t.Fatalf("count = %d, error = %v", count, countErr)
						}
					} else if _, callErr := model.Call(t.Context(), request); callErr != nil {
						t.Fatal(callErr)
					}
					file := <-captured
					want := fileInput{Type: "input_file", Filename: "evidence", FileData: test.fileData, FileID: test.fileID, FileURL: test.fileURL}
					if file != want {
						t.Fatalf("wire file = %#v, want %#v", file, want)
					}
				})
			}
		}
	}
}
