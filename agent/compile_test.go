package agent_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func compiledEcho(t *testing.T, calls *atomic.Int32, gate <-chan struct{}) (run.Program[agent.Input, agent.Output], agent.Config) {
	t.Helper()
	cfg := agent.Config{Name: "test.compiled", Description: "Return a deterministic echo from a managed program.",
		BuildIdentity: run.ComputeDigest([]byte("compiled fixture")), ModelIdentity: run.ComputeDigest([]byte("echo settings")),
		Model: chat.ModelFunc(func(ctx context.Context, _ *chat.Request) (*chat.Response, error) {
			calls.Add(1)
			if gate != nil {
				select {
				case <-gate:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			msg := chat.NewAssistantMessage(chat.NewTextPart("hello"))
			return &chat.Response{Output: &chat.Output{Message: &msg, FinishReason: chat.FinishReasonStop}}, nil
		}),
	}
	p, err := agent.Compile(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return p, cfg
}

func TestCompileRunsTypedOutputAndPinsPolicy(t *testing.T) {
	var calls atomic.Int32
	p, cfg := compiledEcho(t, &calls, nil)
	old := p.ProgramRef()
	cfg.MaxModelCalls = run.NewQuota(2)
	changed, err := agent.Compile(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if old == changed.ProgramRef() {
		t.Fatal("changed model-call limit retained the old reference")
	}
	rt, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	reply, err := rt.Run(t.Context(), p, agent.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("question"))}})
	if err != nil {
		t.Fatal(err)
	}
	typed := reply
	if typed.ModelResponse.Text() != "hello" || calls.Load() != 1 {
		t.Fatalf("unexpected output: %+v; calls=%d", typed, calls.Load())
	}
	if err = rt.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestCompilePinsIndependentStreamAdmissionPolicy(t *testing.T) {
	var calls atomic.Int32
	_, config := compiledEcho(t, &calls, nil)
	config.Model = nil
	config.Streamer = responseStream(streamTextChunk("done", chat.FinishReasonStop))
	implicit, err := agent.Compile(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxStreamDeltaBytes = agent.DefaultMaxStreamDeltaBytes
	explicit, err := agent.Compile(t.Context(), config)
	if err != nil || explicit.ProgramRef() != implicit.ProgramRef() {
		t.Fatalf("equivalent stream defaults have different identities: %v", err)
	}
	config.MaxStreamDeltaBytes = 512
	bounded, err := agent.Compile(t.Context(), config)
	if err != nil || bounded.ProgramRef() == implicit.ProgramRef() {
		t.Fatalf("stream admission policy did not change program identity: %v", err)
	}
	config.MaxStreamDeltaBytes = -1
	if program, err := agent.Compile(t.Context(), config); program.Valid() || !errors.Is(err, agent.ErrInvalidDispatcherConfig) {
		t.Fatalf("negative stream admission budget accepted: %v", err)
	}
	config.Streamer = nil
	config.Model = chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
		calls.Add(1)
		return textResponse("done"), nil
	})
	config.MaxStreamDeltaBytes = 512
	if program, err := agent.Compile(t.Context(), config); program.Valid() || !errors.Is(err, agent.ErrInvalidDispatcherConfig) {
		t.Fatalf("aggregate model accepted a stream-only policy: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("configuration validation executed the model")
	}
}

func TestManagedStartSurvivesRequestAndConsumesOneScopedKey(t *testing.T) {
	gate := make(chan struct{})
	var calls atomic.Int32
	p, _ := compiledEcho(t, &calls, gate)
	store := run.NewMemoryStore()
	rt, err := run.New(t.Context(), run.Config{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	req := run.StartRequest[agent.Input]{ID: run.StartID{Scope: "test", Key: "job-42"}, Input: agent.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hi"))}}}
	h, err := rt.Start(ctx, p, req)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	duplicate, err := rt.Start(t.Context(), p, req)
	if err != nil || duplicate.ID() != h.ID() {
		t.Fatalf("local duplicate: %v", err)
	}
	other, err := run.New(t.Context(), run.Config{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	_, err = other.Start(t.Context(), p, req)
	accepted, ok := errors.AsType[*run.StartAcceptedError](err)
	if !ok || accepted.Receipt.RootID != h.ID() || !strings.Contains(accepted.Error(), req.ID.Scope+"/"+req.ID.Key) || !strings.Contains(accepted.Error(), h.ID().String()) {
		t.Fatalf("other writer: %v", err)
	}
	req.Input.Messages = []chat.Message{chat.NewUserMessage(chat.NewTextPart("different"))}
	_, err = rt.Start(t.Context(), p, req)
	if conflict, ok := errors.AsType[*run.StartConflictError](err); !ok || !strings.Contains(conflict.Error(), h.ID().String()) || strings.Contains(conflict.Error(), "different") {
		t.Fatalf("conflicting start: %v", err)
	}
	close(gate)
	result, err := h.Await(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	output, err := result.Output()
	if err != nil || output.ModelResponse.Text() != "hello" {
		t.Fatalf("typed managed output: %v", err)
	}
	if err = h.Join(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("model called %d times", calls.Load())
	}
	if err = rt.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = other.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type unacknowledgedStart struct{ *run.MemoryStore }

func (u unacknowledgedStart) CommitCheckpoint(ctx context.Context, c run.TreeCheckpoint) error {
	if err := u.MemoryStore.CommitCheckpoint(ctx, c); err != nil {
		return err
	}
	if _, ok := c.Start(); ok {
		return errors.New("lost start acknowledgment")
	}
	return nil
}
func TestStartReconcilesLostAcknowledgmentWithoutDispatch(t *testing.T) {
	var calls atomic.Int32
	p, _ := compiledEcho(t, &calls, nil)
	store := run.NewMemoryStore()
	rt, err := run.New(t.Context(), run.Config{Store: unacknowledgedStart{store}})
	if err != nil {
		t.Fatal(err)
	}
	req := run.StartRequest[agent.Input]{ID: run.StartID{Scope: "test", Key: "lost"}, Input: agent.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hi"))}}}
	_, err = rt.Start(t.Context(), p, req)
	initial, ok := errors.AsType[*run.StartError](err)
	if !ok || initial.StartID() != req.ID {
		t.Fatalf("attempt: %v", err)
	}
	receipt, found, err := store.LookupStart(t.Context(), req.ID)
	if err != nil || !found || receipt.RootID != initial.RootID() {
		t.Fatalf("receipt: %+v, %v", receipt, err)
	}
	_, err = rt.Start(t.Context(), p, req)
	if _, ok := errors.AsType[*run.StartAcceptedError](err); !ok || calls.Load() != 0 {
		t.Fatalf("ambiguous start redispatched: %v", err)
	}
	head, found, err := store.LoadTree(t.Context(), receipt.RootID)
	if err != nil || !found {
		t.Fatal("missing initial head", err)
	}
	recovered, err := rt.Recover(t.Context(), p, head)
	if err != nil {
		t.Fatal(err)
	}
	if err = recovered.Join(t.Context()); err != nil {
		t.Fatal(err)
	}
	result, err := recovered.Await(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	output, err := result.Output()
	if err != nil || output.ModelResponse.Text() != "hello" || calls.Load() != 1 {
		t.Fatalf("recovered output: %v", err)
	}
	if err = rt.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type uncertainPreparation struct{ calls *atomic.Int32 }

func (u uncertainPreparation) PrepareContext(_ context.Context, _ agent.ContextInvocation, _ *chat.Request) ([]chat.Message, error) {
	u.calls.Add(1)
	return nil, errors.New("summary service reply lost")
}
func TestCompiledProgramReconcilesPreparationBeforeAdmittingModel(t *testing.T) {
	var modelCalls, preparations atomic.Int32
	_, cfg := compiledEcho(t, &modelCalls, nil)
	cfg.ContextIdentity = run.ComputeDigest([]byte("summary service"))
	cfg.ContextProcessor = uncertainPreparation{&preparations}
	cfg.Model = chat.ModelFunc(func(_ context.Context, req *chat.Request) (*chat.Response, error) {
		modelCalls.Add(1)
		if len(req.Messages) != 1 || req.Messages[0].Text() != "investigated" {
			return nil, errors.New("model received uncommitted context")
		}
		return textResponse("ok"), nil
	})
	p, err := agent.Compile(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	store := &recoveryRequestRecorder{MemoryStore: run.NewMemoryStore(), unknown: make(chan run.EffectRequest, 1)}
	rt, err := run.New(t.Context(), run.Config{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	h, err := rt.Start(t.Context(), p, run.StartRequest[agent.Input]{ID: run.StartID{Scope: "tests", Key: "prepare"}, Input: agent.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("original"))}}})
	if err != nil {
		t.Fatal(err)
	}
	req := <-store.unknown
	if modelCalls.Load() != 0 || preparations.Load() != 1 {
		t.Fatal("model called before context outcome was known")
	}
	if _, err = agent.SettleModelResult(p, req, textResponse("wrong")); err == nil {
		t.Fatal("preparation accepted a model response")
	}
	wrong := cfg
	wrong.ContextIdentity = run.ComputeDigest([]byte("other summary service"))
	other, err := agent.Compile(t.Context(), wrong)
	if err != nil {
		t.Fatal(err)
	}
	messages := []chat.Message{chat.NewUserMessage(chat.NewTextPart("investigated"))}
	if _, err = agent.SettlePreparedContext(other, req, messages); err == nil {
		t.Fatal("accepted a different program identity")
	}
	settlement, err := agent.SettlePreparedContext(p, req, messages)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.ResolveUnknownEffect(t.Context(), req.ID(), settlement); err != nil {
		t.Fatal(err)
	}
	if err = h.Join(t.Context()); err != nil {
		t.Fatal(err)
	}
	result, err := h.Await(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	output, err := result.Output()
	if err != nil || output.ModelResponse.Text() != "ok" || modelCalls.Load() != 1 || preparations.Load() != 1 {
		t.Fatalf("reconciliation replayed work: %v", err)
	}
	if err = rt.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestCompiledBindingsRequireDeclaredOpaqueIdentitiesWithoutCallingCollaborators(t *testing.T) {
	var modelCalls, toolCalls atomic.Int32
	_, base := compiledEcho(t, &modelCalls, nil)
	echo, err := tool.NewFunc(tool.FuncConfig{Name: "echo", Description: "Return the supplied arguments."}, func(_ context.Context, arg struct {
		Text string `json:"text"`
	}) (string, error) {
		toolCalls.Add(1)
		return arg.Text, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"no build", "no model revision", "unidentified context", "context identity without callback", "invalid definition", "missing model"} {
		t.Run(mode, func(t *testing.T) {
			cfg := base
			switch mode {
			case "no build":
				cfg.BuildIdentity = run.Digest{}
			case "no model revision":
				cfg.ModelIdentity = run.Digest{}
			case "unidentified context":
				cfg.ContextProcessor = uncertainPreparation{&toolCalls}
			case "context identity without callback":
				cfg.ContextIdentity = run.ComputeDigest([]byte("unbound"))
			case "invalid definition":
				cfg.Name = ""
			case "missing model":
				cfg.Model = nil
			}
			p, compileErr := agent.Compile(t.Context(), cfg)
			if compileErr == nil || p.Valid() {
				t.Fatalf("invalid binding admitted: %v", p.ProgramRef())
			}
		})
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if p, compileErr := agent.Compile(canceled, base); p.Valid() || !errors.Is(compileErr, context.Canceled) {
		t.Fatal(p, compileErr)
	}
	tools := agent.ToolSetConfig{
		Name: "test.tools", Description: "Exercise bound tool identity.", Tools: []tool.Tool{echo},
		ImplementationDigest: run.ComputeDigest([]byte("tool implementation")),
	}
	if set, setErr := agent.NewToolSet(tools); !errors.Is(setErr, agent.ErrInvalidToolSet) || set.Configured() {
		t.Fatalf("missing tool configuration digest: %v", setErr)
	}
	cfg := base
	tools.ConfigurationDigest = run.ComputeDigest([]byte("echo settings:v1"))
	cfg.Tools, err = agent.NewToolSet(tools)
	if err != nil {
		t.Fatal(err)
	}
	v1, err := agent.Compile(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	tools.ConfigurationDigest = run.ComputeDigest([]byte("echo settings:v2"))
	cfg.Tools, err = agent.NewToolSet(tools)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := agent.Compile(t.Context(), cfg)
	if err != nil || v1.ProgramRef() == v2.ProgramRef() || toolCalls.Load() != 0 || modelCalls.Load() != 0 {
		t.Fatalf("refs=%v %v calls=%d,%d err=%v", v1.ProgramRef(), v2.ProgramRef(), toolCalls.Load(), modelCalls.Load(), err)
	}
}
func TestLocalContextFailureCannotBecomeAnExternalSideEffect(t *testing.T) {
	for _, mode := range []string{"error", "nothing", "replacement"} {
		t.Run(mode, func(t *testing.T) {
			var calls, prepares atomic.Int32
			_, cfg := compiledEcho(t, &calls, nil)
			cfg.ContextIdentity = run.ComputeDigest([]byte("bounded local policy"))
			cfg.ContextPolicy = func(_ context.Context, _ agent.Input) ([]chat.Message, error) {
				prepares.Add(1)
				switch mode {
				case "error":
					return nil, errors.New("policy declined")
				case "nothing":
					return nil, nil
				}
				return []chat.Message{chat.NewUserMessage(chat.NewTextPart("local replacement"))}, nil
			}
			cfg.Model = chat.ModelFunc(func(_ context.Context, r *chat.Request) (*chat.Response, error) {
				calls.Add(1)
				if len(r.Messages) != 1 || r.Messages[0].Text() != "local replacement" {
					return nil, errors.New("unprepared input")
				}
				return textResponse("ok"), nil
			})
			p, err := agent.Compile(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			rt, err := run.New(t.Context(), run.Config{Store: run.NewMemoryStore()})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if e := rt.Close(context.WithoutCancel(t.Context())); e != nil {
					t.Error(e)
				}
			}()
			input := agent.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("original"))}}
			h, err := rt.Start(t.Context(), p, run.StartRequest[agent.Input]{ID: run.StartID{Scope: "tests", Key: "context"}, Input: input})
			if err != nil {
				t.Fatal(err)
			}
			result, err := h.Await(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if err = h.Join(t.Context()); err != nil {
				t.Fatal(err)
			}
			if mode == "replacement" {
				out, err := result.Output()
				if err != nil || out.ModelResponse.Text() != "ok" || calls.Load() != 1 {
					t.Fatal(out, err)
				}
			} else if result.Termination().Status() != run.StatusFailed || calls.Load() != 0 {
				t.Fatalf("published %v model calls=%d", result.Termination().Status(), calls.Load())
			}
			if prepares.Load() != 1 || input.Messages[0].Text() != "original" {
				t.Fatal("mutated or reran local context policy")
			}
		})
	}
}

func TestUnresolvedContextReceiptIsNeverRetriedByCancellation(t *testing.T) {
	var models, preparations atomic.Int32
	_, cfg := compiledEcho(t, &models, nil)
	cfg.ContextProcessor = uncertainPreparation{&preparations}
	cfg.ContextIdentity = run.ComputeDigest([]byte("summary:v1"))
	p, err := agent.Compile(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	store := &recoveryRequestRecorder{MemoryStore: run.NewMemoryStore(), unknown: make(chan run.EffectRequest, 1)}
	rt, err := run.New(t.Context(), run.Config{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if e := rt.Close(context.WithoutCancel(t.Context())); e != nil {
			t.Error(e)
		}
	}()
	h, err := rt.Start(t.Context(), p, run.StartRequest[agent.Input]{ID: run.StartID{Scope: "research", Key: "unavailable"}, Input: agent.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("source"))}}})
	if err != nil {
		t.Fatal(err)
	}
	req := <-store.unknown
	// Neither an empty evidence payload nor a fabricated model result resolves
	// an external context operation. The Host decides to stop instead of retrying it.
	if proposed, e := agent.SettlePreparedContext(p, req, nil); e == nil || proposed.Valid() {
		t.Fatalf("empty investigation result %v %v", proposed, e)
	}
	if proposed, e := agent.SettleModelResult(p, run.EffectRequest{}, textResponse("fabricated")); e == nil || proposed.Valid() {
		t.Fatalf("forged evidence %v %v", proposed, e)
	}
	if err = h.Cancel(t.Context(), "receipt service unavailable"); err != nil {
		t.Fatal(err)
	}
	result, err := h.Await(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = h.Join(t.Context()); err != nil {
		t.Fatal(err)
	}
	unknown := result.Termination().UnresolvedEffectIDs()
	if result.Termination().Status() != run.StatusCanceled || len(unknown) != 1 || unknown[0] != req.ID() || preparations.Load() != 1 || models.Load() != 0 {
		t.Fatalf("status=%v unknown=%v prepared=%d model=%d", result.Termination().Status(), unknown, preparations.Load(), models.Load())
	}
}
