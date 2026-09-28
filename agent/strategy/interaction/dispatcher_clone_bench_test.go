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
