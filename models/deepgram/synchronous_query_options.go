package deepgram

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/url"
)

const (
	fieldCallback       = "callback"
	fieldCallbackMethod = "callback_method"
)

// Callbacks return a job acknowledgement that neither audio nor transcription
// can represent, so both endpoints admit only synchronous query extensions.
type synchronousQueryOptions url.Values

func (s *synchronousQueryOptions) UnmarshalJSON(data []byte) error {
	var options url.Values
	if err := jsonv2.Unmarshal(data, &options); err != nil {
		return err
	}
	for _, field := range []string{fieldCallback, fieldCallbackMethod} {
		if _, exists := options[field]; exists {
			return fmt.Errorf("deepgram: query option %q requires asynchronous callback delivery", field)
		}
	}
	*s = synchronousQueryOptions(options)
	return nil
}
