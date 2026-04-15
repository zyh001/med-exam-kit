// Package store defines the unified storage interface for both question banks
// and learning progress. Two backends are provided:
//   - sqlite  (default, file-based, zero config)
//   - postgres (optional, structured DB for multi-user / team deployments)
//
// Select backend via --db flag:
//   (none)                         → SQLite files alongside .mqb
//   postgres://user:pass@host/db   → PostgreSQL
package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/zyh001/med-exam-kit/internal/models"
)

// ────────────────────────────────────────────────────────────────
// Question Bank Store
// ────────────────────────────────────────────────────────────────

// BankMeta describes a stored question bank (without loading all questions).
type BankMeta struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Source    string    `json:"source"`     // original file path
	Count     int       `json:"count"`
	CreatedAt time.Time `json:"created_at"`
}

// QuestionStore persists question banks.
type QuestionStore interface {
	// ListBanks returns all stored banks (metadata only, fast).
	ListBanks(ctx context.Context) ([]BankMeta, error)

	// GetBank loads all questions for the given bank id.
	GetBank(ctx context.Context, bankID int64) ([]*models.Question, error)

	// FindBank returns the bank id by name (exact match). Returns -1 if not found.
	FindBank(ctx context.Context, name string) (int64, error)

	// ImportBank stores questions and returns the new bank id.
	// If a bank with the same name already exists it is replaced.
	ImportBank(ctx context.Context, name, source string, questions []*models.Question) (int64, error)

	// DeleteBank removes a bank and all its questions.
	DeleteBank(ctx context.Context, bankID int64) error

	// Close releases resources.
	Close() error
}

// ────────────────────────────────────────────────────────────────
// Progress Store
// ────────────────────────────────────────────────────────────────

// ProgressStore abstracts the learning-progress backend.
// All existing progress.* functions map 1-to-1 to methods here.
type ProgressStore interface {
	// Init creates tables if they don't exist (idempotent).
	Init(ctx context.Context) error

	// DB returns the underlying *sql.DB (SQLite only; nil for Postgres).
	// Server code that still uses raw *sql.DB should call this.
	// New code should use the typed methods below.
	DB() *sql.DB

	// Sessions
	RecordSession(ctx context.Context, session map[string]any, userID string) error
	RecordSessionsBatch(ctx context.Context, sessions []map[string]any, userID string) (processed, skipped []string)
	DeleteSession(ctx context.Context, sessionID, userID string) bool

	// SM-2 review
	GetDueFingerprints(ctx context.Context, userID string, bankID int, clientDate string) []string
	UpdateSM2(ctx context.Context, userID, fingerprint string, quality int) error

	// Queries
	GetHistory(ctx context.Context, userID string, bankID int, limit int) []HistoryEntry
	GetOverallStats(ctx context.Context, userID string, bankID int, clientDate string) OverallStats
	GetUnitStats(ctx context.Context, userID string, bankID int) []UnitStat
	GetWrongFingerprints(ctx context.Context, userID string, bankID int, limit int) []WrongEntry
	GetSyncStatus(ctx context.Context, userID string, bankID int) map[string]any

	// Data management
	ClearUserData(ctx context.Context, userID string, bankID int) map[string]int
	MigrateUserData(ctx context.Context, fromUID, toUID string) (map[string]int, error)

	Close() error
}

// ── Shared result types (mirror progress package types) ───────────

type HistoryEntry struct {
	ID      string   `json:"id"`
	Mode    string   `json:"mode"`
	Total   int      `json:"total"`
	Correct int      `json:"correct"`
	Wrong   int      `json:"wrong"`
	Skip    int      `json:"skip"`
	TimeSec int      `json:"time_sec"`
	Date    string   `json:"date"`
	Units   []string `json:"units"`
	Pct     int      `json:"pct"`
}

type OverallStats struct {
	Sessions  int `json:"sessions"`
	Attempts  int `json:"attempts"`
	Correct   int `json:"correct"`
	Wrong     int `json:"wrong"`
	Skip      int `json:"skip"`
	DueToday  int `json:"due_today"`
}

type UnitStat struct {
	Unit     string `json:"unit"`
	Total    int    `json:"total"`
	Correct  int    `json:"correct"`
	Wrong    int    `json:"wrong"`
	Accuracy int    `json:"accuracy"`
}

type WrongEntry struct {
	Fingerprint string `json:"fingerprint"`
	Total       int    `json:"total"`
	Correct     int    `json:"correct"`
	Wrong       int    `json:"wrong"`
	Accuracy    int    `json:"accuracy"`
}

// FavItem represents a single favorited sub-question.
type FavItem struct {
	Fingerprint string `json:"fp"`
	SI          int    `json:"si"`
	AddedAt     int64  `json:"added_at"` // unix ms
}
