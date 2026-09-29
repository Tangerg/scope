package openai

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"net/http"
)

type modelListResponseBudget struct {
	remaining int64
}

func (m *modelListResponseBudget) read(request *http.Request, next func(*http.Request) (*http.Response, error)) (*http.Response, error) {
	response, err := next(request)
	if err != nil {
		return nil, err
	}
	body := http.MaxBytesReader(nil, response.Body, m.remaining)
	content, readErr := io.ReadAll(body)
	closeErr := body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, fmt.Errorf("openai: read model list response: %w", err)
	}
	m.remaining -= int64(len(content))
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices && !jsontext.Value(content).IsValid() {
		return nil, errors.New("openai: model list response is not valid JSON")
	}
	response.Body = io.NopCloser(bytes.NewReader(content))
	return response, nil
}
