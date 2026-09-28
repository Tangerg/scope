package trajectory_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/Tangerg/scope/agent"
)

func TestBehaviorDigestCanonicalizesHostOutput(t *testing.T) {
	recorded := runTrajectory(t)
	const canonical = `{"label":"<value>","nested":{"a":1,"b":2},"number":9007199254740993,"values":[1,2]}`
	baseline, err := recorded.BehaviorDigest(func(agent.Payload) (json.RawMessage, error) {
		return json.RawMessage(canonical), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		` { "number":9007199254740993, "values":[1,2], "nested":{"b":2,"a":1}, "label":"\u003cvalue\u003e" } `,
		canonical,
	} {
		owned := json.RawMessage(raw)
		before := bytes.Clone(owned)
		digest, err := recorded.BehaviorDigest(func(agent.Payload) (json.RawMessage, error) { return owned, nil })
		if err != nil || digest != baseline {
			t.Errorf("BehaviorDigest(%s) = %s, %v; want %s", raw, digest, err, baseline)
		}
		if !bytes.Equal(owned, before) {
			t.Errorf("BehaviorDigest mutated projection from %s to %s", before, owned)
		}
	}
	for _, changed := range []string{
		`{"label":"<value>","nested":{"a":1,"b":2},"number":9007199254740992,"values":[1,2]}`,
		`{"label":"<value>","nested":{"a":1,"b":2},"number":9007199254740993,"values":[2,1]}`,
	} {
		digest, err := recorded.BehaviorDigest(func(agent.Payload) (json.RawMessage, error) { return json.RawMessage(changed), nil })
		if err != nil {
			t.Fatal(err)
		}
		if digest == baseline {
			t.Errorf("BehaviorDigest ignored semantic output change: %s", changed)
		}
	}
}
