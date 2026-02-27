"""医考练习 Web 应用 - 后端服务"""
from __future__ import annotations
import random, threading, webbrowser
from pathlib import Path
from flask import Flask, jsonify, request, send_from_directory, render_template

_questions: list = []
_bank_path: Path | None = None
_password: str | None = None


def _create_app() -> Flask:
    """创建 Flask 应用，使用包内的模板"""
    app = Flask(__name__, template_folder="templates")
    app.config["JSON_AS_ASCII"] = False
    return app


app = _create_app()


def _sq_flat(q, sq, qi: int, si: int) -> dict:
    return {
        "qi": qi, "si": si,
        "id": f"{qi}-{si}",
        "mode": q.mode or "",
        "unit": q.unit or "",
        "cls": q.cls or "",
        "stem": q.stem or "",
        "text": sq.text or "",
        "options": list(sq.options or []),
        "answer": (getattr(sq, "eff_answer", None) or sq.answer or "").strip(),
        "discuss": getattr(sq, "eff_discuss", None) or sq.discuss or "",
        "point": sq.point or "",
        "rate": sq.rate,
        "has_ai": bool(getattr(sq, "ai_answer", None) or getattr(sq, "ai_discuss", None)),
        "fingerprint": getattr(q, "fingerprint", ""),
    }


@app.get("/api/info")
def api_info():
    from collections import Counter
    mc = Counter(q.mode for q in _questions)
    uc = Counter(q.unit for q in _questions)
    return jsonify({
        "bank_name": _bank_path.stem if _bank_path else "",
        "total_q": len(_questions),
        "total_sq": sum(len(q.sub_questions) for q in _questions),
        "modes": sorted(k for k in mc if k),
        "units": sorted(k for k in uc if k),
        "mode_counts": dict(mc),
        "unit_counts": dict(uc),
    })


@app.get("/api/questions")
def api_questions():
    modes = request.args.getlist("mode")
    units = request.args.getlist("unit")
    limit = int(request.args.get("limit", 0))
    shuffle = request.args.get("shuffle", "0") == "1"
    seed = request.args.get("seed", None)

    # 先按大题分组，过滤后保持子题顺序
    groups: list[list[dict]] = []
    for qi, q in enumerate(_questions):
        if modes and q.mode not in modes:
            continue
        if units and not any(u and u in (q.unit or "") for u in units):
            continue
        grp = [_sq_flat(q, sq, qi, si) for si, sq in enumerate(q.sub_questions)]
        if grp:
            groups.append(grp)

    # 打乱以大题为单位，子题顺序保持不变
    if shuffle:
        rng = random.Random(int(seed)) if seed else random.Random()
        rng.shuffle(groups)

    # 展平
    rows = [sq for grp in groups for sq in grp]

    # 限量：按小题数截断，但不拆散一道大题（取到当前大题结束）
    if limit > 0:
        cut = []
        for grp in groups:
            cut.extend(grp)
            if len(cut) >= limit:
                break
        rows = cut

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
    _password = password
    print(f"[INFO] 加载题库: {_bank_path}")
    _questions = load_bank(_bank_path, password)
    sq_total = sum(len(q.sub_questions) for q in _questions)
    print(f"[INFO] 共 {len(_questions)} 大题 / {sq_total} 小题")
    url = f"http://{host}:{port}"
    print(f"[INFO] 做题应用已启动: {url}")
    print("[INFO] Ctrl+C 退出")
    if not no_browser:
        threading.Timer(0.9, lambda: webbrowser.open(url)).start()
    app.run(host=host, port=port, debug=False, use_reloader=False)


if __name__ == "__main__":
    import argparse
    p = argparse.ArgumentParser(description="医考练习 Web 应用")
    p.add_argument("--bank", required=True)
    p.add_argument("--password", default=None)
    p.add_argument("--port", default=5174, type=int)
    p.add_argument("--no-browser", action="store_true")
    a = p.parse_args()
    start_quiz(a.bank, port=a.port, no_browser=a.no_browser, password=a.password)