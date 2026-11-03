package managed

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"time"

	"github.com/Tangerg/go-sdk/jsonrpc"
	sdk "github.com/Tangerg/go-sdk/mcp"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/mcp"
	"github.com/Tangerg/scope/mcp/internal/elicitation"
)

// Grant is derived by the Host from authenticated request context on every
// round. Session identifies a trusted application conversation on sessionless
// transports. Native session IDs, when present, are bound independently.
type Grant struct {
	StartID run.StartID
	Session string
}
type Config[I, O any] struct {
	Runtime       *run.Runtime
	Program       run.Program[I, O]
	Authorize     func(context.Context, *sdk.CallToolRequest) (Grant, error)
	Project       func(O) (tool.Output, error)
	ResumeKey     [resumeKeyBytes]byte
	TokenLifetime time.Duration
}

func (c Config[I, O]) Validate() error {
	if c.Runtime == nil || !c.Program.Valid() || c.Authorize == nil || c.Project == nil {
		return errors.New("managed: runtime, program, authorization and output projection are required")
	}
	if c.ResumeKey == ([resumeKeyBytes]byte{}) || c.TokenLifetime <= 0 {
		return errors.New("managed: resume key and token lifetime are required")
	}
	return nil
}

// Codes distinguish transport and authorization outcomes from the run's own
// terminal status. Decline and cancel leave the pending wait unchanged.
const (
	CodeUnavailable         = -32051
	CodeDeclined            = -32052
	CodeCanceled            = -32053
	CodeUnauthorized        = -32054
	CodeInvalidContinuation = -32055
	CodeUnsupportedForm     = -32056
	CodeStartUnknown        = -32057
)

func rpc(code int64, message string) *jsonrpc.Error {
	return &jsonrpc.Error{Code: code, Message: message}
}

var (
	errContinuation = rpc(CodeInvalidContinuation, "invalid, expired, or differently bound continuation")
	errPermission   = rpc(CodeUnauthorized, "managed call is not authorized")
	errUnavailable  = rpc(CodeUnavailable, "managed run is unavailable on this writer; reconcile the authorized start")
	errForm         = rpc(CodeUnsupportedForm, "this call requires MCP 2026-07-28 flat form elicitation")
	errArguments    = rpc(jsonrpc.CodeInvalidParams, "invalid managed tool arguments or answer")
	errInternal     = rpc(jsonrpc.CodeInternalError, "managed tool could not produce a result")
)

type binding[I, O any] struct {
	cfg    Config[I, O]
	issuer issuer
}

// Register exposes a Program's descriptor as an MCP tool. The projection only
// renders a committed successful result and must be deterministic and side-effect free.
func Register[I, O any](server *sdk.Server, cfg Config[I, O]) error {
	if server == nil {
		return mcp.ErrNilServer
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	d := cfg.Program.Descriptor()
	definition := tool.Definition{Name: d.Name(), Description: d.Description(), InputSchema: d.InputSchema().JSON()}
	if err := definition.Validate(); err != nil {
		return err
	}
	signer, err := newIssuer(cfg.ResumeKey, cfg.TokenLifetime)
	if err != nil {
		return err
	}
	b := &binding[I, O]{cfg: cfg, issuer: signer}
	server.AddTool(&sdk.Tool{Name: definition.Name, Description: definition.Description, InputSchema: definition.InputSchema}, b.call)
	return nil
}

type admittedCall[I any] struct {
	start     run.StartID
	input     I
	authority []byte
}

func (b *binding[I, O]) call(ctx context.Context, req *sdk.CallToolRequest) (out *sdk.CallToolResult, err error) {
	// Host callback panics do not become model-visible diagnostics.
	defer func() {
		if recover() != nil {
			out, err = nil, errInternal
		}
	}()
	call, err := b.admit(ctx, req)
	if err != nil {
		return nil, err
	}
	if req.Params.RequestState == "" {
		if req.Params.InputResponses != nil {
			return nil, errContinuation
		}
		return b.start(ctx, call)
	}
	return b.resume(ctx, call, req.Params)
}

func (b *binding[I, O]) admit(ctx context.Context, req *sdk.CallToolRequest) (admittedCall[I], error) {
	if req == nil || req.Params == nil || req.Session == nil {
		return admittedCall[I]{}, errArguments
	}
	grant, err := b.cfg.Authorize(ctx, req)
	if err != nil || !grant.StartID.Valid() || (grant.Session == "" && req.Session.ID() == "") {
		return admittedCall[I]{}, errPermission
	}
	caps := req.ClientCapabilities()
	if req.ProtocolVersion() != elicitation.ProtocolVersion || caps == nil || caps.Elicitation == nil || caps.Elicitation.Form == nil && caps.Elicitation.URL != nil {
		return admittedCall[I]{}, errForm
	}
	wire := req.Params.Arguments
	if len(wire) == 0 {
		wire = json.RawMessage("{}")
	}
	payload, err := run.ParsePayload(wire)
	if err != nil || b.cfg.Program.Descriptor().ValidateInput(payload) != nil {
		return admittedCall[I]{}, errArguments
	}
	input, err := payload.Decode[I]()
	if err != nil {
		return admittedCall[I]{}, errArguments
	}
	authority, err := jsonv2.Marshal(callAuthority{
		Domain:  authorizationDomain,
		Program: b.cfg.Program.ProgramRef().Digest().String(),
		Name:    b.cfg.Program.Descriptor().Name(),
		Native:  req.Session.ID(), Session: grant.Session, Start: grant.StartID,
		Arguments: sha256.Sum256(payload.JSON()),
	})
	if err != nil {
		return admittedCall[I]{}, errInternal
	}
	return admittedCall[I]{start: grant.StartID, input: input, authority: authority}, nil
}

func (b *binding[I, O]) start(ctx context.Context, call admittedCall[I]) (*sdk.CallToolResult, error) {
	handle, err := b.cfg.Runtime.Start(ctx, b.cfg.Program, run.StartRequest[I]{ID: call.start, Input: call.input})
	if err == nil {
		return observe(ctx, b, handle, call.start, call.authority, target{})
	}
	if _, ok := errors.AsType[*run.StartConflictError](err); ok {
		return nil, errArguments
	}
	return nil, rpc(CodeStartUnknown, "initial acceptance is unconfirmed; retry only with the same authorized start identity")
}

func (b *binding[I, O]) resume(ctx context.Context, call admittedCall[I], params *sdk.CallToolParamsRaw) (*sdk.CallToolResult, error) {
	destination, err := b.issuer.open(params.RequestState, call.authority)
	if err != nil || destination.Owner.Root() != call.start.Root() {
		return nil, errContinuation
	}
	head, err := b.cfg.Runtime.Inspect(ctx, call.start.Root())
	if err != nil {
		return nil, errUnavailable
	}
	reply, err := parseFormReply(params.InputResponses)
	if err != nil {
		return nil, err
	}
	if reply.action != elicitation.Accept {
		return nil, reply.rejection(destination.pendingIn(head))
	}
	acknowledged, err := b.cfg.Runtime.Answered(ctx, destination.Owner, destination.Wait, reply.answer)
	if err != nil {
		return nil, errArguments
	}
	root, active := b.cfg.Runtime.Process(head.Root)
	owns := active && root.Epoch() == head.Epoch
	if !acknowledged {
		if !destination.pendingIn(head) {
			return nil, errContinuation
		}
		if !owns {
			return nil, errUnavailable
		}
		if err := b.answer(ctx, root, destination, reply.answer); err != nil {
			return nil, err
		}
	} else if !owns {
		return b.viewOnly(ctx, call.start, call.authority, destination)
	}
	return observe(ctx, b, root, call.start, call.authority, destination)
}

func (b *binding[I, O]) answer(ctx context.Context, root *run.Handle[run.Payload], destination target, answer run.Payload) error {
	owner, ok := b.cfg.Runtime.Process(destination.Owner)
	if !ok {
		return errUnavailable
	}
	if owner.ID().Root() != root.ID() || root.ProgramRef() != b.cfg.Program.ProgramRef() {
		return errContinuation
	}
	if err := owner.Answer(ctx, destination.Wait, answer); err != nil {
		if errors.Is(err, run.ErrSignalConflict) || errors.Is(err, run.ErrSignalRejected) {
			return errArguments
		}
		return errUnavailable
	}
	return nil
}

func observe[I, O, H any](ctx context.Context, b *binding[I, O], h *run.Handle[H], id run.StartID, auth []byte, answered target) (*sdk.CallToolResult, error) {
	head, err := b.cfg.Runtime.Inspect(ctx, id.Root())
	if err != nil || head.Root != h.ID() {
		return nil, errUnavailable
	}
	if out, err := b.project(head, auth, answered); out != nil || err != nil {
		return out, err
	}
	for _, err := range h.Observe(ctx, head.Head) {
		if err != nil {
			return nil, errUnavailable
		}
		head, err := b.cfg.Runtime.Inspect(ctx, id.Root())
		if err != nil {
			return nil, errUnavailable
		}
		if out, err := b.project(head, auth, answered); out != nil || err != nil {
			return out, err
		}
	}
	return b.viewOnly(ctx, id, auth, answered)
}

func (b *binding[I, O]) viewOnly(ctx context.Context, id run.StartID, auth []byte, answered target) (*sdk.CallToolResult, error) {
	head, err := b.cfg.Runtime.Inspect(ctx, id.Root())
	if err != nil {
		return nil, errUnavailable
	}
	out, err := b.project(head, auth, answered)
	if out == nil && err == nil {
		return nil, errUnavailable
	}
	return out, err
}

func (b *binding[I, O]) project(head run.TreeInspection, auth []byte, answered target) (*sdk.CallToolResult, error) {
	root, ok := head.Process(head.Root)
	if !ok || root.Program != b.cfg.Program.ProgramRef() {
		return nil, errContinuation
	}
	if root.Termination.Valid() {
		v, ok := root.Termination.Output()
		if !ok {
			return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: "Managed run ended: " + root.Termination.Status().String()}}}, nil
		}
		value, err := b.cfg.Program.Descriptor().DecodeOutput[O](v)
		if err != nil {
			return nil, errInternal
		}
		rendered, err := b.cfg.Project(value)
		if err != nil {
			return nil, errInternal
		}
		out, err := mcp.EncodeToolResult(rendered)
		if err != nil {
			return nil, errInternal
		}
		return out, nil
	}
	for _, process := range head.Processes {
		for _, wait := range process.Waits {
			if process.ID == answered.Owner && wait.Key == answered.Wait {
				continue
			}
			f, err := form(wait)
			if err != nil {
				return nil, err
			}
			token, err := b.issuer.seal(target{Owner: process.ID, Wait: wait.Key}, auth)
			if err != nil {
				return nil, errInternal
			}
			return &sdk.CallToolResult{RequestState: token, InputRequests: sdk.InputRequestMap{formInputKey: f}}, nil
		}
	}
	return nil, nil
}
