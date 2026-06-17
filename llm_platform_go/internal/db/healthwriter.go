package db

import (
	"database/sql"
	"log"
	"sync"

	"llm_platform_go/internal/types"
)

// HealthEventWriter moves model-health event inserts off the request hot path,
// mirroring RunWriter: callers (the health tracker's sink) do a non-blocking
// send; a single goroutine drains to InsertHealthEvent. A dropped event must
// never block or fail a prediction — health observation is best-effort.
type HealthEventWriter struct {
	ch      chan types.HealthEvent
	db      *sql.DB
	wg      sync.WaitGroup
	mu      sync.Mutex
	closed  bool
	dropped int64
}

// NewHealthEventWriter starts the drain goroutine. bufSize <= 0 → default 256.
func NewHealthEventWriter(database *sql.DB, bufSize int) *HealthEventWriter {
	if bufSize <= 0 {
		bufSize = 256
	}
	w := &HealthEventWriter{ch: make(chan types.HealthEvent, bufSize), db: database}
	w.wg.Add(1)
	go w.drain()
	return w
}

func (w *HealthEventWriter) drain() {
	defer w.wg.Done()
	for e := range w.ch {
		ev := e
		if err := InsertHealthEvent(w.db, &ev); err != nil {
			log.Printf("healthwriter: insert failed (task=%s model=%s): %v", e.TaskID, e.Model, err)
		}
	}
}

// Write enqueues an event without blocking; drops (and counts) it if the buffer
// is full or the writer is closed.
func (w *HealthEventWriter) Write(e types.HealthEvent) {
	w.mu.Lock()
	if w.closed {
		w.dropped++
		w.mu.Unlock()
		return
	}
	select {
	case w.ch <- e:
	default:
		w.dropped++
		log.Printf("healthwriter: buffer full, dropped health event (total dropped: %d)", w.dropped)
	}
	w.mu.Unlock()
}

// Close stops accepting events and flushes what's queued.
func (w *HealthEventWriter) Close() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	close(w.ch)
	w.mu.Unlock()
	w.wg.Wait()
}
