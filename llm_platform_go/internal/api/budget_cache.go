package api

import (
	"sync"
	"time"

	"llm_platform_go/internal/db"
)

// spendCache keeps per-task daily spend in memory so the budget gate never
// runs a SUM query on the prediction hot path. The cached value is:
//
//	(DB SUM at last refresh) + (cost of predictions served since, this instance)
//
// Refreshes happen at most once per spendCacheTTL per task. The local
// increments cover the async run-writer lag between a prediction completing
// and its row being queryable, so enforcement errs (slightly) toward blocking
// early at the margin — never toward unbounded overspend.
type spendCache struct {
	mu      sync.Mutex
	entries map[string]*spendEntry
}

type spendEntry struct {
	spend     float64
	refreshed time.Time
}

const spendCacheTTL = 5 * time.Second

// currentSpend returns the task's UTC-today spend for the budget gate,
// refreshing from the database when the cached value is stale.
func (h *Handler) currentSpend(taskID string) (float64, error) {
	h.spend.mu.Lock()
	defer h.spend.mu.Unlock()

	if h.spend.entries == nil {
		h.spend.entries = map[string]*spendEntry{}
	}
	if e, ok := h.spend.entries[taskID]; ok && time.Since(e.refreshed) < spendCacheTTL {
		return e.spend, nil
	}

	fresh, err := db.TaskSpendToday(h.DB, taskID)
	if err != nil {
		return 0, err
	}
	h.spend.entries[taskID] = &spendEntry{spend: fresh, refreshed: time.Now()}
	return fresh, nil
}

// addSpend folds a just-completed prediction's cost into the cached value so
// back-to-back requests see it before the next DB refresh. No-op when the
// task has no cached entry yet (the next gate check will query fresh anyway).
func (h *Handler) addSpend(taskID string, cost float64) {
	if cost <= 0 {
		return
	}
	h.spend.mu.Lock()
	if e, ok := h.spend.entries[taskID]; ok {
		e.spend += cost
	}
	h.spend.mu.Unlock()
}
