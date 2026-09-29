package openai_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/protocol/openai"
)

func TestListModelsUsesTheBoundProvider(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Method != http.MethodGet || request.URL.Path != "/proxy/prefix/models" {
			t.Errorf("request = %s %s", request.Method, request.URL)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("authentication = %q", got)
		}
		if got := request.Header.Get("X-Provider-Binding"); got != "same-binding" {
			t.Errorf("binding header = %q", got)
		}

		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"data":[{"id":"model-b"},{"id":"model-a"},{"id":"model-b"}]}`)
	}))
	defer server.Close()
	model, err := openai.NewChatCompletions(t.Context(), openai.ChatCompletionsConfig{
		APIKey: "test-key", BaseURL: server.URL + "/proxy/prefix", HTTPClient: server.Client(),
		Headers: http.Header{"X-Provider-Binding": {"same-binding"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("construction performed HTTP I/O")
	}
	ids, err := model.ListModels(t.Context(), 3, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"model-b", "model-a", "model-b"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("IDs = %v; want %v", ids, want)
	}
	if calls.Load() != 1 {
		t.Errorf("HTTP calls = %d; want 1", calls.Load())
	}
}

func TestListModelsRejectsIncompleteOrMalformedResponses(t *testing.T) {
	for name, body := range map[string]string{
		"invalid JSON":           "{",
		"trailing JSON":          `{"data":[{"id":"first"},{"id":"second"}]} {}`,
		"invalid UTF-8":          "{\"data\":[{\"id\":\"\xff\"}]}",
		"duplicate field":        `{"data":[],"data":[]}`,
		"null response":          `null`,
		"missing data":           `{"other":[]}`,
		"null data":              `{"data":null}`,
		"object data":            `{"data":{}}`,
		"invalid ID type":        `{"data":[{"id":12}]}`,
		"empty ID":               `{"data":[{"id":""}]}`,
		"missing ID":             `{"data":[{}]}`,
		"unsupported pagination": `{"data":[{"id":"one"}],"has_more":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(writer, body)
			}))
			defer server.Close()
			model, err := openai.NewChatCompletions(t.Context(), openai.ChatCompletionsConfig{APIKey: "test-key", BaseURL: server.URL, HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			ids, err := model.ListModels(t.Context(), 10, 4096)
			if err == nil || ids != nil {
				t.Fatalf("IDs = %v, error = %v; want nil IDs and an error", ids, err)
			}
		})
	}
}

func TestListModelsEnforcesBudgets(t *testing.T) {
	const body = `{"data":[{"id":"first"},{"id":"second"}]}`
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, body)
	}))
	defer server.Close()
	model, err := openai.NewChatCompletions(t.Context(), openai.ChatCompletionsConfig{APIKey: "test-key", BaseURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	for _, limits := range []struct {
		models int
		bytes  int64
	}{{0, 1}, {-1, 1}, {1, 0}, {1, -1}} {
		if ids, limitErr := model.ListModels(t.Context(), limits.models, limits.bytes); limitErr == nil || ids != nil {
			t.Fatalf("invalid budgets returned IDs %v, error %v", ids, limitErr)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid budgets performed HTTP I/O")
	}
	ids, err := model.ListModels(t.Context(), 2, int64(len(body)))
	if err != nil || !reflect.DeepEqual(ids, []string{"first", "second"}) {
		t.Fatalf("exact budgets: IDs = %v, error = %v", ids, err)
	}
	ids, err = model.ListModels(t.Context(), 2, int64(len(body)-1))
	if _, ok := errors.AsType[*http.MaxBytesError](err); !ok || ids != nil {
		t.Fatalf("byte limit: IDs = %v, error = %v", ids, err)
	}
	ids, err = model.ListModels(t.Context(), 1, int64(len(body)))
	if err == nil || ids != nil {
		t.Fatalf("model limit: IDs = %v, error = %v", ids, err)
	}
}

func TestListModelsPreservesHTTPFailuresWithoutRetry(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				writer.Header().Set("Content-Type", "application/json")
				writer.Header().Set("Retry-After", "60")
				writer.WriteHeader(status)
				_, _ = io.WriteString(writer, `{"error":{"type":"upstream_error","message":"unavailable"}}`)
			}))
			defer server.Close()
			model, err := openai.NewChatCompletions(t.Context(), openai.ChatCompletionsConfig{APIKey: "test-key", BaseURL: server.URL, HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			ids, err := model.ListModels(t.Context(), 10, 4096)
			type httpFailure interface {
				error
				HTTPStatus() int
				HTTPHeader() http.Header
			}
			failure, ok := errors.AsType[httpFailure](err)
			if !ok || failure.HTTPStatus() != status || failure.HTTPHeader().Get("Retry-After") != "60" || ids != nil {
				t.Fatalf("HTTP failure: IDs = %v, error = %v", ids, err)
			}
			if calls.Load() != 1 {
				t.Fatalf("HTTP calls = %d; want 1", calls.Load())
			}
		})
	}
}

func TestListModelsCancellationReachesHTTP(t *testing.T) {
	server, lifecycle := modeltest.NewBlockingServer(t, nil)
	model, err := openai.NewChatCompletions(t.Context(), openai.ChatCompletionsConfig{APIKey: "test-key", BaseURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		ids, err := model.ListModels(ctx, 10, 4096)
		if ids != nil {
			done <- errors.New("canceled discovery returned IDs")
			return
		}
		done <- err
	}()
	<-lifecycle.Started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	<-lifecycle.Stopped
}

func TestListModelsAcceptsEmptyCompleteList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.Copy(writer, strings.NewReader(`{"data":[]}`))
	}))
	defer server.Close()
	model, err := openai.NewChatCompletions(t.Context(), openai.ChatCompletionsConfig{APIKey: "test-key", BaseURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	ids, err := model.ListModels(t.Context(), 1, 4096)
	if err != nil || len(ids) != 0 {
		t.Fatalf("empty list: IDs = %v, error = %v", ids, err)
	}
}

type modelListClosingBody struct {
	io.Reader
	closeErr error
	closed   int
}

func (m *modelListClosingBody) Close() error {
	m.closed++
	return m.closeErr
}

type modelListTransport struct{ body io.ReadCloser }

func (m modelListTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: m.body}, nil
}

func TestListModelsOwnsResponseCloseAndPreservesFailure(t *testing.T) {
	closeErr := errors.New("response close failed")
	body := &modelListClosingBody{Reader: strings.NewReader(`{"data":[]}`), closeErr: closeErr}
	client := &http.Client{Transport: modelListTransport{body: body}}
	model, err := openai.NewChatCompletions(t.Context(), openai.ChatCompletionsConfig{APIKey: "test-key", HTTPClient: client})
	if err != nil {
		t.Fatal(err)
	}
	ids, err := model.ListModels(t.Context(), 10, 4096)
	if !errors.Is(err, closeErr) || ids != nil || body.closed != 1 {
		t.Fatalf("response ownership: IDs = %v, error = %v, close calls = %d", ids, err, body.closed)
	}
}
