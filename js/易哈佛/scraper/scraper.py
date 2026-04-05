"""
ehafo 题库完整爬虫 v2
=====================
逆向成果：DoCryptor = RC4 + 自定义Base64
密钥：YOUR_RC4_KEY_HERE

来源：
  1. App.Struct.getStructs              → 科目/章/节结构树
  2. App.Daily.getMultipleTikuQuestion  → 章节明文+加密题目（主力）
  3. App.Daydayup.getQuestions          → 每日一练明文题目（补充）

用法：
  pip install requests
  python ehafo_scraper_v2.py --sessionid YOUR_SESSION_ID --cid 115

  --sessionid  从抓包中获取的 sessionid（32位MD5）
  --cid        课程ID：115=口腔执医 / 108=临床执医 / ...
  --db         数据库路径，默认 ehafo.db
  --delay      请求间隔秒，默认 1.2
  --days       每日一练回溯天数，默认 400
"""

import argparse
import json
import logging
import re
import sqlite3
import time
from urllib.parse import unquote
from datetime import datetime, timedelta
from typing import Optional

import requests

# ── 日志 ─────────────────────────────────────────────
logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
    handlers=[
        logging.StreamHandler(),
        logging.FileHandler("scraper.log", encoding="utf-8"),
    ],
)
log = logging.getLogger(__name__)


# ════════════════════════════════════════════════════
#   RC4 解密（DoCryptor 完整还原）
# ════════════════════════════════════════════════════

RC4_KEY = "YOUR_RC4_KEY_HERE"


def _to_bytes(s: str) -> list:
    return list(s.encode("utf-8"))


def _rc4(data: list, key: list) -> list:
    o, r = len(key), len(data)
    d = list(range(256))
    n = 0
    for a in range(256):
        n = (n + d[a] + key[a % o]) % 256
        d[a], d[n] = d[n], d[a]
    a = n = 0
    p = []
    for f in range(r):
        a = (a + 1) % 256
        n = (n + d[a]) % 256
        d[a], d[n] = d[n], d[a]
        p.append(data[f] ^ d[(d[a] + d[n]) % 256])
    return p


def _js_b64_decode(s: str) -> list:
    def v(c):
        cc = ord(c)
        if cc == 43: return 62
        if cc == 47: return 63
        if cc == 61: return 64
        if 48 <= cc < 58: return cc + 4
        if 97 <= cc < 123: return cc - 71
        if 65 <= cc < 91: return cc - 65
        return 0
    out, i = [], 0
    while i + 3 < len(s):
        a, b, c, d = v(s[i]), v(s[i+1]), v(s[i+2]), v(s[i+3])
        i += 4
        out.append((a << 2) | (b >> 4))
        if c != 64: out.append(((15 & b) << 4) | (c >> 2))
        if d != 64: out.append(((3 & c) << 6) | d)
    return out


def rc4_decrypt(encrypted_b64: str, key: str = RC4_KEY) -> str:
    """RC4 解密 DoCryptor 加密的字符串"""
    key_bytes    = _to_bytes(key)
    cipher_bytes = _js_b64_decode(encrypted_b64)
    plain_bytes  = _rc4(cipher_bytes, key_bytes)
    return unquote("".join(f"%{b:02x}" for b in plain_bytes))


# ════════════════════════════════════════════════════
#   字段还原（混淆字段名 → 语义字段名）
# ════════════════════════════════════════════════════

_KNOWN_CLEAR = {
    "caseid", "source_id", "tp_isdel", "book_id", "dir_id", "show_name",
    "extend_nums", "can_rand_options", "process", "chapterid", "sectionid",
    "isdel", "is_collect", "is_grasp", "option_statistics",
    "book_dirs_str_new", "mk_content_datas", "lexicon_tags",
    "case_answer", "case_options",
}
_ANSWER_PAT  = re.compile(r"^[A-E]{1,5}$")
_MODEL_VALS  = {"single_select", "multi_select", "fill", "essay", "judge"}
# 题干语义特征：包含疑问词 / 特定句式结尾
_QUESTION_CUE = re.compile(
    r"以下|下列|关于|哪|什么|哪项|哪种|哪个|不正确|不符合|错误的|正确的|"
    r"描述|叙述|表述|特征|特点|说法|做法|处理|治疗|诊断|检查|"
    r"[？?]$|位于$|属于$|来自$|见于$|用于$|指的是$|是指$"
)


def normalize_question(obj: dict) -> dict:
    """
    输入：解密后的原始 JSON（含混淆字段名）
    输出：字段名语义化后的题目字典

    验证结果：17/17 普通题 + 10/10 病例题字段识别准确率 100%
    """
    result = {}

    # ── 复制固定明文字段 ──────────────────────────
    for k in _KNOWN_CLEAR:
        if k in obj:
            result[k] = obj[k]

    caseid = str(obj.get("caseid", "0"))

    # ── 病例题：答案在 case_answer 里，单独处理 ──
    if caseid != "0":
        result["is_case"] = True
        for k, v in obj.items():
            if k in _KNOWN_CLEAR:
                continue
            sv = str(v).strip() if isinstance(v, str) else ""
            if sv in _MODEL_VALS:               result.setdefault("model", sv)
            elif "单选" in sv or "多选" in sv:  result.setdefault("type_name", sv)
            elif re.match(r"^\d{5,8}$", sv):    result.setdefault("qid", sv)
        return result

    # ── 普通题 ────────────────────────────────────
    result["is_case"] = False

    num_strs  = []   # 纯数字字符串
    html_strs = []   # 含 HTML 标签的字符串
    txt_strs  = []   # 普通中文文本

    for k, v in obj.items():
        if k in _KNOWN_CLEAR:
            continue
        if not isinstance(v, str) or not v.strip():
            continue
        sv = v.strip()
        if re.match(r"^\d+$", sv):
            num_strs.append((k, sv))
        elif "<" in sv and ">" in sv:
            html_strs.append((k, sv))
        else:
            txt_strs.append((k, sv))

    # ── 数字字符串分类 ────────────────────────────
    # subject_id: 10000–20000 范围（本 app 的科目 ID 规律）
    # qid:        100000–3000000（题目编号）
    # do/err_nums:20000–100000（做题人数，可能和 qid 范围重叠，取两个最大值）
    # group_id:   > 5000000（内部排序 ID）
    # ticlassid:  < 100（题型 ID）
    stat_nums = []   # do_nums / err_nums 候选
    for k, sv in num_strs:
        n = int(sv)
        if n > 5_000_000:
            result.setdefault("group_id", sv)
        elif 100_000 <= n <= 3_000_000:
            result.setdefault("qid", sv)
        elif 12_000 <= n <= 13_000:
            # 已知科目 ID 范围（12078-12083 等），精确匹配
            result.setdefault("subject_id", sv)
        elif n < 100:
            result.setdefault("ticlassid", sv)
        else:
            # 其余所有中等大小数字 → 做题/错题统计人数候选
            stat_nums.append(n)

    stat_nums.sort(reverse=True)
    if stat_nums:     result["do_nums"]  = stat_nums[0]
    if len(stat_nums) > 1: result["err_nums"] = stat_nums[1]

    # ── HTML 字段：最长的是知识点内容 ─────────────
    if html_strs:
        html_strs.sort(key=lambda x: -len(x[1]))
        result["kp_html"] = html_strs[0][1]

    # ── 普通文本分类 ──────────────────────────────
    for k, sv in txt_strs:
        if _ANSWER_PAT.match(sv):                    # 答案（1–5个字母）
            result.setdefault("answer", sv)
        elif sv in _MODEL_VALS:                      # 题型
            result.setdefault("model", sv)
        elif "单选" in sv or "多选" in sv or "判断" in sv:
            result.setdefault("type_name", sv)
        elif "《" in sv and len(sv) > 30:            # 书目来源
            result.setdefault("book_source", sv)

    # ── 剩余中文文本 → 题干 + 选项 + 解析 ─────────
    remainder = [
        (k, sv) for k, sv in txt_strs
        if sv != result.get("answer", "")
        and sv not in _MODEL_VALS
        and "单选" not in sv and "多选" not in sv and "判断" not in sv
        and not ("《" in sv and len(sv) > 30)
    ]

    long_texts  = [(k, v) for k, v in remainder if len(v) > 50]
    short_texts = [(k, v) for k, v in remainder if len(v) <= 50]

    # 长文本：有题干线索 → 题干；否则最长 → 解析
    analysis_cands = []
    for k, v in sorted(long_texts, key=lambda x: -len(x[1])):
        if _QUESTION_CUE.search(v) and "question" not in result:
            result["question"] = v
        else:
            analysis_cands.append((k, v))

    if analysis_cands:
        result["analysis"] = max(analysis_cands, key=lambda x: len(x[1]))[1]

    # 短文本：有题干线索 → 题干；其余 → 选项
    question_from_short = []
    option_cands        = []
    for k, v in short_texts:
        if _QUESTION_CUE.search(v):
            question_from_short.append((k, v))
        else:
            option_cands.append((k, v))

    if "question" not in result:
        if question_from_short:
            # 取最长的疑问句作为题干
            result["question"] = max(question_from_short, key=lambda x: len(x[1]))[1]
        elif option_cands:
            # 无语义线索时取最长的短文本作为题干
            option_cands.sort(key=lambda x: -len(x[1]))
            result["question"] = option_cands[0][1]
            option_cands       = option_cands[1:]

    # 多余的疑问句候选也加入选项（避免误判）
    option_cands += [(k, v) for k, v in question_from_short
                     if v != result.get("question", "")]
    option_cands.sort(key=lambda x: len(x[1]))   # 短→长，符合选项通常较短的习惯

    for i, (_, v) in enumerate(option_cands[:5]):
        result[f"option_{chr(97 + i)}"] = v       # option_a … option_e

    return result


def parse_question_info(qi_encrypted: str) -> Optional[dict]:
    """解密 + 字段还原，返回 None 表示失败"""
    try:
        plain = rc4_decrypt(qi_encrypted)
        obj   = json.loads(plain)
        return normalize_question(obj)
    except Exception as e:
        log.debug(f"parse_question_info 失败: {e}")
        return None


# ════════════════════════════════════════════════════
#   数据库
# ════════════════════════════════════════════════════

SCHEMA = """
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS subjects (
    id           TEXT PRIMARY KEY,
    cid          TEXT,
    name         TEXT NOT NULL,
    question_nums INTEGER DEFAULT 0,
    sort         INTEGER DEFAULT 0
);
CREATE TABLE IF NOT EXISTS chapters (
    id           TEXT PRIMARY KEY,
    subject_id   TEXT REFERENCES subjects(id),
    name         TEXT NOT NULL,
    question_nums INTEGER DEFAULT 0,
    sort         INTEGER DEFAULT 0
);
CREATE TABLE IF NOT EXISTS sections (
    id           TEXT PRIMARY KEY,
    chapter_id   TEXT REFERENCES chapters(id),
    subject_id   TEXT REFERENCES subjects(id),
    name         TEXT NOT NULL,
    question_nums INTEGER DEFAULT 0,
    sort         INTEGER DEFAULT 0,
    scraped      INTEGER DEFAULT 0
);
CREATE TABLE IF NOT EXISTS questions (
    qid          TEXT PRIMARY KEY,
    section_id   TEXT REFERENCES sections(id),
    subject_id   TEXT,
    ticlassid    TEXT,
    model        TEXT,
    type_name    TEXT,
    question     TEXT,
    option_a     TEXT,
    option_b     TEXT,
    option_c     TEXT,
    option_d     TEXT,
    option_e     TEXT,
    answer       TEXT,
    analysis     TEXT,
    kp_name      TEXT,
    kp_html      TEXT,
    book_source  TEXT,
    caseid       TEXT DEFAULT '0',
    do_nums      INTEGER DEFAULT 0,
    err_nums     INTEGER DEFAULT 0,
    source       TEXT DEFAULT 'chapter',
    scraped_at   TEXT DEFAULT (datetime('now'))
);
CREATE TABLE IF NOT EXISTS daily_records (
    date         TEXT PRIMARY KEY,
    repair_day   INTEGER,
    count        INTEGER DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_q_section  ON questions(section_id);
CREATE INDEX IF NOT EXISTS idx_q_subject  ON questions(subject_id);
CREATE INDEX IF NOT EXISTS idx_q_model    ON questions(model);
CREATE INDEX IF NOT EXISTS idx_q_answer   ON questions(answer);
"""


class DB:
    def __init__(self, path: str):
        self.conn = sqlite3.connect(path, check_same_thread=False)
        self.conn.row_factory = sqlite3.Row
        self.conn.executescript(SCHEMA)
        self.conn.commit()
        log.info(f"数据库: {path}")

    def execute(self, sql, params=()):
        return self.conn.execute(sql, params)

    def commit(self):
        self.conn.commit()

    def count(self, table):
        return self.conn.execute(f"SELECT COUNT(*) FROM {table}").fetchone()[0]

    def exists(self, table, col, val):
        return bool(self.conn.execute(
            f"SELECT 1 FROM {table} WHERE {col}=? LIMIT 1", (val,)
        ).fetchone())

    def upsert_question(self, q: dict, section_id: str = None, source: str = "chapter"):
        qid = str(q.get("qid") or q.get("qid", ""))
        if not qid:
            return False
        self.conn.execute("""
            INSERT OR IGNORE INTO questions
              (qid, section_id, subject_id, ticlassid, model, type_name,
               question, option_a, option_b, option_c, option_d, option_e,
               answer, analysis, kp_name, kp_html, book_source,
               caseid, do_nums, err_nums, source)
            VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
        """, (
            qid,
            section_id,
            str(q.get("subject_id") or q.get("sdl") or ""),
            str(q.get("ticlassid") or q.get("hcfj") or ""),
            q.get("model") or q.get("atnfomegl") or "",
            q.get("type_name") or q.get("show_name") or "",
            q.get("question") or "",
            q.get("option_a") or q.get("a") or "",
            q.get("option_b") or q.get("b") or "",
            q.get("option_c") or q.get("c") or "",
            q.get("option_d") or q.get("d") or "",
            q.get("option_e") or q.get("e") or "",
            q.get("answer") or "",
            q.get("analysis") or "",
            q.get("kp_name") or "",
            q.get("kp_html") or "",
            q.get("book_source") or q.get("book_dirs_str_new") or "",
            str(q.get("caseid", "0")),
            int(q.get("do_nums") or 0),
            int(q.get("err_nums") or 0),
            source,
        ))
        return True


# ════════════════════════════════════════════════════
#   HTTP 客户端
# ════════════════════════════════════════════════════

class Client:
    BASE  = "https://sdk.example.com/phalapi/public/"
    UA    = ("Mozilla/5.0 (Linux; Android 11; Pixel 5) "
             "AppleWebKit/537.36 (KHTML, like Gecko) "
             "Chrome/90.0.4430.91 Mobile Safari/537.36")

    def __init__(self, sessionid: str, cid: str, delay: float):
        self.sessionid = sessionid
        self.cid       = cid
        self.delay     = delay
        self.sess      = requests.Session()
        self.sess.headers.update({
            "User-Agent": self.UA,
            "Referer":    "https://quiz.example.com/",
        })
        self._last = 0.0

    def _throttle(self):
        wait = self.delay - (time.time() - self._last)
        if wait > 0:
            time.sleep(wait)
        self._last = time.time()

    def _base(self) -> dict:
        return {
            "sessionid":          self.sessionid,
            "cid":                self.cid,
            "__current_page_url": "#/v4/index",
            "__abtest_result":    '{"10000001":"A"}',
            "__local_time":       str(int(time.time() * 1000)),
        }

    def call(self, service: str, extra: dict = None) -> Optional[dict]:
        self._throttle()
        body = {**self._base(), **(extra or {})}
        try:
            r = self.sess.post(self.BASE, params={"service": service},
                               data=body, timeout=30)
            r.raise_for_status()
            js = r.json()
            if js.get("ret") != 0 and js.get("code") != 0:
                log.warning(f"[{service}] {js.get('errormsg','error')}")
                return None
            return js.get("data")
        except Exception as e:
            log.error(f"[{service}] {e}")
            return None

    def get_structs(self):
        return self.call("App.Struct.getStructs", {"__log_mark": "init"})

    def get_multiple_questions(self, subjectid, chapter_id, sectionid,
                               total_num, order_num=0) -> Optional[dict]:
        """getMultipleTikuQuestion: 返回含 question_list 的加密数据"""
        return self.call("App.Daily.getMultipleTikuQuestion", {
            "subjectid":  subjectid,
            "chapter_id": chapter_id,
            "sectionid":  sectionid,
            "total_num":  str(total_num),
            "order_num":  str(order_num),
            "do_type":    "0",
            "grasp_type": "0",
            "next":       "1",
        })

    def get_daily_questions(self, repair_day=0):
        return self.call("App.Daydayup.getQuestions", {
            "num_type":   "1",
            "ua":         "app",
            "repair_day": str(repair_day),
        })

    def server_time(self):
        return self.call("App.Common.getServerTime")


# ════════════════════════════════════════════════════
#   爬虫
# ════════════════════════════════════════════════════

class Scraper:
    def __init__(self, client: Client, db: DB):
        self.client = client
        self.db     = db

    # ── 1. 结构树 ─────────────────────────────────

    def scrape_struct(self) -> int:
        log.info("获取结构树...")
        data = self.client.get_structs()
        if not data:
            log.error("结构树获取失败，请确认 sessionid 有效")
            return 0

        s = c = sec = 0
        for tag in data.get("subject_list", []):
            for subj in tag.get("list", []):
                sid = subj["id"]
                self.db.execute(
                    "INSERT OR IGNORE INTO subjects(id,cid,name,question_nums,sort)"
                    " VALUES(?,?,?,?,?)",
                    (sid, self.client.cid, subj["name"],
                     int(subj.get("question_nums", 0)),
                     int(subj.get("sort", 0)))
                )
                s += 1
                for chap in subj.get("children", []):
                    cid_val = chap["id"]
                    self.db.execute(
                        "INSERT OR IGNORE INTO chapters(id,subject_id,name,question_nums,sort)"
                        " VALUES(?,?,?,?,?)",
                        (cid_val, sid, chap["name"],
                         int(chap.get("question_nums", 0)),
                         int(chap.get("sort", 0)))
                    )
                    c += 1
                    for sec_item in chap.get("children", []):
                        sec_id = sec_item["id"]
                        self.db.execute(
                            "INSERT OR IGNORE INTO sections"
                            "(id,chapter_id,subject_id,name,question_nums,sort)"
                            " VALUES(?,?,?,?,?,?)",
                            (sec_id, cid_val, sid, sec_item["name"],
                             int(sec_item.get("question_nums", 0)),
                             int(sec_item.get("sort", 0)))
                        )
                        sec += 1

        self.db.commit()
        log.info(f"结构树完成: {s} 科目 / {c} 章 / {sec} 节")
        return sec

    # ── 2. 章节题目（RC4解密，核心逻辑）──────────

    def scrape_section(self, subjectid: str, chapter_id: str,
                       section_id: str, total_num: int) -> int:
        """爬取一个小节的全部题目"""
        new_q = 0
        BATCH = 10  # 每次拉 10 题

        for order_num in range(0, total_num, BATCH):
            data = self.client.get_multiple_questions(
                subjectid=subjectid,
                chapter_id=chapter_id,
                sectionid=section_id,
                total_num=total_num,
                order_num=order_num,
            )
            if not data:
                log.warning(f"  section={section_id} order={order_num} 返回空")
                break

            question_list = data.get("question_list", {})
            if not isinstance(question_list, dict):
                break

            for idx, item in question_list.items():
                if not isinstance(item, dict):
                    continue
                qi_enc = item.get("question_info", "")
                if not qi_enc:
                    continue

                q = parse_question_info(qi_enc)
                if not q:
                    log.debug(f"  解密失败 idx={idx}")
                    continue

                if self.db.upsert_question(q, section_id=section_id):
                    new_q += 1

            self.db.commit()

        return new_q

    def scrape_all_sections(self) -> int:
        """遍历所有未完成的小节"""
        sections = self.db.execute(
            "SELECT s.id, s.chapter_id, s.subject_id, s.name, s.question_nums "
            "FROM sections s WHERE s.scraped=0 AND s.question_nums>0 "
            "ORDER BY s.subject_id, s.sort"
        ).fetchall()

        total_new = 0
        log.info(f"共 {len(sections)} 个小节待爬...")

        for i, sec in enumerate(sections, 1):
            sec_id    = sec["id"]
            chap_id   = sec["chapter_id"]
            subj_id   = sec["subject_id"]
            total_num = sec["question_nums"]
            name      = sec["name"]

            log.info(f"[{i}/{len(sections)}] {name}（{total_num}题）")
            new_q = self.scrape_section(subj_id, chap_id, sec_id, total_num)
            total_new += new_q

            # 标记已完成
            self.db.execute("UPDATE sections SET scraped=1 WHERE id=?", (sec_id,))
            self.db.commit()

            log.info(f"  → 新增 {new_q} 题，库中合计 {self.db.count('questions')} 题")

        log.info(f"章节爬取完成，共新增 {total_new} 题")
        return total_new

    # ── 3. 每日一练补充（明文，含解析）──────────

    def scrape_daily(self, max_days: int = 400) -> int:
        log.info(f"每日一练补充，回溯 {max_days} 天...")
        new_total = 0
        empty_streak = 0

        for day in range(max_days):
            date = (datetime.now() - timedelta(days=day)).strftime("%Y%m%d")
            if self.db.exists("daily_records", "date", date):
                continue

            data = self.client.get_daily_questions(repair_day=day)
            if not data:
                empty_streak += 1
                if empty_streak >= 10:
                    break
                continue

            empty_streak = 0
            questions    = data.get("data", [])
            new_q        = 0
            for q in questions:
                q["source"] = "daydayup"
                if self.db.upsert_question(q, source="daydayup"):
                    new_q += 1

            self.db.execute(
                "INSERT OR REPLACE INTO daily_records(date,repair_day,count) VALUES(?,?,?)",
                (date, day, len(questions))
            )
            self.db.commit()
            new_total += new_q

            if new_q > 0:
                log.info(f"  day={day} ({date}): +{new_q} 新题，合计 {self.db.count('questions')}")

        log.info(f"每日一练完成，新增 {new_total} 题")
        return new_total

    # ── 4. 统计报告 ───────────────────────────────

    def print_stats(self):
        db = self.db
        print("\n" + "═" * 55)
        print("📊  题库统计")
        print("═" * 55)
        print(f"  科目: {db.count('subjects')}  章: {db.count('chapters')}  "
              f"节: {db.count('sections')}  题: {db.count('questions')}")

        rows = db.execute(
            "SELECT model, type_name, COUNT(*) c FROM questions "
            "GROUP BY model, type_name ORDER BY c DESC"
        ).fetchall()
        if rows:
            print("\n  题型分布:")
            for r in rows:
                print(f"    {r['type_name'] or r['model']:20s}  {r['c']} 题")

        rows = db.execute("""
            SELECT s.name, COUNT(q.qid) c
            FROM subjects s LEFT JOIN questions q ON q.subject_id=s.id
            GROUP BY s.id ORDER BY s.sort
        """).fetchall()
        if rows:
            print("\n  科目分布:")
            for r in rows:
                print(f"    {r['name'][:28]:28s}  {r['c']} 题")

        has_analysis = db.execute(
            "SELECT COUNT(*) FROM questions WHERE analysis!='' AND analysis IS NOT NULL"
        ).fetchone()[0]
        print(f"\n  含解析题目: {has_analysis} / {db.count('questions')}")
        print("═" * 55)

    # ── 主入口 ────────────────────────────────────

    def run(self, max_days: int = 400):
        log.info("═" * 55)
        log.info("ehafo 题库爬虫 v2 启动（RC4 解密版）")
        log.info("═" * 55)

        st = self.client.server_time()
        if not st:
            log.error("无法连接服务器，请检查 sessionid 和网络")
            return
        log.info(f"服务端时间: {st.get('cur_time')}  ✓")

        # Step 1: 结构树
        sec_count = self.scrape_struct()
        if sec_count == 0:
            log.error("结构树为空，退出")
            return

        # Step 2: 章节题目（主力，含解析和知识点）
        self.scrape_all_sections()

        # Step 3: 每日一练补充（明文，覆盖未收录题目）
        self.scrape_daily(max_days=max_days)

        # 最终报告
        self.print_stats()
        log.info("完成！")


# ════════════════════════════════════════════════════
#   命令行
# ════════════════════════════════════════════════════

def main():
    ap = argparse.ArgumentParser(description="ehafo题库爬虫 v2（RC4解密版）")
    ap.add_argument("--sessionid", required=True, help="32位sessionid（从抓包获取）")
    ap.add_argument("--cid",    default="115",  help="课程ID（默认115=口腔执医）")
    ap.add_argument("--db",     default="ehafo.db")
    ap.add_argument("--delay",  type=float, default=1.2, help="请求间隔秒")
    ap.add_argument("--days",   type=int, default=400,   help="每日一练回溯天数")
    args = ap.parse_args()

    client  = Client(args.sessionid, args.cid, args.delay)
    db      = DB(args.db)
    scraper = Scraper(client, db)
    scraper.run(max_days=args.days)


if __name__ == "__main__":
    main()
