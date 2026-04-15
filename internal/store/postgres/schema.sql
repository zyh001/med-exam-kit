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

-- ── Learning Progress ───────────────────────────────────────────────
-- bank_id = 0 表示旧数据（升级前写入，尚未关联到具体题库）
-- 正常数据 bank_id = banks.id（通过迁移脚本或写入时注入）

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
CREATE INDEX IF NOT EXISTS idx_sess_uid      ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sess_uid_bank ON sessions(user_id, bank_id);
CREATE INDEX IF NOT EXISTS idx_sess_ts       ON sessions(ts);

CREATE TABLE IF NOT EXISTS attempts (
    id          BIGSERIAL PRIMARY KEY,
    user_id     TEXT    NOT NULL DEFAULT '_legacy',
    bank_id     BIGINT  NOT NULL DEFAULT 0,
    fingerprint TEXT    NOT NULL,
    session_id  TEXT,
    result      INTEGER NOT NULL,  -- 1=correct 0=wrong -1=skip
    mode        TEXT,
    unit        TEXT,
    ts          BIGINT  NOT NULL
);
-- 错题查询核心索引：(user_id, bank_id) + result 过滤 + fingerprint 聚合
CREATE INDEX IF NOT EXISTS idx_att_uid_bank     ON attempts(user_id, bank_id);
CREATE INDEX IF NOT EXISTS idx_att_uid_bank_fp  ON attempts(user_id, bank_id, fingerprint);
CREATE INDEX IF NOT EXISTS idx_att_uid_bank_res ON attempts(user_id, bank_id, result);
CREATE INDEX IF NOT EXISTS idx_att_fp           ON attempts(fingerprint);
CREATE INDEX IF NOT EXISTS idx_att_ts           ON attempts(ts);
-- 保留旧索引，兼容未升级的查询
CREATE INDEX IF NOT EXISTS idx_att_uid          ON attempts(user_id);
CREATE INDEX IF NOT EXISTS idx_att_uid_fp       ON attempts(user_id, fingerprint);

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
CREATE INDEX IF NOT EXISTS idx_sm2_uid_bank     ON sm2(user_id, bank_id);
CREATE INDEX IF NOT EXISTS idx_sm2_uid_bank_due ON sm2(user_id, bank_id, next_due);

-- ── Migration: 升级旧版本（幂等，可重复执行）──────────────────────

-- 1. 加列（已有则跳过）
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS bank_id BIGINT NOT NULL DEFAULT 0;
ALTER TABLE attempts ADD COLUMN IF NOT EXISTS bank_id BIGINT NOT NULL DEFAULT 0;
ALTER TABLE sm2      ADD COLUMN IF NOT EXISTS bank_id BIGINT NOT NULL DEFAULT 0;

-- 2. 修复旧数据：通过 fingerprint 反查 questions 表补全 bank_id
--    只修 bank_id=0 的旧数据，新数据（bank_id>0）不受影响
UPDATE attempts a
SET    bank_id = q.bank_id
FROM   questions q
WHERE  a.bank_id = 0
  AND  a.fingerprint = q.fingerprint;

-- sm2 同理
UPDATE sm2 s
SET    bank_id = q.bank_id
FROM   questions q
WHERE  s.bank_id = 0
  AND  s.fingerprint = q.fingerprint;

-- sessions 通过关联 attempts 反查（session 本身没有 fingerprint）
UPDATE sessions ses
SET    bank_id = sub.bank_id
FROM  (
    SELECT DISTINCT session_id, bank_id
    FROM   attempts
    WHERE  bank_id > 0
) sub
WHERE  ses.bank_id = 0
  AND  ses.id = sub.session_id;

-- ── Share Tokens（试卷分享持久化，仅 PG 模式）──────────────────────
-- 在 SQLite / 内存模式下 share token 仅存内存，重启失效；
-- PG 模式下写入此表，重启后可恢复，有效期 7 天。
CREATE TABLE IF NOT EXISTS share_tokens (
    token      TEXT      PRIMARY KEY,
    bank_idx   INTEGER   NOT NULL DEFAULT 0,
    mode       TEXT      NOT NULL DEFAULT 'exam',
    time_limit INTEGER   NOT NULL DEFAULT 5400,   -- seconds
    fps        JSONB     NOT NULL DEFAULT '[]',   -- fingerprint list
    created_at BIGINT    NOT NULL,                -- unix timestamp
    expires_at BIGINT    NOT NULL                 -- unix timestamp
);
CREATE INDEX IF NOT EXISTS idx_share_expires ON share_tokens(expires_at);

-- ── Favorites（收藏夹持久化）──────────────────────────────────────
-- 仅存 fingerprint+si+时间戳，题目完整数据前端本地缓存，服务端不重复存储。
CREATE TABLE IF NOT EXISTS favorites (
    user_id     TEXT    NOT NULL,
    bank_id     BIGINT  NOT NULL DEFAULT 0,
    fingerprint TEXT    NOT NULL,
    si          INTEGER NOT NULL DEFAULT 0,
    added_at    BIGINT  NOT NULL,            -- unix ms，用于跨端冲突解决（最新时间戳胜出）
    PRIMARY KEY (user_id, bank_id, fingerprint, si)
);
CREATE INDEX IF NOT EXISTS idx_fav_uid_bank ON favorites(user_id, bank_id);

