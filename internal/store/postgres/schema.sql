-- PostgreSQL schema for med-exam-kit
-- Progress tables mirror the SQLite schema exactly for easy migration.

-- ── Question Banks ─────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS banks (
    id         BIGSERIAL   PRIMARY KEY,
    name       TEXT        NOT NULL UNIQUE,
    source     TEXT        DEFAULT '',
    count      INTEGER     DEFAULT 0,
    created_at TIMESTAMPTZ DEFAULT now()
);

CREATE TABLE IF NOT EXISTS questions (
    id          BIGSERIAL PRIMARY KEY,
    bank_id     BIGINT    NOT NULL REFERENCES banks(id) ON DELETE CASCADE,
    fingerprint TEXT      NOT NULL,
    name        TEXT      DEFAULT '',
    pkg         TEXT      DEFAULT '',
    cls         TEXT      DEFAULT '',
    unit        TEXT      DEFAULT '',
    mode        TEXT      DEFAULT '',
    stem        TEXT      DEFAULT '',
    shared_opts JSONB     DEFAULT '[]',
    discuss     TEXT      DEFAULT '',
    source_file TEXT      DEFAULT '',
    UNIQUE (bank_id, fingerprint)
);
CREATE INDEX IF NOT EXISTS idx_q_bank    ON questions(bank_id);
CREATE INDEX IF NOT EXISTS idx_q_unit    ON questions(bank_id, unit);
CREATE INDEX IF NOT EXISTS idx_q_fp      ON questions(fingerprint);

CREATE TABLE IF NOT EXISTS sub_questions (
    id          BIGSERIAL PRIMARY KEY,
    question_id BIGINT    NOT NULL REFERENCES questions(id) ON DELETE CASCADE,
    position    INTEGER   DEFAULT 0,
    text        TEXT      DEFAULT '',
    options     JSONB     DEFAULT '[]',
    answer      TEXT      DEFAULT '',
    discuss     TEXT      DEFAULT '',
    point       TEXT      DEFAULT '',
    rate        TEXT      DEFAULT '',
    error_prone TEXT      DEFAULT '',
    ai_answer   TEXT      DEFAULT '',
    ai_discuss  TEXT      DEFAULT '',
    ai_confidence FLOAT   DEFAULT 0,
    ai_model    TEXT      DEFAULT '',
    ai_status   TEXT      DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_sq_qid ON sub_questions(question_id);

-- ── Learning Progress (mirrors SQLite schema) ──────────────────────
CREATE TABLE IF NOT EXISTS sessions (
    id        TEXT    NOT NULL,
    user_id   TEXT    NOT NULL DEFAULT '_legacy',
    bank_id   BIGINT  NOT NULL DEFAULT 0,
    mode      TEXT,
    total     INTEGER DEFAULT 0,
    correct   INTEGER DEFAULT 0,
    wrong     INTEGER DEFAULT 0,
    skip      INTEGER DEFAULT 0,
    time_sec  INTEGER DEFAULT 0,
    sess_date TEXT,
    units     JSONB   DEFAULT '[]',
    ts        BIGINT  NOT NULL,
    PRIMARY KEY (user_id, bank_id, id)
);
CREATE INDEX IF NOT EXISTS idx_sess_uid ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sess_ts  ON sessions(ts);

CREATE TABLE IF NOT EXISTS attempts (
    id          BIGSERIAL PRIMARY KEY,
    user_id     TEXT    NOT NULL DEFAULT '_legacy',
    bank_id     BIGINT  NOT NULL DEFAULT 0,
    fingerprint TEXT    NOT NULL,
    session_id  TEXT,
    result      INTEGER NOT NULL,
    mode        TEXT,
    unit        TEXT,
    ts          BIGINT  NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_att_fp     ON attempts(fingerprint);
CREATE INDEX IF NOT EXISTS idx_att_ts     ON attempts(ts);
CREATE INDEX IF NOT EXISTS idx_att_uid    ON attempts(user_id);
CREATE INDEX IF NOT EXISTS idx_att_uid_fp ON attempts(user_id, fingerprint);

CREATE TABLE IF NOT EXISTS sm2 (
    user_id     TEXT    NOT NULL DEFAULT '_legacy',
    bank_id     BIGINT  NOT NULL DEFAULT 0,
    fingerprint TEXT    NOT NULL,
    ef          FLOAT   NOT NULL DEFAULT 2.5,
    interval    INTEGER NOT NULL DEFAULT 1,
    reps        INTEGER NOT NULL DEFAULT 0,
    next_due    TEXT    NOT NULL,
    updated_at  BIGINT  NOT NULL,
    PRIMARY KEY (user_id, bank_id, fingerprint)
);

-- ── Migration: add bank_id if upgrading from old schema ────────────
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS bank_id BIGINT NOT NULL DEFAULT 0;
ALTER TABLE attempts ADD COLUMN IF NOT EXISTS bank_id BIGINT NOT NULL DEFAULT 0;
ALTER TABLE sm2      ADD COLUMN IF NOT EXISTS bank_id BIGINT NOT NULL DEFAULT 0;

-- Additional indexes for bank-scoped queries
CREATE INDEX IF NOT EXISTS idx_sess_uid_bank ON sessions(user_id, bank_id);
CREATE INDEX IF NOT EXISTS idx_att_uid_bank  ON attempts(user_id, bank_id);
CREATE INDEX IF NOT EXISTS idx_sm2_uid_bank  ON sm2(user_id, bank_id);
