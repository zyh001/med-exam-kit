package exporters

import (
	"database/sql"
	"fmt"
	"os"

	"github.com/med-exam-kit/med-exam-kit/internal/models"
	_ "modernc.org/sqlite"
)

// ExportDB writes questions to a SQLite database.
func ExportDB(questions []*models.Question, outPath string) error {
	os.Remove(outPath)
	db, err := sql.Open("sqlite", outPath)
	if err != nil {
		return err
	}
	defer db.Close()

	ddl := `CREATE TABLE IF NOT EXISTS questions (
		id             INTEGER PRIMARY KEY AUTOINCREMENT,
		fingerprint    TEXT,
		name           TEXT,
		pkg            TEXT,
		cls            TEXT,
		unit           TEXT,
		mode           TEXT,
		stem           TEXT,
		text           TEXT,
		answer         TEXT,
		answer_source  TEXT,
		discuss        TEXT,
		discuss_source TEXT,
		rate           TEXT,
		error_prone    TEXT,
		point          TEXT,
		options        TEXT,
		ai_answer      TEXT,
		ai_discuss     TEXT,
		ai_confidence  REAL,
		ai_model       TEXT,
		ai_status      TEXT
	);`
	if _, err = db.Exec(ddl); err != nil {
		return fmt.Errorf("db export: create table: %w", err)
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`INSERT INTO questions
		(fingerprint,name,pkg,cls,unit,mode,stem,text,answer,answer_source,
		 discuss,discuss_source,rate,error_prone,point,options,
		 ai_answer,ai_discuss,ai_confidence,ai_model,ai_status)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, q := range questions {
		for _, sq := range q.SubQuestions {
			opts := ""
			for i, o := range sq.Options {
				if i > 0 {
					opts += "|"
				}
				opts += o
			}
			_, err = stmt.Exec(
				q.Fingerprint, q.Name, q.Pkg, q.Cls, q.Unit, q.Mode, q.Stem,
				sq.Text,
				sq.EffAnswer(), sq.AnswerSource(),
				sq.EffDiscuss(), sq.DiscussSource(),
				sq.Rate, sq.ErrorProne, sq.Point, opts,
				sq.AIAnswer, sq.AIDiscuss, sq.AIConfidence, sq.AIModel, sq.AIStatus,
			)
			if err != nil {
				return fmt.Errorf("db export: insert: %w", err)
			}
		}
	}
	return tx.Commit()
}
