package main

import (
	"database/sql"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS checks (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	target      TEXT NOT NULL,
	checked_at  DATETIME NOT NULL,
	status_code INTEGER NOT NULL,
	latency_ms  INTEGER NOT NULL,
	success     INTEGER NOT NULL,
	error       TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS failure_state (
	target               TEXT PRIMARY KEY,
	consecutive_failures INTEGER NOT NULL,
	down                 INTEGER NOT NULL
);
`

// store persists check results to a SQLite database.
type store struct {
	db *sql.DB
}

func openStore(path string) (*store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &store{db: db}, nil
}

// record inserts a single check result. up reflects the same success
// definition used for alerting (no error, status < 400).
func (s *store) record(result checkResult, up bool) error {
	success := 0
	if up {
		success = 1
	}
	errText := ""
	if result.Err != nil {
		errText = result.Err.Error()
	}

	_, err := s.db.Exec(
		`INSERT INTO checks (target, checked_at, status_code, latency_ms, success, error) VALUES (?, ?, ?, ?, ?, ?)`,
		result.URL, time.Now().UTC(), result.StatusCode, result.Latency.Milliseconds(), success, errText,
	)
	return err
}

// loadFailureState reads every target's persisted consecutive-failure count
// and down state, so a tracker can pick up where a previous run left off
// instead of starting every target back at zero. On a fresh database (no
// rows yet) it returns empty maps, which behaves identically to a brand new
// in-memory tracker.
func (s *store) loadFailureState() (failures map[string]int, down map[string]bool, err error) {
	rows, err := s.db.Query(`SELECT target, consecutive_failures, down FROM failure_state`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	failures = make(map[string]int)
	down = make(map[string]bool)
	for rows.Next() {
		var t string
		var count int
		var isDown int
		if err := rows.Scan(&t, &count, &isDown); err != nil {
			return nil, nil, err
		}
		failures[t] = count
		down[t] = isDown != 0
	}
	return failures, down, rows.Err()
}

// saveFailureState persists one target's current consecutive-failure count
// and down state, so it survives a process restart. Called after every
// check, not just on a transition, so the on-disk state never lags what the
// in-memory tracker believes.
func (s *store) saveFailureState(target string, consecutiveFailures int, down bool) error {
	downInt := 0
	if down {
		downInt = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO failure_state (target, consecutive_failures, down) VALUES (?, ?, ?)
		 ON CONFLICT(target) DO UPDATE SET consecutive_failures = excluded.consecutive_failures, down = excluded.down`,
		target, consecutiveFailures, downInt,
	)
	return err
}

func (s *store) Close() error {
	return s.db.Close()
}
