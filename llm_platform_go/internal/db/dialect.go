package db

import (
	"database/sql"
	"strconv"
	"strings"
)

// Driver identifies the active SQL backend. The platform runs on exactly one DB
// per process, so the active dialect is process-global, set once by Open().
type Driver int

const (
	SQLite Driver = iota
	Postgres
)

// activeDriver is set by Open and read by rebind/dialect helpers. Defaults to
// SQLite so tests that open an in-memory sqlite DB directly (without Open) still
// get correct placeholder handling.
var activeDriver = SQLite

// ActiveDriver returns the backend selected at Open time.
func ActiveDriver() Driver { return activeDriver }

// rebind converts the portable `?` placeholders used throughout queries.go into
// the form the active driver expects. SQLite uses `?` as-is; Postgres needs
// positional `$1, $2, …`. All Exec/Query/QueryRow calls route through the
// exec/query/queryRow helpers below so this happens transparently.
func rebind(q string) string {
	if activeDriver != Postgres {
		return q
	}
	var b strings.Builder
	b.Grow(len(q) + 8)
	n := 0
	for i := 0; i < len(q); i++ {
		if q[i] == '?' {
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteByte(q[i])
	}
	return b.String()
}

// --- Dialect-specific SQL fragments. These are the only places the two backends
// diverge in hand-written queries; everything else is portable ANSI SQL. ---

// created_at is stored as TEXT in both backends in the canonical
// "YYYY-MM-DD HH:MM:SS" form the app writes. That keeps scanning (always into a
// string), substr(), and ordering identical across dialects, and lets the date
// gates below compare against a 'YYYY-MM-DD' text lower bound (lexicographic
// order matches chronological order for this fixed-width format).

// nowExpr is the SQL expression for "current UTC timestamp" as canonical text,
// used in INSERT VALUES where the app doesn't supply created_at.
func nowExpr() string {
	if activeDriver == Postgres {
		return "to_char((now() at time zone 'utc'), 'YYYY-MM-DD HH24:MI:SS')"
	}
	return "datetime('now')"
}

// todayExpr is the text 'YYYY-MM-DD' for the current UTC day, a valid lower
// bound for "created today" against the TEXT created_at column.
func todayExpr() string {
	if activeDriver == Postgres {
		return "to_char((now() at time zone 'utc')::date, 'YYYY-MM-DD')"
	}
	return "date('now')"
}

// daysAgoExpr is the text 'YYYY-MM-DD' for UTC midnight n days ago. n is an
// integer the caller controls (never user input), so it is safe to inline.
func daysAgoExpr(n int) string {
	if activeDriver == Postgres {
		return "to_char((now() at time zone 'utc')::date - " + strconv.Itoa(n) + ", 'YYYY-MM-DD')"
	}
	return "date('now', '-" + strconv.Itoa(n) + " days')"
}

// ciLike builds a case-insensitive substring match on a column against a `?`
// placeholder: ILIKE on Postgres, LIKE … COLLATE NOCASE on SQLite.
func ciLike(column string) string {
	if activeDriver == Postgres {
		return column + " ILIKE ?"
	}
	return column + " LIKE ? COLLATE NOCASE"
}

// imageCountExpr is a SQL expression that yields the number of images on a run
// WITHOUT transferring the (potentially multi-MB base64) image column to the
// app — the list view only needs the count, so counting in the DB keeps each
// page's payload bounded no matter how large the stored images are. The column
// holds a JSON array of image refs, or a bare string for legacy single-image
// rows (NULL/empty for text-only runs).
//
//	SQLite: count array elements via json1, treating a non-JSON legacy string as 1.
//	Postgres: presence only (1 if set) — exact multi-image counts await the
//	          pending Postgres validation (see DEPLOY.md); accurate enough for the
//	          list's "has image" indicator.
func imageCountExpr() string {
	if activeDriver == Postgres {
		return "CASE WHEN image IS NULL OR image = '' THEN 0 ELSE 1 END"
	}
	return "CASE WHEN image IS NULL OR image = '' THEN 0 " +
		"WHEN json_valid(image) AND json_type(image) = 'array' THEN json_array_length(image) " +
		"ELSE 1 END"
}

// exec/query/queryRow wrap the database/sql calls, applying rebind so call sites
// can keep writing portable `?` placeholders regardless of backend.
func exec(db *sql.DB, q string, args ...any) (sql.Result, error) { return db.Exec(rebind(q), args...) }
func query(db *sql.DB, q string, args ...any) (*sql.Rows, error) { return db.Query(rebind(q), args...) }
func queryRow(db *sql.DB, q string, args ...any) *sql.Row        { return db.QueryRow(rebind(q), args...) }

// Rebind is the exported placeholder rewriter for callers outside this package
// (e.g. the tasks store, shadow handler) that hold their own *sql.DB and build
// `?`-style queries. Wrap the query string before Exec/Query/QueryRow:
//
//	row := s.db.QueryRow(db.Rebind(`SELECT ... WHERE id = ?`), id)
func Rebind(q string) string { return rebind(q) }

// Exec/Query/QueryRow are exported convenience wrappers (rebind applied) for
// callers in other packages that already hold a *sql.DB.
func Exec(db *sql.DB, q string, args ...any) (sql.Result, error) { return exec(db, q, args...) }
func Query(db *sql.DB, q string, args ...any) (*sql.Rows, error) { return query(db, q, args...) }
func QueryRow(db *sql.DB, q string, args ...any) *sql.Row        { return queryRow(db, q, args...) }
