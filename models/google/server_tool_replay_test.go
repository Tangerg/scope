package google_test

import (
	"bytes"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/invocation"
)

func TestServerToolsSurviveHistoryAndStreamAggregation(t *testing.T) {
	for _, test := range []struct {
		name  string
		parts []json.RawMessage
	}{
		{"interleaved", []json.RawMessage{
			[]byte(`{"text":"original","partMetadata":{"id":9007199254740993}}`),
			[]byte(`{"toolCall":{"id":"search","toolType":"GOOGLE_SEARCH_WEB","args":{"id":9007199254740993,"query":"search"}},"thoughtSignature":"c2lnbmVk"}`),
			[]byte(`{"toolResponse":{"id":"search","toolType":"GOOGLE_SEARCH_WEB","response":{"ratio":12345678901234567890.0123456789}}}`),
			[]byte(`{"text":"answer"}`),
			[]byte(`{"toolCall":{"id":"tail","toolType":"URL_CONTEXT"}}`),
		}},
		{"server-only", []json.RawMessage{
			[]byte(`{"toolCall":{"id":"search","toolType":"GOOGLE_SEARCH_WEB","args":{"id":9007199254740993}}}`),
			[]byte(`{"toolResponse":{"id":"search","toolType":"GOOGLE_SEARCH_WEB","response":{"ratio":12345678901234567890.0123456789}}}`),
		}},
		{"empty-server-object", []json.RawMessage{[]byte(`{"toolCall":{}}`), []byte(`{"toolResponse":{}}`)}},
	} {
		for _, fragmented := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fragmented=%t", test.name, fragmented), func(t *testing.T) {
				var bodies [][]byte
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					bodies = append(bodies, body)
					w.Header().Set("Content-Type", "text/event-stream")
					parts := test.parts
					if len(bodies) > 1 {
						parts = []json.RawMessage{[]byte(`{"text":"continued"}`)}
					}
					if fragmented {
						for _, part := range parts {
							fmt.Fprintf(w, "data: {\"candidates\":[{\"content\":{\"parts\":[%s]}}]}\n\n", part)
						}
						fmt.Fprint(w, "data: {\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n")
						return
					}
					raw, err := jsonv2.Marshal(parts)
					if err != nil {
						t.Error(err)
					}
					fmt.Fprintf(w, "data: {\"candidates\":[{\"content\":{\"parts\":%s},\"finishReason\":\"STOP\"}]}\n\n", raw)
				}))
				defer server.Close()
				model := newNativeChat(t, "google", server)
				user := chat.NewUserMessage(chat.NewTextPart("search"))
				request := &chat.Request{Messages: []chat.Message{user}}
				if err := request.Options.Extensions.Set("google/request", json.RawMessage(`{"toolConfig":{"includeServerSideToolInvocations":true}}`)); err != nil {
					t.Fatal(err)
				}
				response, err := chat.Collect(model.Stream(t.Context(), request))
				if err != nil {
					t.Fatal(err)
				}
				message := response.Output.Message
				if message == nil || len(message.ToolCalls()) != 0 || response.Output.FinishReason != chat.FinishReasonStop {
					t.Fatalf("server tools became host work: %#v", response.Output)
				}
				stored, err := jsonv2.Marshal(message)
				if err != nil {
					t.Fatal(err)
				}
				var restored chat.Message
				if decodeErr := jsonv2.Unmarshal(stored, &restored); decodeErr != nil {
					t.Fatal(decodeErr)
				}
				want := append([]json.RawMessage(nil), test.parts...)
				if test.name == "interleaved" {
					restored.Parts[0].Text = "edited"
					want[0] = []byte(`{"text":"edited","partMetadata":{"id":9007199254740993}}`)
				}
				_, err = model.Call(t.Context(), &chat.Request{Messages: []chat.Message{user, restored, chat.NewUserMessage(chat.NewTextPart("continue"))}})
				if err != nil {
					t.Fatal(err)
				}
				if len(bodies) != 2 {
					t.Fatalf("HTTP calls=%d", len(bodies))
				}
				var replay struct {
					Contents []struct {
						Parts []json.RawMessage `json:"parts"`
					} `json:"contents"`
				}
				if err := jsonv2.Unmarshal(bodies[1], &replay); err != nil {
					t.Fatal(err)
				}
				if len(replay.Contents) != 3 || len(replay.Contents[1].Parts) != len(want) {
					t.Fatalf("replay=%s", bodies[1])
				}
				for index, part := range replay.Contents[1].Parts {
					got := jsontext.Value(part).Clone()
					expected := jsontext.Value(want[index]).Clone()
					if err := got.Format(jsontext.ReorderRawObjects(true)); err != nil {
						t.Fatal(err)
					}
					if err := expected.Format(jsontext.ReorderRawObjects(true)); err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(got, expected) {
						t.Fatalf("part[%d]=%s, want %s", index, got, expected)
					}
				}
			})
		}
	}
}

func TestServerToolReplayCannotShadowCoreContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("invalid replay reached HTTP")
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	model := newNativeChat(t, "google", server)
	for _, state := range []string{
		`{"before":[{"text":"shadow"}]}`,
		`{"before":[{"text":"shadow","toolCall":{"id":"x"}}]}`,
		`{"before":[null]}`,
	} {
		t.Run(state, func(t *testing.T) {
			part := chat.NewTextPart("current")
			if err := part.Metadata.Set("google/part_state", json.RawMessage(state)); err != nil {
				t.Fatal(err)
			}
			response, err := model.Call(t.Context(), &chat.Request{Messages: []chat.Message{chat.NewAssistantMessage(part)}})
			if response != nil || invocation.ClassifyFailure(err) != invocation.FailureNotExecuted || !strings.Contains(err.Error(), "replay") && !strings.Contains(err.Error(), "generation part") {
				t.Fatalf("response=%#v error=%v", response, err)
			}
		})
	}
}
