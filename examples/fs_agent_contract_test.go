package examples_test

import (
	startrand "crypto/rand"

	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/agent/run"
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
			authority := contractValue(os.OpenRoot(directory))
			t.Cleanup(func() {
				if err := authority.Close(); err != nil {
					t.Error(err)
				}
			})
			backend := contractValue(fs.NewLocalExecutor(authority))
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
			runtime := contractValue(run.New(ctx, run.Config{Store: run.NewMemoryStore(), Build: contractBuild}))
			defer runtime.Close(context.WithoutCancel(ctx))
			tools := contractValue(interaction.NewToolSet(interaction.ToolSetConfig{Tools: []tool.Tool{executable}, Revision: "edit-tool"}))
			var calls atomic.Int32
			root := contractInteraction(runtime.Compiler(), interaction.Config{Name: "contract.edit", Description: "Consume filesystem outcomes.", MaxModelCalls: run.NewQuota(2), Tools: tools}, chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
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
			input := interaction.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("edit"))}}
			process := contractValue(runtime.Start(ctx, root, run.StartRequest[interaction.Input]{ID: run.StartID{Scope: "test", Key: startrand.Text()}, Input: input}))
			if unknown {
				committed := awaitContractRecord(t, ctx, process, func(record run.Record) bool {
					settled, ok := record.(run.EffectSettled)
					return ok && settled.Outcome.Status() == run.SettlementStatusUnknown
				}).Record.(run.EffectSettled)
				if err := process.Cancel(ctx, "retain unknown result until investigated"); err != nil {
					t.Fatal(err)
				}
				view := contractValue(process.Inspect(ctx))
				owner, ok := view.Process(process.ID())
				if !ok || owner.Termination.Valid() || len(owner.PendingEffects) != 1 || calls.Load() != 1 {
					t.Fatal("cancel hid unknown filesystem work")
				}
				settlement := contractValue(interaction.SettleToolResult(root, owner.PendingEffects[0].Request, tool.Result{Output: tool.NewTextOutput("investigation confirmed edit failure")}, interaction.ResultFailed, nil))
				if err := process.ResolveUnknownEffect(ctx, committed.ID, settlement); err != nil {
					t.Fatal(err)
				}
			}
			result := contractValue(process.Await(ctx))
			wantStatus, wantCalls := run.StatusCompleted, int32(2)
			if unknown {
				wantStatus, wantCalls = run.StatusCanceled, 1
			}
			if result.Termination().Status() != wantStatus || calls.Load() != wantCalls {
				t.Fatalf("status=%s calls=%d", result.Termination().Status(), calls.Load())
			}
			view := contractValue(process.Inspect(ctx))
			for _, owner := range view.Processes {
				if len(owner.PendingEffects) != 0 {
					t.Fatal("terminal tree retained unresolved effects")
				}
			}
			if data := contractValue(os.ReadFile(path)); string(data) != "original" {
				t.Fatalf("rejected edit modified file: %q", data)
			}
		})
	}
}
