"""医考练习 Web 应用 - 后端服务"""
from __future__ import annotations
import json as _json
import random
import secrets
import socket
import threading
import time
import webbrowser
from collections import defaultdict, deque
from pathlib import Path
from flask import Flask, jsonify, request, render_template
from flask_compress import Compress

_questions:     list        = []
_bank_path:     Path | None = None
_password:      str  | None = None
_session_token: str         = ""   # 启动时由 start_quiz() 生成
_server_port:   int         = 5174
_server_host:   str         = "127.0.0.1"  # 绑定地址，影响 Host 校验策略

# ── 速率限制：滑动窗口，每 IP 最多 120 次/分钟 ──
_RATE_LIMIT  = 120
_RATE_WINDOW = 60   # 秒
_rate_buckets: dict[str, deque] = defaultdict(deque)
_rate_lock   = threading.Lock()


def _check_rate_limit(ip: str) -> bool:
    """返回 True 表示允许，False 表示超限。"""
    now = time.monotonic()
    with _rate_lock:
        q = _rate_buckets[ip]
        while q and now - q[0] > _RATE_WINDOW:
            q.popleft()
        if len(q) >= _RATE_LIMIT:
            return False
        q.append(now)
        return True


def _get_lan_ip() -> str:
    """获取本机局域网 IP，失败时回退到 127.0.0.1。"""
    try:
        with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as s:
            s.connect(("8.8.8.8", 80))
            return s.getsockname()[0]
    except OSError:
        return "127.0.0.1"


def _create_app() -> Flask:
    app = Flask(__name__, template_folder="templates")
    app.config["JSON_AS_ASCII"] = False
    Compress(app)

    @app.before_request
    def _guard():
        # 1. 放行首页 HTML（浏览器直接访问，尚未拿到 token）
        if request.path == "/" and request.method == "GET":
            return None

        # 2. Host 头校验 —— 阻断 DNS 重绑定攻击
        #
        #   策略根据绑定地址分两种：
        #   • 仅本机 (127.0.0.1)：严格白名单，只接受 127.0.0.1 / localhost
        #   • 局域网 (0.0.0.0)：只校验端口是否匹配，不限制主机名
        #     （此时用户已主动开放网络，Token 是主要防线）
        host_header = request.headers.get("Host", "")
        # 提取 Host 头中的端口（格式可能是 "hostname:port" 或 "hostname"）
        if ":" in host_header:
            host_name, host_port_str = host_header.rsplit(":", 1)
            try:
                host_port = int(host_port_str)
            except ValueError:
                host_port = None
        else:
            host_name  = host_header
            host_port  = None  # 浏览器省略默认端口时

        if _server_host in ("127.0.0.1", "localhost"):
            # 严格模式：只允许回环地址，阻断所有外部域名重绑定
            allowed = {
                f"127.0.0.1:{_server_port}",
                f"localhost:{_server_port}",
                "127.0.0.1",
                "localhost",
            }
            if host_header not in allowed:
                return jsonify({"error": "Forbidden"}), 403
        else:
            # 宽松模式：只验证端口，拒绝端口不匹配的请求
            # （防止请求被误路由到其他服务，Token 提供主要认证）
            if host_port is not None and host_port != _server_port:
                return jsonify({"error": "Forbidden"}), 403

        # 3. API 路由：校验 Session Token
        if request.path.startswith("/api/"):
            token = request.headers.get("X-Session-Token", "")
            if not secrets.compare_digest(token, _session_token):
                return jsonify({"error": "Unauthorized"}), 401

            # 4. 速率限制
            ip = request.remote_addr or "unknown"
            if not _check_rate_limit(ip):
                return jsonify({"error": "Too Many Requests"}), 429

        return None

    return app


app = _create_app()


# ════════════════════════════════════════════
# 工具函数
# ════════════════════════════════════════════

def _sq_flat(q, sq, qi: int, si: int) -> dict:
    own_opts    = list(sq.options or [])
    shared_opts = list(q.shared_options or [])
    # B型题：子题自身选项为空时，用大题的共享选项填充，
    # 使前端选项按钮能正常渲染和计分；同时把 shared_options 单独传给前端
    # 以便显示 B型题标识头部。
    effective_opts = own_opts if own_opts else shared_opts
    return {
        "qi": qi, "si": si,
        "id": f"{qi}-{si}",
        "mode":           q.mode or "",
        "unit":           q.unit or "",
        "cls":            q.cls  or "",
        "stem":           q.stem or "",
        "shared_options": shared_opts,
        "text":           sq.text or "",
        "options":        effective_opts,
        "answer":  (getattr(sq, "eff_answer", None) or sq.answer or "").strip(),
        "discuss": getattr(sq, "eff_discuss", None) or sq.discuss or "",
        "point":   sq.point or "",
        "rate":    sq.rate,
        "has_ai":  bool(getattr(sq, "ai_answer", None) or getattr(sq, "ai_discuss", None)),
        "fingerprint": getattr(q, "fingerprint", ""),
    }


def _parse_rate(raw) -> float | None:
    """解析正确率字段 → 0‑100 浮点数"""
    if not raw:
        return None
    s = str(raw).strip().rstrip("%")
    try:
        v = float(s)
        return v if 0 <= v <= 100 else None
    except ValueError:
        return None


def _classify_group_difficulty(grp: list[dict]) -> str:
    """按大题内各子题平均正确率分四档"""
    rates = [r for sq in grp if (r := _parse_rate(sq.get("rate"))) is not None]
    if not rates:
        return "medium"
    avg = sum(rates) / len(rates)
    if avg >= 80: return "easy"
    if avg >= 60: return "medium"
    if avg >= 40: return "hard"
    return "extreme"


def _distribute_by_ratio(total: int, weights: dict) -> dict:
    """按权重比例把 total 分配为整数，总和恰好 == total"""
    w_sum = sum(weights.values())
    if w_sum == 0:
        return {k: 0 for k in weights}
    result = {}
    allocated = 0
    items = list(weights.items())
    for i, (key, w) in enumerate(items):
        if i == len(items) - 1:
            result[key] = max(0, total - allocated)
        else:
            n = round(total * w / w_sum)
            result[key] = n
            allocated += n
    return result


def _greedy_fill(pool: list, target: int) -> list:
    """
    从 pool（每项为一组子题 list）中贪心抽取，使子题总数尽量逼近但不超过 target。
    绝不拆散大题（子题组）。两轮策略：顺序贪心 → 最优匹配（找放得下的最大题）。
    宁可略少于 target，也不截断多子题大题。
    """
    available = list(pool)
    picked: list = []
    total = 0

    # 第一轮：顺序贪心
    remaining: list = []
    for grp in available:
        c = len(grp)
        if total + c <= target:
            picked.append(grp)
            total += c
            if total == target:
                return picked
        else:
            remaining.append(grp)

    # 第二轮：最优匹配（找放得下的最大题）
    while total < target and remaining:
        gap = target - total
        best_idx, best_c = -1, 0
        for i, grp in enumerate(remaining):
            c = len(grp)
            if c <= gap and c > best_c:
                best_c = c
                best_idx = i
        if best_idx >= 0:
            picked.append(remaining.pop(best_idx))
            total += best_c
        else:
            break  # 剩余的每题都超过 gap，宁可停在这里

    return picked


def _sample_with_difficulty(pool: list, target: int, difficulty: dict, rng) -> list:
    """在 pool 内按 difficulty 比例（{level: weight}）分难度抽取"""
    by_diff: dict = defaultdict(list)
    for grp in pool:
        by_diff[_classify_group_difficulty(grp)].append(grp)

    targets = _distribute_by_ratio(target, difficulty)
    selected: list = []
    for level, need in targets.items():
        lvl_pool = list(by_diff.get(level, []))
        rng.shuffle(lvl_pool)
        selected.extend(_greedy_fill(lvl_pool, need))

    # 不足的从剩余补齐
    used_ids = {id(g) for g in selected}
    got = sum(len(g) for g in selected)
    if got < target:
        rest = [g for g in pool if id(g) not in used_ids]
        rng.shuffle(rest)
        selected.extend(_greedy_fill(rest, target - got))

    return selected


# ════════════════════════════════════════════
# API
# ════════════════════════════════════════════

@app.get("/api/info")
def api_info():
    from collections import Counter
    mc    = Counter(q.mode for q in _questions)
    uc    = Counter(q.unit for q in _questions)
    mc_sq = Counter()
    # {unit: {mode: sq_count}}
    unit_mode_sq: dict = {}
    for q in _questions:
        mc_sq[q.mode] += len(q.sub_questions)
        u = q.unit or ""
        m = q.mode or ""
        if u not in unit_mode_sq:
            unit_mode_sq[u] = {}
        unit_mode_sq[u][m] = unit_mode_sq[u].get(m, 0) + len(q.sub_questions)
    # {unit: sq_count}
    unit_sq = {u: sum(v.values()) for u, v in unit_mode_sq.items()}
    return jsonify({
        "bank_name":      _bank_path.stem if _bank_path else "",
        "total_q":        len(_questions),
        "total_sq":       sum(len(q.sub_questions) for q in _questions),
        "modes":          sorted(k for k in mc if k),
        "units":          sorted(k for k in uc if k),
        "mode_counts":    dict(mc),
        "mode_counts_sq": dict(mc_sq),
        "unit_counts":    dict(uc),
        "unit_sq":        unit_sq,        # 每章节小题数
        "unit_mode_sq":   unit_mode_sq,   # 每章节每题型小题数
    })


@app.get("/api/questions")
def api_questions():
    modes_filter  = request.args.getlist("mode")
    units_filter  = request.args.getlist("unit")
    limit         = int(request.args.get("limit", 0))
    shuffle       = request.args.get("shuffle", "0") == "1"
    seed          = request.args.get("seed", None)
    per_mode_raw  = request.args.get("per_mode",   None)
    per_unit_raw  = request.args.get("per_unit",   None)  # JSON {"unit": sq_count}
    difficulty_raw= request.args.get("difficulty", None)

    per_mode   = _json.loads(per_mode_raw)   if per_mode_raw   else None
    per_unit   = _json.loads(per_unit_raw)   if per_unit_raw   else None
    difficulty = _json.loads(difficulty_raw) if difficulty_raw else None

    rng = random.Random(int(seed)) if seed else random.Random()

    # ── 1. 基础筛选 → groups ─────────────────────────────────────────
    groups: list[list[dict]] = []
    for qi, q in enumerate(_questions):
        if modes_filter and q.mode not in modes_filter:
            continue
        if units_filter and not any(u and u in (q.unit or "") for u in units_filter):
            continue
        # per_unit 模式：只保留有配额的章节
        if per_unit and (q.unit or "") not in per_unit:
            continue
        grp = [_sq_flat(q, sq, qi, si) for si, sq in enumerate(q.sub_questions)]
        if grp:
            groups.append(grp)

    # ── 2. 按题型保序分组 ────────────────────────────────────────────
    mode_order: list[str] = []
    mode_map:   dict[str, list[list[dict]]] = {}
    for grp in groups:
        mk = grp[0]["mode"] if grp else ""
        if mk not in mode_map:
            mode_map[mk] = []
            mode_order.append(mk)
        mode_map[mk].append(grp)

    # ── 3. 抽题策略 ──────────────────────────────────────────────────

    if per_unit:
        # 按章节配额：每章节独立贪心抽取，再合并后按题型排序输出
        # 先按章节抽出各章节的 groups
        unit_order: list[str] = []
        unit_map:   dict[str, list[list[dict]]] = {}
        for grp in groups:
            uk = grp[0]["unit"] if grp else ""
            if uk not in unit_map:
                unit_map[uk] = []
                unit_order.append(uk)
            unit_map[uk].append(grp)

        # 每章节按配额贪心抽取
        picked_by_unit: dict[str, list] = {}
        for uk in unit_order:
            need = per_unit.get(uk, 0)
            if need <= 0:
                continue
            pool = list(unit_map[uk])
            rng.shuffle(pool)
            if difficulty:
                picked_by_unit[uk] = _sample_with_difficulty(pool, need, difficulty, rng)
            else:
                picked_by_unit[uk] = _greedy_fill(pool, need)

        # 合并后按题型重新排序（保持题型分组顺序）
        all_picked = [g for gs in picked_by_unit.values() for g in gs]
        reorder: dict[str, list] = {}
        for grp in all_picked:
            mk = grp[0]["mode"] if grp else ""
            reorder.setdefault(mk, []).append(grp)
        result_groups = [g for mk in mode_order if mk in reorder for g in reorder[mk]]

    elif not shuffle:
        result_groups = [g for mk in mode_order for g in mode_map[mk]]
        if limit > 0:
            cut: list = []
            n = 0
            for grp in result_groups:
                cut.append(grp)
                n += len(grp)
                if n >= limit:
                    break
            result_groups = cut

    elif per_mode:
        # 精细配额：每种题型独立指定小题数，用贪心填充
        result_groups = []
        for mk in mode_order:
            need = per_mode.get(mk, 0)
            if need <= 0:
                continue
            pool = list(mode_map[mk])
            rng.shuffle(pool)
            if difficulty:
                picked = _sample_with_difficulty(pool, need, difficulty, rng)
            else:
                picked = _greedy_fill(pool, need)
            result_groups.extend(picked)

    else:
        # 普通随机：按各题型小题数比例分配配额，保证每种都有题
        mode_sq_total = {mk: sum(len(g) for g in mode_map[mk]) for mk in mode_order}
        total_sq_all  = sum(mode_sq_total.values())
        total_need    = limit if limit > 0 else total_sq_all

        # 按比例分配，每种至少 1 题（前提是该题型有题）
        quotas = _distribute_by_ratio(total_need, mode_sq_total)
        # 强制每种题型至少 1 小题（若该题型有题目的话）
        for mk in mode_order:
            if mode_sq_total[mk] > 0:
                quotas[mk] = min(max(quotas[mk], 1), mode_sq_total[mk])
        # 强制 min 后总量可能溢出，从最大配额的题型依次缩减
        overflow = sum(quotas.values()) - total_need
        if overflow > 0:
            for mk in sorted(mode_order, key=lambda k: -quotas[k]):
                if overflow <= 0:
                    break
                reducible = max(0, quotas[mk] - 1)
                cut = min(reducible, overflow)
                quotas[mk] -= cut
                overflow   -= cut

        result_groups = []
        for mk in mode_order:
            need = quotas.get(mk, 0)
            if need <= 0:
                continue
            pool = list(mode_map[mk])
            rng.shuffle(pool)
            if difficulty:
                picked = _sample_with_difficulty(pool, need, difficulty, rng)
            else:
                picked = _greedy_fill(pool, need)
            result_groups.extend(picked)

    rows = [sq for grp in result_groups for sq in grp]
    return jsonify({"total": len(rows), "items": rows})


@app.get("/")
def index():
    return render_template("quiz.html", session_token=_session_token)


def start_quiz(bank_path: str, port: int = 5174, host: str = "127.0.0.1",
               no_browser: bool = False, password: str | None = None) -> None:
    """启动医考练习 Web 应用"""
    from med_exam_toolkit.bank import load_bank
    global _questions, _bank_path, _password, _session_token, _server_port, _server_host
    _bank_path     = Path(bank_path).resolve()
    _password      = password
    _server_port   = port
    _server_host   = host
    _session_token = secrets.token_hex(32)   # 每次启动生成新 token

    print(f"[INFO] 加载题库: {_bank_path}")
    _questions = load_bank(_bank_path, password)
    sq_total = sum(len(q.sub_questions) for q in _questions)
    print(f"[INFO] 共 {len(_questions)} 大题 / {sq_total} 小题")

    local_url = f"http://127.0.0.1:{port}"
    print(f"[INFO] 本机访问: {local_url}")
    if host == "0.0.0.0":
        lan_ip  = _get_lan_ip()
        lan_url = f"http://{lan_ip}:{port}"
        print(f"[INFO] 局域网访问: {lan_url}  （同网段其他设备可用此地址）")
    print("[INFO] Ctrl+C 退出")

    open_url = local_url
    if not no_browser:
        threading.Timer(0.9, lambda: webbrowser.open(open_url)).start()

    # threaded=True：每个请求在独立线程中处理，支持多用户并发访问
    app.run(host=host, port=port, debug=False, use_reloader=False, threaded=True)


if __name__ == "__main__":
    import argparse
    p = argparse.ArgumentParser(description="医考练习 Web 应用")
    p.add_argument("--bank",       required=True)
    p.add_argument("--password",   default=None)
    p.add_argument("--port",       default=5174, type=int)
    p.add_argument("--no-browser", action="store_true")
    a = p.parse_args()
    start_quiz(a.bank, port=a.port, no_browser=a.no_browser, password=a.password)