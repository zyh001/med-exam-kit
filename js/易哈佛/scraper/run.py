#!/usr/bin/env python3
"""
ehafo 题库爬虫 — 一键启动
==========================
自动完成：微信登录 → cid 选择 → 题目爬取 → 本地 SQLite 存储

依赖安装：
    pip install requests playwright
    playwright install chromium

用法：
    python run_scraper.py             # 首次：打开浏览器登录，自动爬取
    python run_scraper.py             # 再次：复用缓存 session，继续断点爬取
    python run_scraper.py --relogin   # 强制重新登录（session 过期时用）
    python run_scraper.py --check     # 仅查看当前 session 状态
    python run_scraper.py --clear     # 清除登录缓存
"""

import argparse, sys

def main():
    ap = argparse.ArgumentParser(description="ehafo 题库一键爬虫")
    ap.add_argument("--relogin", action="store_true", help="强制重新登录")
    ap.add_argument("--check",   action="store_true", help="检查 session 状态后退出")
    ap.add_argument("--clear",   action="store_true", help="清除登录缓存")
    ap.add_argument("--db",      default="ehafo.db",  help="数据库路径（默认 ehafo.db）")
    ap.add_argument("--delay",   type=float, default=1.2, help="请求间隔秒（默认 1.2）")
    ap.add_argument("--days",    type=int,   default=400, help="每日一练回溯天数（默认 400）")
    args = ap.parse_args()

    try:
        from ehafo_auth import get_credentials, clear_cache, _load_cache, validate_session
    except ImportError:
        print("❌  找不到 ehafo_auth.py，请确保三个文件在同一目录下")
        sys.exit(1)

    if args.clear:
        clear_cache(); print("✓ 缓存已清除"); return

    if args.check:
        cache = _load_cache()
        sid = cache.get("sessionid", "")
        if not sid:
            print("❌  无缓存，请先运行: python run_scraper.py")
        elif validate_session(sid):
            print("✅  session 有效")
            print(f"   sessionid = {sid[:8]}...{sid[-4:]}")
            print(f"   cid       = {cache.get('cid')}")
            print(f"   保存时间  = {cache.get('saved_at', '未知')}")
            for e in cache.get("exams", []):
                vip = "👑 VIP" if e.get("is_vip") else "     "
                print(f"   {vip} {e['name']}  cid={e['cid']}")
        else:
            print("❌  session 已过期，请运行: python run_scraper.py --relogin")
        return

    print("=" * 55)
    print("  ehafo 题库爬虫  —  RC4 解密版 v2")
    print("=" * 55)

    try:
        sessionid, cid = get_credentials(force_relogin=args.relogin)
    except RuntimeError as e:
        print(f"\n❌  认证失败: {e}")
        print("\n💡  若浏览器打开失败，手动方式：")
        print("   python ehafo_scraper_v2.py --sessionid <你的sessionid> --cid 115")
        sys.exit(1)

    try:
        from ehafo_scraper_v2 import Client, DB, Scraper
    except ImportError:
        print("❌  找不到 ehafo_scraper_v2.py")
        sys.exit(1)

    client  = Client(sessionid=sessionid, cid=cid, delay=args.delay)
    db      = DB(args.db)
    scraper = Scraper(client, db)

    try:
        scraper.run(max_days=args.days)
    except KeyboardInterrupt:
        print("\n⏸  已暂停（进度已保存）")
        print("   下次直接运行 python run_scraper.py 自动继续")

if __name__ == "__main__":
    main()
