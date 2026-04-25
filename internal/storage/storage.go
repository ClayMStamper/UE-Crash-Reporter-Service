package storage

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"ue-crash-reporter/internal/models"
)

const schema = `
CREATE TABLE IF NOT EXISTS crashes (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	guid           TEXT    NOT NULL UNIQUE,
	game_name      TEXT,
	platform       TEXT,
	build_version  TEXT,
	engine_version TEXT,
	crash_type     TEXT,
	error_message  TEXT,
	call_stack     TEXT,
	user_desc      TEXT,
	received_at    DATETIME NOT NULL
);

CREATE TABLE IF NOT EXISTS crash_files (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	crash_id   INTEGER NOT NULL REFERENCES crashes(id) ON DELETE CASCADE,
	filename   TEXT    NOT NULL,
	size_bytes INTEGER NOT NULL DEFAULT 0,
	store_path TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_crashes_received_at ON crashes(received_at DESC);
CREATE INDEX IF NOT EXISTS idx_crash_files_crash_id ON crash_files(crash_id);
`

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

// New opens (or creates) the SQLite database at dbPath and runs migrations.
func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite is single-writer

	// Enable WAL mode and foreign key enforcement.
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			return nil, fmt.Errorf("pragma %q: %w", pragma, err)
		}
	}

	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// StoreCrash inserts a crash record and its associated files in one transaction.
// If a crash with the same GUID already exists the existing ID is returned.
func (s *Store) StoreCrash(c *models.Crash) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck

	var existingID int64
	err = tx.QueryRow(`SELECT id FROM crashes WHERE guid = ?`, c.GUID).Scan(&existingID)
	if err == nil {
		return existingID, nil // duplicate — already stored
	}

	res, err := tx.Exec(`
		INSERT INTO crashes
			(guid, game_name, platform, build_version, engine_version,
			 crash_type, error_message, call_stack, user_desc, received_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		c.GUID, c.GameName, c.Platform, c.BuildVersion, c.EngineVersion,
		c.CrashType, c.ErrorMessage, c.CallStack, c.UserDesc, c.ReceivedAt,
	)
	if err != nil {
		return 0, fmt.Errorf("insert crash: %w", err)
	}
	id, _ := res.LastInsertId()

	for _, f := range c.Files {
		if _, err := tx.Exec(`
			INSERT INTO crash_files (crash_id, filename, size_bytes, store_path)
			VALUES (?,?,?,?)`,
			id, f.Filename, f.SizeBytes, f.StorePath,
		); err != nil {
			return 0, fmt.Errorf("insert crash_file %s: %w", f.Filename, err)
		}
	}

	return id, tx.Commit()
}

// GetCrash returns a single crash with its files attached.
func (s *Store) GetCrash(id int64) (*models.Crash, error) {
	c := &models.Crash{}
	err := s.db.QueryRow(`
		SELECT id, guid, game_name, platform, build_version, engine_version,
		       crash_type, error_message, call_stack, user_desc, received_at
		FROM crashes WHERE id = ?`, id,
	).Scan(
		&c.ID, &c.GUID, &c.GameName, &c.Platform, &c.BuildVersion, &c.EngineVersion,
		&c.CrashType, &c.ErrorMessage, &c.CallStack, &c.UserDesc, &c.ReceivedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	rows, err := s.db.Query(`
		SELECT id, crash_id, filename, size_bytes, store_path
		FROM crash_files WHERE crash_id = ? ORDER BY filename`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var f models.CrashFile
		if err := rows.Scan(&f.ID, &f.CrashID, &f.Filename, &f.SizeBytes, &f.StorePath); err != nil {
			return nil, err
		}
		c.Files = append(c.Files, f)
	}
	return c, rows.Err()
}

// ListCrashes returns crashes ordered newest-first with simple offset pagination.
func (s *Store) ListCrashes(limit, offset int) ([]models.Crash, int, error) {
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM crashes`).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := s.db.Query(`
		SELECT id, guid, game_name, platform, build_version,
		       crash_type, error_message, received_at
		FROM crashes
		ORDER BY received_at DESC
		LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var crashes []models.Crash
	for rows.Next() {
		var c models.Crash
		var receivedAt time.Time
		if err := rows.Scan(
			&c.ID, &c.GUID, &c.GameName, &c.Platform, &c.BuildVersion,
			&c.CrashType, &c.ErrorMessage, &receivedAt,
		); err != nil {
			return nil, 0, err
		}
		c.ReceivedAt = receivedAt
		crashes = append(crashes, c)
	}
	return crashes, total, rows.Err()
}

// Stats returns simple aggregate counts.
func (s *Store) Stats() (total int, last24h int, err error) {
	s.db.QueryRow(`SELECT COUNT(*) FROM crashes`).Scan(&total)           //nolint:errcheck
	s.db.QueryRow(`SELECT COUNT(*) FROM crashes WHERE received_at > datetime('now','-1 day')`).Scan(&last24h) //nolint:errcheck
	return
}
