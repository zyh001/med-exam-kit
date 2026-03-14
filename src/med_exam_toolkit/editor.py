"""本地题库编辑器 Web 服务"""
from __future__ import annotations

import copy
import json
import secrets
import socket
import threading
import time
import webbrowser
from collections import defaultdict, deque
from pathlib import Path
from typing import Any

from flask import Flask, jsonify, request, render_template
from flask_compress import Compress

# ── 全局状态 ──
_questions:     list        = []
_bank_path:     Path | None = None
_dirty                      = False
_password:      str  | None = None
_session_token: str         = ""
_asset_ver:     str         = ""     # 静态文件版本号，用于缓存破坏
_server_port:   int         = 5173
_server_host:   str         = "127.0.0.1"

# ── 写操作锁：多线程并发时保护 _questions 的结构完整性 ──
# 使用 RLock（可重入锁），允许同一线程在持有锁时再次加锁（防死锁）。
# 所有会修改 _questions 的 endpoint 都必须持有此锁。
_write_lock = threading.RLock()

# ── 速率限制：编辑器含写操作，限制更严：60 次/分钟 ──
_RATE_LIMIT  = 60
_RATE_WINDOW = 60
_rate_buckets: dict[str, deque] = defaultdict(deque)
_rate_lock   = threading.Lock()


def _check_rate_limit(ip: str) -> bool:
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


app = Flask(__name__, template_folder="templates", static_folder="static")
app.config["JSON_AS_ASCII"] = False
# 防止超大请求体导致 OOM：编辑器只处理题库文件，限制为 64 MB
# 超过此限制 Flask 自动返回 413 Request Entity Too Large
app.config["MAX_CONTENT_LENGTH"] = 64 * 1024 * 1024  # 64 MB
Compress(app)


@app.errorhandler(413)
def _too_large(e):
    return jsonify({"error": "请求体过大，单次请求不得超过 64 MB"}), 413



def _guard():
    # 1. 放行首页 HTML
    if request.path == "/" and request.method == "GET":
        return None

    # 2. Host 头校验
    #   • 仅本机 (127.0.0.1)：严格白名单
    #   • 局域网 (0.0.0.0)：只校验端口匹配
    host_header = request.headers.get("Host", "")
    if ":" in host_header:
        _, host_port_str = host_header.rsplit(":", 1)
        try:
            host_port = int(host_port_str)
        except ValueError:
            host_port = None
    else:
        host_port = None

    if _server_host in ("127.0.0.1", "localhost"):
        allowed = {
            f"127.0.0.1:{_server_port}",
            f"localhost:{_server_port}",
            "127.0.0.1",
            "localhost",
        }
        if host_header not in allowed:
            return jsonify({"error": "Forbidden"}), 403
    else:
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


# ═══════════════════════════════════════════════════════════════════
# 工具
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
        "has_ai":      bool(sq.ai_answer or sq.ai_discuss),
    }


def _parse_rate(raw) -> float | None:
    if not raw:
        return None
    s = str(raw).strip().rstrip("%")
    try:
        v = float(s)
        return v if 0 <= v <= 100 else None
    except ValueError:
        return None


# ═══════════════════════════════════════════════════════════════════
# REST API
# ═══════════════════════════════════════════════════════════════════

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


@app.get("/api/stats")
def api_stats():
    """题库质量统计"""
    from collections import Counter
    mode_sq: Counter = Counter()
    unit_sq: Counter = Counter()
    missing_answer = missing_discuss = missing_both = has_ai = 0
    diff = {"easy": 0, "medium": 0, "hard": 0, "extreme": 0, "unknown": 0}

    for q in _questions:
        for sq in q.sub_questions:
            mode_sq[q.mode or "（未分类）"] += 1
            unit_sq[q.unit or "（未分类）"] += 1
            no_ans = not (sq.answer or "").strip()
            no_dis = not (sq.discuss or "").strip()
            if no_ans: missing_answer += 1
            if no_dis: missing_discuss += 1
            if no_ans and no_dis: missing_both += 1
            if sq.ai_answer or sq.ai_discuss: has_ai += 1
            r = _parse_rate(sq.rate)
            if r is None:        diff["unknown"] += 1
            elif r >= 80:        diff["easy"] += 1
            elif r >= 60:        diff["medium"] += 1
            elif r >= 40:        diff["hard"] += 1
            else:                diff["extreme"] += 1

    total_sq = sum(mode_sq.values())
    return jsonify({
        "total_q":        len(_questions),
        "total_sq":       total_sq,
        "missing_answer": missing_answer,
        "missing_discuss":missing_discuss,
        "missing_both":   missing_both,
        "has_ai":         has_ai,
        "mode_sq":        dict(mode_sq.most_common()),
        "unit_sq":        dict(unit_sq.most_common(20)),
        "difficulty":     diff,
    })


@app.get("/api/questions")
def api_questions():
    q_kw   = request.args.get("q",  "").strip()
    fp_kw  = request.args.get("fp", "").strip()
    mode   = request.args.get("mode", "")
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
        "total": total, "page": page,
        "per_page": per, "pages": (total + per - 1) // per,
        "items": rows[start: start + per],
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
        "sub_questions": [_sq_to_dict(q, sq, qi, si) for si, sq in enumerate(q.sub_questions)],
    })


@app.put("/api/subquestion/<int:qi>/<int:si>")
def api_update_sq(qi: int, si: int):
    global _dirty
    data = request.get_json()
    with _write_lock:
        if qi < 0 or qi >= len(_questions):
            return jsonify({"error": "not found"}), 404
        q = _questions[qi]
        if si < 0 or si >= len(q.sub_questions):
            return jsonify({"error": "not found"}), 404
        sq = q.sub_questions[si]

        for field in ("text", "answer", "discuss", "point"):
            if field in data:
                setattr(sq, field, data[field])
        if "rate" in data:
            sq.rate = str(data["rate"]) if data["rate"] is not None else ""
        if "options" in data and isinstance(data["options"], list):
            sq.options = data["options"]
        if "shared_options" in data and isinstance(data["shared_options"], list):
            q.shared_options = data["shared_options"]
        for field in ("mode", "unit", "cls", "stem"):
            if field in data:
                setattr(q, field, data[field])

        _dirty = True
        return jsonify({"ok": True, "row": _sq_to_dict(q, sq, qi, si)})


@app.delete("/api/question/<int:qi>")
def api_delete_question(qi: int):
    global _dirty
    with _write_lock:
        if qi < 0 or qi >= len(_questions):
            return jsonify({"error": "not found"}), 404
        _questions.pop(qi)
        _dirty = True
        return jsonify({"ok": True, "total": len(_questions)})


@app.delete("/api/subquestion/<int:qi>/<int:si>")
def api_delete_subquestion(qi: int, si: int):
    """删除单个子题（大题至少保留 1 个子题）"""
    global _dirty
    with _write_lock:
        if qi < 0 or qi >= len(_questions):
            return jsonify({"error": "not found"}), 404
        q = _questions[qi]
        if si < 0 or si >= len(q.sub_questions):
            return jsonify({"error": "not found"}), 404
        if len(q.sub_questions) <= 1:
            return jsonify({"error": "大题至少保留一个子题，如需删除整题请用删除大题"}), 400
        q.sub_questions.pop(si)
        _dirty = True
        return jsonify({"ok": True, "sub_total": len(q.sub_questions)})


@app.post("/api/question")
def api_create_question():
    """新建大题（复制结构模板，内容清空）"""
    global _dirty
    data = request.get_json() or {}
    with _write_lock:
        if not _questions:
            return jsonify({"error": "题库为空，无法创建"}), 400

        tmpl_q  = copy.deepcopy(_questions[0])
        tmpl_sq = copy.deepcopy(tmpl_q.sub_questions[0])

        tmpl_q.mode  = data.get("mode",  tmpl_q.mode)
        tmpl_q.unit  = data.get("unit",  tmpl_q.unit)
        tmpl_q.cls   = ""
        tmpl_q.stem  = ""
        tmpl_q.shared_options = []

        for attr in ("text", "answer", "discuss", "point"):
            try: setattr(tmpl_sq, attr, "")
            except: pass
        for attr in ("rate", "ai_answer", "ai_discuss", "ai_confidence",
                     "ai_model", "eff_answer", "eff_discuss"):
            try: setattr(tmpl_sq, attr, None)
            except: pass
        tmpl_sq.options = ["", "", "", ""]
        try: tmpl_sq.answer_source  = "manual"
        except: pass
        try: tmpl_sq.discuss_source = "manual"
        except: pass
        tmpl_q.sub_questions = [tmpl_sq]

        _questions.append(tmpl_q)
        qi = len(_questions) - 1
        _dirty = True
        return jsonify({"ok": True, "qi": qi})


@app.post("/api/question/<int:qi>/subquestion")
def api_add_subquestion(qi: int):
    """给大题追加一个新子题"""
    global _dirty
    with _write_lock:
        if qi < 0 or qi >= len(_questions):
            return jsonify({"error": "not found"}), 404
        q = _questions[qi]
        tmpl = copy.deepcopy(q.sub_questions[0])
        for attr in ("text", "answer", "discuss", "point"):
            try: setattr(tmpl, attr, "")
            except: pass
        for attr in ("rate", "ai_answer", "ai_discuss", "ai_confidence",
                     "ai_model", "eff_answer", "eff_discuss"):
            try: setattr(tmpl, attr, None)
            except: pass
        try: tmpl.answer_source  = "manual"
        except: pass
        try: tmpl.discuss_source = "manual"
        except: pass
        q.sub_questions.append(tmpl)
        si = len(q.sub_questions) - 1
        _dirty = True
        return jsonify({"ok": True, "si": si, "sub_total": len(q.sub_questions)})


@app.post("/api/replace/preview")
def api_replace_preview():
    """预览替换命中项（不实际修改）"""
    data   = request.get_json() or {}
    find   = data.get("find", "")
    fields = set(data.get("fields", ["discuss", "text"]))
    mode   = data.get("mode", "")
    unit   = data.get("unit", "")
    limit  = min(int(data.get("limit", 30)), 100)

    if not find:
        return jsonify({"error": "find 不能为空"}), 400

    hits = []
    for qi, q in enumerate(_questions):
        if mode and q.mode != mode: continue
        if unit and unit not in (q.unit or ""): continue
        for si, sq in enumerate(q.sub_questions):
            for field in fields:
                val = getattr(sq, field, "") or ""
                if find not in val: continue
                idx = val.find(find)
                s = max(0, idx - 40)
                e = min(len(val), idx + len(find) + 40)
                hits.append({
                    "qi": qi, "si": si, "field": field,
                    "mode": q.mode, "unit": q.unit,
                    "before": val[s:idx],
                    "match":  val[idx: idx + len(find)],
                    "after":  val[idx + len(find): e],
                })
                if len(hits) >= limit:
                    return jsonify({"hits": hits, "truncated": True, "total": len(hits)})
    return jsonify({"hits": hits, "truncated": False, "total": len(hits)})


@app.post("/api/replace")
def api_replace():
    """批量文本替换"""
    global _dirty
    data    = request.get_json() or {}
    find    = data.get("find", "")
    replace = data.get("replace", "")
    fields  = set(data.get("fields", ["discuss", "text"]))
    mode    = data.get("mode", "")
    unit    = data.get("unit", "")

    if not find:
        return jsonify({"error": "find 不能为空"}), 400

    with _write_lock:
        count = 0
        for q in _questions:
            if mode and q.mode != mode: continue
            if unit and unit not in (q.unit or ""): continue
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
    with _write_lock:
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
    return render_template("editor.html", session_token=_session_token, asset_ver=_asset_ver)


# ═══════════════════════════════════════════════════════════════════
# 启动入口
# ═══════════════════════════════════════════════════════════════════

def start_editor(bank_path: str, port: int = 5173, host: str = "127.0.0.1",
                 no_browser: bool = False, password: str | None = None) -> None:
    from med_exam_toolkit.bank import load_bank

    global _questions, _bank_path, _password, _session_token, _asset_ver, _server_port, _server_host
    _bank_path     = Path(bank_path).resolve()
    _password      = password
    _server_port   = port
    _server_host   = host
    _session_token = secrets.token_hex(32)
    _asset_ver     = secrets.token_hex(8)    # 每次启动刷新缓存
    print(f"[INFO] 加载题库: {_bank_path}")
    _questions = load_bank(_bank_path, password)
    print(f"[INFO] 已加载 {len(_questions)} 道大题")

    local_url = f"http://127.0.0.1:{port}"
    print(f"[INFO] 本机访问: {local_url}")
    if host == "0.0.0.0":
        lan_ip  = _get_lan_ip()
        lan_url = f"http://{lan_ip}:{port}"
        print(f"[INFO] 局域网访问: {lan_url}  （同网段其他设备可用此地址）")
        print("[WARN] ⚠️  编辑器已开放局域网访问，所有持有 Token 的用户均可修改题库。")
        print("[WARN]    建议仅在受信任的私有网络中使用，不要暴露到公网。")
    print("[INFO] 按 Ctrl+C 退出")

    if not no_browser:
        threading.Timer(0.8, lambda: webbrowser.open(local_url)).start()

    # threaded=True：每个请求在独立线程中处理，支持多用户并发访问
    # 写操作已通过 _write_lock (RLock) 保护，读操作无锁并发安全
    app.run(host=host, port=port, debug=False, use_reloader=False, threaded=True)


if __name__ == "__main__":
    import argparse
    p = argparse.ArgumentParser(description="题库编辑器")
    p.add_argument("--bank",       required=True)
    p.add_argument("--password",   default=None)
    p.add_argument("--port",       default=5173, type=int)
    p.add_argument("--no-browser", action="store_true")
    a = p.parse_args()
    start_editor(a.bank, port=a.port, no_browser=a.no_browser, password=a.password)