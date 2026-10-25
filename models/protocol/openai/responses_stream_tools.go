package openai

import (
	"fmt"

	"github.com/openai/openai-go/v3/responses"

	corechat "github.com/Tangerg/scope/core/chat"
)

type responsesStreamTool struct {
	itemID        string
	callID        string
	name          string
	open          bool
	arguments     []byte
	argumentsDone bool
}

func (r responsesStreamTool) matchesCall(call responses.ResponseFunctionToolCall) bool {
	return r.callID == call.CallID && r.name == call.Name && call.JSON.Arguments.Valid() && string(r.arguments) == call.Arguments
}

type responsesStreamTools map[int64]responsesStreamTool

func (r responsesStreamTools) add(event responses.ResponseOutputItemAddedEvent) (*corechat.PartDelta, error) {
	if event.Item.Type != responsesItemTypeFunctionCall {
		return nil, nil
	}
	call := event.Item.AsFunctionCall()
	if !event.JSON.OutputIndex.Valid() || event.OutputIndex < 0 || call.CallID == "" || call.Name == "" || !call.JSON.Arguments.Valid() {
		return nil, fmt.Errorf("openai responses: %w: function call lacks a valid index, call ID, or name", corechat.ErrInvalidResponse)
	}
	if _, exists := r[event.OutputIndex]; exists {
		return nil, fmt.Errorf("openai responses: %w: tool item %d added twice", corechat.ErrInvalidResponse, event.OutputIndex)
	}
	for _, other := range r {
		if other.callID == call.CallID {
			return nil, fmt.Errorf("openai responses: %w: tool identity reused at output index %d", corechat.ErrInvalidResponse, event.OutputIndex)
		}
	}
	tool, err := r.bindItemID(event.OutputIndex, responsesStreamTool{callID: call.CallID, name: call.Name, arguments: []byte(call.Arguments), open: true}, call.ID)
	if err != nil {
		return nil, err
	}
	r[event.OutputIndex] = tool
	part := corechat.NewToolCallDelta(corechat.ToolCallDelta{ID: call.CallID, Name: call.Name, Arguments: call.Arguments})
	return &part, nil
}

func (r responsesStreamTools) appendArguments(event responses.ResponseFunctionCallArgumentsDeltaEvent) (*corechat.PartDelta, error) {
	tool, exists := r[event.OutputIndex]
	if !event.JSON.OutputIndex.Valid() || !exists || !tool.open || tool.argumentsDone || event.ItemID == "" {
		return nil, fmt.Errorf("openai responses: %w: arguments delta for closed, unknown, or unidentified tool item %q at index %d", corechat.ErrInvalidResponse, event.ItemID, event.OutputIndex)
	}
	tool, err := r.bindItemID(event.OutputIndex, tool, event.ItemID)
	if err != nil {
		return nil, err
	}
	tool.arguments = append(tool.arguments, event.Delta...)
	r[event.OutputIndex] = tool
	if event.Delta == "" {
		return nil, nil
	}
	part := corechat.NewToolCallDelta(corechat.ToolCallDelta{ID: tool.callID, Name: tool.name, Arguments: event.Delta})
	return &part, nil
}

func (r responsesStreamTools) finishArguments(event responses.ResponseFunctionCallArgumentsDoneEvent) error {
	tool, exists := r[event.OutputIndex]
	if !event.JSON.OutputIndex.Valid() || !event.JSON.Arguments.Valid() || !exists || !tool.open || tool.argumentsDone || event.ItemID == "" || string(tool.arguments) != event.Arguments {
		return fmt.Errorf("%w: openai responses: arguments completion changed, duplicated, or omitted tool arguments", corechat.ErrInvalidResponse)
	}
	tool, err := r.bindItemID(event.OutputIndex, tool, event.ItemID)
	if err != nil {
		return err
	}
	tool.argumentsDone = true
	r[event.OutputIndex] = tool
	return nil
}

func (r responsesStreamTools) finishItem(event responses.ResponseOutputItemDoneEvent) error {
	call := event.Item.AsFunctionCall()
	tool, exists := r[event.OutputIndex]
	if !event.JSON.OutputIndex.Valid() || !exists || !tool.open || !tool.matchesCall(call) {
		return fmt.Errorf("openai responses: %w: completion of closed, unknown, or changed tool item at index %d", corechat.ErrInvalidResponse, event.OutputIndex)
	}
	tool, err := r.bindItemID(event.OutputIndex, tool, call.ID)
	if err != nil {
		return err
	}
	tool.open = false
	r[event.OutputIndex] = tool
	return nil
}

func (r responsesStreamTools) bindItemID(index int64, tool responsesStreamTool, itemID string) (responsesStreamTool, error) {
	if itemID == "" || tool.itemID == itemID {
		return tool, nil
	}
	if tool.itemID != "" {
		return responsesStreamTool{}, fmt.Errorf("openai responses: %w: tool item %d changed native identity from %q to %q", corechat.ErrInvalidResponse, index, tool.itemID, itemID)
	}
	for otherIndex, other := range r {
		if otherIndex != index && other.itemID == itemID {
			return responsesStreamTool{}, fmt.Errorf("openai responses: %w: native tool identity %q reused at output index %d", corechat.ErrInvalidResponse, itemID, index)
		}
	}
	tool.itemID = itemID
	return tool, nil
}

func (r responsesStreamTools) validateTerminal(response responses.Response) error {
	for index, tool := range r {
		if response.Status == responses.ResponseStatusCompleted && tool.open {
			return fmt.Errorf("openai responses: %w: tool item %d is still open at response.completed", corechat.ErrInvalidResponse, index)
		}
		if index >= int64(len(response.Output)) || response.Output[index].Type != responsesItemTypeFunctionCall {
			return fmt.Errorf("openai responses: %w: terminal response omitted tool item at index %d", corechat.ErrInvalidResponse, index)
		}
		call := response.Output[index].AsFunctionCall()
		if !tool.matchesCall(call) {
			return fmt.Errorf("openai responses: %w: terminal response changed tool call identity at index %d", corechat.ErrInvalidResponse, index)
		}
		identified, err := r.bindItemID(index, tool, call.ID)
		if err != nil {
			return err
		}
		r[index] = identified
	}
	if response.Status != responses.ResponseStatusCompleted {
		return nil
	}
	for index, item := range response.Output {
		if item.Type != responsesItemTypeFunctionCall {
			continue
		}
		if _, exists := r[int64(index)]; !exists {
			return fmt.Errorf("openai responses: %w: completed response introduced tool item at index %d", corechat.ErrInvalidResponse, index)
		}
	}
	return nil
}
