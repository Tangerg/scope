package anthropic

import "fmt"

const (
	headerAuthorization      = "Authorization"
	headerProxyAuthorization = "Proxy-Authorization"
	headerXAPIKey            = "X-Api-Key"
	headerAPIKey             = "Api-Key"
	headerCookie             = "Cookie"
	headerAnthropicVersion   = "Anthropic-Version"
)

// Credential placement belongs to the endpoint binding. Ordinary metadata
// cannot replace a key or select a second authentication mechanism.
type APIKeyScheme uint8

const (
	APIKeySchemeBearer APIKeyScheme = iota + 1
	APIKeySchemeXAPIKey
	APIKeySchemeAPIKey
)

func (a APIKeyScheme) credential(key string) (string, string, error) {
	switch a {
	case APIKeySchemeBearer:
		return headerAuthorization, "Bearer " + key, nil
	case APIKeySchemeXAPIKey:
		return headerXAPIKey, key, nil
	case APIKeySchemeAPIKey:
		return headerAPIKey, key, nil
	default:
		return "", "", fmt.Errorf("anthropic: invalid API key scheme %d", a)
	}
}
