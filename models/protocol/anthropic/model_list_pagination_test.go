package anthropic_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/scope/models/protocol/anthropic"
)

func TestListModelsUsesSDKPaginationAndTotalBudget(t *testing.T) {
	const first = `{"data":[{"id":"first"}],"has_more":true,"first_id":"first","last_id":"first"}`
	const second = `{"data":[{"id":"second"}],"has_more":false,"first_id":"second","last_id":"second"}`
	cursors := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		cursor := request.URL.Query().Get("after_id")
		cursors <- cursor
		if request.URL.Path != "/gateway/v1/models" || request.URL.Query().Get("limit") != "2" {
			t.Errorf("pagination URL = %s", request.URL)
		}
		if request.Header.Get("X-Api-Key") != "test-key" || request.Header.Get("Anthropic-Version") != "2023-06-01" {
			t.Error("pagination did not retain provider authentication")
		}
		writer.Header().Set("Content-Type", "application/json")
		switch cursor {
		case "":
			_, _ = io.WriteString(writer, first)
		case "first":
			_, _ = io.WriteString(writer, second)
		default:
			t.Errorf("unexpected cursor %q", cursor)
			writer.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	model, err := anthropic.NewMessages(t.Context(), anthropic.MessagesConfig{APIKey: "test-key", BaseURL: server.URL + "/gateway", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	ids, err := model.ListModels(t.Context(), 2, int64(len(first)+len(second)))
	if err != nil || !reflect.DeepEqual(ids, []string{"first", "second"}) {
		t.Fatalf("paginated list: IDs = %v, error = %v", ids, err)
	}
	if got, want := []string{<-cursors, <-cursors}, []string{"", "first"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cursors = %v; want %v", got, want)
	}
	ids, err = model.ListModels(t.Context(), 2, int64(len(first)+len(second)-1))
	if _, ok := errors.AsType[*http.MaxBytesError](err); !ok || ids != nil {
		t.Fatalf("total byte budget: IDs = %v, error = %v", ids, err)
	}
}

func TestListModelsRejectsFailuresAfterTheFirstPage(t *testing.T) {
	for name, test := range map[string]struct {
		body      string
		maxModels int
	}{
		"invalid JSON":         {"{", 10},
		"repeated cursor":      {`{"data":[{"id":"first"}],"has_more":true,"last_id":"first"}`, 10},
		"empty page with more": {`{"data":[],"has_more":true,"last_id":"second"}`, 10},
		"invalid cursor type":  {`{"data":[{"id":"second"}],"has_more":true,"last_id":12}`, 10},
		"model limit":          {`{"data":[{"id":"second"},{"id":"third"}],"has_more":false}`, 2},
	} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				writer.Header().Set("Content-Type", "application/json")
				if request.URL.Query().Get("after_id") == "" {
					_, _ = io.WriteString(writer, `{"data":[{"id":"first"}],"has_more":true,"last_id":"first"}`)
					return
				}
				_, _ = io.WriteString(writer, test.body)
			}))
			defer server.Close()
			model, err := anthropic.NewMessages(t.Context(), anthropic.MessagesConfig{APIKey: "test-key", BaseURL: server.URL, HTTPClient: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			ids, err := model.ListModels(t.Context(), test.maxModels, 4096)
			if err == nil || ids != nil {
				t.Fatalf("partial list escaped: IDs = %v, error = %v", ids, err)
			}
			if calls.Load() != 2 {
				t.Fatalf("HTTP calls = %d; want 2", calls.Load())
			}
		})
	}
}

func TestListModelsStopsBeforeFetchingBeyondModelLimit(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"data":[{"id":"first"}],"has_more":true,"last_id":"first"}`)
	}))
	defer server.Close()
	model, err := anthropic.NewMessages(t.Context(), anthropic.MessagesConfig{APIKey: "test-key", BaseURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	ids, err := model.ListModels(t.Context(), 1, 4096)
	if err == nil || ids != nil || calls.Load() != 1 {
		t.Fatalf("incomplete list: IDs = %v, error = %v, HTTP calls = %d", ids, err, calls.Load())
	}
}

func ExampleMessages_ListModels() {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Query().Get("after_id") == "" {
			_, _ = io.WriteString(writer, `{"data":[{"id":"first"}],"has_more":true,"last_id":"first"}`)
			return
		}
		_, _ = io.WriteString(writer, `{"data":[{"id":"second"}],"has_more":false,"last_id":"second"}`)
	}))
	defer server.Close()
	model, err := anthropic.NewMessages(context.Background(), anthropic.MessagesConfig{APIKey: "test-key", BaseURL: server.URL, HTTPClient: server.Client()})
	if err != nil {
		fmt.Println(err)
		return
	}
	ids, err := model.ListModels(context.Background(), 2, 1024)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(ids)
	ids, err = model.ListModels(context.Background(), 1, 1024)
	fmt.Println("incomplete result rejected:", ids == nil && err != nil)
	// Output:
	// [first second]
	// incomplete result rejected: true
}
