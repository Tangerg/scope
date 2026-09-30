package agent

import (
	"slices"
	"testing"
)

func TestRunQueueKeepsMembershipAndOrderTogether(t *testing.T) {
	first, second, third := newProcessID(), newProcessID(), newProcessID()
	queue := newRunQueue(0)
	for _, processID := range []ProcessID{first, second, first, third} {
		queue.push(processID)
	}
	queue.remove(second)
	queue.remove(newProcessID())
	var popped []ProcessID
	for {
		processID, queued := queue.pop()
		if !queued {
			break
		}
		popped = append(popped, processID)
	}
	if !slices.Equal(popped, []ProcessID{first, third}) || !queue.empty() || queue.contains(first) {
		t.Fatalf("popped %v, empty=%t", popped, queue.empty())
	}
	queue.push(first)
	queue.clear()
	if !queue.empty() || queue.contains(first) {
		t.Fatal("clear retained a queued Process")
	}
}
