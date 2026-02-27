"""本地题库编辑器 Web 服务"""
from __future__ import annotations

import copy
import json
import threading
import webbrowser
from pathlib import Path
from typing import Any

from flask import Flask, jsonify, request, render_template

# ── 全局状态 ──
_questions: list = []
_bank_path: Path | None = None
_dirty    = False
_password: str | None = None

app = Flask(__name__, template_folder="templates")
app.config["JSON_AS_ASCII"] = False


# ═══════════════════════════════════════════════════════════════════
# REST API
# ═══════════════════════════════════════════════════════════════════

def _sq_to_dict(q, sq, qi: int, si: int) -> dict:
    return {
        "qi": qi, "si": si,
        "mode": q.mode, "unit": q.unit, "cls": q.cls,
        "stem": q.stem,
        "shared_options": q.shared_options,
        "text": sq.text,
        "options": sq.options,
        "answer": sq.answer,
        "discuss": sq.discuss,
        "point": sq.point,
        "rate": sq.rate,
        "ai_answer": sq.ai_answer,
        "ai_discuss": sq.ai_discuss,
        "ai_confidence": sq.ai_confidence,
        "ai_model": sq.ai_model,
        "eff_answer": sq.eff_answer,
        "eff_discuss": sq.eff_discuss,
        "answer_source": sq.answer_source,
        "discuss_source": sq.discuss_source,
        "sub_total":   len(q.sub_questions),
        "fingerprint": getattr(q, "fingerprint", ""),
    }


@app.get("/api/info")
def api_info():
    from collections import Counter
    mode_cnt = Counter(q.mode for q in _questions)
    unit_cnt = Counter(q.unit for q in _questions)
    return jsonify({
        "bank_path": str(_bank_path),
        "total_q":   len(_questions),
        "total_sq":  sum(len(q.sub_questions) for q in _questions),
        "dirty":     _dirty,
        "modes":     sorted(mode_cnt.keys()),
        "units":     sorted(unit_cnt.keys()),
        "mode_counts":  dict(mode_cnt),
        "unit_counts":  dict(unit_cnt),
    })


@app.get("/api/questions")
def api_questions():
    q_kw  = request.args.get("q",  "").strip()
    fp_kw = request.args.get("fp", "").strip()
    mode  = request.args.get("mode", "")
    unit   = request.args.get("unit", "")
    has_ai = request.args.get("has_ai", "") == "1"
    missing = request.args.get("missing", "") == "1"
    page   = max(1, int(request.args.get("page", 1)))
    per    = min(100, max(1, int(request.args.get("per_page", 50))))

    rows = []
    for qi, q in enumerate(_questions):
        q_fp = getattr(q, "fingerprint", "") or ""
        if fp_kw and fp_kw.lower() not in q_fp.lower():
            continue
        if mode and q.mode != mode:
            continue
        if unit and unit not in (q.unit or ""):
            continue
        for si, sq in enumerate(q.sub_questions):
            if q_kw and q_kw not in (sq.text or "") and q_kw not in (q.stem or "") \
               and q_kw not in (sq.discuss or "") and q_kw not in (sq.answer or ""):
                continue
            if has_ai and not (sq.ai_answer or sq.ai_discuss):
                continue
            if missing and (sq.answer or "").strip() and (sq.discuss or "").strip():
                continue
            rows.append(_sq_to_dict(q, sq, qi, si))

    total = len(rows)
    start = (page - 1) * per
    return jsonify({
        "total": total,
        "page": page,
        "per_page": per,
        "pages": (total + per - 1) // per,
        "items": rows[start : start + per],
    })


@app.get("/api/question/<int:qi>")
def api_get_question(qi: int):
    if qi < 0 or qi >= len(_questions):
        return jsonify({"error": "not found"}), 404
    q = _questions[qi]
    return jsonify({
        "qi": qi,
        "mode": q.mode, "unit": q.unit, "cls": q.cls,
        "stem": q.stem, "shared_options": q.shared_options,
        "sub_questions": [
            _sq_to_dict(q, sq, qi, si)
            for si, sq in enumerate(q.sub_questions)
        ],
    })


@app.put("/api/subquestion/<int:qi>/<int:si>")
def api_update_sq(qi: int, si: int):
    global _dirty
    if qi < 0 or qi >= len(_questions):
        return jsonify({"error": "not found"}), 404
    q = _questions[qi]
    if si < 0 or si >= len(q.sub_questions):
        return jsonify({"error": "not found"}), 404
    sq = q.sub_questions[si]
    data = request.get_json()

    for field in ("text", "answer", "discuss", "point", "rate"):
        if field in data:
            setattr(sq, field, data[field])
    if "options" in data and isinstance(data["options"], list):
        sq.options = data["options"]
    # 同步更新题目级元信息
    for field in ("mode", "unit", "cls", "stem"):
        if field in data:
            setattr(q, field, data[field])

    _dirty = True
    return jsonify({"ok": True, "row": _sq_to_dict(q, sq, qi, si)})


@app.delete("/api/question/<int:qi>")
def api_delete_question(qi: int):
    global _dirty
    if qi < 0 or qi >= len(_questions):
        return jsonify({"error": "not found"}), 404
    _questions.pop(qi)
    _dirty = True
    return jsonify({"ok": True, "total": len(_questions)})


@app.post("/api/replace")
def api_replace():
    """批量文本替换"""
    global _dirty
    data    = request.get_json()
    find    = data.get("find", "")
    replace = data.get("replace", "")
    fields  = set(data.get("fields", ["discuss", "text"]))
    mode    = data.get("mode", "")
    unit    = data.get("unit", "")

    if not find:
        return jsonify({"error": "find 不能为空"}), 400

    count = 0
    for q in _questions:
        q_fp = getattr(q, "fingerprint", "") or ""
        if fp_kw and fp_kw.lower() not in q_fp.lower():
            continue
        if mode and q.mode != mode:
            continue
        if unit and unit not in (q.unit or ""):
            continue
        for sq in q.sub_questions:
            for field in fields:
                val = getattr(sq, field, "") or ""
                if find in val:
                    setattr(sq, field, val.replace(find, replace))
                    count += 1

    if count:
        _dirty = True
    return jsonify({"ok": True, "replaced": count})


@app.post("/api/save")
def api_save():
    global _dirty
    try:
        from med_exam_toolkit.bank import save_bank
        save_bank(_questions, _bank_path, _password)
        _dirty = False
        return jsonify({"ok": True, "path": str(_bank_path)})
    except Exception as e:
        return jsonify({"error": str(e)}), 500


@app.post("/api/shutdown")
def api_shutdown():
    import os, signal
    os.kill(os.getpid(), signal.SIGTERM)
    return jsonify({"ok": True})


# ═══════════════════════════════════════════════════════════════════
# 前端页面
# ═══════════════════════════════════════════════════════════════════

@app.get("/")
def index():
    return render_template("editor.html")


# ═══════════════════════════════════════════════════════════════════
# 启动入口
# ═══════════════════════════════════════════════════════════════════

def start_editor(bank_path: str, port: int = 5173, host: str = "127.0.0.1",
                 no_browser: bool = False, password: str | None = None) -> None:
    from med_exam_toolkit.bank import load_bank

    global _questions, _bank_path, _password
    _bank_path = Path(bank_path).resolve()
    _password  = password
    print(f"[INFO] 加载题库: {_bank_path}")
    _questions = load_bank(_bank_path, password)
    print(f"[INFO] 已加载 {len(_questions)} 道大题")

    url = f"http://{host}:{port}"
    print(f"[INFO] 编辑器启动: {url}")
    print(f"[INFO] 按 Ctrl+C 退出")

    if not no_browser:
        threading.Timer(0.8, lambda: webbrowser.open(url)).start()

    app.run(host=host, port=port, debug=False, use_reloader=False)