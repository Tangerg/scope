package ollama_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/models/ollama"
)

type keepAliveTransport struct {
	calls int
	wire  json.RawMessage
}

func (k *keepAliveTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	k.calls++
	defer request.Body.Close()
	var payload struct {
		KeepAlive json.RawMessage `json:"keep_alive"`
	}
	if err := jsonv2.UnmarshalRead(request.Body, &payload); err != nil {
		return nil, err
	}
	k.wire = payload.KeepAlive
	var response string
	switch request.URL.Path {
	case "/api/chat":
		response = `{"model":"test-model","message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"stop"}`
	case "/api/embed":
		response = `{"model":"test-model","embeddings":[[0.5,1.0]]}`
	default:
		return nil, fmt.Errorf("unexpected path %q", request.URL.Path)
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(response)), Request: request}, nil
}

func TestKeepAlivePreservesFiniteAndIndefiniteDurations(t *testing.T) {
	for _, endpoint := range []string{"chat", "embedding"} {
		for _, defaults := range []bool{false, true} {
			for _, test := range []struct {
				name, value, wire string
				invalid           bool
			}{
				{name: "seconds", value: `300`, wire: `"5m0s"`},
				{name: "minutes", value: `"5m"`, wire: `"5m0s"`},
				{name: "scientific notation", value: `3e2`, wire: `"5m0s"`},
				{name: "fractional seconds", value: `0.5`, wire: `"500ms"`},
				{name: "subnanosecond", value: `0.0000000009`, wire: `"0s"`},
				{name: "zero", value: `0`, wire: `"0s"`},
				{name: "null", value: `null`},
				{name: "negative seconds", value: `-1`, wire: `-1`},
				{name: "negative underflow", value: `-1e-10000`, wire: `-1`},
				{name: "positive underflow", value: `1e-10000`, wire: `"0s"`},
				{name: "negative zero exponent", value: `-0e-10000`, wire: `"0s"`},
				{name: "negative zero", value: `-0`, wire: `"0s"`},
				{name: "negative duration", value: `"-1m"`, wire: `-1`},
				{name: "maximum seconds", value: `9223372036.854775807`, wire: `"2562047h47m16.854775807s"`},
				{name: "maximum duration", value: `"9223372036.854775807s"`, wire: `"2562047h47m16.854775807s"`},
				{name: "maximum fractional nanoseconds", value: `9223372036.8547758079`, wire: `"2562047h47m16.854775807s"`},
				{name: "maximum fractional nanoseconds string", value: `"9223372036.8547758079s"`, wire: `"2562047h47m16.854775807s"`},
				{name: "nanosecond below maximum", value: `9223372036.854775806`, wire: `"2562047h47m16.854775806s"`},
				{name: "nanosecond above maximum", value: `9223372036.854775808`, invalid: true},
				{name: "nanosecond above maximum string", value: `"9223372036.854775808s"`, invalid: true},
				{name: "overflow seconds", value: `10000000000`, invalid: true},
				{name: "overflow duration", value: `"10000000000s"`, invalid: true},
			} {
				t.Run(fmt.Sprintf("%s/%s/defaults=%t", endpoint, test.name, defaults), func(t *testing.T) {
					transport := new(keepAliveTransport)
					client := &http.Client{Transport: transport}
					extension := json.RawMessage(`{"keep_alive":` + test.value + `}`)
					var callErr error
					switch endpoint {
					case "chat":
						options := chat.Options{Model: "test-model"}
						request, err := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("hello")))
						if err != nil {
							t.Fatal(err)
						}
						target := &request.Options
						if defaults {
							target = &options
						}
						if setErr := target.Extensions.Set(ollama.RequestExtensionKey, extension); setErr != nil {
							t.Fatal(setErr)
						}
						model, err := ollama.NewChat(t.Context(), ollama.ChatConfig{DefaultOptions: options, HTTPClient: client})
						if err != nil {
							t.Fatal(err)
						}
						_, callErr = model.Call(t.Context(), request)
					case "embedding":
						options := embedding.Options{Model: "test-model"}
						request, err := embedding.NewRequest([]string{"hello"})
						if err != nil {
							t.Fatal(err)
						}
						target := &request.Options
						if defaults {
							target = &options
						}
						if setErr := target.Extensions.Set(ollama.EmbeddingRequestExtensionKey, extension); setErr != nil {
							t.Fatal(setErr)
						}
						model, err := ollama.NewEmbeddingModel(t.Context(), ollama.EmbeddingModelConfig{DefaultOptions: options, HTTPClient: client})
						if err != nil {
							t.Fatal(err)
						}
						_, callErr = model.Call(t.Context(), request)
					}
					if test.invalid {
						if transport.calls != 0 || invocation.ClassifyFailure(callErr) != invocation.FailureNotExecuted {
							t.Fatalf("wire=%s calls=%d error=%v", transport.wire, transport.calls, callErr)
						}
						return
					}
					if callErr != nil || transport.calls != 1 || string(transport.wire) != test.wire {
						t.Fatalf("wire=%s want=%s calls=%d error=%v", transport.wire, test.wire, transport.calls, callErr)
					}
				})
			}
		}
	}
}
