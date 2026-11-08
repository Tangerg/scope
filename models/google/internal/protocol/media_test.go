package protocol

import (
	"errors"
	"testing"

	"google.golang.org/genai"

	corechat "github.com/Tangerg/scope/core/chat"
	corecontent "github.com/Tangerg/scope/core/content"
	"github.com/Tangerg/scope/core/media"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

func TestUnknownMIMEAdheresToNativeMediaContract(t *testing.T) {
	inline, err := media.NewBytes("", []byte("unknown"))
	if err != nil {
		t.Fatal(err)
	}
	remote, err := media.NewURI("", "https://example.com/source")
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{GoogleProviderID, "vertexai"} {
		for _, value := range []*media.Media{inline, remote} {
			t.Run(provider+"/"+string(value.Source.Kind), func(t *testing.T) {
				wantError := value.Source.Kind == media.SourceBytes || provider != GoogleProviderID
				for _, message := range []corechat.Message{
					corechat.NewUserMessage(corechat.NewMediaPart(value)),
					corechat.NewAssistantMessage(corechat.NewMediaPart(value)),
					corechat.NewToolMessage(corechat.ToolResult{ID: "call", Name: "read", Output: toolcontract.Output{
						Content: []corecontent.Block{{Kind: corecontent.Media, Media: value}},
					}}),
				} {
					wantMessageError := wantError || message.Role == corechat.RoleTool
					_, _, err := mapProtocolMessages(provider, []corechat.Message{message})
					if (err != nil) != wantMessageError {
						t.Errorf("role %s: error=%v, want error=%t", message.Role, err, wantMessageError)
					}
					if wantMessageError && !errors.Is(err, media.ErrInvalidMIME) {
						t.Errorf("role %s: error=%v, want invalid MIME", message.Role, err)
					}
				}
				part := &genai.Part{InlineData: &genai.Blob{Data: []byte("unknown")}}
				if value.Source.Kind == media.SourceURI {
					part = &genai.Part{FileData: &genai.FileData{FileURI: value.Source.URI}}
				}
				_, _, _, err := mapProtocolCandidatePartDelta(provider, 0, part, nil)
				if (err != nil) != wantError {
					t.Errorf("response: error=%v, want error=%t", err, wantError)
				}
			})
		}
	}
}
