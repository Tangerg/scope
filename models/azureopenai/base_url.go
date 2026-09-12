package azureopenai

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const protocolProvider = "azureopenai"

// Config identifies one Azure OpenAI endpoint and its transport.
type Config struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

type endpointConfig struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

func (c Config) resolve() (endpointConfig, error) {
	if c.APIKey == "" {
		return endpointConfig{}, errors.New("azureopenai: APIKey is required")
	}
	baseURL, err := normalizeBaseURL(c.BaseURL)
	if err != nil {
		return endpointConfig{}, err
	}
	return endpointConfig{apiKey: c.APIKey, baseURL: baseURL, httpClient: c.HTTPClient}, nil
}

func (c Config) Validate() error {
	_, err := c.resolve()
	return err
}

func (c Config) resolveModel(model string, validateOptions func() error) (endpointConfig, error) {
	endpoint, err := c.resolve()
	if err != nil {
		return endpointConfig{}, err
	}
	if model == "" {
		return endpointConfig{}, errors.New("azureopenai: DefaultOptions.Model is required")
	}
	if err := validateOptions(); err != nil {
		return endpointConfig{}, fmt.Errorf("azureopenai: DefaultOptions: %w", err)
	}
	return endpoint, nil
}

func (c Config) resolveChat(validateOptions func() error) (endpointConfig, error) {
	endpoint, err := c.resolve()
	if err != nil {
		return endpointConfig{}, err
	}
	if err := validateOptions(); err != nil {
		return endpointConfig{}, fmt.Errorf("azureopenai: DefaultOptions: %w", err)
	}
	return endpoint, nil
}

func normalizeBaseURL(baseURL string) (string, error) {
	if baseURL == "" {
		return "", errors.New("azureopenai: BaseURL is required")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", errors.New("azureopenai: BaseURL must be a valid absolute URL")
	}
	if parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("azureopenai: BaseURL must be an absolute URL without credentials, query parameters, or fragments")
	}
	if !strings.HasSuffix(parsed.Path, "/openai/v1") && !strings.HasSuffix(parsed.Path, "/openai/v1/") {
		return "", errors.New("azureopenai: BaseURL must end with /openai/v1/")
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/") + "/"
	return parsed.String(), nil
}
