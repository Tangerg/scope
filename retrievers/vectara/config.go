package vectara

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"net/http"
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

// validCorpusKey admits Vectara's corpus key alphabet within its length bound.
func validCorpusKey(key string) bool {
	if key == "" || len(key) > nativeCorpusKeyMaxBytes {
		return false
	}
	for _, char := range key {
		allowed := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-' || char == '='
		if !allowed {
			return false
		}
	}
	return true
}

func (c Config) validateConnection() error {
	if _, err := httpclient.ParseBaseURL(cmp.Or(c.Endpoint, DefaultEndpoint)); err != nil {
		return errors.New("vectara: Endpoint must be an HTTP origin or base path without user info, query or fragment")
	}
	if strings.TrimSpace(c.APIKey) == "" {
		return errors.New("vectara: APIKey is required")
	}
	if !validCorpusKey(c.CorpusKey) {
		return errors.New("vectara: invalid CorpusKey")
	}
	if c.Limit < 0 || c.Limit > maximumResults {
		return fmt.Errorf("vectara: Limit must be in [0,%d]", maximumResults)
	}
	if c.MaxResponseBytes < 0 || c.MaxResponseBytes == math.MaxInt64 {
		return errors.New("vectara: invalid MaxResponseBytes")
	}
	return nil
}
