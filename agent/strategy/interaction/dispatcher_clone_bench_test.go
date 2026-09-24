package interaction

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/chat"
)

// The reducer path clones the conversation three times per model call and then
// compares two copies of it. The comparison is the part worth measuring: a
// clone is proportional to the messages, but reflect.DeepEqual walks an
// interface slice and a metadata map per message, and it only returns early
// when the reducer actually changed something.
//
// These run the two halves against the same conversation so their costs are
// directly comparable.
func benchmarkConversation(messages int) []chat.Message {
	conversation := make([]chat.Message, messages)
	for index := range conversation {
		conversation[index] = chat.NewUserMessage(
			chat.NewTextPart(fmt.Sprintf("message %d: %s", index, longFillerText)),
		)
	}
	return conversation
}

// Roughly a paragraph, so a message is a realistic size rather than a word.
const longFillerText = "the quick brown fox jumps over the lazy dog, and then it does so again, " +
	"and again, until the sentence is long enough to resemble a real turn in a " +
	"conversation rather than a single token of filler text used in a microbenchmark."

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
