package managed

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	jsonv2 "encoding/json/v2"
	"strings"
	"unicode/utf8"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/Tangerg/scope/agent/run"
)

const (
	maxMessageIDBytes      = 1024
	maxContextIDBytes      = 1024
	maxTaskIDBytes         = 1500
	authorityScopePrefix   = "a2a:"
	generatedContextPrefix = "ctx:"
	taskIDSeparator        = "."
)

type taskKey struct {
	id      sdk.TaskID
	context string
	start   run.StartID
}

func taskKeyFromMessage(scope string, message *sdk.Message) (taskKey, error) {
	if message == nil || strings.TrimSpace(message.ID) == "" || len(message.ID) > maxMessageIDBytes || len(message.ContextID) > maxContextIDBytes || !utf8.ValidString(message.ID) || !utf8.ValidString(message.ContextID) {
		return taskKey{}, sdk.ErrInvalidParams
	}
	if message.TaskID != "" {
		key, err := parseTaskKey(scope, message.TaskID)
		if err != nil {
			return taskKey{}, err
		}
		if message.ContextID != "" && message.ContextID != key.context {
			return taskKey{}, sdk.ErrInvalidParams
		}
		return key, nil
	}
	contextID := message.ContextID
	if contextID == "" {
		id, err := identityDigest(scope, message.ID)
		if err != nil {
			return taskKey{}, sdk.ErrInvalidParams
		}
		contextID = generatedContextPrefix + id
	}
	key, err := identityDigest(contextID, message.ID)
	if err != nil {
		return taskKey{}, sdk.ErrInvalidParams
	}
	token := base64.RawURLEncoding.EncodeToString([]byte(contextID)) + taskIDSeparator + key
	return parseTaskKey(scope, sdk.TaskID(token))
}

func parseTaskKey(scope string, id sdk.TaskID) (taskKey, error) {
	if len(id) > maxTaskIDBytes {
		return taskKey{}, sdk.ErrTaskNotFound
	}
	contextToken, messageDigest, ok := strings.Cut(string(id), taskIDSeparator)
	if !ok || len(messageDigest) != hex.EncodedLen(sha256.Size) {
		return taskKey{}, sdk.ErrTaskNotFound
	}
	if _, err := hex.DecodeString(messageDigest); err != nil {
		return taskKey{}, sdk.ErrTaskNotFound
	}
	text, err := base64.RawURLEncoding.DecodeString(contextToken)
	if err != nil || len(text) == 0 || len(text) > maxContextIDBytes || !utf8.Valid(text) || base64.RawURLEncoding.EncodeToString(text) != contextToken {
		return taskKey{}, sdk.ErrTaskNotFound
	}
	key, err := identityDigest(string(id))
	if err != nil {
		return taskKey{}, sdk.ErrTaskNotFound
	}
	return taskKey{id: id, context: string(text), start: run.StartID{Scope: scope, Key: key}}, nil
}

func identityDigest(parts ...string) (string, error) {
	encoded, err := jsonv2.Marshal(parts)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
