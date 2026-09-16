package examples_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/tools/fs"
)

type uncertainEditor struct{}

func (uncertainEditor) Edit(context.Context, fs.EditRequest) (fs.EditResponse, error) {
	return fs.EditResponse{}, errors.New("backend response lost after execution")
}

func TestFilesystemEditOutcomeThroughInteraction(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(fmt.Sprint(unknown), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			directory := t.TempDir()
			path := filepath.Join(directory, "file")
			if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			backend := contractValue(fs.NewLocalExecutor(directory))
			t.Cleanup(func() {
				if err := backend.Close(); err != nil {
					t.Error(err)
				}
			})
			var editor fs.Editor = backend
			if unknown {
				editor = uncertainEditor{}
			}
			executable := contractValue(fs.NewEditTool(editor))
			tools := contractValue(interaction.NewToolSet(interaction.ToolSetConfig{
				Name: "contract.tools", Description: "Exercise the filesystem tool boundary.", Tools: []tool.Tool{executable},
				ImplementationDigest: agent.ComputeDigest([]byte("edit-tool")), ConfigurationDigest: agent.ComputeDigest([]byte("edit-config")),
			}))
			var calls atomic.Int32
			root := contractInteraction(interaction.DefinitionConfig{Name: "contract.edit", Description: "Consume filesystem outcomes.", MaxModelCalls: 2, Tools: tools, ToolBudget: agent.Budget{Steps: 16, Effects: 8, Signals: 16}}, chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
				if calls.Add(1) == 1 {
					return contractToolCall("edit", `{"path":"file","old_string":"missing","new_string":"new"}`), nil
				}
				result := request.Messages[len(request.Messages)-1].Parts[0].ToolResult
				if result == nil || !result.IsError {
					return nil, errors.New("edit rejection was not model-visible")
				}
				text, _ := result.Output.Text()
				if !strings.Contains(text, "old_string not found") {
					return nil, errors.New("edit rejection lost its diagnostic")
				}
				return contractText("repair the arguments"), nil
			}))
			resolver := contractResolver{tools.Deployment().DeploymentRef(): tools.Deployment()}
			events := &agenttest.ObservationRecorder{}
			engine := contractValue(agent.NewEngine(agent.EngineConfig{DeploymentResolver: resolver, EventListeners: []agent.EventListener{events}}))
			defer engine.Close(context.WithoutCancel(ctx))
			input := contractValue(agent.EncodePayload(interaction.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("edit"))}}))
			process := contractValue(engine.Start(ctx, root, input))
			defer process.Kill(context.WithoutCancel(ctx), "test cleanup")
			if unknown {
				if _, err := events.AwaitEvent(ctx, func(event agent.Event) bool {
					fact, ok := event.EffectFinished()
					return ok && fact.SettlementStatus() == agent.SettlementStatusUnknown
				}); err != nil {
					t.Fatal(err)
				}
				if err := process.RequestCancellation(ctx, "retain unknown result"); err != nil {
					t.Fatal(err)
				}
			}
			result := contractValue(process.Await(ctx))
			if err := process.Join(ctx); err != nil {
				t.Fatal(err)
			}
			wantStatus, wantCalls, wantUnknown := agent.StatusCompleted, int32(2), 0
			if unknown {
				wantStatus, wantCalls, wantUnknown = agent.StatusCanceled, 1, 1
			}
			if result.Status() != wantStatus || calls.Load() != wantCalls {
				t.Fatalf("status=%s calls=%d", result.Status(), calls.Load())
			}
			snapshot := contractValue(engine.CaptureTree(ctx, process.ID()))
			count := 0
			for _, child := range snapshot.ProcessSnapshots() {
				count += len(child.UnknownEffectIDs())
			}
			if count != wantUnknown {
				t.Fatalf("unknown effects=%d, want %d", count, wantUnknown)
			}
			if data := contractValue(os.ReadFile(path)); string(data) != "original" {
				t.Fatalf("rejected edit modified file: %q", data)
			}
		})
	}
}
