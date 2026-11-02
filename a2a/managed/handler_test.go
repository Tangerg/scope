package managed_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/Tangerg/scope/a2a"
	"github.com/Tangerg/scope/a2a/managed"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/core/tool"
)

type definition struct{ descriptor run.Descriptor }

func (d definition) Descriptor() run.Descriptor                             { return d.descriptor }
func (d definition) ChildPrograms() []run.Program[run.Payload, run.Payload] { return nil }
func (d definition) Start(p run.Payload) (run.Execution, error) {
	text, err := p.Decode[string]()
	return &execution{Text: text}, err
}
func (d definition) Restore(_ context.Context, s run.ExecutionState) (run.Execution, error) {
	v, err := s.Decode[execution](d.descriptor.Name())
	return &v, err
}

type execution struct {
	Text  string `json:"text"`
	Phase int    `json:"phase"`
}

func (e *execution) Snapshot() (run.ExecutionState, error) {
	return run.EncodeExecutionState("test.remote", e)
}
func (e *execution) Step(_ context.Context, turn run.Turn) (run.Transition, error) {
	switch {
	case e.Text == "ask" && e.Phase == 0:
		schema, err := jsonschema.For[string]()
		if err != nil {
			return run.Transition{}, err
		}
		key, err := run.ParseWaitKey("author")
		if err != nil {
			return run.Transition{}, err
		}
		question, err := run.EncodePayload("Your name?")
		if err != nil {
			return run.Transition{}, err
		}
		e.Phase++
		return run.Yield(0, nil, run.Wait{Key: key, Question: question, AnswerSchema: schema})
	case e.Text == "ask":
		if len(turn.Messages) != 1 {
			return run.Transition{}, errors.New("missing answer")
		}
		answer, ok := turn.Messages[0].(run.Signal)
		if !ok {
			return run.Transition{}, run.ErrInvalidMessage
		}
		return run.Complete(1, answer.Payload)
	case e.Text == "slow" && e.Phase == 0:
		effect, err := run.NewDispatcherEffect(json.RawMessage(`"blocked"`))
		if err != nil {
			return run.Transition{}, err
		}
		e.Phase++
		return run.Yield(0, []run.Effect{effect})
	default:
		payload, err := run.EncodePayload(e.Text)
		if err != nil {
			return run.Transition{}, err
		}
		return run.Complete(uint32(len(turn.Messages)), payload)
	}
}

type dispatch struct {
	calls            atomic.Int64
	entered, release chan struct{}
}

func (d *dispatch) Policy(run.Effect) run.EffectPolicy {
	return run.EffectPolicy{Replay: run.ReplayPolicyNever}
}
func (d *dispatch) Dispatch(ctx context.Context, _ run.EffectRequest, _ run.DeltaEmitter) (run.Settlement, error) {
	d.calls.Add(1)
	close(d.entered)
	<-d.release
	return run.NewSettlement(run.SettlementStatusSucceeded, json.RawMessage(`"returned"`))
}
func program(t *testing.T, compiler *run.Compiler, d *dispatch) run.Program[run.Payload, run.Payload] {
	t.Helper()
	s, err := jsonschema.For[string]()
	if err != nil {
		t.Fatal(err)
	}
	desc, err := run.NewDescriptor(run.DescriptorConfig{Name: "test.remote", Description: "Test execution", InputSchema: s, OutputSchema: s})
	if err != nil {
		t.Fatal(err)
	}
	p, err := run.Bind[string, string](compiler, run.BindingConfig{Definition: definition{desc}, Dispatcher: d, Revision: "test.remote"})
	if err != nil {
		t.Fatal(err)
	}
	return p.Erase()
}

type principalKey struct{}

func server(t *testing.T, r *run.Runtime, p run.Program[run.Payload, run.Payload]) *httptest.Server {
	t.Helper()
	s := httptest.NewUnstartedServer(nil)
	handler, err := managed.NewHTTPHandler(managed.Config{Runtime: r, Program: p, Service: "test", Card: &sdk.AgentCard{Name: "test", SupportedInterfaces: []*sdk.AgentInterface{sdk.NewAgentInterface("http://"+s.Listener.Addr().String()+"/rpc", sdk.TransportProtocolJSONRPC)}, Capabilities: sdk.AgentCapabilities{Streaming: true}},
		Authority: func(ctx context.Context, _ string) (string, error) {
			v, _ := ctx.Value(principalKey{}).(string)
			return v, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	s.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, r.Header.Get("X-Test-Principal"))))
	})
	s.Start()
	t.Cleanup(s.Close)
	return s
}

type envelope struct {
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

func rpc(t *testing.T, s *httptest.Server, scope, method string, params any) envelope {
	t.Helper()
	wire, err := jsonv2.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "id": "request", "params": params})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, s.URL+"/rpc", bytes.NewReader(wire))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Test-Principal", scope)
	resp, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var e envelope
	if err = jsonv2.UnmarshalRead(resp.Body, &e); err != nil {
		t.Fatal(err)
	}
	return e
}
func message(id, text string) map[string]any {
	return map[string]any{"messageId": id, "role": "ROLE_USER", "parts": []any{map[string]string{"text": text}}}
}
func send(t *testing.T, s *httptest.Server, m map[string]any, now bool) *sdk.Task {
	t.Helper()
	e := rpc(t, s, "alice", "SendMessage", map[string]any{"message": m, "configuration": map[string]bool{"returnImmediately": now}})
	if len(e.Error) > 0 {
		t.Fatal(string(e.Error))
	}
	var result struct {
		Task *sdk.Task `json:"task"`
	}
	if err := jsonv2.Unmarshal(e.Result, &result); err != nil || result.Task == nil {
		t.Fatal(string(e.Result), err)
	}
	return result.Task
}
func read(t *testing.T, s *httptest.Server, method string, id sdk.TaskID) *sdk.Task {
	t.Helper()
	e := rpc(t, s, "alice", method, map[string]any{"id": id})
	if len(e.Error) > 0 {
		t.Fatal(string(e.Error))
	}
	var task sdk.Task
	if err := jsonv2.Unmarshal(e.Result, &task); err != nil {
		t.Fatal(err)
	}
	return &task
}
func TestWireTaskReadsAndWaitRepliesDoNotAcquireAnotherWriter(t *testing.T) {
	store := run.NewMemoryStore()
	owner, err := run.New(t.Context(), run.Config{Build: "managed-test", Store: store})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := run.New(t.Context(), run.Config{Build: "managed-test", Store: store})
	if err != nil {
		t.Fatal(err)
	}
	p := program(t, owner.Compiler(), &dispatch{})
	primary := server(t, owner, p)
	replica := server(t, reader, p)
	initial := send(t, primary, message("first", "ask"), false)
	if initial.Status.State != sdk.TaskStateInputRequired || initial.Status.Message == nil {
		t.Fatalf("expected pending child/parent wait: %+v", initial)
	}
	var pending []struct {
		Address a2a.WaitAddress `json:"address"`
	}
	b, err := jsonv2.Marshal(initial.Metadata[managed.PendingKey])
	if err != nil {
		t.Fatal(err)
	}
	if err = jsonv2.Unmarshal(b, &pending); err != nil || len(pending) != 1 {
		t.Fatal(string(b), err)
	}
	same := read(t, replica, "GetTask", initial.ID)
	root, err := run.ParseProcessID(same.Metadata[managed.RootKey].(string))
	if err != nil {
		t.Fatal(err)
	}
	if _, owns := reader.Process(root); owns || same.ID != initial.ID || same.ContextID != initial.ContextID || same.Status.State != sdk.TaskStateInputRequired {
		t.Fatal("GET claimed an existing task")
	}
	if e := rpc(t, replica, "bob", "GetTask", map[string]any{"id": initial.ID}); len(e.Error) == 0 {
		t.Fatal("authority scope widened")
	}
	duplicate := send(t, replica, message("first", "ask"), false)
	if duplicate.ID != initial.ID {
		t.Fatal("duplicate submission created another task")
	}
	reply := message("follow", "Ada")
	reply["taskId"] = initial.ID
	reply["contextId"] = initial.ContextID
	reply["metadata"] = map[string]any{a2a.WaitMetadataKey: pending[0].Address}
	done := send(t, primary, reply, false)
	if done.Status.State != sdk.TaskStateCompleted || len(done.Artifacts) != 1 || done.Artifacts[0].Parts[0].Text() != "Ada" {
		t.Fatal(done)
	}
	if err = owner.ReleaseTree(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	replay := send(t, replica, reply, false)
	if replay.Status.State != sdk.TaskStateCompleted || replay.Artifacts[0].ID != done.Artifacts[0].ID {
		t.Fatal("historical reply did not reconcile")
	}
	reply["parts"] = []any{map[string]any{"text": "Mallory"}}
	if e := rpc(t, replica, "alice", "SendMessage", map[string]any{"message": reply}); len(e.Error) == 0 {
		t.Fatal("conflicting reply accepted")
	}
	for _, r := range []*run.Runtime{reader, owner} {
		if err = r.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
}
func TestWireCancellationIsAnIntentUntilExternalCallReturns(t *testing.T) {
	store := run.NewMemoryStore()
	rt, err := run.New(t.Context(), run.Config{Build: "managed-test", Store: store})
	if err != nil {
		t.Fatal(err)
	}
	d := &dispatch{entered: make(chan struct{}), release: make(chan struct{})}
	s := server(t, rt, program(t, rt.Compiler(), d))
	started := send(t, s, message("slow-job", "slow"), true)
	select {
	case <-d.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("dispatch did not start")
	}
	cancel := read(t, s, "CancelTask", started.ID)
	if cancel.Status.State == sdk.TaskStateCanceled {
		t.Fatal("accepted cancellation fabricated a stopped external operation")
	}
	close(d.release)
	root, _ := run.ParseProcessID(started.Metadata[managed.RootKey].(string))
	h, ok := rt.Process(root)
	if !ok {
		t.Fatal("lost handle")
	}
	if err = h.Join(t.Context()); err != nil {
		t.Fatal(err)
	}
	actual := read(t, s, "GetTask", started.ID)
	if actual.Status.State != sdk.TaskStateCanceled || d.calls.Load() != 1 {
		t.Fatal(actual, d.calls.Load())
	}
	if err = rt.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// The transport consumer goes away after obtaining the first log projection. A new
// HTTP subscription observes the original execution and its final artifact.
func TestWireStreamingReconnectDoesNotReplayAdmission(t *testing.T) {
	store := run.NewMemoryStore()
	rt, err := run.New(t.Context(), run.Config{Build: "managed-test", Store: store})
	if err != nil {
		t.Fatal(err)
	}
	d := &dispatch{entered: make(chan struct{}), release: make(chan struct{})}
	s := server(t, rt, program(t, rt.Compiler(), d))
	open := func(method string, params any) (*http.Response, *bufio.Scanner) {
		b, e := jsonv2.Marshal(map[string]any{"jsonrpc": "2.0", "id": "stream", "method": method, "params": params})
		if e != nil {
			t.Fatal(e)
		}
		req, e := http.NewRequestWithContext(t.Context(), http.MethodPost, s.URL+"/rpc", bytes.NewReader(b))
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("X-Test-Principal", "alice")
		res, e := s.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		return res, bufio.NewScanner(res.Body)
	}
	next := func(scanner *bufio.Scanner) json.RawMessage {
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			var e envelope
			if decodeErr := jsonv2.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &e); decodeErr != nil || len(e.Error) > 0 {
				t.Fatal(line, decodeErr)
			}
			return e.Result
		}
		t.Fatal("missing SSE data:", scanner.Err())
		return nil
	}
	first, frames := open("SendStreamingMessage", map[string]any{"message": message("stream", "slow")})
	var start struct {
		Task *sdk.Task `json:"task"`
	}
	if err = jsonv2.Unmarshal(next(frames), &start); err != nil || start.Task == nil {
		t.Fatal(start, err)
	}
	first.Body.Close()
	<-d.entered
	sub, frames := open("SubscribeToTask", map[string]any{"id": start.Task.ID})
	defer sub.Body.Close()
	var resumed struct {
		Task *sdk.Task `json:"task"`
	}
	if err = jsonv2.Unmarshal(next(frames), &resumed); err != nil || resumed.Task == nil || resumed.Task.ID != start.Task.ID {
		t.Fatal(resumed, err)
	}
	close(d.release)
	artifact, completed := false, false
	for !completed {
		raw := next(frames)
		var e struct {
			Artifact *sdk.TaskArtifactUpdateEvent `json:"artifactUpdate"`
			Status   *sdk.TaskStatusUpdateEvent   `json:"statusUpdate"`
		}
		if err = jsonv2.Unmarshal(raw, &e); err != nil {
			t.Fatal(err)
		}
		if e.Artifact != nil {
			artifact = e.Artifact.TaskID == start.Task.ID && e.Artifact.Artifact.ID == "result"
		}
		if e.Status != nil {
			completed = e.Status.Status.State == sdk.TaskStateCompleted
		}
	}
	if !artifact || d.calls.Load() != 1 {
		t.Fatalf("artifact=%v dispatches=%d", artifact, d.calls.Load())
	}
	// A completed task can still be read, but must not establish a fresh stream.
	final, scanner := open("SubscribeToTask", map[string]any{"id": start.Task.ID})
	defer final.Body.Close()
	var late envelope
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") && !strings.HasPrefix(line, "{") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if err = jsonv2.Unmarshal([]byte(line), &late); err != nil {
			t.Fatalf("terminal subscription: %s (%v)", line, err)
		}
		break
	}
	if len(late.Result) != 0 || !strings.Contains(string(late.Error), sdk.ErrUnsupportedOperation.Error()) {
		t.Fatalf("terminal subscription produced a task or the wrong error: %+v", late)
	}
	if got := read(t, s, "GetTask", start.Task.ID); got.Status.State != sdk.TaskStateCompleted {
		t.Fatalf("GET could not render completed task: %v", got)
	}
	if err = rt.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}
func TestRejectedMediaPrecedesDurableAdmission(t *testing.T) {
	rt, err := run.New(t.Context(), run.Config{Build: "managed-test", Store: run.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	s := server(t, rt, program(t, rt.Compiler(), &dispatch{}))
	m := message("mime", "hello")
	m["parts"] = []any{map[string]any{"url": "https://example.invalid/x", "mediaType": "image/png"}}
	resp := rpc(t, s, "alice", "SendMessage", map[string]any{"message": m})
	if len(resp.Error) == 0 {
		t.Fatal("unsupported image was admitted")
	}
	m = message("mime", "hello")
	e := rpc(t, s, "alice", "SendMessage", map[string]any{"message": m, "configuration": map[string]any{"acceptedOutputModes": []string{"application/json"}}})
	if len(e.Error) == 0 {
		t.Fatal("unavailable output mode was admitted")
	}
	task := send(t, s, message("mime", "different input"), false)
	if task.Status.State != sdk.TaskStateCompleted {
		t.Fatal(task)
	}
	if err = rt.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type signed struct{ base http.RoundTripper }

func (s signed) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header.Set("X-Test-Principal", "alice")
	return s.base.RoundTrip(r)
}
func TestScopeRemoteToolCompletesTheManagedProtocolWait(t *testing.T) {
	rt, err := run.New(t.Context(), run.Config{Build: "managed-test", Store: run.NewMemoryStore()})
	if err != nil {
		t.Fatal(err)
	}
	s := server(t, rt, program(t, rt.Compiler(), &dispatch{}))
	c := *s.Client()
	c.Transport = signed{c.Transport}
	set, err := a2a.OpenToolSet(t.Context(), a2a.Endpoint{CardURL: s.URL, HTTPClient: &c})
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	b, err := tool.Bind(set.Tools()[0])
	if err != nil {
		t.Fatal(err)
	}
	req := tool.Request{Name: b.Contract().Definition().Name, Arguments: `{"message":"ask"}`}
	inv, err := b.Contract().Prepare(req, tool.Admission{})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := b.Call(t.Context(), inv)
	if err != nil || pending.InputRequest == nil {
		t.Fatal(pending, err)
	}
	resume, err := pending.InputRequest.Continuation(json.RawMessage(`"Ada"`))
	if err != nil {
		t.Fatal(err)
	}
	inv, err = b.Contract().Prepare(req, tool.Admission{Continuation: &resume})
	if err != nil {
		t.Fatal(err)
	}
	result, err := b.Call(t.Context(), inv)
	if err != nil {
		t.Fatal(result, err)
	}
	if answer, ok := result.Output.Text(); !ok || answer != "Ada" {
		t.Fatal(result.Output)
	}
	if err = rt.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// lostStartReply persists an actual acceptance, then loses only its acknowledgment.
type lostStartReply struct {
	*run.MemoryStore
	attempts  atomic.Int64
	failReads atomic.Bool
}

func (l *lostStartReply) Append(ctx context.Context, batch run.Batch) error {
	if err := l.MemoryStore.Append(ctx, batch); err != nil {
		return err
	}
	if batch.After == 0 && l.attempts.Add(1) == 1 {
		return errors.New("private-backend-diagnostic")
	}
	return nil
}

func (l *lostStartReply) Load(ctx context.Context, root run.ProcessID) (run.Log, bool, error) {
	if l.failReads.Load() {
		return run.Log{}, false, errors.New("private-read-diagnostic")
	}
	return l.MemoryStore.Load(ctx, root)
}

func TestUnknownStartIsNotAClientRejection(t *testing.T) {
	storage := &lostStartReply{MemoryStore: run.NewMemoryStore()}
	rt, err := run.New(t.Context(), run.Config{Build: "managed-test", Store: storage})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := rt.Close(context.Background()); closeErr != nil {
			t.Error(closeErr)
		}
	})
	s := server(t, rt, program(t, rt.Compiler(), &dispatch{}))
	m := message("stable", "ask")
	reply := rpc(t, s, "alice", "SendMessage", map[string]any{"message": m, "configuration": map[string]bool{"returnImmediately": true}})
	var failure struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err = jsonv2.Unmarshal(reply.Error, &failure); err != nil || failure.Code != -32603 || !strings.Contains(failure.Message, "same") || strings.Contains(string(reply.Error), "private-backend-diagnostic") {
		t.Fatalf("uncertain acceptance was not safely classified: %s %v", reply.Error, err)
	}
	task := send(t, s, m, true)
	if task.ID == "" || storage.attempts.Load() != 1 {
		t.Fatalf("retry did not reconcile the original acceptance: %+v commits=%d", task, storage.attempts.Load())
	}
	changed := rpc(t, s, "alice", "SendMessage", map[string]any{"message": message("stable", "different"), "configuration": map[string]bool{"returnImmediately": true}})
	if err = jsonv2.Unmarshal(changed.Error, &failure); err != nil || failure.Code != -32600 || storage.attempts.Load() != 1 {
		t.Fatalf("conflicting intent was not refused: %s %v", changed.Error, err)
	}
	storage.failReads.Store(true)
	unavailable := rpc(t, s, "alice", "GetTask", map[string]any{"id": task.ID})
	if err = jsonv2.Unmarshal(unavailable.Error, &failure); err != nil || failure.Code != -32603 || strings.Contains(string(unavailable.Error), "private-read-diagnostic") {
		t.Fatalf("task read exposed a private storage diagnostic: %s %v", unavailable.Error, err)
	}
}
