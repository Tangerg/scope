package managed

import (
	jsonv2 "encoding/json/v2"

	sdk "github.com/Tangerg/go-sdk/mcp"

	"github.com/Tangerg/scope/agent/run"
)

const (
	formProtocolVersion = "2026-07-28"
	formInputKey        = "answer"
	formMode            = "form"
	formAccept          = "accept"
	formDecline         = "decline"
	formCancel          = "cancel"
)

type formReply struct {
	action string
	answer run.Payload
}

func parseFormReply(responses sdk.InputResponseMap) (formReply, error) {
	if len(responses) != 1 {
		return formReply{}, errArguments
	}
	reply, ok := responses[formInputKey].(*sdk.ElicitResult)
	if !ok || reply == nil {
		return formReply{}, errArguments
	}
	switch reply.Action {
	case formDecline, formCancel:
		if reply.Content != nil {
			return formReply{}, errArguments
		}
		return formReply{action: reply.Action}, nil
	case formAccept:
		if reply.Content == nil {
			return formReply{}, errArguments
		}
	default:
		return formReply{}, errArguments
	}
	// The pinned SDK loses arbitrary numeric precision before this boundary.
	// Only the form vocabulary we can transport exactly may acquire a receipt.
	for _, value := range reply.Content {
		switch value.(type) {
		case bool, string:
		default:
			return formReply{}, errArguments
		}
	}
	content, err := jsonv2.Marshal(reply.Content, jsonv2.Deterministic(true))
	if err != nil {
		return formReply{}, errArguments
	}
	answer, err := run.ParsePayload(content)
	if err != nil {
		return formReply{}, errArguments
	}
	return formReply{action: formAccept, answer: answer}, nil
}

func (f formReply) rejection(pending bool) error {
	if !pending {
		return errContinuation
	}
	switch f.action {
	case formDecline:
		return rpc(CodeDeclined, "form declined; no answer was submitted")
	case formCancel:
		return rpc(CodeCanceled, "form canceled; no answer was submitted")
	default:
		return errArguments
	}
}
