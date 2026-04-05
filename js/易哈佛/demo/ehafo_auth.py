"""
ehafo 认证管理器
================
功能：
  1. sessionid 自动获取 —— 打开浏览器让用户扫码登录，自动读取 localStorage
  2. sessionid 持久缓存 —— 保存到 ~/.ehafo_session.json，下次直接复用
  3. sessionid 有效性校验 —— 每次启动自动验证，过期自动重新登录
  4. cid 自动选择 —— 调接口拉取用户已报名的考试列表，单考试直接用，多考试交互选择

用法：
  python ehafo_auth.py                   # 独立运行，输出 sessionid 和 cid
  from ehafo_auth import get_credentials # 作为模块导入
  session, cid = get_credentials()
"""

import json
import os
import sys
import time
from pathlib import Path
from typing import Optional

import requests

# ── 缓存文件路径 ───────────────────────────────────────────────
CACHE_FILE = Path.home() / ".ehafo_session.json"

# ── HTTP 基础配置 ──────────────────────────────────────────────
API_BASE = "https://sdk.ehafo.com/phalapi/public/"
HEADERS  = {
    "User-Agent": "Mozilla/5.0 (Linux; Android 11; Pixel 5) "
                  "AppleWebKit/537.36 (KHTML, like Gecko) "
                  "Chrome/120.0.0.0 Mobile Safari/537.36",
    "Referer":    "https://quiz.ehafo.com/",
}
LOGIN_URL = "https://quiz.ehafo.com/v5/v4/public/wxlogin.1aad573e.html"


# ════════════════════════════════════════════════════════
#   API 工具函数
# ════════════════════════════════════════════════════════

def _api_call(service: str, data: dict, timeout: int = 10) -> dict:
    try:
        r = requests.post(
            API_BASE,
            params={"service": service},
            data={
                "__current_page_url": "#/v4/index",
                "__local_time": str(int(time.time() * 1000)),
                **data,
            },
            headers=HEADERS,
            timeout=timeout,
        )
        return r.json()
    except Exception as e:
        return {"ret": -1, "errormsg": str(e)}


def validate_session(sessionid: str) -> bool:
    """验证 sessionid 是否仍然有效"""
    res = _api_call("App.Common.getServerTime", {"sessionid": sessionid})
    return res.get("ret") == 0 or res.get("code") == 0


def get_user_exams(sessionid: str) -> list:
    """
    拉取用户已报名的考试列表
    返回 [{"cid": "115", "name": "口腔执业医师", "is_vip": 1}, ...]
    """
    res = _api_call(
        "App.Index.IndexInfo",
        {"sessionid": sessionid, "cid": "", "has_join_wap": "0"},
    )
    d = res.get("data", {})
    if isinstance(d, dict):
        return d.get("category_relations", [])
    return []


# ════════════════════════════════════════════════════════
#   sessionid 缓存管理
# ════════════════════════════════════════════════════════

def _load_cache() -> dict:
    if CACHE_FILE.exists():
        try:
            return json.loads(CACHE_FILE.read_text(encoding="utf-8"))
        except Exception:
            pass
    return {}


def _save_cache(data: dict):
    try:
        CACHE_FILE.write_text(
            json.dumps(data, ensure_ascii=False, indent=2),
            encoding="utf-8"
        )
        CACHE_FILE.chmod(0o600)  # 仅当前用户可读
    except Exception as e:
        print(f"⚠️  缓存保存失败: {e}")


# ════════════════════════════════════════════════════════
#   浏览器自动登录（Playwright）
# ════════════════════════════════════════════════════════

def _login_via_browser() -> Optional[str]:
    """
    打开 quiz.ehafo.com 微信登录页，等待用户完成微信扫码，
    自动从 localStorage 读取 sessionid。

    返回 sessionid 字符串，失败返回 None。
    """
    try:
        from playwright.sync_api import sync_playwright, TimeoutError as PWTimeout
    except ImportError:
        print("❌ 未安装 playwright，请运行: pip install playwright && playwright install chromium")
        return None

    print("\n" + "═" * 55)
    print("🌐  正在打开浏览器登录页...")
    print("   请在弹出的浏览器窗口中点击「微信一键登录」")
    print("   用手机微信扫码授权后，窗口会自动关闭")
    print("═" * 55)

    sessionid = None

    with sync_playwright() as p:
        # headless=False：给用户显示浏览器窗口
        browser = p.chromium.launch(
            headless=False,
            args=[
                "--no-sandbox",
                "--disable-blink-features=AutomationControlled",
                "--window-size=480,800",
            ],
        )

        ctx = browser.new_context(
            viewport={"width": 480, "height": 800},
            user_agent=(
                "Mozilla/5.0 (Linux; Android 11; Pixel 5) "
                "AppleWebKit/537.36 (KHTML, like Gecko) "
                "Chrome/120.0.0.0 Mobile Safari/537.36"
            ),
            locale="zh-CN",
        )

        page = ctx.new_page()

        # 屏蔽自动化检测
        page.add_init_script("""
            Object.defineProperty(navigator, 'webdriver', {get: () => undefined});
            window.chrome = {runtime: {}};
        """)

        try:
            page.goto(LOGIN_URL, wait_until="domcontentloaded", timeout=20000)
        except Exception as e:
            print(f"⚠️  页面加载超时，尝试继续: {e}")

        print("⏳  等待登录完成（最多 3 分钟）...")

        deadline = time.time() + 180  # 3 分钟超时
        CHECK_INTERVAL = 1.5          # 每 1.5 秒检查一次

        while time.time() < deadline:
            time.sleep(CHECK_INTERVAL)
            try:
                sid = page.evaluate("localStorage.getItem('sessionid')")
                if sid and len(sid) == 32:
                    sessionid = sid
                    print(f"\n✅  登录成功！sessionid 已获取")
                    break
            except Exception:
                pass  # 页面可能正在跳转

        browser.close()

    if not sessionid:
        print("❌  登录超时或取消")

    return sessionid


# ════════════════════════════════════════════════════════
#   CID 交互选择
# ════════════════════════════════════════════════════════

def _select_cid(exams: list) -> Optional[str]:
    """
    exams: [{"cid": "115", "name": "口腔执业医师", "is_vip": 1}, ...]

    - 只有一个考试 → 直接返回
    - 多个考试 → 打印菜单，用户输入序号
    """
    if not exams:
        print("⚠️  未找到已报名的考试，请先在 App 内选择考试科目")
        return None

    if len(exams) == 1:
        exam = exams[0]
        vip  = "👑 VIP" if exam.get("is_vip") else "    "
        print(f"📚  自动选择考试: {exam['name']} (cid={exam['cid']}) {vip}")
        return exam["cid"]

    print("\n📚  检测到多个已报名考试，请选择：")
    print("─" * 40)
    for i, exam in enumerate(exams, 1):
        vip = "👑" if exam.get("is_vip") else "  "
        print(f"  {i}. {vip} {exam['name']:20s}  cid={exam['cid']}")
    print("─" * 40)

    while True:
        try:
            choice = input(f"请输入序号 [1-{len(exams)}]: ").strip()
            idx = int(choice) - 1
            if 0 <= idx < len(exams):
                selected = exams[idx]
                print(f"✓  已选择: {selected['name']} (cid={selected['cid']})")
                return selected["cid"]
        except (ValueError, KeyboardInterrupt):
            pass
        print(f"  ❗ 请输入 1 到 {len(exams)} 之间的数字")


# ════════════════════════════════════════════════════════
#   主入口
# ════════════════════════════════════════════════════════

def get_credentials(force_relogin: bool = False) -> tuple[str, str]:
    """
    获取有效的 (sessionid, cid)。

    逻辑：
      1. 读缓存 → 验证 sessionid 有效性
      2. 无效或 force_relogin → 打开浏览器登录
      3. 登录成功 → 拉考试列表 → 选 cid
      4. 保存缓存 → 返回 (sessionid, cid)

    Args:
        force_relogin: True = 强制重新登录（忽略缓存）

    Returns:
        (sessionid, cid) 元组，失败时抛出 RuntimeError
    """
    cache = _load_cache()
    sessionid: Optional[str] = cache.get("sessionid")
    cid:       Optional[str] = cache.get("cid")

    # ── Step 1: 验证缓存 ──────────────────────────────
    if not force_relogin and sessionid:
        print(f"🔑  检查缓存 sessionid... ", end="", flush=True)
        if validate_session(sessionid):
            print("有效 ✓")
            # cid 也已缓存则直接返回
            if cid:
                print(f"📚  使用缓存 cid={cid}")
                return sessionid, cid
            # cid 未缓存，重新拉考试列表
        else:
            print("已过期，需要重新登录")
            sessionid = None

    # ── Step 2: 浏览器登录 ────────────────────────────
    if not sessionid:
        sessionid = _login_via_browser()
        if not sessionid:
            raise RuntimeError("登录失败，无法获取 sessionid")

        # 二次验证
        if not validate_session(sessionid):
            raise RuntimeError("获取到的 sessionid 验证失败，请重试")

    # ── Step 3: 获取考试列表，选择 cid ────────────────
    print("📡  获取考试列表...", end=" ", flush=True)
    exams = get_user_exams(sessionid)
    print(f"共 {len(exams)} 个")

    cid = _select_cid(exams)
    if not cid:
        raise RuntimeError("未选择考试 cid")

    # ── Step 4: 保存缓存 ──────────────────────────────
    _save_cache({
        "sessionid":  sessionid,
        "cid":        cid,
        "saved_at":   time.strftime("%Y-%m-%d %H:%M:%S"),
        "exams":      exams,
    })
    print(f"💾  凭据已保存到 {CACHE_FILE}")

    return sessionid, cid


def clear_cache():
    """清除缓存，下次运行时重新登录"""
    if CACHE_FILE.exists():
        CACHE_FILE.unlink()
        print(f"✓ 已清除缓存: {CACHE_FILE}")


# ════════════════════════════════════════════════════════
#   命令行独立运行
# ════════════════════════════════════════════════════════

if __name__ == "__main__":
    import argparse

    ap = argparse.ArgumentParser(description="ehafo 登录认证工具")
    ap.add_argument("--relogin", action="store_true", help="强制重新登录（忽略缓存）")
    ap.add_argument("--clear",   action="store_true", help="清除缓存")
    ap.add_argument("--check",   action="store_true", help="仅验证当前缓存的 sessionid")
    args = ap.parse_args()

    if args.clear:
        clear_cache()
        sys.exit(0)

    if args.check:
        cache = _load_cache()
        sid = cache.get("sessionid", "")
        if not sid:
            print("❌  无缓存 sessionid")
        elif validate_session(sid):
            print(f"✅  sessionid 有效\n   cid={cache.get('cid')}\n   保存于 {cache.get('saved_at')}")
        else:
            print("❌  sessionid 已过期")
        sys.exit(0)

    try:
        sessionid, cid = get_credentials(force_relogin=args.relogin)
        print("\n" + "═" * 40)
        print(f"✅  认证完成")
        print(f"   sessionid = {sessionid}")
        print(f"   cid       = {cid}")
        print("═" * 40)
    except RuntimeError as e:
        print(f"\n❌  {e}")
        sys.exit(1)
