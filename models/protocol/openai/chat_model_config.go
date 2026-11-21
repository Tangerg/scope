package openai

import (
	"fmt"
	"net/http"

	corechat "github.com/Tangerg/scope/core/chat"
)

// ChatModelConfig binds either chat protocol, Chat Completions or Responses.
// DefaultOptions are copied during construction; callers may select the model
// per request. MaxRetries replaces the SDK retry count for every request the
// binding sends: nil keeps the SDK default, and 0 sends each request once.
type ChatModelConfig struct {
	APIKey         string
	DefaultOptions corechat.Options
	BaseURL        string
	HTTPClient     *http.Client
	Headers        http.Header
	MaxRetries     *int
}

func (c ChatModelConfig) Validate() error {
	if _, _, err := c.api().admit(); err != nil {
		return err
	}
	if err := c.DefaultOptions.Validate(); err != nil {
		return fmt.Errorf("openai: DefaultOptions: %w", err)
	}
	return nil
}

func (c ChatModelConfig) api() apiConfig {
	return apiConfig{APIKey: c.APIKey, BaseURL: c.BaseURL, HTTPClient: c.HTTPClient, Headers: c.Headers, MaxRetries: c.MaxRetries}
}
