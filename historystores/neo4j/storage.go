package neo4j

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"regexp"
	"slices"

	"github.com/neo4j/neo4j-go-driver/v6/neo4j"

	"github.com/Tangerg/scope/core/chat"
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func validIdentifier(value string) bool { return identifierPattern.MatchString(value) }

func decodeMessages(raw []byte) ([]chat.Message, error) {
	var messages []chat.Message
	if err := jsonv2.Unmarshal(raw, &messages); err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		return nil, errors.New("stored append has no messages")
	}
	return messages, nil
}

func recordStrings(record *neo4j.Record, key string) ([]string, error) {
	value, found := record.Get(key)
	if !found {
		return nil, fmt.Errorf("missing %s", key)
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a string list", key)
	}
	strings := make([]string, len(items))
	for index, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("%s must contain strings", key)
		}
		strings[index] = text
	}
	return strings, nil
}

func sameStrings(left, right []string) bool {
	left = slices.Clone(left)
	right = slices.Clone(right)
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
}

func requiredRecordValue[T neo4j.RecordValue](record *neo4j.Record, key string) (T, error) {
	value, absent, err := neo4j.GetRecordValue[T](record, key)
	if err != nil {
		return value, err
	}
	if absent {
		return value, fmt.Errorf("record value %s is null", key)
	}
	return value, nil
}

func requireClosed(result *neo4j.EagerResult) error {
	if len(result.Records) != 1 {
		return errors.New("neo4j: missing closure acknowledgment")
	}
	closed, err := requiredRecordValue[bool](result.Records[0], fieldClosed)
	if err != nil || !closed {
		return errors.New("neo4j: conversation was not closed")
	}
	return nil
}
