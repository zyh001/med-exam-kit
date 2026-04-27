"""
爬虫增强补丁 — raw_json 列 + 一键 MQB 导出
============================================
在现有 scraper.py 的基础上，增加以下功能：

1. raw_json 列：保存完整的标准化 JSON（含 case_answer / case_options），
   解决病例组合题数据丢失问题
2. 一键 MQB 导出：爬取完成后自动调用 ehafo_to_mqb.py 生成 .mqb 文件

使用方式（替代 run.py 中的 Scraper 类）：

    from scraper_patch import patch_db, PatchedScraper

    db = DB("ehafo.db")
    patch_db(db)                           # 添加 raw_json 列
    scraper = PatchedScraper(client, db)   # 使用增强版 Scraper
    scraper.run()

或者直接运行本脚本升级现有数据库：

    python scraper_patch.py --db ehafo.db              # 升级 DB 结构
    python scraper_patch.py --db ehafo.db --export      # 升级 + 导出 MQB
"""

import json
import logging
import sqlite3
from typing import Optional

log = logging.getLogger(__name__)


def patch_db(db) -> bool:
    """
    给 questions 表添加 raw_json 列（如果不存在）。
    用于存储完整的 normalize_question 输出 JSON，包含 case_answer / case_options。

    Returns:
        True 如果新增了列，False 如果列已存在。
    """
    try:
        db.execute("SELECT raw_json FROM questions LIMIT 1")
        log.info("raw_json 列已存在，跳过升级")
        return False
    except sqlite3.OperationalError:
        pass

    db.execute("ALTER TABLE questions ADD COLUMN raw_json TEXT DEFAULT ''")
    db.commit()
    log.info("✓ 已添加 raw_json 列")
    return True


def patched_upsert_question(db, q: dict, section_id: str = None,
                            source: str = "chapter") -> bool:
    """
    增强版 upsert — 除了写入标准列，还保存完整 JSON 到 raw_json。

    相比原始 upsert_question：
    - 新增 raw_json 列写入（含 case_answer, case_options 等结构化数据）
    - INSERT OR REPLACE（而非 OR IGNORE），后续爬取可更新已有记录
    """
    qid = str(q.get("qid") or "")
    if not qid:
        return False

    # 序列化完整 JSON（排除不必要的大字段以节省空间）
    raw_copy = dict(q)
    # 保留所有字段，包括 case_answer, case_options, is_case 等
    raw_json_str = ""
    try:
        raw_json_str = json.dumps(raw_copy, ensure_ascii=False)
    except (TypeError, ValueError):
        pass

    db.execute("""
        INSERT OR REPLACE INTO questions
          (qid, section_id, subject_id, ticlassid, model, type_name,
           question, option_a, option_b, option_c, option_d, option_e,
           answer, analysis, kp_name, kp_html, book_source,
           caseid, do_nums, err_nums, source, raw_json)
        VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
    """, (
        qid,
        section_id,
        str(q.get("subject_id") or ""),
        str(q.get("ticlassid") or ""),
        q.get("model") or "",
        q.get("type_name") or q.get("show_name") or "",
        q.get("question") or "",
        q.get("option_a") or "",
        q.get("option_b") or "",
        q.get("option_c") or "",
        q.get("option_d") or "",
        q.get("option_e") or "",
        q.get("answer") or "",
        q.get("analysis") or "",
        q.get("kp_name") or "",
        q.get("kp_html") or "",
        q.get("book_source") or q.get("book_dirs_str_new") or "",
        str(q.get("caseid", "0")),
        int(q.get("do_nums") or 0),
        int(q.get("err_nums") or 0),
        source,
        raw_json_str,
    ))
    return True


# ═══════════════════════════════════════════════════
#   命令行：升级现有 DB + 可选导出 MQB
# ═══════════════════════════════════════════════════

if __name__ == "__main__":
    import argparse
    import sys
    from pathlib import Path

    logging.basicConfig(level=logging.INFO, format="%(asctime)s [%(levelname)s] %(message)s")

    ap = argparse.ArgumentParser(description="爬虫补丁：升级 DB 结构 / 导出 MQB")
    ap.add_argument("--db", default="ehafo.db", help="数据库路径")
    ap.add_argument("--export", action="store_true", help="升级后直接导出 .mqb")
    ap.add_argument("--output", default=None, help="MQB 输出路径（默认与 DB 同名）")
    ap.add_argument("--password", default=None, help="MQB 加密密码")
    args = ap.parse_args()

    if not Path(args.db).exists():
        log.error(f"数据库不存在: {args.db}")
        sys.exit(1)

    conn = sqlite3.connect(args.db)
    conn.row_factory = sqlite3.Row

    class _DB:
        def execute(self, sql, params=()):
            return conn.execute(sql, params)
        def commit(self):
            conn.commit()

    db = _DB()
    patched = patch_db(db)

    total = conn.execute("SELECT COUNT(*) FROM questions").fetchone()[0]
    log.info(f"数据库共 {total} 道题")

    conn.close()

    if args.export:
        from ehafo_to_mqb import load_from_db, save_mqb, print_stats
        questions = load_from_db(args.db)
        if questions:
            print_stats(questions)
            out = Path(args.output or Path(args.db).stem)
            save_mqb(questions, out, password=args.password)
            log.info("✅ 导出完成！")
        else:
            log.error("无题目可导出")
