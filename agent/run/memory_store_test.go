package run

import (
	"bytes"
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestMemoryStoreReadKeepsItsHeadAndDetachesRecords(t *testing.T) {
	original := publicBoundaryLog(t, false)
	want := controlValue(original.Clone())
	batch := Batch{Root: original.Root, Records: make([]Record, len(original.Entries))}
	for index, entry := range original.Entries {
		batch.Records[index] = entry.Record
	}
	store := NewMemoryStore()
	if err := store.Append(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	started := want.Entries[0].Record.(Started)
	later := SignalAdmitted{Process: want.Root, Signal: Signal{ID: controlValue(ParseSignalID("later")), Payload: started.Input}, At: started.At}
	var read []Entry
	for entry, err := range store.Read(t.Context(), want.Root, 0) {
		if err != nil {
			t.Fatal(err)
		}
		if len(read) == 0 {
			if err := store.Append(t.Context(), Batch{Root: want.Root, Epoch: want.Epoch(), After: want.Head(), Records: []Record{later}}); err != nil {
				t.Fatal(err)
			}
			publicBoundaryMutateRecords(batch.Entries())
		}
		owned := controlValue(cloneEntries([]Entry{entry}))
		read = append(read, owned[0])
		publicBoundaryMutateRecords([]Entry{entry})
	}
	if !bytes.Equal(controlValue(jsonv2.Marshal(read)), controlValue(jsonv2.Marshal(want.Entries))) {
		t.Fatal("read changed its captured head or exposed caller-owned records")
	}
	want = integrityAppend(want, later)
	read = nil
	for entry, err := range store.Read(t.Context(), want.Root, 0) {
		if err != nil {
			t.Fatal(err)
		}
		read = append(read, entry)
	}
	if !bytes.Equal(controlValue(jsonv2.Marshal(read)), controlValue(jsonv2.Marshal(want.Entries))) {
		t.Fatal("mutating a yielded record rewrote committed history")
	}
}

func TestMemoryStoreReadObservesCancellationBetweenEntries(t *testing.T) {
	log := integrityInitialLog(t)
	store := NewMemoryStore()
	if err := store.Append(t.Context(), Batch{Root: log.Root, Records: []Record{log.Entries[0].Record, log.Entries[1].Record}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var entries, failures int
	for _, err := range store.Read(ctx, log.Root, 0) {
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			failures++
			continue
		}
		entries++
		cancel()
	}
	if entries != 1 || failures != 1 {
		t.Fatalf("canceled read yielded %d records and %d errors", entries, failures)
	}
}

func BenchmarkMemoryStoreReadOneEntry(b *testing.B) {
	for _, laterEntries := range []int{64, 4096} {
		b.Run(fmt.Sprint(laterEntries), func(b *testing.B) {
			runtime := kernelRuntime(b, Config{})
			program := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{Definition: &boundaryDefinition{descriptor: kernelDescriptor(b, "benchmark.history"), step: func(context.Context, Turn) (Transition, error) { return Continue(0) }}, Revision: "benchmark"}))
			root := hostStartID().Root()
			started := Started{Process: root, Program: program.ProgramRef(), Input: controlValue(EncodePayload(0)), Limits: DefaultTreeLimits(), At: time.Unix(1, 0).UTC()}
			store := NewMemoryStore()
			if err := store.Append(b.Context(), Batch{Root: root, Records: []Record{started, Activated{Epoch: 1}}}); err != nil {
				b.Fatal(err)
			}
			records := make([]Record, laterEntries)
			for index := range records {
				records[index] = SignalAdmitted{Process: root, Signal: Signal{ID: controlValue(ParseSignalID(fmt.Sprintf("benchmark-%d", index))), Payload: controlValue(EncodePayload(index))}, At: started.At}
			}
			if err := store.Append(b.Context(), Batch{Root: root, Epoch: 1, After: 2, Records: records}); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				count := 0
				for _, err := range store.Read(b.Context(), root, 2) {
					if err != nil {
						b.Fatal(err)
					}
					count++
					break
				}
				if count != 1 {
					b.Fatalf("read %d entries", count)
				}
			}
			b.ReportMetric(1, "yielded_entries")
		})
	}
}

func BenchmarkMemoryStoreAppendPayload(b *testing.B) {
	for _, payloadBytes := range []int{1 << 10, 1 << 20, MaxPayloadBytes} {
		b.Run(fmt.Sprint(payloadBytes), func(b *testing.B) {
			runtime := kernelRuntime(b, Config{})
			program := controlValue(Bind[int, int](runtime.Compiler(), BindingConfig{
				Definition: &boundaryDefinition{descriptor: kernelDescriptor(b, "benchmark.append"), step: func(context.Context, Turn) (Transition, error) { return Continue(0) }},
				Revision:   "benchmark",
			}))
			root := hostStartID().Root()
			started := Started{Process: root, Program: program.ProgramRef(), Input: controlValue(EncodePayload(0)), Limits: DefaultTreeLimits(), At: time.Unix(1, 0).UTC()}
			initial := Batch{Root: root, Records: []Record{started, Activated{Epoch: 1}}}
			payload := controlValue(EncodePayload(strings.Repeat("a", payloadBytes-2)))
			batch := Batch{Root: root, Epoch: 1, After: 2, Records: []Record{SignalAdmitted{
				Process: root, Signal: Signal{ID: controlValue(ParseSignalID("benchmark")), Payload: payload}, At: started.At,
			}}}
			b.SetBytes(int64(payloadBytes))
			b.ReportAllocs()
			for b.Loop() {
				b.StopTimer()
				store := NewMemoryStore()
				if err := store.Append(b.Context(), initial); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				if err := store.Append(b.Context(), batch); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
