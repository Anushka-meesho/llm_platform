package db

import (
	"database/sql"
	"log"
	"sync"
	"sync/atomic"

	"llm_platform_go/internal/types"
)

// GatewayAttemptWriter moves gateway-trace inserts off the request hot path,
// exactly like RunWriter: handlers do a non-blocking send, a single goroutine
// drains to InsertGatewayAttempt. A run produces several attempts, so its buffer
// is larger than RunWriter's. If the buffer is ever full the row is dropped and
// counted — losing a trace row must never block or fail a prediction.
type GatewayAttemptWriter struct {
	ch      chan *types.GatewayAttempt
	db      *sql.DB
	dropped atomic.Int64
	wg      sync.WaitGroup

	mu     sync.Mutex
	closed bool
}

// NewGatewayAttemptWriter starts the drain goroutine. bufSize <= 0 selects the
// default (4096 — several attempts per run, so larger than RunWriter's).
func NewGatewayAttemptWriter(database *sql.DB, bufSize int) *GatewayAttemptWriter {
	if bufSize <= 0 {
		bufSize = 4096
	}
	w := &GatewayAttemptWriter{ch: make(chan *types.GatewayAttempt, bufSize), db: database}
	w.wg.Add(1)
	go w.drain()
	return w
}

func (w *GatewayAttemptWriter) drain() {
	defer w.wg.Done()
	for a := range w.ch {
		if err := InsertGatewayAttempt(w.db, a); err != nil {
			log.Printf("attemptwriter: insert failed (run_id=%s seq=%d): %v", a.RunID, a.Seq, err)
		}
	}
}

// Write enqueues an attempt without blocking. Returns false if it was dropped
// (buffer full or writer closed).
func (w *GatewayAttemptWriter) Write(a *types.GatewayAttempt) bool {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		w.dropped.Add(1)
		return false
	}
	select {
	case w.ch <- a:
		w.mu.Unlock()
		return true
	default:
		w.mu.Unlock()
		n := w.dropped.Add(1)
		log.Printf("attemptwriter: buffer full, dropped attempt row (total dropped: %d)", n)
		return false
	}
}

// Dropped returns how many rows have been dropped since start.
func (w *GatewayAttemptWriter) Dropped() int64 { return w.dropped.Load() }

// Close stops accepting writes and blocks until everything queued is flushed.
func (w *GatewayAttemptWriter) Close() {
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
