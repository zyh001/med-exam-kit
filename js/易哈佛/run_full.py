#!/usr/bin/env python3
"""
ehafo 题库完整管线 — 爬取 + MQB 导出
=====================================
一键完成：认证 → 爬取 → SQLite → .mqb 导出

用法：
    python run_full.py                          # 首次运行（扫码登录 + 爬取 + 导出）
    python run_full.py                          # 后续运行（复用 session + 断点续爬）
    python run_full.py --relogin                # 强制重新登录
    python run_full.py --export-only            # 仅从已有 DB 导出 MQB（跳过爬取）
    python run_full.py --split-by-subject       # 按科目拆分 MQB
    python run_full.py --sessionid XXX --cid 115  # 手动指定凭据

    # 如果不需要 Playwright 自动登录，可以手动指定 sessionid：
    python run_full.py --sessionid <从抓包获取的32位MD5> --cid 115

输出文件：
    ehafo.db    — SQLite 数据库（爬虫中间产物）
    ehafo.mqb   — MQB2 题库文件（最终产物，供 med-exam quiz 使用）

使用 MQB：
    med-exam quiz ehafo.mqb
    # 或
    python -m med_exam_toolkit.cli quiz ehafo.mqb
"""

import argparse
import logging
import sys
from pathlib import Path

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s [%(levelname)s] %(message)s",
    handlers=[
        logging.StreamHandler(),
        logging.FileHandler("pipeline.log", encoding="utf-8"),
    ],
)
log = logging.getLogger(__name__)


def get_credentials(args) -> tuple[str, str]:
    """获取 sessionid 和 cid，支持多种方式。"""

    # 方式一：命令行直接指定
    if args.sessionid:
        return args.sessionid, args.cid

    # 方式二：Playwright 自动登录
    try:
        from auth_manager import get_credentials as auto_get
        return auto_get(force_relogin=args.relogin)
    except ImportError:
        pass

    # 方式三：交互式输入
    print("\n未找到 auth_manager.py 且未指定 --sessionid")
    print("请从 mitmproxy 抓包中获取 sessionid 并手动输入:")
    sid = input("  sessionid (32位): ").strip()
    cid = input("  cid (默认115): ").strip() or "115"

    if len(sid) != 32:
        log.error(f"sessionid 长度应为 32，实际 {len(sid)}")
        sys.exit(1)

    return sid, cid


def run_scraper(sessionid: str, cid: str, db_path: str,
                delay: float, max_days: int):
    """运行爬虫，自动应用 raw_json 补丁。"""
    try:
        from scraper import Client, DB, Scraper, parse_question_info
    except ImportError:
        log.error("找不到 scraper.py，请确保在正确目录下运行")
        sys.exit(1)

    from scraper_patch import patch_db, patched_upsert_question

    client = Client(sessionid=sessionid, cid=cid, delay=delay)
    db = DB(db_path)

    # 应用补丁：添加 raw_json 列
    patch_db(db)

    # 猴子补丁：替换 upsert 为增强版
    original_upsert = db.upsert_question
    db.upsert_question = lambda q, **kw: patched_upsert_question(db, q, **kw)

    scraper = Scraper(client, db)
    scraper.run(max_days=max_days)


def run_export(db_path: str, output: str, password: str = None,
               split: bool = False):
    """从 DB 导出 MQB。"""
    from ehafo_to_mqb import load_from_db, save_mqb, print_stats
    import re
    from collections import defaultdict

    questions = load_from_db(db_path)
    if not questions:
        log.error("数据库中无题目")
        return

    print_stats(questions)

    if split:
        by_cls: dict[str, list] = defaultdict(list)
        for q in questions:
            by_cls[q.cls or "未分类"].append(q)

        out_dir = Path(output)
        out_dir.mkdir(parents=True, exist_ok=True)

        for cls_name, qs in by_cls.items():
            safe = re.sub(r'[\\/:*?"<>|]', '_', cls_name)
            save_mqb(qs, out_dir / safe, password=password)
    else:
        save_mqb(questions, Path(output), password=password)


def main():
    ap = argparse.ArgumentParser(
        description="ehafo 题库完整管线：爬取 → MQB 导出",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
示例：
  python run_full.py                                # 自动登录 + 爬取 + 导出
  python run_full.py --sessionid abc...xyz --cid 115  # 手动凭据
  python run_full.py --export-only                  # 仅从已有 DB 导出
  python run_full.py --split-by-subject             # 按科目拆分
        """,
    )

    # 认证
    ap.add_argument("--sessionid", default="", help="32位 sessionid（从抓包获取）")
    ap.add_argument("--cid", default="115", help="课程ID (默认115=口腔执医)")
    ap.add_argument("--relogin", action="store_true", help="强制重新登录")

    # 爬虫
    ap.add_argument("--db", default="ehafo.db", help="数据库路径")
    ap.add_argument("--delay", type=float, default=1.2, help="请求间隔秒")
    ap.add_argument("--days", type=int, default=400, help="每日一练回溯天数")

    # 导出
    ap.add_argument("-o", "--output", default="ehafo", help="MQB 输出路径")
    ap.add_argument("--password", default=None, help="MQB 加密密码")
    ap.add_argument("--split-by-subject", action="store_true", help="按科目拆分")
    ap.add_argument("--export-only", action="store_true", help="仅导出，跳过爬取")
    ap.add_argument("--no-export", action="store_true", help="仅爬取，跳过导出")

    args = ap.parse_args()

    print("═" * 60)
    print("  ehafo 题库管线 — 爬取 + MQB 导出")
    print("═" * 60)

    # ── Step 1: 爬取 ──
    if not args.export_only:
        try:
            sessionid, cid = get_credentials(args)
        except (RuntimeError, KeyboardInterrupt) as e:
            log.error(f"认证失败: {e}")
            sys.exit(1)

        log.info(f"sessionid = {sessionid[:8]}...{sessionid[-4:]}")
        log.info(f"cid = {cid}")

        try:
            run_scraper(sessionid, cid, args.db, args.delay, args.days)
        except KeyboardInterrupt:
            print("\n⏸  爬取中断（进度已保存），继续导出已有数据...")

    # ── Step 2: 导出 MQB ──
    if not args.no_export:
        if not Path(args.db).exists():
            log.error(f"数据库不存在: {args.db}")
            sys.exit(1)

        print()
        log.info("开始导出 MQB...")
        run_export(args.db, args.output, args.password, args.split_by_subject)

    print()
    log.info("🎉 全部完成！")
    log.info(f"使用方式: med-exam quiz {args.output}.mqb")


if __name__ == "__main__":
    main()
