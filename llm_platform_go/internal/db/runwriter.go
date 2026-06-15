package db

import (
	"database/sql"
	"log"
	"sync"
	"sync/atomic"

	"llm_platform_go/internal/types"
)

// RunWriter moves observability inserts off the request hot path: handlers do
// a non-blocking send, a single goroutine drains to InsertRun. If the buffer
// is ever full the row is dropped and counted — losing a trace row must never
// block or fail a prediction. (Kafka replaces this in a later phase.)
type RunWriter struct {
	ch      chan *types.RunRow
	db      *sql.DB
	dropped atomic.Int64
	wg      sync.WaitGroup

	mu     sync.Mutex
	closed bool
}

// NewRunWriter starts the drain goroutine. bufSize <= 0 selects the default (1024).
func NewRunWriter(database *sql.DB, bufSize int) *RunWriter {
	if bufSize <= 0 {
		bufSize = 1024
	}
	w := &RunWriter{ch: make(chan *types.RunRow, bufSize), db: database}
	w.wg.Add(1)
	go w.drain()
	return w
}

func (w *RunWriter) drain() {
	defer w.wg.Done()
	for row := range w.ch {
		if err := InsertRun(w.db, row); err != nil {
			log.Printf("runwriter: insert failed (run_id=%s): %v", row.RunID, err)
		}
	}
}

// Write enqueues a row without blocking. Returns false if the row was dropped
// (buffer full or writer closed).
func (w *RunWriter) Write(row *types.RunRow) bool {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		w.dropped.Add(1)
		return false
	}
	select {
	case w.ch <- row:
		w.mu.Unlock()
		return true
	default:
		w.mu.Unlock()
		n := w.dropped.Add(1)
		log.Printf("runwriter: buffer full, dropped run row (total dropped: %d)", n)
		return false
	}
}

// Dropped returns how many rows have been dropped since start.
func (w *RunWriter) Dropped() int64 { return w.dropped.Load() }

// Close stops accepting writes and blocks until everything queued is flushed.
func (w *RunWriter) Close() {
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
