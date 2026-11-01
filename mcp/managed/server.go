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
	ResumeKey     [32]byte
	TokenLifetime time.Duration
}

func (c Config[I, O]) Validate() error {
	if c.Runtime == nil || !c.Program.Valid() || c.Authorize == nil || c.Project == nil {
		return errors.New("managed: runtime, program, authorization and output projection are required")
	}
	if c.ResumeKey == ([32]byte{}) || c.TokenLifetime <= 0 {
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
	views  *run.StartedTrees
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
	views, err := cfg.Runtime.StartedTrees()
	if err != nil {
		return err
	}
	b := &binding[I, O]{cfg: cfg, issuer: signer, views: views}
	server.AddTool(&sdk.Tool{Name: definition.Name, Description: definition.Description, InputSchema: definition.InputSchema}, b.call)
	return nil
}
func (b *binding[I, O]) call(ctx context.Context, req *sdk.CallToolRequest) (out *sdk.CallToolResult, err error) {
	// Host callback panics do not become model-visible diagnostics.
	defer func() {
		if recover() != nil {
			out, err = nil, errInternal
		}
	}()
	if req == nil || req.Params == nil || req.Session == nil {
		return nil, errArguments
	}
	grant, err := b.cfg.Authorize(ctx, req)
	if err != nil || !grant.StartID.Valid() || (grant.Session == "" && req.Session.ID() == "") {
		return nil, errPermission
	}
	caps := req.ClientCapabilities()
	if req.ProtocolVersion() != "2026-07-28" || caps == nil || caps.Elicitation == nil || caps.Elicitation.Form == nil && caps.Elicitation.URL != nil {
		return nil, errForm
	}
	wire := req.Params.Arguments
	if len(wire) == 0 {
		wire = json.RawMessage("{}")
	}
	p, err := run.ParsePayload(wire)
	if err != nil || b.cfg.Program.Descriptor().ValidateInput(p) != nil {
		return nil, errArguments
	}
	input, err := p.Decode[I]()
	if err != nil {
		return nil, errArguments
	}
	auth, err := jsonv2.Marshal(struct {
		Domain, Program, Name, Native, Session string
		Start                                  run.StartID
		Arguments                              [32]byte
	}{
		"scope.mcp.managed.v1", b.cfg.Program.ProgramRef().Digest().String(), b.cfg.Program.Descriptor().Name(), req.Session.ID(), grant.Session, grant.StartID, sha256.Sum256(p.JSON()),
	})
	if err != nil {
		return nil, errInternal
	}
	if req.Params.RequestState == "" {
		if req.Params.InputResponses != nil {
			return nil, errContinuation
		}
		h, startErr := b.cfg.Runtime.Start(ctx, b.cfg.Program, run.StartRequest[I]{ID: grant.StartID, Input: input})
		if startErr != nil {
			if _, unknown := errors.AsType[*run.StartError](startErr); unknown {
				return nil, rpc(CodeStartUnknown, "initial acceptance is uncertain; retry only with the same authorized start identity")
			}
			if accepted, ok := errors.AsType[*run.StartAcceptedError](startErr); ok {
				return b.viewOnly(ctx, grant.StartID, accepted.Receipt.RootID, auth, run.WaitID{})
			}
			if _, ok := errors.AsType[*run.StartConflictError](startErr); ok {
				return nil, errArguments
			}
			return nil, errUnavailable
		}
		return observe(ctx, b, h, auth, run.WaitID{})
	}
	t, err := b.issuer.open(req.Params.RequestState, auth)
	if err != nil {
		return nil, errContinuation
	}
	head, found, err := b.views.Load(ctx, grant.StartID)
	if err != nil || !found {
		return nil, errUnavailable
	}
	if head.RootID() != t.Root {
		return nil, errContinuation
	}
	pending := false
	for _, q := range head.Pending() {
		if q.WaitID == t.Wait && q.ProcessID == t.Owner {
			pending = true
			break
		}
	}
	if len(req.Params.InputResponses) != 1 {
		return nil, errArguments
	}
	reply, ok := req.Params.InputResponses["answer"].(*sdk.ElicitResult)
	if !ok || reply == nil {
		return nil, errArguments
	}
	switch reply.Action {
	case "decline", "cancel":
		if !pending {
			return nil, errContinuation
		}
		if reply.Content != nil {
			return nil, errArguments
		}
		if reply.Action == "decline" {
			return nil, rpc(CodeDeclined, "form declined; no answer was submitted")
		}
		return nil, rpc(CodeCanceled, "form canceled; no answer was submitted")
	case "accept":
		if reply.Content == nil {
			return nil, errArguments
		}
	default:
		return nil, errArguments
	}
	// The pinned SDK decodes arbitrary form numbers into float64 before this
	// handler sees them. Refuse them even in additional properties; otherwise a
	// rounded wire value could acquire a durable receipt for the wrong answer.
	for _, value := range reply.Content {
		switch value.(type) {
		case bool, string:
		default:
			return nil, errArguments
		}
	}
	content, err := jsonv2.Marshal(reply.Content, jsonv2.Deterministic(true))
	if err != nil {
		return nil, errArguments
	}
	answer, err := run.ParsePayload(content)
	if err != nil {
		return nil, errArguments
	}
	acknowledged, err := head.Answered(t.Wait, answer)
	if err != nil {
		return nil, errArguments
	}
	root, active := b.cfg.Runtime.Process(t.Root)
	if !acknowledged {
		if !pending {
			return nil, errContinuation
		}
		if !active {
			return nil, errUnavailable
		}
		owner, ok := b.cfg.Runtime.Process(t.Owner)
		if !ok {
			return nil, errUnavailable
		}
		if owner.Relation().RootID() != t.Root || root.ProgramRef() != b.cfg.Program.ProgramRef() {
			return nil, errContinuation
		}
		if err = owner.Answer(ctx, t.Wait, answer.JSON()); err != nil {
			if errors.Is(err, run.ErrSignalConflict) || errors.Is(err, run.ErrSignalRejected) {
				return nil, errArguments
			}
			return nil, errUnavailable
		}
	} else if !active {
		return b.viewOnly(ctx, grant.StartID, t.Root, auth, t.Wait)
	}
	return observe(ctx, b, root, auth, t.Wait)
}
func observe[I, O, H any](ctx context.Context, b *binding[I, O], h *run.Handle[H], auth []byte, answered run.WaitID) (*sdk.CallToolResult, error) {
	for ctx.Err() == nil {
		gap := false
		for observation, err := range h.Observe(ctx, run.Cursor{}, 16) {
			if errors.Is(err, run.ErrObservationGap) {
				gap = true
				break
			}
			if err != nil {
				return nil, errUnavailable
			}
			head := observation.Snapshot
			if !head.Valid() {
				continue
			}
			out, err := b.project(head, auth, answered)
			if err != nil || out != nil {
				return out, err
			}
		}
		if !gap {
			return nil, errUnavailable
		}
	}
	return nil, errUnavailable
}

func (b *binding[I, O]) viewOnly(ctx context.Context, id run.StartID, root run.ProcessID, auth []byte, answered run.WaitID) (*sdk.CallToolResult, error) {
	head, ok, err := b.views.Load(ctx, id)
	if err != nil || !ok {
		return nil, errUnavailable
	}
	if head.RootID() != root {
		return nil, errContinuation
	}
	out, err := b.project(head, auth, answered)
	if out == nil && err == nil {
		return nil, errUnavailable
	}
	return out, err
}
func (b *binding[I, O]) project(head run.TreeSnapshot, auth []byte, answered run.WaitID) (*sdk.CallToolResult, error) {
	var terminal run.Result[run.Payload]
	allDone := true
	for _, p := range head.ProcessSnapshots() {
		if !p.Status().Terminal() {
			allDone = false
		}
		if p.Relation().IsRoot() {
			if p.ProgramRef() != b.cfg.Program.ProgramRef() {
				return nil, errContinuation
			}
			terminal, _ = p.Result()
		}
	}
	if allDone && terminal.Valid() {
		v, ok := terminal.Termination().Output()
		if !ok {
			return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: "Managed run ended: " + terminal.Termination().Status().String()}}}, nil
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
	for _, q := range head.Pending() {
		if q.WaitID == answered {
			continue
		}
		f, err := form(q)
		if err != nil {
			return nil, err
		}
		token, err := b.issuer.seal(target{Root: head.RootID(), Owner: q.ProcessID, Wait: q.WaitID}, auth)
		if err != nil {
			return nil, errInternal
		}
		return &sdk.CallToolResult{RequestState: token, InputRequests: sdk.InputRequestMap{"answer": f}}, nil
	}
	return nil, nil
}
