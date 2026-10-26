package web

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"testing"

	toolcontract "github.com/Tangerg/scope/core/tool"
)

type fakeFetcher struct {
	last *FetchRequest
	resp *FetchResponse
	err  error
}

func (f *fakeFetcher) Fetch(_ context.Context, req *FetchRequest) (*FetchResponse, error) {
	f.last = req
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func TestFetchNewTool_NilFetcher(t *testing.T) {
	_, err := NewFetchTool(nil)
	if !errors.Is(err, ErrMissingFetcher) {
		t.Errorf("NewFetchTool(nil): err = %v, want ErrMissingFetcher", err)
	}
}

func TestFetchNewTool_TypedNilFetcher(t *testing.T) {
	var fetcher *fakeFetcher
	if _, err := NewFetchTool(fetcher); !errors.Is(err, ErrMissingFetcher) {
		t.Fatalf("NewFetchTool(typed nil) error = %v, want ErrMissingFetcher", err)
	}
}

func TestFetchTool_Definition(t *testing.T) {
	tool, err := NewFetchTool(&fakeFetcher{})
	if err != nil {
		t.Fatal(err)
	}
	def := tool.Definition()
	if def.Name != "web_fetch" {
		t.Errorf("Name = %q, want %q", def.Name, "web_fetch")
	}
	if len(def.InputSchema) == 0 {
		t.Error("InputSchema is empty")
	}
}

func TestFetchTool_Call_HappyPath(t *testing.T) {
	fetcher := &fakeFetcher{

		resp: &FetchResponse{Content: "# Hello", Format: FormatMarkdown},
	}
	tool, _ := NewFetchTool(fetcher)
	output, err := invokeTestTool(t.Context(), tool, `{"url":"https://example.com","format":"markdown"}`)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	var resp FetchResponse
	if err := jsonv2.Unmarshal(output.Details, &resp); err != nil {
		t.Fatalf("Unmarshal: %v body=%s", err, output.Details)
	}
	if resp.Content != "# Hello" {
		t.Errorf("Content = %q", resp.Content)
	}
	if resp.Format != FormatMarkdown {
		t.Errorf("Format = %q", resp.Format)
	}
	if fetcher.last == nil || fetcher.last.URL != "https://example.com" {
		t.Errorf("fetcher.Fetch not called as expected: %+v", fetcher.last)
	}
	if fetcher.last.Format != FormatMarkdown {
		t.Errorf("Format forwarded = %q", fetcher.last.Format)
	}
}

func TestFetchTool_Call_EmptyURL(t *testing.T) {
	fetcher := &fakeFetcher{}
	tool, _ := NewFetchTool(fetcher)
	// An empty URL fails schema admission before the Call; a blank URL passes
	// minLength=1 and becomes a definite failure the model can correct.
	if _, err := invokeTestTool(t.Context(), tool, `{"url":""}`); err == nil {
		t.Fatal("Call empty url: want schema admission error")
	}
	_, err := invokeTestTool(t.Context(), tool, `{"url":"   "}`)
	failure, ok := toolcontract.ClassifyFailure(err)
	if !ok || failure.Kind() != toolcontract.FailureKindFailed || !errors.Is(failure.Cause(), ErrEmptyURL) {
		t.Fatalf("Call blank url: err = %v, want FailureKindFailed wrapping ErrEmptyURL", err)
	}
	if fetcher.last != nil {
		t.Error("blank url reached the fetcher")
	}
}

func TestFetchTool_Call_BadJSON(t *testing.T) {
	tool, _ := NewFetchTool(&fakeFetcher{})
	if _, err := invokeTestTool(t.Context(), tool, `{bad json`); err == nil {
		t.Fatal("want error on bad JSON")
	}
}

func TestFetchTool_Call_EnforcesAdvertisedContract(t *testing.T) {
	fetcher := &fakeFetcher{}
	tool, _ := NewFetchTool(fetcher)
	for _, arguments := range []string{
		`{"url":"https://example.com","response_format":"text"}`,
		`{"url":"https://example.com","format":"json"}`,
		`{"url":"relative/path"}`,
		`{"url":"http://:80/page"}`,
	} {
		fetcher.last = nil
		if _, err := invokeTestTool(t.Context(), tool, arguments); err == nil {
			t.Errorf("Call(%s): want contract error", arguments)
		}
		if fetcher.last != nil {
			t.Errorf("Call(%s): invalid arguments reached fetcher", arguments)
		}
	}
}

func TestFetchTool_Call_FetcherError(t *testing.T) {
	fetcher := &fakeFetcher{err: errors.New("fetch boom")}
	tool, _ := NewFetchTool(fetcher)
	_, err := invokeTestTool(t.Context(), tool, `{"url":"https://example.com"}`)
	if err == nil {
		t.Fatal("want error when fetcher fails")
	}
	if !strings.Contains(err.Error(), "fetch boom") {
		t.Errorf("err = %v, want wrapped 'fetch boom'", err)
	}
}

// Unlike the uncertain upstream error in TestFetchTool_Call_FetcherError, a
// pre-request provider rejection must reach the model as a definite failure.
func TestFetchTool_Call_UnsupportedFormatIsDefiniteFailure(t *testing.T) {
	fetcher := &fakeFetcher{err: fmt.Errorf("provider: %w: plain text", ErrUnsupportedFormat)}
	tool, _ := NewFetchTool(fetcher)
	_, err := invokeTestTool(t.Context(), tool, `{"url":"https://example.com","format":"text"}`)
	failure, ok := toolcontract.ClassifyFailure(err)
	if !ok || failure.Kind() != toolcontract.FailureKindFailed || !errors.Is(failure.Cause(), ErrUnsupportedFormat) {
		t.Fatalf("err = %v, want FailureKindFailed wrapping ErrUnsupportedFormat", err)
	}
}

func TestFetchToolRejectsInvalidProviderResponse(t *testing.T) {
	for _, response := range []*FetchResponse{
		nil,
		{Content: "body"},
		{Content: "body", Format: ContentFormat("binary")},
	} {
		tool, err := NewFetchTool(&fakeFetcher{resp: response})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := invokeTestTool(t.Context(), tool, `{"url":"https://example.com"}`); err == nil {
			t.Fatalf("provider response %#v was accepted", response)
		}
	}
}

func TestFetchToolRejectsAResponseInAnotherFormat(t *testing.T) {
	for _, format := range []ContentFormat{FormatHTML, FormatText} {
		t.Run(string(format), func(t *testing.T) {
			executable, err := NewFetchTool(&fakeFetcher{resp: &FetchResponse{Content: "body", Format: format}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := invokeTestTool(t.Context(), executable, `{"url":"https://example.com","format":"markdown"}`); !errors.Is(err, ErrInvalidFetchResponse) {
				t.Fatalf("another format replaced the requested representation: %v", err)
			}
		})
	}
}

func TestFetchContentFormat(t *testing.T) {
	if got, err := ContentFormat("").Normalize(); err != nil || got != FormatMarkdown {
		t.Fatalf("empty ContentFormat.Normalize() = %q, %v; want %q", got, err, FormatMarkdown)
	}
	if got, err := ContentFormat("json").Normalize(); got != "" || !errors.Is(err, ErrInvalidFormat) {
		t.Fatalf("ContentFormat(json).Normalize() = %q, %v", got, err)
	}
	if err := FormatText.Validate(); err != nil {
		t.Fatalf("FormatText.Validate() error = %v", err)
	}
	if err := ContentFormat("json").Validate(); !errors.Is(err, ErrInvalidFormat) {
		t.Fatalf("ContentFormat(json).Validate() error = %v, want ErrInvalidFormat", err)
	}
}

func TestFetchRequestPrepareReturnsNormalizedCopy(t *testing.T) {
	original := &FetchRequest{URL: "  https://example.com/page  "}
	if err := original.Validate(); err != nil {
		t.Fatal(err)
	}
	prepared, err := original.Prepare()
	if err != nil {
		t.Fatal(err)
	}
	if prepared.URL != "https://example.com/page" || prepared.Format != FormatMarkdown {
		t.Fatalf("prepared request = %#v", prepared)
	}
	if original.URL != "  https://example.com/page  " || original.Format != "" {
		t.Fatalf("Prepare mutated its input: %#v", original)
	}
}
