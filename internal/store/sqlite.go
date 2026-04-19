package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const (
	StatusOpen       = "open"
	StatusInProgress = "in_progress"
	StatusDone       = "done"
	StatusFailed     = "failed"
	StatusPending    = "pending"
)

const schema = `
CREATE TABLE IF NOT EXISTS tickets (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    app TEXT NOT NULL,
    fingerprint_hash TEXT NOT NULL,
    fingerprint_text TEXT NOT NULL,
    classification TEXT NOT NULL,
    severity TEXT NOT NULL,
    component TEXT NOT NULL,
    raw_log TEXT NOT NULL,
    first_seen DATETIME NOT NULL,
    last_seen DATETIME NOT NULL,
    created_date TEXT NOT NULL DEFAULT (date('now')),
    occurrence_count INTEGER NOT NULL DEFAULT 1,
    status TEXT NOT NULL DEFAULT 'open',
    sqs_message_id TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_tickets_fingerprint ON tickets(app, fingerprint_hash, created_date);
`

type Ticket struct {
	ID              int64
	App             string
	FingerprintHash string
	FingerprintText string
	Classification  string
	Severity        string
	Component       string
	RawLog          string
	FirstSeen       time.Time
	LastSeen        time.Time
	CreatedDate     string
	OccurrenceCount int
	Status          string
	SQSMessageID    string
}

type UpsertResult struct {
	IsNew   bool
	Skipped bool
	Ticket  *Ticket
}

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	dsn := path
	if path != ":memory:" {
		dsn = path + "?_busy_timeout=5000&_txlock=immediate"
	} else {
		dsn = "file::memory:?_busy_timeout=5000&_txlock=immediate&cache=shared"
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening SQLite database at %q: %w", path, err)
	}

	db.SetMaxOpenConns(1)

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enabling WAL mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enabling foreign keys: %w", err)
	}

	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrating schema: %w", err)
	}

	return &Store{db: db}, nil
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("applying schema: %w", err)
	}

	rows, err := db.Query("PRAGMA table_info(tickets)")
	if err != nil {
		return fmt.Errorf("reading table info: %w", err)
	}
	defer rows.Close()

	hasCreatedDate := false
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			return fmt.Errorf("scanning table info: %w", err)
		}
		if name == "created_date" {
			hasCreatedDate = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	if !hasCreatedDate {
		if _, err := db.Exec(`ALTER TABLE tickets ADD COLUMN created_date TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("adding created_date column: %w", err)
		}
		if _, err := db.Exec(`UPDATE tickets SET created_date = date(first_seen) WHERE created_date = ''`); err != nil {
			return fmt.Errorf("backfilling created_date: %w", err)
		}
		if _, err := db.Exec(`DROP INDEX IF EXISTS idx_tickets_fingerprint`); err != nil {
			return fmt.Errorf("dropping old index: %w", err)
		}
		if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_tickets_fingerprint ON tickets(app, fingerprint_hash, created_date)`); err != nil {
			return fmt.Errorf("creating new index: %w", err)
		}
	}

	return nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Upsert(ctx context.Context, t *Ticket) (*UpsertResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	today := time.Now().UTC().Format("2006-01-02")

	var existing Ticket
	row := tx.QueryRowContext(ctx,
		`SELECT id, occurrence_count, first_seen, status, sqs_message_id, created_date
		 FROM tickets WHERE app = ? AND fingerprint_hash = ? AND created_date = ?`,
		t.App, t.FingerprintHash, today,
	)

	err = row.Scan(
		&existing.ID,
		&existing.OccurrenceCount,
		&existing.FirstSeen,
		&existing.Status,
		&existing.SQSMessageID,
		&existing.CreatedDate,
	)

	if err == sql.ErrNoRows {
		now := time.Now().UTC()
		if t.FirstSeen.IsZero() {
			t.FirstSeen = now
		}
		if t.LastSeen.IsZero() {
			t.LastSeen = now
		}
		t.CreatedDate = today

		result, err := tx.ExecContext(ctx,
			`INSERT INTO tickets
			 (app, fingerprint_hash, fingerprint_text, classification, severity, component,
			  raw_log, first_seen, last_seen, created_date, occurrence_count, status, sqs_message_id)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
			t.App, t.FingerprintHash, t.FingerprintText, t.Classification, t.Severity,
			t.Component, t.RawLog, t.FirstSeen.Format(time.RFC3339),
			t.LastSeen.Format(time.RFC3339), today, StatusOpen, t.SQSMessageID,
		)
		if err != nil {
			return nil, fmt.Errorf("inserting ticket: %w", err)
		}

		id, err := result.LastInsertId()
		if err != nil {
			return nil, fmt.Errorf("getting last insert id: %w", err)
		}
		t.ID = id
		t.OccurrenceCount = 1
		t.Status = StatusOpen

		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("committing insert: %w", err)
		}

		return &UpsertResult{IsNew: true, Ticket: t}, nil
	}

	if err != nil {
		return nil, fmt.Errorf("querying existing ticket: %w", err)
	}

	if existing.Status == StatusDone || existing.Status == StatusFailed {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("committing skip: %w", err)
		}
		t.ID = existing.ID
		t.Status = existing.Status
		t.CreatedDate = existing.CreatedDate
		return &UpsertResult{IsNew: false, Skipped: true, Ticket: t}, nil
	}

	now := time.Now().UTC()
	newCount := existing.OccurrenceCount + 1

	_, err = tx.ExecContext(ctx,
		`UPDATE tickets SET last_seen = ?, occurrence_count = ?, raw_log = ? WHERE id = ?`,
		now.Format(time.RFC3339), newCount, t.RawLog, existing.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("updating ticket: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing update: %w", err)
	}

	t.ID = existing.ID
	t.FirstSeen = existing.FirstSeen
	t.LastSeen = now
	t.OccurrenceCount = newCount
	t.Status = existing.Status
	t.SQSMessageID = existing.SQSMessageID
	t.CreatedDate = existing.CreatedDate

	return &UpsertResult{IsNew: false, Ticket: t}, nil
}

func (s *Store) UpdateSQSMessageID(ctx context.Context, ticketID int64, msgID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE tickets SET sqs_message_id = ? WHERE id = ?`,
		msgID, ticketID,
	)
	if err != nil {
		return fmt.Errorf("updating sqs_message_id for ticket %d: %w", ticketID, err)
	}
	return nil
}

func (s *Store) UpdateStatus(ctx context.Context, ticketID int64, status string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE tickets SET status = ? WHERE id = ?`,
		status, ticketID,
	)
	if err != nil {
		return fmt.Errorf("updating status for ticket %d: %w", ticketID, err)
	}
	return nil
}

func (s *Store) ListActiveTickets(ctx context.Context) ([]*Ticket, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, app, fingerprint_hash, fingerprint_text, classification, severity,
		        component, raw_log, first_seen, last_seen, created_date, occurrence_count, status, sqs_message_id
		 FROM tickets
		 WHERE status IN ('open', 'in_progress', 'pending', 'failed')
		 ORDER BY last_seen DESC
		 LIMIT 200`,
	)
	if err != nil {
		return nil, fmt.Errorf("listing active tickets: %w", err)
	}
	defer rows.Close()

	return scanTickets(rows)
}

func scanTickets(rows *sql.Rows) ([]*Ticket, error) {
	var tickets []*Ticket
	for rows.Next() {
		t := &Ticket{}
		var firstSeen, lastSeen string
		var sqsMsgID sql.NullString

		if err := rows.Scan(
			&t.ID, &t.App, &t.FingerprintHash, &t.FingerprintText,
			&t.Classification, &t.Severity, &t.Component, &t.RawLog,
			&firstSeen, &lastSeen, &t.CreatedDate, &t.OccurrenceCount, &t.Status, &sqsMsgID,
		); err != nil {
			return nil, fmt.Errorf("scanning ticket row: %w", err)
		}

		if ts, err := time.Parse(time.RFC3339, firstSeen); err == nil {
			t.FirstSeen = ts
		}
		if ts, err := time.Parse(time.RFC3339, lastSeen); err == nil {
			t.LastSeen = ts
		}
		if sqsMsgID.Valid {
			t.SQSMessageID = sqsMsgID.String
		}

		tickets = append(tickets, t)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating ticket rows: %w", err)
	}

	return tickets, nil
}
