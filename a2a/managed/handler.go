package managed

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"slices"
	"strings"
	"time"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/Tangerg/scope/a2a"
	"github.com/Tangerg/scope/agent/run"
)

const (

	// PendingKey contains all unresolved questions, including questions on children.
	PendingKey = "scope.pending"
	ProgramKey = "scope.program"
	RootKey    = "scope.root_id"
)

type Config struct {
	Runtime *run.Runtime
	// Program fixes the binding used for new submissions. Bind an existing
	// typed program with Program.Erase; its actual input/output schemas remain.
	Program run.Program[run.Payload, run.Payload]
	Card    *sdk.AgentCard
	// Authority returns a stable server-authorized scope for this authenticated
	// caller and requested tenant. Never return the untrusted tenant verbatim.
	Authority func(ctx context.Context, tenant string) (string, error)
	// Service isolates independently deployed endpoints sharing a runtime store.
	Service string
	// ViewBuffer bounds a live subscription. Zero uses 32 entries.
	ViewBuffer int
}
type handler struct {
	runtime         *run.Runtime
	program         run.Program[run.Payload, run.Payload]
	heads           *run.StartedTrees
	authority       func(context.Context, string) (string, error)
	service         string
	streaming       bool
	capacity        int
	inputs, outputs []string
}

var _ a2asrv.RequestHandler = (*handler)(nil)

// NewHTTPHandler serves actual A2A JSON-RPC methods. It requires a readable
// runtime store before admitting any task. A nil or unsupported request is an
// explicit protocol error; this handler never creates an independent task store.
func NewHTTPHandler(cfg Config) (http.Handler, error) {
	if cfg.Card == nil || cfg.Runtime == nil || !cfg.Program.Valid() || cfg.Authority == nil || strings.TrimSpace(cfg.Service) == "" || cfg.ViewBuffer < 0 {
		return nil, errors.New("a2a/managed: runtime, program, card, service and authority are required")
	}
	heads, err := cfg.Runtime.StartedTrees()
	if err != nil {
		return nil, err
	}
	cap := cfg.ViewBuffer
	if cap == 0 {
		cap = 32
	}
	card := *cfg.Card
	card.Capabilities.Extensions = slices.Clone(card.Capabilities.Extensions)
	for _, extension := range card.Capabilities.Extensions {
		if extension.Required {
			return nil, fmt.Errorf("%w: managed handler cannot claim arbitrary required extensions", a2a.ErrInvalidCard)
		}
		if extension.URI == a2a.ExternalWaitExtension {
			return nil, fmt.Errorf("%w: external-wait capability is supplied by the binding", a2a.ErrInvalidCard)
		}
	}
	card.Capabilities.Extensions = append(card.Capabilities.Extensions, sdk.AgentExtension{URI: a2a.ExternalWaitExtension, Description: "Select a pending wait by scope.wait_id and answer its declared schema."})
	if len(card.DefaultInputModes) == 0 {
		card.DefaultInputModes = []string{"text/plain", "application/json"}
	}
	allowed := []string{"text/plain", "application/json"}
	for _, mode := range card.DefaultInputModes {
		if !slices.Contains(allowed, mode) {
			return nil, sdk.ErrUnsupportedContentType
		}
	}
	// A known string is projected as text; other concrete JSON kinds use an
	// exact JSON part. An unconstrained union requires both output modes.
	var schema struct {
		Type json.RawMessage `json:"type"`
	}
	if err := jsonv2.Unmarshal(cfg.Program.Descriptor().OutputSchema().JSON(), &schema); err != nil {
		return nil, err
	}
	outputs := allowed
	switch string(schema.Type) {
	case `"string"`:
		outputs = []string{"text/plain"}
	case `"object"`, `"array"`, `"integer"`, `"number"`, `"boolean"`, `"null"`:
		outputs = []string{"application/json"}
	}
	if len(card.DefaultOutputModes) > 0 && !sameModes(outputs, card.DefaultOutputModes) {
		return nil, sdk.ErrUnsupportedContentType
	}
	card.DefaultOutputModes = slices.Clone(outputs)
	for _, skill := range card.Skills {
		if len(skill.InputModes) > 0 && !sameModes(skill.InputModes, card.DefaultInputModes) || len(skill.OutputModes) > 0 && !sameModes(skill.OutputModes, outputs) {
			return nil, sdk.ErrUnsupportedContentType
		}
	}
	h := &handler{runtime: cfg.Runtime, program: cfg.Program, heads: heads, authority: cfg.Authority, service: cfg.Service, streaming: card.Capabilities.Streaming, capacity: cap, inputs: slices.Clone(card.DefaultInputModes), outputs: outputs}
	return a2a.NewProtocolHTTPHandler(&card, h)
}

type taskKey struct {
	id      sdk.TaskID
	context string
	start   run.StartID
}

func digest(parts ...string) string {
	b, _ := jsonv2.Marshal(parts)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func (h *handler) scope(ctx context.Context, tenant string) (string, error) {
	authority, err := h.authority(ctx, tenant)
	if err != nil || strings.TrimSpace(authority) == "" {
		return "", sdk.ErrTaskNotFound
	}
	return "a2a:" + digest(h.service, authority), nil
}
func newKey(scope string, m *sdk.Message) (taskKey, error) {
	if m == nil || strings.TrimSpace(m.ID) == "" || len(m.ID) > 1024 || len(m.ContextID) > 1024 {
		return taskKey{}, sdk.ErrInvalidParams
	}
	contextID := m.ContextID
	if contextID == "" {
		contextID = "ctx:" + digest(scope, m.ID)
	}
	key := digest(contextID, m.ID)
	token := base64.RawURLEncoding.EncodeToString([]byte(contextID)) + "." + key
	return parseKey(scope, sdk.TaskID(token))
}
func parseKey(scope string, id sdk.TaskID) (taskKey, error) {
	if len(id) > 1500 {
		return taskKey{}, sdk.ErrTaskNotFound
	}
	c, k, ok := strings.Cut(string(id), ".")
	if !ok || len(k) != 64 {
		return taskKey{}, sdk.ErrTaskNotFound
	}
	if _, err := hex.DecodeString(k); err != nil {
		return taskKey{}, sdk.ErrTaskNotFound
	}
	text, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil || len(text) == 0 || len(text) > 1024 || base64.RawURLEncoding.EncodeToString(text) != c {
		return taskKey{}, sdk.ErrTaskNotFound
	}
	return taskKey{id: id, context: string(text), start: run.StartID{Scope: scope, Key: digest(string(id))}}, nil
}
func (h *handler) load(ctx context.Context, k taskKey) (run.TreeSnapshot, error) {
	head, ok, err := h.heads.Load(ctx, k.start)
	if err != nil {
		return run.TreeSnapshot{}, err
	}
	if !ok {
		return run.TreeSnapshot{}, sdk.ErrTaskNotFound
	}
	return head, nil
}

// Raw JSON bytes and JSON text preserve numeric lexemes. The SDK's generic
// Data decoder rounds numbers through float64; refuse numeric Data rather
// than allowing a changed value into a run or an idempotency digest.
func input(m *sdk.Message) (run.Payload, error) {
	if m == nil || m.Role != sdk.MessageRoleUser || len(m.Parts) != 1 || m.Parts[0] == nil {
		return run.Payload{}, sdk.ErrUnsupportedContentType
	}
	p := m.Parts[0]
	if p.Filename != "" || len(p.Metadata) > 0 {
		return run.Payload{}, sdk.ErrUnsupportedContentType
	}
	var raw []byte
	switch v := p.Content.(type) {
	case sdk.Text:
		switch p.MediaType {
		case "", "text/plain":
			return run.EncodePayload(string(v))
		case "application/json":
			raw = []byte(v)
		default:
			return run.Payload{}, sdk.ErrUnsupportedContentType
		}
	case sdk.Raw:
		if p.MediaType != "application/json" {
			return run.Payload{}, sdk.ErrUnsupportedContentType
		}
		raw = []byte(v)
	case sdk.Data:
		if p.MediaType != "" && p.MediaType != "application/json" || !exactData(v.Value) {
			return run.Payload{}, sdk.ErrUnsupportedContentType
		}
		return run.EncodePayload(v.Value)
	default:
		return run.Payload{}, sdk.ErrUnsupportedContentType
	}
	payload, err := run.ParsePayload(raw)
	if err != nil {
		return run.Payload{}, fmt.Errorf("%w: malformed JSON part", sdk.ErrInvalidParams)
	}
	return payload, nil
}
func exactData(value any) bool {
	switch v := value.(type) {
	case nil, bool, string, json.Number:
		return true
	case []any:
		for _, item := range v {
			if !exactData(item) {
				return false
			}
		}
		return true
	case map[string]any:
		for _, item := range v {
			if !exactData(item) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func (h *handler) admit(ctx context.Context, p *sdk.SendMessageRequest) (taskKey, error) {
	if p == nil || p.Message == nil {
		return taskKey{}, sdk.ErrInvalidParams
	}
	scope, err := h.scope(ctx, p.Tenant)
	if err != nil {
		return taskKey{}, err
	}
	m := p.Message
	if strings.TrimSpace(m.ID) == "" || len(m.ID) > 1024 {
		return taskKey{}, sdk.ErrInvalidParams
	}
	if p.Config != nil {
		if historyErr := history(p.Config.HistoryLength); historyErr != nil {
			return taskKey{}, historyErr
		}
	}
	payload, err := input(m)
	if err != nil {
		return taskKey{}, err
	}
	if p.Config != nil && p.Config.PushConfig != nil {
		return taskKey{}, sdk.ErrPushNotificationNotSupported
	}
	mode := m.Parts[0].MediaType
	if mode == "" {
		if _, ok := m.Parts[0].Content.(sdk.Data); ok {
			mode = "application/json"
		} else {
			mode = "text/plain"
		}
	}
	if !slices.Contains(h.inputs, mode) {
		return taskKey{}, sdk.ErrUnsupportedContentType
	}
	if p.Config != nil && len(p.Config.AcceptedOutputModes) > 0 {
		for _, mode := range h.outputs {
			if !slices.Contains(p.Config.AcceptedOutputModes, mode) {
				return taskKey{}, sdk.ErrUnsupportedContentType
			}
		}
	}
	if m.TaskID == "" {
		if _, ok := m.Metadata[a2a.WaitIDMetadataKey]; ok {
			return taskKey{}, sdk.ErrInvalidParams
		}
		k, keyErr := newKey(scope, m)
		if keyErr != nil {
			return k, keyErr
		}
		_, startErr := h.runtime.Start(ctx, h.program, run.StartRequest[run.Payload]{ID: k.start, Input: payload})
		if startErr != nil {
			if _, accepted := errors.AsType[*run.StartAcceptedError](startErr); !accepted {
				return taskKey{}, fmt.Errorf("%w: start: %w", sdk.ErrInvalidRequest, startErr)
			}
		}
		return k, nil
	}
	k, err := parseKey(scope, m.TaskID)
	if err != nil {
		return k, err
	}
	if m.ContextID != "" && m.ContextID != k.context {
		return k, sdk.ErrInvalidParams
	}
	head, err := h.load(ctx, k)
	if err != nil {
		return k, err
	}
	waitText, ok := m.Metadata[a2a.WaitIDMetadataKey].(string)
	if !ok {
		return k, fmt.Errorf("%w: follow-up requires %s", sdk.ErrInvalidParams, a2a.WaitIDMetadataKey)
	}
	wait, err := run.ParseWaitID(waitText)
	if err != nil {
		return k, sdk.ErrInvalidParams
	}
	if match, receiptErr := head.Answered(wait, payload); receiptErr != nil {
		return k, fmt.Errorf("%w: %w", sdk.ErrInvalidRequest, receiptErr)
	} else if match {
		return k, nil
	}
	local, ok := h.runtime.Process(head.RootID())
	if !ok || local.TreeIncarnationID() != head.IncarnationID() {
		return k, fmt.Errorf("%w: run writer is not attached here", sdk.ErrUnsupportedOperation)
	}
	if err = local.Answer(ctx, wait, payload); err != nil {
		return k, fmt.Errorf("%w: answer: %w", sdk.ErrInvalidRequest, err)
	}
	return k, nil
}
func (h *handler) GetTask(ctx context.Context, p *sdk.GetTaskRequest) (*sdk.Task, error) {
	if p == nil {
		return nil, sdk.ErrInvalidParams
	}
	if err := history(p.HistoryLength); err != nil {
		return nil, err
	}
	scope, err := h.scope(ctx, p.Tenant)
	if err != nil {
		return nil, err
	}
	k, err := parseKey(scope, p.ID)
	if err != nil {
		return nil, err
	}
	head, err := h.load(ctx, k)
	if err != nil {
		return nil, err
	}
	return project(k, head)
}
func (h *handler) CancelTask(ctx context.Context, p *sdk.CancelTaskRequest) (*sdk.Task, error) {
	if p == nil {
		return nil, sdk.ErrInvalidParams
	}
	scope, err := h.scope(ctx, p.Tenant)
	if err != nil {
		return nil, err
	}
	k, err := parseKey(scope, p.ID)
	if err != nil {
		return nil, err
	}
	head, err := h.load(ctx, k)
	if err != nil {
		return nil, err
	}
	root := rootOf(head)
	if root.Status().Terminal() {
		return nil, sdk.ErrTaskNotCancelable
	}
	local, ok := h.runtime.Process(head.RootID())
	if !ok || local.TreeIncarnationID() != head.IncarnationID() {
		return nil, sdk.ErrTaskNotCancelable
	}
	if err = local.Cancel(ctx, "A2A cancellation requested"); err != nil {
		return nil, fmt.Errorf("%w: %w", sdk.ErrTaskNotCancelable, err)
	}
	head, err = h.load(ctx, k)
	if err != nil {
		return nil, err
	}
	return project(k, head)
}
func (h *handler) SendMessage(ctx context.Context, p *sdk.SendMessageRequest) (sdk.SendMessageResult, error) {
	k, err := h.admit(ctx, p)
	if err != nil {
		return nil, err
	}
	if p.Config != nil && p.Config.ReturnImmediately {
		head, err := h.load(ctx, k)
		if err != nil {
			return nil, err
		}
		return project(k, head)
	}
	for task, err := range h.views(ctx, k) {
		if err != nil {
			return nil, err
		}
		if settlesTurn(task.Status.State) {
			return task, nil
		}
	}
	return nil, sdk.ErrInternalError
}
func settlesTurn(s sdk.TaskState) bool {
	return s == sdk.TaskStateCompleted || s == sdk.TaskStateFailed || s == sdk.TaskStateCanceled || s == sdk.TaskStateInputRequired
}
func rootOf(head run.TreeSnapshot) run.ProcessSnapshot {
	for _, p := range head.ProcessSnapshots() {
		if p.Relation().IsRoot() {
			return p
		}
	}
	return run.ProcessSnapshot{}
}
func project(k taskKey, head run.TreeSnapshot) (*sdk.Task, error) {
	root := rootOf(head)
	if !root.Valid() {
		return nil, sdk.ErrInternalError
	}
	t := &sdk.Task{ID: k.id, ContextID: k.context, Status: sdk.TaskStatus{State: sdk.TaskStateWorking}, Metadata: map[string]any{RootKey: root.Relation().ProcessID().String(), ProgramKey: root.ProgramRef().String(), "scope.root_status": root.Status().String()}}
	for _, member := range head.ProcessSnapshots() {
		if root.Status().Terminal() && !member.Status().Terminal() {
			return t, nil
		}
	}
	if result, ok := root.Result(); ok {
		termination := result.Termination()
		switch termination.Status() {
		case run.StatusCompleted:
			t.Status.State = sdk.TaskStateCompleted
			if payload, ok := termination.Output(); ok {
				var part *sdk.Part
				if text, err := payload.Decode[string](); err == nil && string(payload.JSON()) != "null" {
					part = sdk.NewTextPart(text)
					part.MediaType = "text/plain"
				} else {
					part = sdk.NewTextPart(string(payload.JSON()))
					part.MediaType = "application/json"
				}
				t.Artifacts = []*sdk.Artifact{{ID: "result", Parts: []*sdk.Part{part}}}
			}
		case run.StatusCanceled, run.StatusKilled:
			t.Status.State = sdk.TaskStateCanceled
		default:
			t.Status.State = sdk.TaskStateFailed
		}
		t.Metadata["scope.termination"] = termination.Cause().String()
		return t, nil
	}
	pending := head.Pending()
	if len(pending) > 0 {
		questions := make([]map[string]any, 0, len(pending))
		for _, w := range pending {
			questions = append(questions, map[string]any{"process_id": w.ProcessID.String(), "wait_id": w.WaitID.String(), "key": w.Key.String(), "question": json.RawMessage(w.Question), "answer_schema": json.RawMessage(w.AnswerSchema), "max_answer_bytes": w.MaxAnswerBytes})
		}
		t.Metadata[PendingKey] = questions
		t.Status.State = sdk.TaskStateInputRequired
		part := sdk.NewTextPart(string(pending[0].Question))
		part.MediaType = "application/json"
		var text string
		if string(pending[0].Question) != "null" && jsonv2.Unmarshal(pending[0].Question, &text) == nil {
			part = sdk.NewTextPart(text)
			part.MediaType = "text/plain"
		}
		t.Status.Message = sdk.NewMessageForTask(sdk.MessageRoleAgent, t, part)
		t.Status.Message.Metadata = map[string]any{a2a.WaitIDMetadataKey: pending[0].WaitID.String(), a2a.AnswerSchemaMetadataKey: string(pending[0].AnswerSchema)}
	}
	return t, nil
}

func sameModes(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}
func history(n *int) error {
	if n == nil || *n == 0 {
		return nil
	}
	if *n < 0 {
		return sdk.ErrInvalidParams
	}
	return sdk.ErrUnsupportedOperation
}

// views reads a current head first, then follows its matching local writer.
// Reattachment after gaps or writer replacement always restarts from an
// acknowledged head. A remote writer is observed by read-only polling.
func (h *handler) views(ctx context.Context, k taskKey) iter.Seq2[*sdk.Task, error] {
	return func(yield func(*sdk.Task, error) bool) {
		var last run.Digest
		emit := func(head run.TreeSnapshot) bool {
			if !head.Valid() || head.Digest() == last {
				return true
			}
			last = head.Digest()
			t, err := project(k, head)
			if !yield(t, err) || err != nil {
				return false
			}
			return !settlesTurn(t.Status.State)
		}
		for {
			head, err := h.load(ctx, k)
			if err != nil {
				yield(nil, err)
				return
			}
			if !emit(head) {
				return
			}
			local, ok := h.runtime.Process(head.RootID())
			if !ok || local.TreeIncarnationID() != head.IncarnationID() {
				select {
				case <-ctx.Done():
					yield(nil, ctx.Err())
					return
				case <-time.After(250 * time.Millisecond):
					continue
				}
			}
			// A different runtime can take over while this local process is still
			// waiting on a host call. Periodically reconcile with the durable writer.
			watch, stop := context.WithTimeout(ctx, time.Second)
			active := true
			for view, err := range local.Observe(watch, run.Cursor{}, h.capacity) {
				if err != nil {
					if !errors.Is(err, run.ErrObservationGap) && !errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
						yield(nil, err)
						active = false
					}
					break
				}
				// The store, not a late local writer, certifies the next head. The same
				// digest may be seen twice during atomic snapshot/subscription attachment.
				if view.Snapshot.Valid() && view.Snapshot.Digest() != last {
					acknowledged, err := h.load(ctx, k)
					if err != nil {
						yield(nil, err)
						active = false
						break
					}
					if acknowledged.IncarnationID() != local.TreeIncarnationID() {
						break
					}
					if !emit(acknowledged) {
						active = false
						break
					}
				}
			}
			stop()
			if !active {
				return
			}
			if err = ctx.Err(); err != nil {
				yield(nil, err)
				return
			}
			// If the writer has stopped without a durable terminal result, do not
			// invent one or retry execution. Avoid hot-looping a closed subscription.
			select {
			case <-ctx.Done():
				yield(nil, ctx.Err())
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
}

func (h *handler) events(ctx context.Context, k taskKey, subscribing bool) iter.Seq2[sdk.Event, error] {
	return func(yield func(sdk.Event, error) bool) {
		first := true
		for t, err := range h.views(ctx, k) {
			if err != nil {
				yield(nil, err)
				return
			}
			if first {
				// A2A refuses a new subscription to an already terminal task. Decide on the
				// same snapshot that would be sent first, without a racy preliminary GET.
				if subscribing && t.Status.State.Terminal() {
					yield(nil, sdk.ErrUnsupportedOperation)
					return
				}
				first = false
				if !yield(t, nil) {
					return
				}
				continue
			}
			for _, a := range t.Artifacts {
				e := sdk.NewArtifactEvent(t, a.Parts...)
				e.Artifact = a
				e.LastChunk = true
				if !yield(e, nil) {
					return
				}
			}
			e := sdk.NewStatusUpdateEvent(t, t.Status.State, t.Status.Message)
			e.Metadata = t.Metadata
			if !yield(e, nil) {
				return
			}
		}
	}
}

func (h *handler) SendStreamingMessage(ctx context.Context, p *sdk.SendMessageRequest) iter.Seq2[sdk.Event, error] {
	return func(yield func(sdk.Event, error) bool) {
		if !h.streaming {
			yield(nil, sdk.ErrUnsupportedOperation)
			return
		}
		k, err := h.admit(ctx, p)
		if err != nil {
			yield(nil, err)
			return
		}
		h.events(ctx, k, false)(yield)
	}
}

func (h *handler) SubscribeToTask(ctx context.Context, p *sdk.SubscribeToTaskRequest) iter.Seq2[sdk.Event, error] {
	if !h.streaming {
		return failStream(sdk.ErrUnsupportedOperation)
	}
	if p == nil {
		return failStream(sdk.ErrInvalidParams)
	}
	scope, err := h.scope(ctx, p.Tenant)
	if err != nil {
		return failStream(err)
	}
	k, err := parseKey(scope, p.ID)
	if err != nil {
		return failStream(err)
	}
	return h.events(ctx, k, true)
}

func (h *handler) ListTasks(context.Context, *sdk.ListTasksRequest) (*sdk.ListTasksResponse, error) {
	return nil, sdk.ErrUnsupportedOperation
}

func (h *handler) GetTaskPushConfig(context.Context, *sdk.GetTaskPushConfigRequest) (*sdk.PushConfig, error) {
	return nil, sdk.ErrPushNotificationNotSupported
}

func (h *handler) ListTaskPushConfigs(context.Context, *sdk.ListTaskPushConfigRequest) (*sdk.ListTaskPushConfigResponse, error) {
	return nil, sdk.ErrPushNotificationNotSupported
}

func (h *handler) CreateTaskPushConfig(context.Context, *sdk.PushConfig) (*sdk.PushConfig, error) {
	return nil, sdk.ErrPushNotificationNotSupported
}

func (h *handler) DeleteTaskPushConfig(context.Context, *sdk.DeleteTaskPushConfigRequest) error {
	return sdk.ErrPushNotificationNotSupported
}

func (h *handler) GetExtendedAgentCard(context.Context, *sdk.GetExtendedAgentCardRequest) (*sdk.AgentCard, error) {
	return nil, sdk.ErrUnsupportedOperation
}
