package protocol

import (
	"fmt"
	"strings"
	"testing"

	"google.golang.org/genai"
)

func TestGenerationDecoderRejectsEveryConflictingPartPayload(t *testing.T) {
	payloads := []string{
		`"text":""`,
		`"inlineData":{"mimeType":"image/png","data":"aW1hZ2U="}`,
		`"fileData":{"mimeType":"image/png","fileUri":"gs://bucket/image"}`,
		`"functionCall":{"name":"lookup"}`,
		`"functionResponse":{"name":"lookup"}`,
		`"toolCall":{"id":"server"}`,
		`"toolResponse":{"id":"server"}`,
		`"executableCode":{"code":"pass"}`,
		`"codeExecutionResult":{"output":"result"}`,
	}
	for _, backend := range []genai.Backend{genai.BackendGeminiAPI, genai.BackendVertexAI} {
		client := &GenerationClient{config: genai.ClientConfig{Backend: backend}}
		for left, first := range payloads {
			for right := left + 1; right < len(payloads); right++ {
				t.Run(fmt.Sprintf("backend=%d/%d+%d", backend, left, right), func(t *testing.T) {
					frame := `{"candidates":[{"content":{"parts":[{` + first + "," + payloads[right] + `}]},"finishReason":"STOP"}]}`
					response, err := client.decode([]byte(frame), nil)
					if response != nil || err == nil || !strings.Contains(err.Error(), "multiple data payloads") {
						t.Fatalf("conflicting Part accepted: response=%#v error=%v", response, err)
					}
				})
			}
		}
	}
}

func TestGenerationPartMetadataIsOrthogonalToData(t *testing.T) {
	client := &GenerationClient{config: genai.ClientConfig{Backend: genai.BackendGeminiAPI}}
	for _, part := range []string{
		`{"text":"visible","thought":true,"thoughtSignature":"c2lnbmVk","speechMetadata":{"speaker":"A"},"partMetadata":{"id":9007199254740993}}`,
		`{"thoughtSignature":"c2lnbmVk"}`,
		`{"toolCall":{},"thought":true,"thoughtSignature":"c2lnbmVk"}`,
		`{"text":null,"inlineData":{"mimeType":"image/png","data":"aW1hZ2U="}}`,
	} {
		t.Run(part, func(t *testing.T) {
			frame := `{"candidates":[{"content":{"parts":[` + part + `]},"finishReason":"STOP"}]}`
			if _, err := client.decode([]byte(frame), nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}
