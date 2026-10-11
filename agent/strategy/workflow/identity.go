package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"

	agent "github.com/Tangerg/scope/agent"
)

func workflowChildKey(parts ...string) (agent.ChildKey, error) {
	return agent.ParseChildKey(workflowIdentity("child", parts...))
}

func workflowIdentity(kind string, parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		hash.Write([]byte(strconv.Itoa(len(part))))
		hash.Write([]byte{':'})
		hash.Write([]byte(part))
	}
	return "workflow:" + kind + ":" + hex.EncodeToString(hash.Sum(nil))
}
