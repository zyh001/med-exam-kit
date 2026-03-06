"""医考练习 Web 应用 - 后端服务"""
from __future__ import annotations
import json as _json
import random
import threading
import webbrowser
from collections import defaultdict
from pathlib import Path
from flask import Flask, jsonify, request, render_template

_questions: list = []
_bank_path: Path | None = None
_password:  str  | None = None


def _create_app() -> Flask:
    app = Flask(__name__, template_folder="templates")
    app.config["JSON_AS_ASCII"] = False
    return app


app = _create_app()


# ════════════════════════════════════════════
# 工具函数
# ════════════════════════════════════════════

def _sq_flat(q, sq, qi: int, si: int) -> dict:
    return {
        "qi": qi, "si": si,
        "id": f"{qi}-{si}",
        "mode":    q.mode or "",
        "unit":    q.unit or "",
        "cls":     q.cls  or "",
        "stem":    q.stem or "",
        "text":    sq.text or "",
        "options": list(sq.options or []),
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
    for q in _questions:
        mc_sq[q.mode] += len(q.sub_questions)
    return jsonify({
        "bank_name":      _bank_path.stem if _bank_path else "",
        "total_q":        len(_questions),
        "total_sq":       sum(len(q.sub_questions) for q in _questions),
        "modes":          sorted(k for k in mc if k),
        "units":          sorted(k for k in uc if k),
        "mode_counts":    dict(mc),       # 大题数/题型
        "mode_counts_sq": dict(mc_sq),    # 小题数/题型（用于配额 UI）
        "unit_counts":    dict(uc),
    })


@app.get("/api/questions")
def api_questions():
    modes_filter  = request.args.getlist("mode")
    units_filter  = request.args.getlist("unit")
    limit         = int(request.args.get("limit", 0))
    shuffle       = request.args.get("shuffle", "0") == "1"
    seed          = request.args.get("seed", None)
    per_mode_raw  = request.args.get("per_mode",   None)  # JSON {"mode": sq_count}
    difficulty_raw= request.args.get("difficulty", None)  # JSON {"easy":25,"medium":45,...}

    per_mode   = _json.loads(per_mode_raw)   if per_mode_raw   else None
    difficulty = _json.loads(difficulty_raw) if difficulty_raw else None

    rng = random.Random(int(seed)) if seed else random.Random()

    # ── 1. 基础筛选 → groups ─────────────────────────────────────────
    groups: list[list[dict]] = []
    for qi, q in enumerate(_questions):
        if modes_filter and q.mode not in modes_filter:
            continue
        if units_filter and not any(u and u in (q.unit or "") for u in units_filter):
            continue
        grp = [_sq_flat(q, sq, qi, si) for si, sq in enumerate(q.sub_questions)]
        if grp:
            groups.append(grp)

    # ── 2. 按题型保序分组 ────────────────────────────────────────────
    # mode_order 保持题型在题库中的原始出现顺序（A1→A3→案例…）
    mode_order: list[str] = []
    mode_map:   dict[str, list[list[dict]]] = {}
    for grp in groups:
        mk = grp[0]["mode"] if grp else ""
        if mk not in mode_map:
            mode_map[mk] = []
            mode_order.append(mk)
        mode_map[mk].append(grp)

    # ── 3. 抽题策略 ──────────────────────────────────────────────────

    if not shuffle:
        # 不随机：顺序截断（不拆散大题）
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
                reducible = max(0, quotas[mk] - 1)  # 至少保留 1
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
    return render_template("quiz.html")


def start_quiz(bank_path: str, port: int = 5174, host: str = "127.0.0.1",
               no_browser: bool = False, password: str | None = None) -> None:
    """启动医考练习 Web 应用"""
    from med_exam_toolkit.bank import load_bank
    global _questions, _bank_path, _password
    _bank_path = Path(bank_path).resolve()
    _password  = password
    print(f"[INFO] 加载题库: {_bank_path}")
    _questions = load_bank(_bank_path, password)
    sq_total = sum(len(q.sub_questions) for q in _questions)
    print(f"[INFO] 共 {len(_questions)} 大题 / {sq_total} 小题")
    url = f"http://127.0.0.1:{port}"
    print(f"[INFO] 做题应用已启动: {url}")
    print("[INFO] Ctrl+C 退出")
    if not no_browser:
        threading.Timer(0.9, lambda: webbrowser.open(url)).start()
    app.run(host=host, port=port, debug=False, use_reloader=False)


if __name__ == "__main__":
    import argparse
    p = argparse.ArgumentParser(description="医考练习 Web 应用")
    p.add_argument("--bank",       required=True)
    p.add_argument("--password",   default=None)
    p.add_argument("--port",       default=5174, type=int)
    p.add_argument("--no-browser", action="store_true")
    a = p.parse_args()
    start_quiz(a.bank, port=a.port, no_browser=a.no_browser, password=a.password)