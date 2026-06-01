package listing

import (
	"context"
	"sort"
	"sync"
	"time"
)

// MemoryRepository is an in-memory implementation of Repository used by the
// tests (and handy for a quick DB-less local run). A mutex makes it safe for
// concurrent use, since the HTTP server handles requests in many goroutines.
type MemoryRepository struct {
	mu     sync.Mutex
	nextID int64
	items  map[int64]Listing
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{nextID: 1, items: make(map[int64]Listing)}
}

func (m *MemoryRepository) Create(ctx context.Context, l *Listing) (*Listing, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l.ID = m.nextID
	m.nextID++
	l.CreatedAt = time.Now().UTC()
	m.items[l.ID] = *l
	cp := m.items[l.ID]
	return &cp, nil
}

func (m *MemoryRepository) List(ctx context.Context, f ListFilter) ([]Listing, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Listing, 0, len(m.items))
	for _, l := range m.items {
		if f.Status != "" && string(l.Status) != f.Status {
			continue
		}
		if f.Category != "" && l.Category != f.Category {
			continue
		}
		out = append(out, l)
	}
	// Newest first, matching the Postgres ORDER BY.
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

func (m *MemoryRepository) Get(ctx context.Context, id int64) (*Listing, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.items[id]
	if !ok {
		return nil, ErrNotFound
	}
	return &l, nil
}

func (m *MemoryRepository) Update(ctx context.Context, l *Listing) (*Listing, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.items[l.ID]; !ok {
		return nil, ErrNotFound
	}
	m.items[l.ID] = *l
	cp := m.items[l.ID]
	return &cp, nil
}

func (m *MemoryRepository) Delete(ctx context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.items[id]; !ok {
		return ErrNotFound
	}
	delete(m.items, id)
	return nil
}
