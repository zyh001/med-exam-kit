"""做题进度持久化：错题本 + SM-2 间隔复习 + 历史统计

数据库文件与题库同目录，后缀 .progress.db，例如：
  exam.mqb  →  exam.progress.db

多用户隔离
──────────
每个浏览器会话通过 Cookie "med_exam_uid" 分配一个持久 UUID。
所有读写操作均以 user_id 过滤，用户之间数据完全隔离。
旧版无 user_id 的历史数据归入伪用户 '_legacy'。
"""
from __future__ import annotations

import json
import sqlite3
import time
from contextlib import contextmanager
from datetime import date, timedelta
from pathlib import Path

_DEFAULT_EF = 2.5
_MIN_EF     = 1.3
LEGACY_USER = "_legacy"

_DDL = """
CREATE TABLE IF NOT EXISTS sessions (
    id        TEXT    NOT NULL,
    user_id   TEXT    NOT NULL DEFAULT '_legacy',
    mode      TEXT,
    total     INTEGER DEFAULT 0,
    correct   INTEGER DEFAULT 0,
    wrong     INTEGER DEFAULT 0,
    skip      INTEGER DEFAULT 0,
    time_sec  INTEGER DEFAULT 0,
    sess_date TEXT,
    units     TEXT    DEFAULT '[]',
    ts        INTEGER NOT NULL,
    PRIMARY KEY (user_id, id)
);
CREATE INDEX IF NOT EXISTS idx_sess_uid ON sessions(user_id);

CREATE TABLE IF NOT EXISTS attempts (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id     TEXT    NOT NULL DEFAULT '_legacy',
    fingerprint TEXT    NOT NULL,
    session_id  TEXT,
    result      INTEGER NOT NULL,
    mode        TEXT,
    unit        TEXT,
    ts          INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_att_fp     ON attempts(fingerprint);
CREATE INDEX IF NOT EXISTS idx_att_ts     ON attempts(ts);
CREATE INDEX IF NOT EXISTS idx_att_uid    ON attempts(user_id);
CREATE INDEX IF NOT EXISTS idx_att_uid_fp ON attempts(user_id, fingerprint);

CREATE TABLE IF NOT EXISTS sm2 (
    user_id     TEXT    NOT NULL DEFAULT '_legacy',
    fingerprint TEXT    NOT NULL,
    ef          REAL    NOT NULL DEFAULT 2.5,
    interval    INTEGER NOT NULL DEFAULT 1,
    reps        INTEGER NOT NULL DEFAULT 0,
    next_due    TEXT    NOT NULL,
    updated_at  INTEGER NOT NULL,
    PRIMARY KEY (user_id, fingerprint)
);
"""


def db_path_for_bank(bank_path: Path) -> Path:
    return bank_path.with_suffix(".progress.db")


@contextmanager
def _open(db_path: Path):
    with sqlite3.connect(str(db_path), check_same_thread=False) as c:
        c.row_factory = sqlite3.Row
        c.execute("PRAGMA journal_mode=WAL")
        c.execute("PRAGMA foreign_keys=ON")
        yield c


def init_db(db_path: Path) -> None:
    """初始化或升级数据库，幂等安全。"""
    with _open(db_path) as c:
        # 建新表
        c.executescript(_DDL)

        # 迁移旧版 sessions（单列主键 → 复合主键）
        cols_sess = {r[1] for r in c.execute("PRAGMA table_info(sessions)")}
        if "user_id" not in cols_sess:
            c.executescript("""
                ALTER TABLE sessions RENAME TO sessions_old;
                CREATE TABLE sessions (
                    id TEXT NOT NULL, user_id TEXT NOT NULL DEFAULT '_legacy',
                    mode TEXT, total INTEGER DEFAULT 0, correct INTEGER DEFAULT 0,
                    wrong INTEGER DEFAULT 0, skip INTEGER DEFAULT 0,
                    time_sec INTEGER DEFAULT 0, sess_date TEXT,
                    units TEXT DEFAULT '[]', ts INTEGER NOT NULL,
                    PRIMARY KEY (user_id, id)
                );
                INSERT OR IGNORE INTO sessions
                    SELECT id,'_legacy',mode,total,correct,wrong,skip,
                           time_sec,sess_date,units,ts FROM sessions_old;
                DROP TABLE sessions_old;
                CREATE INDEX IF NOT EXISTS idx_sess_uid ON sessions(user_id);
            """)

        # 迁移旧版 sm2（单列主键 → 复合主键）
        cols_sm2 = {r[1] for r in c.execute("PRAGMA table_info(sm2)")}
        if "user_id" not in cols_sm2:
            c.executescript("""
                ALTER TABLE sm2 RENAME TO sm2_old;
                CREATE TABLE sm2 (
                    user_id TEXT NOT NULL DEFAULT '_legacy',
                    fingerprint TEXT NOT NULL,
                    ef REAL NOT NULL DEFAULT 2.5, interval INTEGER NOT NULL DEFAULT 1,
                    reps INTEGER NOT NULL DEFAULT 0, next_due TEXT NOT NULL,
                    updated_at INTEGER NOT NULL,
                    PRIMARY KEY (user_id, fingerprint)
                );
                INSERT OR IGNORE INTO sm2
                    SELECT '_legacy',fingerprint,ef,interval,reps,next_due,updated_at
                    FROM sm2_old;
                DROP TABLE sm2_old;
            """)

        # 迁移旧版 attempts（加 user_id 列）
        cols_att = {r[1] for r in c.execute("PRAGMA table_info(attempts)")}
        if "user_id" not in cols_att:
            try:
                c.execute("ALTER TABLE attempts ADD COLUMN user_id TEXT NOT NULL DEFAULT '_legacy'")
            except sqlite3.OperationalError:
                pass


# ── 写操作 ────────────────────────────────────────────────────────────

def record_session(db_path: Path, session: dict, user_id: str = LEGACY_USER) -> None:
    now_ms = int(time.time() * 1000)
    today  = date.today().isoformat()
    items  = session.get("items", [])

    with _open(db_path) as c:
        c.execute(
            """INSERT OR REPLACE INTO sessions
               (id,user_id,mode,total,correct,wrong,skip,time_sec,sess_date,units,ts)
               VALUES (?,?,?,?,?,?,?,?,?,?,?)""",
            (session["id"], user_id, session.get("mode"),
             session.get("total",0), session.get("correct",0),
             session.get("wrong",0), session.get("skip",0),
             session.get("time_sec",0), session.get("date",today),
             json.dumps(session.get("units",[]), ensure_ascii=False), now_ms),
        )
        for item in items:
            fp  = item.get("fingerprint")
            res = item.get("result", -1)
            if not fp:
                continue
            c.execute(
                """INSERT INTO attempts
                   (user_id,fingerprint,session_id,result,mode,unit,ts)
                   VALUES (?,?,?,?,?,?,?)""",
                (user_id, fp, session["id"], res,
                 item.get("mode"), item.get("unit"), now_ms),
            )
            if res != -1:
                # 优先使用 item 中携带的 SM-2 quality（0-5），
                # 背题模式会传入精确评分；练习/考试模式回退到二值映射
                quality = item.get("quality")
                if quality is None:
                    quality = 4 if res == 1 else 1
                quality = max(0, min(5, int(quality)))
                _update_sm2(c, user_id, fp, quality, today)


def _update_sm2(c, user_id: str, fingerprint: str, quality: int, today: str) -> None:
    row = c.execute(
        "SELECT ef,interval,reps FROM sm2 WHERE user_id=? AND fingerprint=?",
        (user_id, fingerprint),
    ).fetchone()
    ef       = float(row["ef"])     if row else _DEFAULT_EF
    interval = int(row["interval"]) if row else 1
    reps     = int(row["reps"])     if row else 0

    if quality < 3:
        reps = 0; interval = 1
    else:
        if   reps == 0: interval = 1
        elif reps == 1: interval = 6
        else:           interval = round(interval * ef)
        reps += 1
    ef = max(_MIN_EF, ef + 0.1 - (5-quality)*(0.08 + (5-quality)*0.02))
    next_due = (date.fromisoformat(today) + timedelta(days=interval)).isoformat()
    c.execute(
        """INSERT OR REPLACE INTO sm2
           (user_id,fingerprint,ef,interval,reps,next_due,updated_at)
           VALUES (?,?,?,?,?,?,?)""",
        (user_id, fingerprint, ef, interval, reps, next_due,
         int(time.time()*1000)),
    )


def record_sessions_batch(
    db_path: Path,
    sessions: list[dict],
    user_id: str = LEGACY_USER,
) -> dict:
    """批量写入多条答题会话（供离线同步端点使用）。

    - 已存在的 session_id 用 INSERT OR IGNORE 跳过（不覆盖服务端已有记录）
    - attempts 同理：重复 (user_id, fingerprint, session_id) 由 UNIQUE INDEX 去重
    - 返回 {processed: [session_id, ...], skipped: [session_id, ...]}
    """
    processed: list[str] = []
    skipped:   list[str] = []
    today = date.today().isoformat()
    now_ms = int(time.time() * 1000)

    with _open(db_path) as c:
        for session in sessions:
            sid = session.get("id")
            if not sid:
                continue

            # 检查 session 是否已存在（离线重传场景）
            exists = c.execute(
                "SELECT 1 FROM sessions WHERE user_id=? AND id=?", (user_id, sid)
            ).fetchone()
            if exists:
                skipped.append(sid)
                continue

            # 写入 session 行
            c.execute(
                """INSERT OR IGNORE INTO sessions
                   (id,user_id,mode,total,correct,wrong,skip,time_sec,sess_date,units,ts)
                   VALUES (?,?,?,?,?,?,?,?,?,?,?)""",
                (sid, user_id, session.get("mode"),
                 session.get("total", 0), session.get("correct", 0),
                 session.get("wrong", 0), session.get("skip", 0),
                 session.get("time_sec", 0), session.get("date", today),
                 json.dumps(session.get("units", []), ensure_ascii=False), now_ms),
            )

            # 写入 attempts + 更新 SM-2
            for item in session.get("items", []):
                fp  = item.get("fingerprint")
                res = item.get("result", -1)
                if not fp:
                    continue
                try:
                    c.execute(
                        """INSERT INTO attempts
                           (user_id,fingerprint,session_id,result,mode,unit,ts)
                           VALUES (?,?,?,?,?,?,?)""",
                        (user_id, fp, sid, res,
                         item.get("mode"), item.get("unit"), now_ms),
                    )
                except Exception:
                    pass  # 重复 attempt，跳过
                if res != -1:
                    quality = item.get("quality")
                    if quality is None:
                        quality = 4 if res == 1 else 1
                    quality = max(0, min(5, int(quality)))
                    _update_sm2(c, user_id, fp, quality, today)

            processed.append(sid)

    return {"processed": processed, "skipped": skipped}


def get_sync_status(db_path: Path, user_id: str = LEGACY_USER) -> dict:
    """返回数据库中该用户的会话数和最近同步时间，供前端展示。"""
    with _open(db_path) as c:
        row = c.execute(
            "SELECT COUNT(*) AS cnt, MAX(ts) AS last_ts FROM sessions WHERE user_id=?",
            (user_id,),
        ).fetchone()
    return {
        "session_count": row["cnt"] if row else 0,
        "last_ts":       row["last_ts"] if row else None,
    }


def clear_user_data(db_path: Path, user_id: str) -> dict:
    """清空指定用户的全部做题记录，返回各表删除行数。"""
    with _open(db_path) as c:
        att  = c.execute("DELETE FROM attempts WHERE user_id=?", (user_id,)).rowcount
        sess = c.execute("DELETE FROM sessions WHERE user_id=?", (user_id,)).rowcount
        sm2  = c.execute("DELETE FROM sm2      WHERE user_id=?", (user_id,)).rowcount
    return {"attempts": att, "sessions": sess, "sm2_cards": sm2}



def migrate_user_data(db_path: Path, from_uid: str, to_uid: str) -> dict:
    """将 from_uid 的全部记录合并到 to_uid，跳过冲突，完成后删除源数据。

    三张表的冲突策略：
      sessions  — PRIMARY KEY (user_id, id)，用 INSERT OR IGNORE 跳过重复
      attempts  — autoincrement PK，无冲突，全量复制
      sm2       — PRIMARY KEY (user_id, fingerprint)，to_uid 已有的保留，不覆盖
    所有操作在同一个 connection（WAL 模式）内完成，_open 上下文退出时统一 commit。
    """
    if not from_uid or not to_uid or from_uid == to_uid:
        raise ValueError("无效的用户 ID")
    with _open(db_path) as c:
        sess = c.execute(
            """INSERT OR IGNORE INTO sessions
                   (id, user_id, mode, total, correct, wrong, skip,
                    time_sec, sess_date, units, ts)
               SELECT id, ?, mode, total, correct, wrong, skip,
                      time_sec, sess_date, units, ts
               FROM sessions WHERE user_id=?""",
            (to_uid, from_uid),
        ).rowcount
        att = c.execute(
            """INSERT INTO attempts
                   (user_id, fingerprint, session_id, result, mode, unit, ts)
               SELECT ?, fingerprint, session_id, result, mode, unit, ts
               FROM attempts WHERE user_id=?""",
            (to_uid, from_uid),
        ).rowcount
        sm2 = c.execute(
            """INSERT OR IGNORE INTO sm2
                   (user_id, fingerprint, ef, interval, reps, next_due, updated_at)
               SELECT ?, fingerprint, ef, interval, reps, next_due, updated_at
               FROM sm2 WHERE user_id=?""",
            (to_uid, from_uid),
        ).rowcount
        for tbl in ("attempts", "sessions", "sm2"):
            c.execute(f"DELETE FROM {tbl} WHERE user_id=?", (from_uid,))  # noqa: S608
    return {"sessions": sess, "attempts": att, "sm2_cards": sm2}


# ── 读操作 ────────────────────────────────────────────────────────────

def get_due_fingerprints(db_path: Path, user_id: str = LEGACY_USER) -> list[str]:
    today = date.today().isoformat()
    with _open(db_path) as c:
        rows = c.execute(
            "SELECT fingerprint FROM sm2 WHERE user_id=? AND next_due<=? ORDER BY next_due ASC",
            (user_id, today),
        ).fetchall()
    return [r["fingerprint"] for r in rows]


def get_wrong_fingerprints(
    db_path: Path, user_id: str = LEGACY_USER, limit: int = 300
) -> list[dict]:
    with _open(db_path) as c:
        rows = c.execute(
            """SELECT fingerprint,
                COUNT(*) AS total,
                SUM(CASE WHEN result=1 THEN 1 ELSE 0 END) AS correct,
                SUM(CASE WHEN result=0 THEN 1 ELSE 0 END) AS wrong,
                MAX(ts) AS last_ts
               FROM attempts
               WHERE user_id=? AND result!=-1
               GROUP BY fingerprint HAVING wrong>0
               ORDER BY wrong DESC, last_ts DESC LIMIT ?""",
            (user_id, limit),
        ).fetchall()
    return [{"fingerprint":r["fingerprint"],"total":r["total"],"correct":r["correct"],
             "wrong":r["wrong"],
             "accuracy":round(r["correct"]/r["total"]*100) if r["total"] else 0}
            for r in rows]


def get_history(db_path: Path, user_id: str = LEGACY_USER, limit: int = 30) -> list[dict]:
    with _open(db_path) as c:
        rows = c.execute(
            """SELECT id,mode,total,correct,wrong,skip,time_sec,sess_date,units,ts
               FROM sessions WHERE user_id=? ORDER BY ts DESC LIMIT ?""",
            (user_id, limit),
        ).fetchall()
    return [{"id":r["id"],"mode":r["mode"],"total":r["total"],"correct":r["correct"],
             "wrong":r["wrong"],"skip":r["skip"],"time_sec":r["time_sec"],
             "date":r["sess_date"],"units":json.loads(r["units"] or "[]"),
             "pct":round(r["correct"]/r["total"]*100) if r["total"] else 0}
            for r in rows]


def delete_session(db_path: Path, session_id: str, user_id: str = LEGACY_USER) -> bool:
    """按 id 删除指定用户的会话记录，返回是否真正删除了一行。"""
    with _open(db_path) as c:
        c.execute(
            "DELETE FROM sessions WHERE id=? AND user_id=?",
            (session_id, user_id),
        )
        return c.rowcount > 0


def get_unit_stats(db_path: Path, user_id: str = LEGACY_USER) -> list[dict]:
    with _open(db_path) as c:
        rows = c.execute(
            """SELECT unit, COUNT(*) AS total,
                SUM(CASE WHEN result=1 THEN 1 ELSE 0 END) AS correct
               FROM attempts
               WHERE user_id=? AND result!=-1 AND unit IS NOT NULL AND unit!=''
               GROUP BY unit ORDER BY total DESC""",
            (user_id,),
        ).fetchall()
    return [{"unit":r["unit"],"total":r["total"],"correct":r["correct"],
             "wrong":r["total"]-r["correct"],
             "accuracy":round(r["correct"]/r["total"]*100) if r["total"] else 0}
            for r in rows]


def get_overall_stats(db_path: Path, user_id: str = LEGACY_USER) -> dict:
    today = date.today().isoformat()
    with _open(db_path) as c:
        att = c.execute(
            """SELECT COUNT(*) AS total,
                SUM(CASE WHEN result=1 THEN 1 ELSE 0 END) AS correct,
                SUM(CASE WHEN result=0 THEN 1 ELSE 0 END) AS wrong
               FROM attempts WHERE user_id=? AND result!=-1""",
            (user_id,),
        ).fetchone()
        sessions = c.execute(
            "SELECT COUNT(*) AS cnt FROM sessions WHERE user_id=?", (user_id,)
        ).fetchone()["cnt"]
        due = c.execute(
            "SELECT COUNT(*) AS cnt FROM sm2 WHERE user_id=? AND next_due<=?",
            (user_id, today),
        ).fetchone()["cnt"]
        wrong_topics = c.execute(
            "SELECT COUNT(DISTINCT fingerprint) FROM attempts WHERE user_id=? AND result=0",
            (user_id,),
        ).fetchone()[0]
    total = att["total"] or 0; correct = att["correct"] or 0
    return {"total_attempts":total,"correct":correct,
            "wrong_attempts":att["wrong"] or 0,
            "accuracy":round(correct/total*100) if total else 0,
            "sessions":sessions,"due_today":due,"wrong_topics":wrong_topics}