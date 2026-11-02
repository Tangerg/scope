package run

import (
	"context"
	"iter"
	"sync"
)

// MemoryStore keeps each tree's log for the lifetime of the store value. It
// implements the complete Store contract, so it is a faithful stand-in for a
// durable backend in tests and single-process hosts.
type MemoryStore struct {
	mu   sync.Mutex
	logs map[ProcessID]*memoryLog
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{logs: make(map[ProcessID]*memoryLog)} }

func (m *MemoryStore) Append(ctx context.Context, batch Batch) error {
	if err := RequireContext(ctx).Err(); err != nil {
		return err
	}
	if err := batch.Validate(); err != nil {
		return err
	}
	owned, err := cloneEntries(batch.Entries())
	if err != nil {
		return err
	}
	stream, created, err := m.stream(ctx, batch, owned)
	if err != nil || created {
		return err
	}
	return stream.append(ctx, batch, owned)
}

func (m *MemoryStore) Load(ctx context.Context, root ProcessID) (Log, bool, error) {
	stream, err := m.existing(ctx, root)
	if err != nil || stream == nil {
		return Log{}, false, err
	}
	entries, err := stream.cut()
	if err != nil {
		return Log{}, false, err
	}
	return Log{Root: root, Entries: entries}, true, nil
}

func (m *MemoryStore) Read(ctx context.Context, root ProcessID, after LSN) iter.Seq2[Entry, error] {
	return func(yield func(Entry, error) bool) {
		stream, err := m.existing(ctx, root)
		if err != nil {
			yield(Entry{}, err)
			return
		}
		if stream == nil {
			return
		}
		entries, err := stream.after(after)
		if err != nil {
			yield(Entry{}, err)
			return
		}
		for _, entry := range entries {
			if !yield(entry, nil) {
				return
			}
		}
	}
}

// stream returns the log for batch.Root, creating it when batch starts the
// tree. created reports that the batch has already been applied.
func (m *MemoryStore) stream(ctx context.Context, batch Batch, owned []Entry) (*memoryLog, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if stream := m.logs[batch.Root]; stream != nil {
		return stream, false, nil
	}
	if _, _, err := batch.Admit(0, 0, nil); err != nil {
		return nil, false, err
	}
	if m.logs == nil {
		m.logs = make(map[ProcessID]*memoryLog)
	}
	stream := &memoryLog{}
	stream.extend(owned)
	m.logs[batch.Root] = stream
	return stream, true, nil
}

func (m *MemoryStore) existing(ctx context.Context, root ProcessID) (*memoryLog, error) {
	if err := RequireContext(ctx).Err(); err != nil {
		return nil, err
	}
	if !root.Valid() || root.Depth() != 0 {
		return nil, ErrInvalidIdentity
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.logs[root], nil
}

type memoryLog struct {
	mu      sync.Mutex
	entries []Entry
	// checkpoint indexes the newest Checkpoint entry so Load never scans history.
	checkpoint int
}

func (m *memoryLog) append(ctx context.Context, batch Batch, owned []Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	head := LSN(len(m.entries))
	var existing []Entry
	if end := batch.After + LSN(len(batch.Records)); batch.After < head && end <= head {
		existing = m.entries[batch.After:end]
	}
	_, duplicate, err := batch.Admit(m.entries[len(m.entries)-1].Epoch, head, existing)
	if err != nil || duplicate {
		return err
	}
	m.extend(owned)
	return nil
}

func (m *memoryLog) extend(entries []Entry) {
	for _, entry := range entries {
		if _, checkpoint := entry.Record.(Checkpoint); checkpoint {
			m.checkpoint = len(m.entries)
		}
		m.entries = append(m.entries, entry)
	}
}

func (m *memoryLog) cut() ([]Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneEntries(m.entries[m.checkpoint:])
}

func (m *memoryLog) after(position LSN) ([]Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if position >= LSN(len(m.entries)) {
		return nil, nil
	}
	return cloneEntries(m.entries[position:])
}

func cloneEntries(entries []Entry) ([]Entry, error) {
	result := make([]Entry, len(entries))
	for index, entry := range entries {
		record, err := cloneRecord(entry.Record)
		if err != nil {
			return nil, err
		}
		result[index] = Entry{LSN: entry.LSN, Epoch: entry.Epoch, Record: record}
	}
	return result, nil
}
