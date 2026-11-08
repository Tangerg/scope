package managed_test

import (
	"context"
	jsonv2 "encoding/json/v2"
	"net/http/httptest"
	"slices"
	"testing"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/Tangerg/scope/a2a/managed"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

func TestOutputEncodingIsIndependentOfEquivalentSchemas(t *testing.T) {
	for _, schema := range []string{
		`true`,
		`{}`,
		`{"type":"string"}`,
		`{"type":["string"]}`,
		`{"allOf":[{"type":"string"}]}`,
		`{"$ref":"#/$defs/text","$defs":{"text":{"type":"string"}}}`,
	} {
		for _, mode := range []string{"", "text/plain"} {
			t.Run(schema+"/"+mode, func(t *testing.T) {
				runtime, err := run.New(t.Context(), run.Config{Build: "output-policy", Store: run.NewMemoryStore()})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if closeErr := runtime.Close(context.Background()); closeErr != nil {
						t.Error(closeErr)
					}
				})
				input, err := jsonschema.For[string]()
				if err != nil {
					t.Fatal(err)
				}
				output, err := jsonschema.Parse([]byte(schema))
				if err != nil {
					t.Fatal(err)
				}
				descriptor, err := run.NewDescriptor(run.DescriptorConfig{Name: "test.remote", Description: "Test schema-independent encoding", InputSchema: input, OutputSchema: output})
				if err != nil {
					t.Fatal(err)
				}
				program, err := run.Bind[run.Payload, run.Payload](runtime.Compiler(), run.BindingConfig{Definition: definition{descriptor}, Dispatcher: &dispatch{}, Revision: "output-policy"})
				if err != nil {
					t.Fatal(err)
				}
				server := httptest.NewUnstartedServer(nil)
				card := sdk.AgentCard{Name: "output-policy", SupportedInterfaces: []*sdk.AgentInterface{sdk.NewAgentInterface("http://"+server.Listener.Addr().String()+"/rpc", sdk.TransportProtocolJSONRPC)}}
				wantMode, wantText := "application/json", `"hello"`
				if mode != "" {
					card.DefaultOutputModes = []string{mode}
					wantMode, wantText = mode, "hello"
				}
				server.Config.Handler, err = managed.NewHTTPHandler(managed.Config{
					Runtime: runtime, Program: program, Service: "output-policy", Card: &card,
					Authority: func(context.Context, string) (string, error) { return "public", nil },
				})
				if err != nil {
					server.Close()
					t.Fatal(err)
				}
				server.Start()
				t.Cleanup(server.Close)
				if len(card.DefaultOutputModes) > 0 {
					card.DefaultOutputModes[0] = "image/png"
				}
				response, err := server.Client().Get(server.URL + a2asrv.WellKnownAgentCardPath)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				var served sdk.AgentCard
				if err := jsonv2.UnmarshalRead(response.Body, &served); err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(served.DefaultOutputModes, []string{wantMode}) {
					t.Fatalf("served modes=%v, want %s", served.DefaultOutputModes, wantMode)
				}
				reply := rpc(t, server, "alice", "SendMessage", map[string]any{
					"message":       message("accepted", "hello"),
					"configuration": map[string]any{"acceptedOutputModes": []string{wantMode}},
				})
				var result struct {
					Task *sdk.Task `json:"task"`
				}
				if len(reply.Error) != 0 {
					t.Fatal(string(reply.Error))
				}
				if err := jsonv2.Unmarshal(reply.Result, &result); err != nil || result.Task == nil {
					t.Fatalf("result=%s error=%v", reply.Result, err)
				}
				task := result.Task
				if task.Status.State != sdk.TaskStateCompleted || len(task.Artifacts) != 1 || len(task.Artifacts[0].Parts) != 1 {
					t.Fatalf("task=%+v", task)
				}
				part := task.Artifacts[0].Parts[0]
				if part.MediaType != wantMode || part.Text() != wantText {
					t.Fatalf("part=%+v, want %s %q", part, wantMode, wantText)
				}
			})
		}
	}
}
