package openai_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"

	"github.com/Tangerg/scope/models/protocol/openai"
)

func ExampleResponses_ListModels() {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			_, _ = io.WriteString(writer, `{"data":[{"id":"first"},{"id":"second"}]}`)
			return
		}
		writer.Header().Set("Retry-After", "3")
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(writer, `{"error":{"message":"slow down","type":"rate_limit_error"}}`)
	}))
	defer server.Close()
	model, err := openai.NewResponses(context.Background(), openai.ResponsesConfig{
		APIKey: "test-key", BaseURL: server.URL + "/v1", HTTPClient: server.Client(),
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	ids, err := model.ListModels(context.Background(), 10, 1024)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(ids)
	_, err = model.ListModels(context.Background(), 10, 1024)
	type httpFailure interface {
		error
		HTTPStatus() int
		HTTPHeader() http.Header
	}
	if failure, ok := errors.AsType[httpFailure](err); ok {
		fmt.Printf("status: %d, retry after: %s\n", failure.HTTPStatus(), failure.HTTPHeader().Get("Retry-After"))
	}
	// Output:
	// [first second]
	// status: 429, retry after: 3
}
