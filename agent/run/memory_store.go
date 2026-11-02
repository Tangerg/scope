package run

import (
	"context"
	"sync"
)

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
	m.mu.Lock()
	if contextErr := ctx.Err(); contextErr != nil {
		m.mu.Unlock()
		return contextErr
	}
	if m.logs == nil {
		m.logs = make(map[ProcessID]*memoryLog)
	}
	stream := m.logs[batch.Root]
	if stream == nil {
		_, _, admissionErr := batch.Admit(0, 0, nil)
		if admissionErr == nil {
			m.logs[batch.Root] = &memoryLog{entries: owned}
		}
		m.mu.Unlock()
		return admissionErr
	}
	m.mu.Unlock()
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	head := LSN(len(stream.entries))
	epoch := Epoch(0)
	if head > 0 {
		epoch = stream.entries[len(stream.entries)-1].Epoch
	}
	var existing []Entry
	end := batch.After + LSN(len(batch.Records))
	if batch.After < head && end <= head {
		existing = stream.entries[int(batch.After):int(end)]
	}
	_, duplicate, err := batch.Admit(epoch, head, existing)
	if err != nil || duplicate {
		return err
	}
	stream.entries = append(stream.entries, owned...)
	return nil
}
func (m *MemoryStore) Load(ctx context.Context, root ProcessID) (Log, bool, error) {
	if err := RequireContext(ctx).Err(); err != nil {
		return Log{}, false, err
	}
	if !root.Valid() || root.Depth() != 0 {
		return Log{}, false, ErrInvalidIdentity
	}
	m.mu.Lock()
	stream := m.logs[root]
	m.mu.Unlock()
	if stream == nil {
		return Log{}, false, nil
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Log{}, false, err
	}
	if len(stream.entries) == 0 {
		return Log{}, false, nil
	}
	entries, err := cloneEntries(stream.entries)
	if err != nil {
		return Log{}, false, err
	}
	return Log{Root: root, Entries: entries}, true, nil
}

type memoryLog struct {
	mu      sync.Mutex
	entries []Entry
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
