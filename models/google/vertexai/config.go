package vertexai

import (
	"errors"
	"fmt"
	"net/http"

	"google.golang.org/genai"

	"github.com/Tangerg/scope/models/google/internal/protocol"
)

const protocolProvider = "vertexai"

// HTTPClient remains caller-owned and is never closed by a model; BaseURL is
// reserved for compatible gateways and test servers.
type ClientConfig struct {
	Project    string
	Location   string
	BaseURL    string
	HTTPClient *http.Client
}

func (c ClientConfig) protocol() protocol.ClientConfig {
	return protocol.ClientConfig{
		Backend: genai.BackendVertexAI, Project: c.Project, Location: c.Location,
		BaseURL: c.BaseURL, HTTPClient: c.HTTPClient,
	}
}

type configOptions interface {
	Validate() error
}

func (c ClientConfig) Validate() error {
	if c.Project == "" {
		return errors.New("vertexai: project is required")
	}
	if c.Location == "" {
		return errors.New("vertexai: location is required")
	}
	return nil
}

func (c ClientConfig) validateOptions(options configOptions) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if err := options.Validate(); err != nil {
		return fmt.Errorf("vertexai: default options: %w", err)
	}
	return nil
}

func (c ClientConfig) validateModelOptions(model string, options configOptions) error {
	if model == "" {
		return errors.New("vertexai: default options model is required")
	}
	return c.validateOptions(options)
}
