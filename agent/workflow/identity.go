package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	"github.com/Tangerg/scope/agent/run"
)

const workflowChildKeyPrefix = "workflow:child:"

func workflowChildKey(parts ...string) (run.ChildKey, error) {
	hash := sha256.New()
	for _, part := range parts {
		hash.Write([]byte(strconv.Itoa(len(part))))
		hash.Write([]byte{':'})
		hash.Write([]byte(part))
	}
	return run.ParseChildKey(workflowChildKeyPrefix + hex.EncodeToString(hash.Sum(nil)))
}
