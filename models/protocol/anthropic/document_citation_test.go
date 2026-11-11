package anthropic

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	corecontent "github.com/Tangerg/scope/core/content"
	"github.com/Tangerg/scope/core/media"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

func TestDocumentCitationsUseRequestSourcesAcrossMessageAndStreamContent(t *testing.T) {
	citations := []string{
		`{"type":"page_location","document_index":0,"document_title":null,"file_id":null,"cited_text":"first","start_page_number":1,"end_page_number":2}`,
		`{"type":"page_location","document_index":1,"document_title":"report","file_id":"unrelated-native-file","cited_text":"second","start_page_number":1,"end_page_number":2}`,
		`{"type":"page_location","document_index":2,"document_title":"report","file_id":null,"cited_text":"third","start_page_number":1,"end_page_number":2}`,
	}
	want := []corecontent.Citation{
		{Source: corecontent.CitationSource{Kind: corecontent.CitationSourceURI, Value: "https://example.com/first.pdf"}, Quote: "first"},
		{Source: corecontent.CitationSource{Kind: corecontent.CitationSourceReference, Value: "document-b"}, Title: "report", Quote: "second"},
		{Source: corecontent.CitationSource{Kind: corecontent.CitationSourceURI, Value: "urn:sha256:ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"}, Title: "report", Quote: "third"},
	}
	for _, phase := range []string{"message start", "block start", "citation delta"} {
		for _, messageID := range []string{"", "message-id"} {
			t.Run(phase+"/"+messageID, func(t *testing.T) {
				textBlock := `{"type":"text","text":"answer","citations":[` + strings.Join(citations, ",") + `]}`
				content := `[]`
				if phase == "message start" {
					content = `[` + textBlock + `]`
				}
				events := []string{fmt.Sprintf(`{"type":"message_start","message":{"id":%q,"type":"message","role":"assistant","model":"claude-test","content":%s,"usage":{"input_tokens":1,"output_tokens":0}}}`, messageID, content)}
				if phase != "message start" {
					if phase == "citation delta" {
						textBlock = `{"type":"text","text":"answer"}`
					}
					events = append(events, `{"type":"content_block_start","index":0,"content_block":`+textBlock+`}`)
					if phase == "citation delta" {
						for _, citation := range citations {
							events = append(events, `{"type":"content_block_delta","index":0,"delta":{"type":"citations_delta","citation":`+citation+`}}`)
						}
					}
					events = append(events, `{"type":"content_block_stop","index":0}`)
				}
				events = append(events, `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`, `{"type":"message_stop"}`)
				model := newLifecycleMessages(t, events)
				response, err := model.Call(t.Context(), documentCitationRequest(t))
				if err != nil || response == nil || response.Text() != "answer" {
					t.Fatalf("response=%#v error=%v", response, err)
				}
				got := response.Output.Message.Parts[0].Citations
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("citations=%#v, want %#v", got, want)
				}
			})
		}
	}
}

func TestDocumentCitationRequiresAnExactExistingDocumentIndex(t *testing.T) {
	for _, raw := range []string{"", "null", `"0"`, "false", "0.5", "-1", "3", "9223372036854775808"} {
		t.Run(raw, func(t *testing.T) {
			index := ""
			if raw != "" {
				index = `"document_index":` + raw + `,`
			}
			model := newLifecycleMessages(t, []string{
				`{"type":"message_start","message":{"type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"answer"}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"citations_delta","citation":{"type":"page_location",` + index + `"document_title":null,"file_id":null,"cited_text":"quote","start_page_number":1,"end_page_number":2}}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
				`{"type":"message_stop"}`,
			})
			response, err := model.Call(t.Context(), documentCitationRequest(t))
			if response != nil || !errors.Is(err, corechat.ErrInvalidResponse) {
				t.Fatalf("document_index=%s response=%#v error=%v", raw, response, err)
			}
		})
	}
}

func TestInitialDocumentCitationsCannotAttachToAnotherTextBlock(t *testing.T) {
	const citationOnly = `{"type":"text","text":"","citations":[{"type":"page_location","document_index":0,"document_title":null,"file_id":null,"cited_text":"quote","start_page_number":1,"end_page_number":2}]}`
	for _, phase := range []string{"message start", "block start"} {
		t.Run(phase, func(t *testing.T) {
			var events []string
			if phase == "message start" {
				events = []string{`{"type":"message_start","message":{"type":"message","role":"assistant","model":"claude-test","content":[{"type":"text","text":"previous"},` + citationOnly + `],"usage":{"input_tokens":1,"output_tokens":0}}}`}
			} else {
				events = []string{
					`{"type":"message_start","message":{"type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
					`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"previous"}}`,
					`{"type":"content_block_stop","index":0}`,
					`{"type":"content_block_start","index":1,"content_block":` + citationOnly + `}`,
					`{"type":"content_block_stop","index":1}`,
				}
			}
			events = append(events, `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`, `{"type":"message_stop"}`)
			model := newLifecycleMessages(t, events)
			response, err := model.Call(t.Context(), documentCitationRequest(t))
			if response != nil || !errors.Is(err, corechat.ErrInvalidResponse) {
				t.Fatalf("response=%#v error=%v; want rejection of unattached citations", response, err)
			}
		})
	}
}

func TestCitationDeltasAttachToTheirOwnBlock(t *testing.T) {
	const textDelta = `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"current"}}`
	const citationDelta = `{"type":"content_block_delta","index":1,"delta":{"type":"citations_delta","citation":{"type":"page_location","document_index":0,"document_title":null,"file_id":null,"cited_text":"quote","start_page_number":1,"end_page_number":2}}}`
	for name, blockEvents := range map[string][]string{
		"before text":      {citationDelta, textDelta},
		"after empty text": {`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":""}}`, citationDelta, textDelta},
		"after text":       {textDelta, citationDelta},
	} {
		t.Run(name, func(t *testing.T) {
			events := []string{
				`{"type":"message_start","message":{"type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"previous"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
			}
			events = append(events, blockEvents...)
			events = append(events, `{"type":"content_block_stop","index":1}`, `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`, `{"type":"message_stop"}`)
			model := newLifecycleMessages(t, events)
			response, err := model.Call(t.Context(), documentCitationRequest(t))
			if err != nil || response == nil || response.Text() != "previouscurrent" {
				t.Fatalf("response=%#v error=%v", response, err)
			}
			parts := response.Output.Message.Parts
			if len(parts) != 2 || parts[0].Text != "previous" || len(parts[0].Citations) != 0 {
				t.Fatalf("parts=%#v, want the previous block as its own uncited part", parts)
			}
			if citations := parts[1].Citations; parts[1].Text != "current" || len(citations) != 1 || citations[0].Quote != "quote" {
				t.Fatalf("cited part=%#v, want the citation on its own block", parts[1])
			}
		})
	}
}

func documentCitationRequest(t *testing.T) *corechat.Request {
	t.Helper()
	first, err := media.NewURI("application/pdf", "https://example.com/first.pdf")
	if err != nil {
		t.Fatal(err)
	}
	second, err := media.NewBytes("application/pdf", []byte("second document"))
	if err != nil {
		t.Fatal(err)
	}
	second.ID = "document-b"
	second.Name = "report"
	third, err := media.NewBytes("application/pdf", []byte("abc"))
	if err != nil {
		t.Fatal(err)
	}
	third.Name = "report"
	image, err := media.NewBytes("image/png", []byte("image"))
	if err != nil {
		t.Fatal(err)
	}
	request, err := corechat.NewRequest(
		corechat.NewUserMessage(corechat.NewMediaPart(first), corechat.NewMediaPart(image)),
		corechat.NewAssistantMessage(corechat.NewToolCallPart(corechat.ToolCall{ID: "lookup", Name: "lookup", Arguments: "{}"})),
		corechat.NewToolMessage(corechat.ToolResult{ID: "lookup", Name: "lookup", Output: toolcontract.Output{Content: []corecontent.Block{{Kind: corecontent.Media, Media: second}}}}),
		corechat.NewUserMessage(corechat.NewMediaPart(third), corechat.NewTextPart("cite the documents")),
	)
	if err != nil {
		t.Fatal(err)
	}
	return request
}
