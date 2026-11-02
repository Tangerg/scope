package managed_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tangerg/go-sdk/jsonrpc"
	sdk "github.com/Tangerg/go-sdk/mcp"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/workflow"
	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/core/tool"
	scopemcp "github.com/Tangerg/scope/mcp"
	"github.com/Tangerg/scope/mcp/managed"
)

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

type request struct {
	Order string `json:"order"`
}
type decision struct {
	City string `json:"city"`
}
type definition struct {
	descriptor run.Descriptor
	wait       run.ExternalWaitSpec
	starts     *atomic.Int64
}

func (d definition) Descriptor() run.Descriptor                             { return d.descriptor }
func (d definition) ChildPrograms() []run.Program[run.Payload, run.Payload] { return nil }
func (d definition) Start(in run.Payload) (run.Execution, error) {
	d.starts.Add(1)
	return &execution{wait: d.wait}, nil
}
func (d definition) Restore(_ context.Context, s run.ExecutionState) (run.Execution, error) {
	v, err := s.Decode[state]("test.booking")
	return &execution{wait: d.wait, state: v}, err
}

type state struct {
	Phase int        `json:"phase"`
	Wait  run.WaitID `json:"wait,omitzero"`
}
type execution struct {
	wait  run.ExternalWaitSpec
	state state
}

func (e *execution) Snapshot() (run.ExecutionState, error) {
	return run.EncodeExecutionState("test.booking", e.state)
}
func (e *execution) Step(_ context.Context, signals []run.Signal) (run.Transition, error) {
	switch e.state.Phase {
	case 0:
		e.state.Phase++
		effect, err := run.NewWaitEffect(e.wait)
		if err != nil {
			return run.Transition{}, err
		}
		return run.Continue(0, effect)
	case 1:
		if len(signals) != 1 {
			return run.Transition{}, run.ErrInvalidSignal
		}
		opening, err := signals[0].Frame[run.WaitOpened]()
		if err != nil {
			return run.Transition{}, err
		}
		key := opening.WaitID()
		e.state = state{Phase: 2, Wait: key}
		return run.Wait(1, key)
	case 2:
		if len(signals) != 1 {
			return run.Transition{}, run.ErrInvalidSignal
		}
		id, ok := signals[0].WaitID()
		if !ok || id != e.state.Wait {
			return run.Transition{}, run.ErrInvalidSignal
		}
		v, err := run.ParsePayload(signals[0].Payload())
		if err != nil {
			return run.Transition{}, err
		}
		e.state.Phase++
		return run.Complete(1, v)
	}
	return run.Transition{}, run.ErrInvalidExecutionState
}
func program(ctx context.Context, starts *atomic.Int64) run.Program[request, decision] {
	input := must(jsonschema.For[request]())
	answer := must(jsonschema.Parse([]byte(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false}`)))
	d := definition{descriptor: must(run.NewDescriptor(run.DescriptorConfig{Name: "test.form", Description: "Select a city.", InputSchema: input, OutputSchema: must(jsonschema.For[decision]())})), wait: must(run.NewExternalWaitSpec(run.ExternalWaitSpecConfig{Key: must(run.ParseWaitKey("city")), Question: json.RawMessage(`"Which city?"`), AnswerSchema: answer})), starts: starts}
	child := must(run.Bind[request, decision](run.BindingConfig{Definition: d, ImplementationDigest: run.ComputeDigest([]byte("test.child")), ConfigurationDigest: run.ComputeDigest([]byte("city"))}))
	stage := must(workflow.Call(workflow.CallConfig{ID: "ask", Program: child.Erase()}))
	return must(workflow.Compile[request, decision](ctx, workflow.Config{Name: "test.booking", Description: "Confirm a booking.", Stages: []workflow.Stage{stage}, BuildIdentity: run.ComputeDigest([]byte("test.workflow"))}))
}

type service struct {
	runtime *run.Runtime
	store   *run.MemoryStore
	server  *sdk.Server
	grants  sync.Map
	starts  atomic.Int64
	cfg     managed.Config[request, decision]
}

func newService(t *testing.T) *service {
	t.Helper()
	storage := run.NewMemoryStore()
	rt := must(run.New(t.Context(), run.Config{Store: storage}))
	s := &service{runtime: rt, store: storage, server: sdk.NewServer(&sdk.Implementation{Name: "managed", Version: "v1"}, nil)}
	t.Cleanup(func() {
		if err := rt.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	s.cfg = managed.Config[request, decision]{Runtime: rt, Program: program(t.Context(), &s.starts), ResumeKey: [32]byte{42}, TokenLifetime: time.Minute,
		Authorize: func(_ context.Context, r *sdk.CallToolRequest) (managed.Grant, error) {
			v, ok := s.grants.Load(r.Session)
			if !ok {
				return managed.Grant{}, errors.New("not authorized")
			}
			return v.(managed.Grant), nil
		},
		Project: func(d decision) (tool.Output, error) { return tool.NewTextOutput(d.City), nil }}
	if err := managed.Register(s.server, s.cfg); err != nil {
		t.Fatal(err)
	}
	return s
}

type identifiedTransport struct {
	sdk.Transport
	id string
}

func (i identifiedTransport) Connect(ctx context.Context) (sdk.Connection, error) {
	conn, err := i.Transport.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return identifiedConnection{Connection: conn, id: i.id}, nil
}

type identifiedConnection struct {
	sdk.Connection
	id string
}

func (i identifiedConnection) SessionID() string { return i.id }
func (s *service) connect(t *testing.T, session string, forms bool, nativeID ...string) *sdk.ClientSession {
	t.Helper()
	st, ct := sdk.NewInMemoryTransports()
	var transport sdk.Transport = st
	if len(nativeID) == 1 {
		transport = identifiedTransport{Transport: st, id: nativeID[0]}
	}
	ss := must(s.server.Connect(t.Context(), transport, nil))
	t.Cleanup(func() { _ = ss.Close() })
	if session != "" {
		s.grants.Store(ss, managed.Grant{StartID: run.StartID{Scope: "tenant:1", Key: "order:1"}, Session: session})
	}
	caps := &sdk.ClientCapabilities{}
	if forms {
		caps.Elicitation = &sdk.ElicitationCapabilities{Form: &sdk.FormElicitationCapabilities{}}
	}
	cl := sdk.NewClient(&sdk.Implementation{Name: "host", Version: "v1"}, &sdk.ClientOptions{Capabilities: caps, MultiRoundTrip: &sdk.MultiRoundTripOptions{Disabled: true}})
	cs := must(cl.Connect(t.Context(), ct, nil))
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}
func call(ctx context.Context, s *sdk.ClientSession, token string, replies sdk.InputResponseMap, order string) (*sdk.CallToolResult, error) {
	return s.CallTool(ctx, &sdk.CallToolParams{Name: "test.booking", Arguments: request{Order: order}, RequestState: token, InputResponses: replies})
}
func answer(action string, fields map[string]any) sdk.InputResponseMap {
	return sdk.InputResponseMap{"answer": &sdk.ElicitResult{Action: action, Content: fields}}
}
func wantCode(t *testing.T, err error, code int64) {
	t.Helper()
	var rpc *jsonrpc.Error
	if !errors.As(err, &rpc) || rpc.Code != code {
		t.Fatalf("expected JSON-RPC %d, got %T: %v", code, err, err)
	}
}
func TestManagedChildFormOverSDK(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
	defer cancel()
	s := newService(t)
	first := s.connect(t, "session:one", true)
	other := s.connect(t, "session:two", true)
	denied := s.connect(t, "", true)
	incapable := s.connect(t, "session:one", false)
	_, err := call(ctx, denied, "", nil, "order")
	wantCode(t, err, managed.CodeUnauthorized)
	_, err = call(ctx, incapable, "", nil, "order")
	wantCode(t, err, managed.CodeUnsupportedForm)
	if s.starts.Load() != 0 {
		t.Fatal("authorization or capability rejection started work")
	}
	pending, err := call(ctx, first, "", nil, "order")
	if err != nil {
		t.Fatal(err)
	}
	form, ok := pending.InputRequests["answer"].(*sdk.ElicitParams)
	if !ok || form.Message != "Which city?" || pending.RequestState == "" {
		t.Fatalf("pending: %#v", pending)
	}
	token := pending.RequestState
	accept := answer("accept", map[string]any{"city": "Paris"})
	for _, check := range []struct {
		cs           *sdk.ClientSession
		token, order string
		reply        sdk.InputResponseMap
		code         int64
	}{
		{other, token, "order", accept, managed.CodeInvalidContinuation}, {first, token + "x", "order", accept, managed.CodeInvalidContinuation},
		{first, token, "different", accept, managed.CodeInvalidContinuation}, {first, "", "order", accept, managed.CodeInvalidContinuation},
		{first, token, "order", nil, jsonrpc.CodeInvalidParams}, {first, token, "order", answer("decline", nil), managed.CodeDeclined}, {first, token, "order", answer("cancel", nil), managed.CodeCanceled},
		{first, token, "order", answer("accept", map[string]any{}), jsonrpc.CodeInvalidParams},
		{first, token, "order", answer("accept", map[string]any{"city": "Paris", "extra": int64(9007199254740993)}), jsonrpc.CodeInvalidParams},
	} {
		_, callErr := call(ctx, check.cs, check.token, check.reply, check.order)
		wantCode(t, callErr, check.code)
	}
	for _, retry := range []bool{false, true} {
		result, callErr := call(ctx, first, token, accept, "order")
		if callErr != nil || result.IsError || result.NeedsInput() || len(result.Content) != 1 || result.Content[0].(*sdk.TextContent).Text != "Paris" {
			t.Fatalf("retry %t: %#v %v", retry, result, callErr)
		}
	}
	_, err = call(ctx, first, token, answer("accept", map[string]any{"city": "Berlin"}), "order")
	wantCode(t, err, jsonrpc.CodeInvalidParams)
	result, err := call(ctx, first, "", nil, "order")
	if err != nil || result.Content[0].(*sdk.TextContent).Text != "Paris" || s.starts.Load() != 1 {
		t.Fatalf("resubmit: %#v %v starts=%d", result, err, s.starts.Load())
	}
}

func TestReadOnlyNodeDoesNotAcquireManagedChild(t *testing.T) {
	s := newService(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	origin := s.connect(t, "shared-host-session", true)
	node := &service{server: sdk.NewServer(&sdk.Implementation{Name: "reader", Version: "v1"}, nil), runtime: must(run.New(ctx, run.Config{Store: s.store})), cfg: s.cfg}
	t.Cleanup(func() {
		if err := node.runtime.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	node.cfg.Runtime = node.runtime
	node.cfg.Authorize = func(_ context.Context, r *sdk.CallToolRequest) (managed.Grant, error) {
		v, ok := node.grants.Load(r.Session)
		if !ok {
			return managed.Grant{}, errors.New("denied")
		}
		return v.(managed.Grant), nil
	}
	if err := managed.Register(node.server, node.cfg); err != nil {
		t.Fatal(err)
	}
	reader := node.connect(t, "shared-host-session", true)
	original, err := call(ctx, origin, "", nil, "order")
	if err != nil {
		t.Fatal(err)
	}
	reply := answer("accept", map[string]any{"city": "Paris"})
	_, err = call(ctx, reader, original.RequestState, reply, "order")
	wantCode(t, err, managed.CodeUnavailable)
	echo, err := call(ctx, reader, "", nil, "order")
	if err != nil || !echo.NeedsInput() {
		t.Fatalf("read-only pending: %v %v", echo, err)
	}
	success, err := call(ctx, origin, echo.RequestState, reply, "order")
	if err != nil || success.Content[0].(*sdk.TextContent).Text != "Paris" {
		t.Fatalf("owner acceptance: %v %v", success, err)
	}
	for _, initial := range []bool{false, true} {
		token := original.RequestState
		replies := reply
		if initial {
			token = ""
			replies = nil
		}
		got, callErr := call(ctx, reader, token, replies, "order")
		if callErr != nil || got.Content[0].(*sdk.TextContent).Text != "Paris" {
			t.Fatalf("read-only terminal: %v %v", got, callErr)
		}
	}
	_, err = call(ctx, reader, original.RequestState, answer("accept", map[string]any{"city": "Rome"}), "order")
	wantCode(t, err, jsonrpc.CodeInvalidParams)
	views := must(node.runtime.StartedTrees())
	head, found, err := views.Load(ctx, run.StartID{Scope: "tenant:1", Key: "order:1"})
	if err != nil || !found {
		t.Fatal(err)
	}
	if _, active := node.runtime.Process(head.RootID()); active || s.starts.Load() != 1 {
		t.Fatal("read-only reconciliation acquired or repeated work")
	}
}
func TestScopeClientConsumesManagedServerForm(t *testing.T) {
	s := newService(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	cs := s.connect(t, "dialog", true)
	tools, err := scopemcp.DiscoverTools(ctx, []scopemcp.ToolSource{{Session: cs}}, scopemcp.ToolDiscoveryConfig{})
	if err != nil || len(tools) != 1 {
		t.Fatalf("discovery: %v %v", tools, err)
	}
	binding := must(tool.Bind(tools[0]))
	request := tool.Request{Name: binding.Contract().Definition().Name, Arguments: `{"order":"order"}`}
	first := must(binding.Call(ctx, must(binding.Contract().Prepare(request, nil))))
	if first.InputRequest == nil {
		t.Fatal("remote form was not a pending tool result")
	}
	response := must(first.InputRequest.Continuation(json.RawMessage(`{"action":"accept","content":{"city":"Paris"}}`)))
	done := must(binding.Call(ctx, must(binding.Contract().Prepare(request, &response))))
	if got, _ := done.Output.Text(); got != "Paris" || s.starts.Load() != 1 {
		t.Fatalf("text=%s starts=%d", got, s.starts.Load())
	}
}
func TestNativeSessionsRemainIndependentOfHostDialog(t *testing.T) {
	s := newService(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	a := s.connect(t, "same-authorized-dialog", true, "native-a")
	b := s.connect(t, "same-authorized-dialog", true, "native-b")
	pending, err := call(ctx, a, "", nil, "order")
	if err != nil {
		t.Fatal(err)
	}
	reply := answer("accept", map[string]any{"city": "Paris"})
	_, err = call(ctx, b, pending.RequestState, reply, "order")
	wantCode(t, err, managed.CodeInvalidContinuation)
	result, err := call(ctx, a, pending.RequestState, reply, "order")
	if err != nil || result.Content[0].(*sdk.TextContent).Text != "Paris" {
		t.Fatalf("native owner: %v %v", result, err)
	}
}

func TestOldMCPWriterDoesNotRepublishReplacedWait(t *testing.T) {
	s := newService(t)
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	client := s.connect(t, "authenticated-dialog", true)
	pending, err := call(ctx, client, "", nil, "order")
	if err != nil || !pending.NeedsInput() {
		t.Fatalf("wait=%+v %v", pending, err)
	}
	views := must(s.runtime.StartedTrees())
	id := run.StartID{Scope: "tenant:1", Key: "order:1"}
	before, found, err := views.Load(ctx, id)
	if err != nil || !found || len(before.Pending()) != 1 {
		t.Fatalf("head=%v %v", found, err)
	}
	// Let the superseded local activation meet its fence and drain before
	// tearing down the original service. No production path takes over implicitly.
	t.Cleanup(func() {
		clean, stop := context.WithTimeout(context.Background(), time.Second)
		defer stop()
		if old, ok := s.runtime.Process(before.RootID()); ok {
			_ = old.Cancel(clean, "finish superseded activation")
			_ = old.Join(clean)
		}
	})
	next := must(run.New(ctx, run.Config{Store: s.store}))
	t.Cleanup(func() { _ = next.Close(context.Background()) })
	// Opening the child form, parking the child, and parking its parent may
	// advance the head concurrently. A rejected CAS installed no activation;
	// retry the deliberate takeover against the new authoritative head.
	var fresh *run.Handle[decision]
	for {
		fresh, err = next.Recover(ctx, s.cfg.Program, before)
		if !errors.Is(err, run.ErrTreeIncarnationConflict) {
			break
		}
		before, found, err = views.Load(ctx, id)
		if err != nil || !found || len(before.Pending()) != 1 {
			t.Fatalf("load after CAS rejection: found=%t err=%v", found, err)
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	if err = fresh.Answer(ctx, before.Pending()[0].WaitID, map[string]string{"city": "Paris"}); err != nil {
		t.Fatal(err)
	}
	if err = fresh.Join(ctx); err != nil {
		t.Fatal(err)
	}
	result, err := call(ctx, client, "", nil, "order")
	if err != nil || result.NeedsInput() || len(result.Content) != 1 || result.Content[0].(*sdk.TextContent).Text != "Paris" {
		t.Fatalf("superseded writer predicted another wait after durable completion: %+v %v", result, err)
	}
	retry, err := call(ctx, client, pending.RequestState, answer("accept", map[string]any{"city": "Paris"}), "order")
	if err != nil || retry.NeedsInput() || len(retry.Content) != 1 || retry.Content[0].(*sdk.TextContent).Text != "Paris" {
		t.Fatalf("accepted answer replay reached stale writer: %+v %v", retry, err)
	}
}
