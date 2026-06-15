package cache

import (
	"context"
	"sync"
	"time"
)

// Memory is an in-process cache for development boxes without Redis and for
// tests. Same semantics as Redis (TTL, best-effort); not for multi-instance
// deployments — entries are per-process.
type Memory struct {
	mu      sync.Mutex
	entries map[string]memoryEntry
	now     func() time.Time // injectable for tests
}

type memoryEntry struct {
	val       []byte
	expiresAt time.Time
}

func NewMemory() *Memory {
	return &Memory{entries: map[string]memoryEntry{}, now: time.Now}
}

// NewMemoryWithClock is for tests that need to control expiry.
func NewMemoryWithClock(now func() time.Time) *Memory {
	return &Memory{entries: map[string]memoryEntry{}, now: now}
}

func (m *Memory) Get(_ context.Context, key string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[key]
	if !ok {
		return nil, false
	}
	if m.now().After(e.expiresAt) {
		delete(m.entries, key)
		return nil, false
	}
	return e.val, true
}

func (m *Memory) Set(_ context.Context, key string, val []byte, ttl time.Duration) {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[key] = memoryEntry{val: val, expiresAt: m.now().Add(ttl)}
}
