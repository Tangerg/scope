package modeltest

import (
	"bytes"
	jsonv2 "encoding/json/v2"
	"testing"

	"github.com/Tangerg/scope/core/chat"
)

// ChatContract describes one provider's happy-path Model and Streamer contract.
// New and Request are called independently for each subtest so provider state
// and request mutation cannot leak between Call and Stream.
type ChatContract struct {
	New              func(t *testing.T) (chat.Model, chat.Streamer)
	Request          func(t *testing.T) *chat.Request
	AssertCall       func(t *testing.T, response *chat.Response)
	AssertStream     func(t *testing.T, deltas []*chat.ResponseDelta)
	AssertAggregated func(t *testing.T, response *chat.Response)
}

// RunChatContract checks the shared synchronous and streaming cases through the
// provider transport boundary.
func RunChatContract(t *testing.T, contract ChatContract) {
	t.Helper()
	if contract.New == nil {
		t.Fatal("modeltest.ChatContract.New must not be nil")
	}
	if contract.Request == nil {
		t.Fatal("modeltest.ChatContract.Request must not be nil")
	}

	t.Run("call", func(t *testing.T) {
		model, _ := contract.New(t)
		if model == nil {
			t.Fatal("provider returned nil Model")
		}
		request := contract.validRequest(t)
		before := requestWire(t, request)
		response, err := model.Call(t.Context(), request)
		if err != nil {
			t.Fatalf("Call: %v", err)
		}
		assertResponse(t, response)
		if after := requestWire(t, request); !bytes.Equal(before, after) {
			t.Fatalf("Call mutated Request\nbefore: %s\nafter:  %s", before, after)
		}
		if contract.AssertCall != nil {
			contract.AssertCall(t, response)
		}
	})

	t.Run("stream", func(t *testing.T) {
		_, streamer := contract.New(t)
		if streamer == nil {
			t.Fatal("provider returned nil Streamer")
		}
		request := contract.validRequest(t)
		before := requestWire(t, request)
		var deltas []*chat.ResponseDelta
		var accumulator chat.ResponseAccumulator
		for delta, err := range streamer.Stream(t.Context(), request) {
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			assertResponseDelta(t, delta)
			if err := accumulator.Add(delta); err != nil {
				t.Fatalf("ResponseAccumulator.Add: %v", err)
			}
			deltas = append(deltas, delta)
		}
		if len(deltas) == 0 {
			t.Fatal("Stream yielded no response deltas")
		}
		if after := requestWire(t, request); !bytes.Equal(before, after) {
			t.Fatalf("Stream mutated Request\nbefore: %s\nafter:  %s", before, after)
		}
		if contract.AssertStream != nil {
			contract.AssertStream(t, deltas)
		}
		aggregated, err := accumulator.Response()
		if err != nil {
			t.Fatalf("ResponseAccumulator.Response: %v", err)
		}
		assertResponse(t, aggregated)
		if contract.AssertAggregated != nil {
			contract.AssertAggregated(t, aggregated)
		}
	})
}

func assertResponseDelta(t *testing.T, delta *chat.ResponseDelta) {
	t.Helper()
	if delta == nil {
		t.Fatal("provider yielded nil ResponseDelta without error")
	}
	if err := delta.Validate(); err != nil {
		t.Fatalf("ResponseDelta.Validate: %v", err)
	}
}

func (c ChatContract) validRequest(t *testing.T) *chat.Request {
	t.Helper()
	request := c.Request(t)
	if request == nil {
		t.Fatal("provider returned nil Request fixture")
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("Request.Validate: %v", err)
	}
	return request
}

func assertResponse(t *testing.T, response *chat.Response) {
	t.Helper()
	if response == nil {
		t.Fatal("provider yielded nil Response without error")
	}
	if err := response.Validate(); err != nil {
		t.Fatalf("Response.Validate: %v", err)
	}
}

func requestWire(t *testing.T, request *chat.Request) []byte {
	t.Helper()
	body, err := jsonv2.Marshal(request)
	if err != nil {
		t.Fatalf("marshal Request: %v", err)
	}
	return body
}
