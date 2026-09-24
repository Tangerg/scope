package interaction

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/chat"
)

func benchmarkConversation(messages int) []chat.Message {
	conversation := make([]chat.Message, messages)
	for index := range conversation {
		conversation[index] = chat.NewUserMessage(
			chat.NewTextPart(fmt.Sprintf("message %d: %s", index, longFillerText)),
		)
	}
	return conversation
}

// A paragraph rather than a word, so the measurement reflects a real turn.
const longFillerText = "the quick brown fox jumps over the lazy dog, and then it does so again, " +
	"and again, until the sentence is long enough to resemble a real turn in a " +
	"conversation rather than a single token of filler text used in a microbenchmark."

// The dispatcher clones the conversation and compares two copies of it on every
// reduced model call, so these measure the two halves against the same
// conversation. The comparison is the part that can surprise: a clone is
// proportional to the messages, while reflect.DeepEqual walks an interface
// slice and a metadata map per message and exits early only when the lengths
// differ.
func BenchmarkCloneMessages(b *testing.B) {
	for _, size := range []int{50, 200, 800} {
		conversation := benchmarkConversation(size)
		b.Run(fmt.Sprintf("messages=%d", size), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = cloneMessages(conversation)
			}
		})
	}
}

// DeepEqualUnchanged is the case the dispatcher hits whenever a reducer
// returns the conversation it was given: no early exit, every message walked.
func BenchmarkDeepEqualUnchanged(b *testing.B) {
	for _, size := range []int{50, 200, 800} {
		conversation := benchmarkConversation(size)
		clone := cloneMessages(conversation)
		b.Run(fmt.Sprintf("messages=%d", size), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if !reflect.DeepEqual(conversation, clone) {
					b.Fatal("clone must compare equal")
				}
			}
		})
	}
}

// DeepEqualTrimmed is the case a trimming reducer produces: the length differs,
// which reflect.DeepEqual rejects before looking at any message.
func BenchmarkDeepEqualTrimmed(b *testing.B) {
	for _, size := range []int{50, 200, 800} {
		conversation := benchmarkConversation(size)
		trimmed := cloneMessages(conversation)[size/2:]
		b.Run(fmt.Sprintf("messages=%d", size), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if reflect.DeepEqual(conversation, trimmed) {
					b.Fatal("trimmed must compare unequal")
				}
			}
		})
	}
}
