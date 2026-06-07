// Package store is a thin wrapper around a SQLite key/value table. It owns all
// SQL so the rest of the program never touches the database directly. The MCP
// handlers just call typed methods like Set/Get/Query.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, no cgo
)

// Store is a handle to the key/value database. It is safe for concurrent use.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at dsn and ensures the
// kv table exists. Pass "file:mcp_toy.db" for an on-disk store, or
// "file::memory:?cache=shared" for an ephemeral one.
func Open(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS kv (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close releases the underlying database handle.
func (s *Store) Close() error { return s.db.Close() }

// Set inserts or overwrites the value for key.
func (s *Store) Set(ctx context.Context, key, value string) error {
	if key == "" {
		return fmt.Errorf("key must not be empty")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO kv(key, value) VALUES(?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value)
	return err
}

// Get returns the value for key. found is false if the key does not exist.
func (s *Store) Get(ctx context.Context, key string) (value string, found bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT value FROM kv WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

// Delete removes key. The bool reports whether a row was actually deleted.
func (s *Store) Delete(ctx context.Context, key string) (bool, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM kv WHERE key = ?`, key)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// List returns every key, sorted alphabetically.
func (s *Store) List(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key FROM kv ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	keys := []string{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// All returns every key/value pair, sorted by key. Used by the prompt handler
// to embed the current contents of the store into a templated message.
func (s *Store) All(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM kv ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// QueryResult is the outcome of a read-only SQL query. Every cell is rendered
// as a string so the result is trivially JSON-serialisable for the AI.
type QueryResult struct {
	Columns []string   `json:"columns"`
	Rows    [][]string `json:"rows"`
}

// Query runs a read-only SQL statement and returns its rows.
//
// SECURITY: the SQL here originates from an AI, which originates from a user, so
// it is untrusted. We only permit a single SELECT/WITH statement. This is a
// belt-and-braces guard; the more robust layer is opening the connection with
// "?mode=ro" or "&_pragma=query_only(true)", but validating the statement up
// front gives a clear error the model can react to.
func (s *Store) Query(ctx context.Context, query string) (*QueryResult, error) {
	if err := guardReadOnly(query); err != nil {
		return nil, err
	}

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	result := &QueryResult{Columns: cols, Rows: [][]string{}}
	for rows.Next() {
		// Scan into a slice of *any, one per column, then stringify.
		cells := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range cells {
			ptrs[i] = &cells[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make([]string, len(cols))
		for i, c := range cells {
			row[i] = stringify(c)
		}
		result.Rows = append(result.Rows, row)
	}
	return result, rows.Err()
}

// guardReadOnly rejects anything that isn't a single read-only statement.
func guardReadOnly(query string) error {
	q := strings.TrimSpace(query)
	if q == "" {
		return fmt.Errorf("query must not be empty")
	}
	// Reject statement-stacking (e.g. "SELECT 1; DROP TABLE kv").
	if strings.Contains(strings.TrimSuffix(q, ";"), ";") {
		return fmt.Errorf("only a single statement is allowed")
	}
	lower := strings.ToLower(q)
	if !strings.HasPrefix(lower, "select") && !strings.HasPrefix(lower, "with") {
		return fmt.Errorf("only read-only SELECT/WITH queries are allowed")
	}
	for _, banned := range []string{"insert", "update", "delete", "drop", "alter", "create", "replace", "attach", "pragma"} {
		if strings.Contains(lower, banned) {
			return fmt.Errorf("disallowed keyword %q in query", banned)
		}
	}
	return nil
}

// stringify renders a scanned SQL value as a string.
func stringify(v any) string {
	switch t := v.(type) {
	case nil:
		return "NULL"
	case []byte:
		return string(t)
	default:
		return fmt.Sprintf("%v", t)
	}
}
