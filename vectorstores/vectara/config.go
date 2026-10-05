package vectara

import (
	"cmp"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strings"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/vectorstore"
)

const (
	Provider                = "Vectara"
	DefaultEndpoint         = "https://api.vectara.io"
	DefaultMaxResponseBytes = int64(16 * 1024 * 1024)
	apiVersion              = "v2"
	metadataField           = "metadata_json"
	listPageSize            = 100
	identityGroupSize       = 128
	nativeFilterMaxRunes    = 8000
	nativeCorpusKeyMaxBytes = 50
	nativeDocumentType      = "core"
	nativeTextResultType    = "text"
	nativeRerankerNone      = "none"
	nativeMinimumScore      = -1.0
	nativeMaximumScore      = 1.0
)

type StoreConfig struct {
	Endpoint        string
	APIKey          string
	CorpusKey       string
	DocumentBatcher vectorstore.Batcher
	// HTTPClient owns transport and request lifetime; Scope never retries calls.
	HTTPClient       *http.Client
	MaxResponseBytes int64
}

func (s StoreConfig) Validate() error {
	address, err := url.Parse(cmp.Or(s.Endpoint, DefaultEndpoint))
	if err != nil || (address.Scheme != "https" && address.Scheme != "http") || address.Host == "" || address.User != nil || address.RawQuery != "" || address.Fragment != "" {
		return errors.New("vectara: Endpoint must be an HTTP origin or base path without user info, query or fragment")
	}
	if strings.TrimSpace(s.APIKey) == "" {
		return errors.New("vectara: APIKey is required")
	}
	if s.CorpusKey == "" || len(s.CorpusKey) > nativeCorpusKeyMaxBytes {
		return errors.New("vectara: invalid CorpusKey")
	}
	for _, char := range s.CorpusKey {
		allowed := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-' || char == '='
		if !allowed {
			return errors.New("vectara: invalid CorpusKey")
		}
	}
	if lo.IsNil(s.DocumentBatcher) {
		return errors.New("vectara: DocumentBatcher is required")
	}
	if s.MaxResponseBytes < 0 || s.MaxResponseBytes == math.MaxInt64 {
		return errors.New("vectara: invalid MaxResponseBytes")
	}
	return nil
}
