package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	agent "github.com/Tangerg/scope/agent"
)

const workflowChildKeyPrefix = "workflow:child:"

func workflowChildKey(parts ...string) (agent.ChildKey, error) {
	hash := sha256.New()
	for _, part := range parts {
		hash.Write([]byte(strconv.Itoa(len(part))))
		hash.Write([]byte{':'})
		hash.Write([]byte(part))
	}
	return agent.ParseChildKey(workflowChildKeyPrefix + hex.EncodeToString(hash.Sum(nil)))
}
