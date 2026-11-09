package managed

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/Tangerg/scope/a2a"
	"github.com/Tangerg/scope/agent/run"
)

const remoteLogPollInterval = 250 * time.Millisecond

type Config struct {
	Runtime *run.Runtime
	// Program fixes the binding used for new submissions. Bind an existing
	// typed program with Program.Erase; its actual input/output schemas remain.
	Program run.Program[run.Payload, run.Payload]
	// Card declares the encoding policy independently of the program's value
	// schemas. DefaultOutputModes defaults to application/json; text/plain
	// renders strings as text and other values as their exact JSON text. When
	// both modes are declared, strings use text/plain and other values use JSON.
	// The same policy encodes questions and artifacts. Clients must accept every
	// declared output mode. Construction freezes the declaration.
	Card *sdk.AgentCard
	// Authority returns a stable server-authorized scope for this authenticated
	// caller and requested tenant. Never return the untrusted tenant verbatim.
	Authority func(ctx context.Context, tenant string) (string, error)
	// Service isolates independently deployed endpoints sharing a runtime store.
	Service string
	// MaxRequestBytes has the meaning of [a2a.ServerConfig.MaxRequestBytes].
	MaxRequestBytes int64
}
type handler struct {
	runtime   *run.Runtime
	program   run.Program[run.Payload, run.Payload]
	authority func(context.Context, string) (string, error)
	service   string
	streaming bool
	content   contentPolicy
}

var _ a2asrv.RequestHandler = (*handler)(nil)

// Tasks derive acceptance and lifecycle facts from readable runtime storage;
// the protocol endpoint cannot maintain an independent task store.
func NewHTTPHandler(cfg Config) (http.Handler, error) {
	if cfg.Card == nil || cfg.Runtime == nil || !cfg.Program.Valid() || cfg.Authority == nil || strings.TrimSpace(cfg.Service) == "" || !utf8.ValidString(cfg.Service) {
		return nil, errors.New("a2a/managed: runtime, program, card, service and authority are required")
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
	card.Capabilities.Extensions = append(card.Capabilities.Extensions, sdk.AgentExtension{URI: a2a.ExternalWaitExtension, Description: "Select a pending wait by " + a2a.WaitMetadataKey + " and answer its declared schema."})
	content, err := newContentPolicy(card)
	if err != nil {
		return nil, err
	}
	card.DefaultInputModes = slices.Clone(content.inputs)
	card.DefaultOutputModes = slices.Clone(content.outputs)
	h := &handler{runtime: cfg.Runtime, program: cfg.Program, authority: cfg.Authority, service: cfg.Service, streaming: card.Capabilities.Streaming, content: content}
	return a2a.NewProtocolHTTPHandler(a2a.ProtocolServerConfig{Card: &card, Handler: h, MaxRequestBytes: cfg.MaxRequestBytes})
}

func (h *handler) scope(ctx context.Context, tenant string) (string, error) {
	authority, err := h.authority(ctx, tenant)
	if err != nil || strings.TrimSpace(authority) == "" {
		return "", sdk.ErrTaskNotFound
	}
	key, err := identityDigest(h.service, authority)
	if err != nil {
		return "", sdk.ErrTaskNotFound
	}
	return authorityScopePrefix + key, nil
}
func (h *handler) load(ctx context.Context, k taskKey) (run.TreeInspection, error) {
	head, err := h.runtime.Inspect(ctx, k.start.Root())
	if errors.Is(err, run.ErrProcessNotFound) {
		return run.TreeInspection{}, sdk.ErrTaskNotFound
	}
	if err != nil {
		return run.TreeInspection{}, sdk.ErrInternalError
	}
	return head, nil
}

func (h *handler) admit(ctx context.Context, request *sdk.SendMessageRequest) (taskKey, error) {
	if request == nil || request.Message == nil {
		return taskKey{}, sdk.ErrInvalidParams
	}
	scope, err := h.scope(ctx, request.Tenant)
	if err != nil {
		return taskKey{}, err
	}
	key, err := taskKeyFromMessage(scope, request.Message)
	if err != nil {
		return taskKey{}, err
	}
	payload, err := h.content.prepare(request)
	if err != nil {
		return taskKey{}, err
	}
	if request.Message.TaskID == "" {
		if _, present := request.Message.Metadata[a2a.WaitMetadataKey]; present {
			return taskKey{}, sdk.ErrInvalidParams
		}
		return key, h.startTask(ctx, key, payload)
	}
	return key, h.answerWait(ctx, key, request.Message.Metadata, payload)
}

func (h *handler) startTask(ctx context.Context, key taskKey, payload run.Payload) error {
	_, err := h.runtime.Start(ctx, h.program, run.StartRequest[run.Payload]{ID: key.start, Input: payload})
	if err == nil {
		return nil
	}
	if _, conflict := errors.AsType[*run.StartConflictError](err); conflict {
		return sdk.NewError(sdk.ErrInvalidRequest, "message identity already accepted a different input")
	}
	if errors.Is(err, run.ErrInvalidPayload) {
		return sdk.ErrInvalidParams
	}
	return sdk.NewError(sdk.ErrInternalError, "initial acceptance is unconfirmed; retry the same message and context IDs")
}

func (h *handler) answerWait(ctx context.Context, key taskKey, metadata map[string]any, payload run.Payload) error {
	head, err := h.load(ctx, key)
	if err != nil {
		return err
	}
	address, err := a2a.ParseWaitAddress(metadata[a2a.WaitMetadataKey])
	if err != nil {
		return sdk.ErrInvalidParams
	}
	process, err := run.ParseProcessID(address.Process)
	if err != nil || process.Root() != head.Root {
		return sdk.ErrInvalidParams
	}
	wait, err := run.ParseWaitKey(address.Key)
	if err != nil {
		return sdk.ErrInvalidParams
	}
	matched, err := h.runtime.Answered(ctx, process, wait, payload)
	if err != nil {
		return sdk.NewError(sdk.ErrInvalidRequest, "answer is not consistent with the external wait")
	}
	if matched {
		return nil
	}
	local, attached := h.runtime.Process(process)
	if !attached || local.Epoch() != head.Epoch {
		return fmt.Errorf("%w: run writer is not attached here", sdk.ErrUnsupportedOperation)
	}
	if err = local.Answer(ctx, wait, payload); err == nil {
		return nil
	}
	if errors.Is(err, run.ErrSignalRejected) || errors.Is(err, run.ErrSignalConflict) || errors.Is(err, run.ErrInvalidMessage) || errors.Is(err, run.ErrInvalidPayload) {
		return sdk.NewError(sdk.ErrInvalidRequest, "answer does not match the external wait")
	}
	if errors.Is(err, run.ErrEpochConflict) {
		return sdk.ErrUnsupportedOperation
	}
	return sdk.NewError(sdk.ErrInternalError, "answer was not acknowledged; retry the same wait and answer")
}

func (h *handler) GetTask(ctx context.Context, p *sdk.GetTaskRequest) (*sdk.Task, error) {
	if p == nil {
		return nil, sdk.ErrInvalidParams
	}
	if err := validateHistoryLength(p.HistoryLength); err != nil {
		return nil, err
	}
	scope, err := h.scope(ctx, p.Tenant)
	if err != nil {
		return nil, err
	}
	k, err := parseTaskKey(scope, p.ID)
	if err != nil {
		return nil, err
	}
	head, err := h.load(ctx, k)
	if err != nil {
		return nil, err
	}
	return projectTask(k, head, h.content)
}
func (h *handler) CancelTask(ctx context.Context, p *sdk.CancelTaskRequest) (*sdk.Task, error) {
	if p == nil {
		return nil, sdk.ErrInvalidParams
	}
	scope, err := h.scope(ctx, p.Tenant)
	if err != nil {
		return nil, err
	}
	k, err := parseTaskKey(scope, p.ID)
	if err != nil {
		return nil, err
	}
	head, err := h.load(ctx, k)
	if err != nil {
		return nil, err
	}
	root := rootProcess(head)
	if root.Status.Terminal() {
		return nil, sdk.ErrTaskNotCancelable
	}
	local, ok := h.runtime.Process(head.Root)
	if !ok || local.Epoch() != head.Epoch {
		return nil, sdk.ErrTaskNotCancelable
	}
	if err = local.Cancel(ctx, "A2A cancellation requested"); err != nil {
		if errors.Is(err, run.ErrProcessFinished) || errors.Is(err, run.ErrEpochConflict) {
			return nil, sdk.ErrTaskNotCancelable
		}
		if errors.Is(err, run.ErrClosed) || errors.Is(err, run.ErrInvalidProcessControl) {
			if current, readErr := h.load(ctx, k); readErr == nil && rootProcess(current).Status.Terminal() {
				return nil, sdk.ErrTaskNotCancelable
			}
		}
		return nil, sdk.NewError(sdk.ErrInternalError, "cancellation was not acknowledged; read the task status")
	}
	head, err = h.load(ctx, k)
	if err != nil {
		return nil, err
	}
	return projectTask(k, head, h.content)
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
		return projectTask(k, head, h.content)
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

func (h *handler) views(ctx context.Context, k taskKey) iter.Seq2[*sdk.Task, error] {
	return func(yield func(*sdk.Task, error) bool) {
		var after run.LSN
		emit := func(head run.TreeInspection) bool {
			if head.Head == after {
				return true
			}
			after = head.Head
			task, err := projectTask(k, head, h.content)
			return yield(task, err) && err == nil && !settlesTurn(task.Status.State)
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
			if local, ok := h.runtime.Process(head.Root); ok {
				for entry, err := range local.Observe(ctx, after) {
					if err != nil {
						yield(nil, err)
						return
					}
					if entry.LSN <= after {
						continue
					}
					view, err := h.load(ctx, k)
					if err != nil {
						yield(nil, err)
						return
					}
					if !emit(view) {
						return
					}
				}
			}
			select {
			case <-ctx.Done():
				yield(nil, ctx.Err())
				return
			case <-time.After(remoteLogPollInterval):
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
				// same log projection that would be sent first, without a racy preliminary GET.
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
	k, err := parseTaskKey(scope, p.ID)
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
