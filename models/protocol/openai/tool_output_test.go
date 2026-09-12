package openai

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3/responses"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
)

func TestResponsesToolOutputPreservesStructuredAndMultimodalContent(t *testing.T) {
	structured, err := mapResponsesToolOutput(corechat.ToolOutput{Details: json.RawMessage(`{"count":2}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !structured.OfString.Valid() || structured.OfString.Value != `{"count":2}` {
		t.Fatalf("structured output = %#v", structured)
	}

	image, err := media.NewBytes("image/png", []byte("image"))
	if err != nil {
		t.Fatal(err)
	}
	file, err := media.NewReference("application/pdf", "file-id")
	if err != nil {
		t.Fatal(err)
	}
	file.Name = "evidence.pdf"
	mapped, err := mapResponsesToolOutput(corechat.ToolOutput{Content: []corechat.ToolContent{
		{Kind: corechat.PartText, Text: "summary"}, {Kind: corechat.PartMedia, Media: image}, {Kind: corechat.PartMedia, Media: file},
	}})
	if err != nil {
		t.Fatal(err)
	}
	items := mapped.OfResponseFunctionCallOutputItemArray
	if len(items) != 3 || items[0].OfInputText == nil || items[1].OfInputImage == nil || items[2].OfInputFile == nil {
		t.Fatalf("mapped output = %#v", mapped)
	}
	mappedImage := items[1].OfInputImage
	if mappedImage.FileID.Valid() || !mappedImage.ImageURL.Valid() || !strings.HasPrefix(mappedImage.ImageURL.Value, "data:image/png;base64,") {
		t.Fatalf("mapped image = %#v", mappedImage)
	}
	mappedFile := items[2].OfInputFile
	if !mappedFile.FileID.Valid() || mappedFile.FileID.Value != "file-id" || mappedFile.FileURL.Valid() || mappedFile.FileData.Valid() {
		t.Fatalf("mapped file = %#v", mappedFile)
	}
}

func TestOpenAIChatToolOutputRejectsMediaInsteadOfDroppingIt(t *testing.T) {
	image, err := media.NewBytes("image/png", []byte("image"))
	if err != nil {
		t.Fatal(err)
	}
	message := corechat.NewToolMessage(corechat.ToolResult{
		ID: "call", Name: "inspect", Output: corechat.ToolOutput{Content: []corechat.ToolContent{{Kind: corechat.PartMedia, Media: image}}},
	})
	if _, err := mapRequestMessage(message); err == nil || !strings.Contains(err.Error(), "does not support media Tool output") {
		t.Fatalf("mapRequestMessage error = %v", err)
	}
}

func TestResponsesCitationsCoverOfficialAnnotationVariants(t *testing.T) {
	tests := []struct {
		name      string
		data      string
		wantKind  corechat.CitationSourceKind
		wantValue string
	}{
		{name: "URL", data: `{"type":"url_citation","url":"https://example.com/source","title":"Source","start_index":0,"end_index":6}`, wantKind: corechat.CitationSourceURI, wantValue: "https://example.com/source"},
		{name: "file citation", data: `{"type":"file_citation","file_id":"file-1","filename":"paper.pdf","index":0}`, wantKind: corechat.CitationSourceReference, wantValue: "file-1"},
		{name: "container file", data: `{"type":"container_file_citation","container_id":"container-1","file_id":"file-2","filename":"result.txt","start_index":0,"end_index":6}`, wantKind: corechat.CitationSourceReference, wantValue: "file-2"},
		{name: "file path", data: `{"type":"file_path","file_id":"file-3","index":0}`, wantKind: corechat.CitationSourceReference, wantValue: "file-3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var annotation responses.ResponseOutputTextAnnotationUnion
			if unmarshalErr := json.Unmarshal([]byte(tt.data), &annotation); unmarshalErr != nil {
				t.Fatal(unmarshalErr)
			}
			citation, include, err := responsesCitation(annotation)
			assertProjectedCitation(t, citation, include, err, tt.wantKind, tt.wantValue)

			var streamAnnotation responses.ResponseOutputTextAnnotationAddedEventAnnotationUnion
			if unmarshalErr := json.Unmarshal([]byte(tt.data), &streamAnnotation); unmarshalErr != nil {
				t.Fatal(unmarshalErr)
			}
			citation, include, err = responsesStreamCitation(streamAnnotation)
			assertProjectedCitation(t, citation, include, err, tt.wantKind, tt.wantValue)
		})
	}

	if citation, include, err := responsesCitation(responses.ResponseOutputTextAnnotationUnion{}); err != nil || include || citation != (corechat.Citation{}) {
		t.Fatalf("empty response annotation = %#v, %v, %v", citation, include, err)
	}
	if citation, include, err := responsesStreamCitation(responses.ResponseOutputTextAnnotationAddedEventAnnotationUnion{}); err != nil || include || citation != (corechat.Citation{}) {
		t.Fatalf("empty stream annotation = %#v, %v, %v", citation, include, err)
	}
}

func assertProjectedCitation(
	t *testing.T,
	citation corechat.Citation,
	include bool,
	err error,
	wantKind corechat.CitationSourceKind,
	wantValue string,
) {
	t.Helper()
	if err != nil || !include {
		t.Fatalf("citation = %#v, %v, %v", citation, include, err)
	}
	if citation.Source.Kind != wantKind || citation.Source.Value != wantValue {
		t.Fatalf("citation source = %#v", citation.Source)
	}
	if err := citation.Validate(); err != nil {
		t.Fatalf("projected citation is invalid: %v", err)
	}
}
