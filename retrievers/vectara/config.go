package vectara

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"

	"github.com/Tangerg/scope/core/httpclient"
)

const (
	Provider                = "Vectara"
	DefaultLimit            = 5
	maximumResults          = 100
	DefaultEndpoint         = "https://api.vectara.io"
	DefaultMaxResponseBytes = int64(16 * 1024 * 1024)
	apiVersion              = "v2"
	nativeFilterMaxRunes    = 8000
	nativeCorpusKeyMaxBytes = 50
	nativeDocumentType      = "core"
	nativeTextResultType    = "text"
	nativeRerankerNone      = "none"
)

type Config struct {
	Endpoint  string
	APIKey    string
	CorpusKey string
	// AllowedDocumentIDs fixes the host-authorized scope. Nil allows the whole
	// corpus; a non-nil empty slice denies every document. New freezes the list.
	AllowedDocumentIDs []string
	// Limit bounds passages per query; zero uses DefaultLimit.
	Limit int
	// HTTPClient remains host-owned. Scope snapshots client settings and
	// restricts requests to Endpoint's origin while sharing its transport and jar.
	HTTPClient       *http.Client
	MaxResponseBytes int64
}

func (c Config) Validate() error {
	if err := c.validateConnection(); err != nil {
		return err
	}
	_, err := bindScope(c.AllowedDocumentIDs)
	return err
}
func (c Config) validateConnection() error {
	address, err := url.Parse(cmp.Or(c.Endpoint, DefaultEndpoint))
	_, originErr := httpclient.NewOrigin(context.Background(), address)
	if err != nil || originErr != nil || strings.ContainsAny(c.Endpoint, "?#") {
		return errors.New("vectara: Endpoint must be an HTTP origin or base path without user info, query or fragment")
	}
	if strings.TrimSpace(c.APIKey) == "" {
		return errors.New("vectara: APIKey is required")
	}
	if c.CorpusKey == "" || len(c.CorpusKey) > nativeCorpusKeyMaxBytes {
		return errors.New("vectara: invalid CorpusKey")
	}
	for _, char := range c.CorpusKey {
		allowed := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-' || char == '='
		if !allowed {
			return errors.New("vectara: invalid CorpusKey")
		}
	}
	if c.Limit < 0 || c.Limit > maximumResults {
		return fmt.Errorf("vectara: Limit must be in [0,%d]", maximumResults)
	}
	if c.MaxResponseBytes < 0 || c.MaxResponseBytes == math.MaxInt64 {
		return errors.New("vectara: invalid MaxResponseBytes")
	}
	return nil
}
