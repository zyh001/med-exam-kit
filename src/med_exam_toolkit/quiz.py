from __future__ import annotations
import json as _json
import math
import random
import secrets
import socket
import threading
import time
import webbrowser
from collections import defaultdict, deque
from dataclasses import dataclass, field
from pathlib import Path
from typing import Optional
from flask import Flask, jsonify, request, render_template, make_response
from flask_compress import Compress

# ════════════════════════════════════════════
# 多题库状态
# ════════════════════════════════════════════

@dataclass
class BankState:
    """单个题库的全部运行时状态。"""
    bank_path:      Path
    password:       Optional[str]
    questions:      list          = field(default_factory=list)
    db_path:        Optional[Path] = None
    record_enabled: bool          = True

    @property
    def name(self) -> str:
        return self.bank_path.stem

# 所有已加载的题库，索引即为 ?bank=N 中的 N
_banks: list[BankState] = []

# ── 共享服务级状态 ──
_session_token: str  = ""
_asset_ver:     str  = ""
_server_port:   int  = 5174
_server_host:   str  = "127.0.0.1"

# ── 访问码验证 ──
_access_code:   str  = ""
_push_store: "object | None" = None
_vapid_keys: "object | None" = None
_cookie_secret: str  = ""
_pin_enabled:   bool = True
_pin_len:       int  = 8

# ── AI 答疑 ──
_ai_client:   "object | None" = None
_ai_model:    str  = ""
_ai_provider: str  = ""
_ai_enable_thinking: "bool | None" = None

# ── 速率限制 ──
_RATE_LIMIT  = 120
_RATE_WINDOW = 60
_rate_buckets: dict[str, deque] = defaultdict(deque)
_rate_lock   = threading.Lock()

# ── 考试防作弊：sealed 模式答案暂存 ──
_exam_sessions: dict[str, dict] = {}   # exam_id -> {fingerprint: {answer, discuss}, ts}
_exam_lock = threading.Lock()

# ── 试卷分享令牌（内存存储，7天有效，服务重启后失效）──
_share_tokens: dict[str, dict] = {}    # token -> {fingerprints, mode, bank_idx, time_limit, ts, expires_at}
_share_lock = threading.Lock()
_SHARE_TTL = 7 * 24 * 3600             # 7 天有效期（秒）


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
    try:
        with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as s:
            s.connect(("8.8.8.8", 80))
            return s.getsockname()[0]
    except OSError:
        return "127.0.0.1"


def _get_real_ip() -> str:
    """优先读取反代传递的真实客户端 IP。
    nginx 配置示例：
      proxy_set_header X-Real-IP       $remote_addr;
      proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    """
    ip = (request.headers.get("X-Real-IP")
          or request.headers.get("X-Forwarded-For", ""))
    return ip.split(",")[0].strip() or request.remote_addr or "unknown"


def _get_user_id() -> str:
    return request.cookies.get("med_exam_uid", "_legacy")


def _get_bank() -> tuple[BankState, bool]:
    """从请求的 ?bank=N 参数中解析题库，返回 (bank, ok)。"""
    try:
        idx = int(request.args.get("bank", 0))
    except (ValueError, TypeError):
        return None, False
    if idx < 0 or idx >= len(_banks):
        return None, False
    return _banks[idx], True


def _get_bank_with_idx() -> tuple[BankState, int, bool]:
    """Like _get_bank() but also returns the index."""
    try:
        idx = int(request.args.get("bank", 0))
    except (ValueError, TypeError):
        return None, 0, False
    if idx < 0 or idx >= len(_banks):
        return None, 0, False
    return _banks[idx], idx, True


def _select_questions_by_fp(questions: list, fp_set: set) -> list[dict]:
    """将题库中 fingerprint 在 fp_set 内的题目展开为 sqFlat 风格字典列表。"""
    rows = []
    for qi, q in enumerate(questions):
        if q.fingerprint not in fp_set:
            continue
        shared = list(q.shared_options or [])
        for si, sq in enumerate(q.sub_questions):
            eff_opts = list(sq.options) if sq.options else shared
            rows.append({
                "qi": qi, "si": si,
                "id": f"{qi}-{si}",
                "mode": q.mode or "", "unit": q.unit or "",
                "cls": getattr(q, "cls", "") or "",
                "stem": q.stem or "", "shared_options": shared,
                "text": sq.text or "", "options": eff_opts,
                "answer": (sq.ai_answer or sq.answer or ""),
                "discuss": (sq.ai_discuss or sq.discuss or ""),
                "point": getattr(sq, "point", "") or "",
                "rate": getattr(sq, "rate", "") or "",
                "fingerprint": q.fingerprint,
            })
    return rows


def _create_app() -> Flask:
    app = Flask(__name__, template_folder="templates", static_folder="static")
    app.config["JSON_AS_ASCII"] = False
    app.config["MAX_CONTENT_LENGTH"] = 32 * 1024 * 1024
    Compress(app)

    @app.before_request
    def _guard():
        from med_exam_toolkit.auth import is_authenticated, render_pin_page
        from flask import Response

        if request.path == "/auth" and request.method == "POST":
            if _pin_enabled:
                ip = _get_real_ip()
                from med_exam_toolkit.auth import check_brute_force
                allowed, retry_after = check_brute_force(ip)
                if not allowed:
                    if retry_after >= 60:
                        reason = f"尝试次数过多，请 {(retry_after+59)//60} 分钟后重试"
                    else:
                        reason = f"尝试次数过多，请 {retry_after} 秒后重试"
                    return Response(
                        render_pin_page("医考练习", error=reason, pin_len=_pin_len),
                        mimetype="text/html", status=429,
                    )
            return None

        host_header = request.headers.get("Host", "")
        if ":" in host_header:
            host_name, host_port_str = host_header.rsplit(":", 1)
            try:
                host_port = int(host_port_str)
            except ValueError:
                host_port = None
        else:
            host_name  = host_header
            host_port  = None

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

        # PWA 必需资源不要求认证：被拦截会导致 PWA 完全失效
        _pwa_public = request.path in (
            "/sw.js", "/manifest.json",
            "/static/icon.svg", "/static/icon-192.png", "/static/icon-512.png",
            "/api/push/vapid-key",
        )
        if _pin_enabled and not _pwa_public and not is_authenticated(_cookie_secret, _access_code):
            if request.path == "/" and request.method == "GET":
                from med_exam_toolkit.auth import needs_captcha, new_captcha
                tok, svg = (new_captcha() if needs_captcha(ip := _get_real_ip()) else ("", ""))
                return Response(render_pin_page("医考练习", pin_len=_pin_len,
                                               captcha_token=tok, captcha_svg=svg),
                                mimetype="text/html")
            return jsonify({"error": "Unauthorized", "auth": False}), 401

        if request.path.startswith("/api/"):
            token = request.headers.get("X-Session-Token", "")
            if not secrets.compare_digest(token, _session_token):
                return jsonify({"error": "Unauthorized"}), 401
            ip = _get_real_ip()
            if not _check_rate_limit(ip):
                return jsonify({"error": "Too Many Requests"}), 429

        return None

    @app.after_request
    def _security_headers(response):
        from med_exam_toolkit.auth import apply_security_headers
        apply_security_headers(response)
        return response

    return app


app = _create_app()


@app.errorhandler(413)
def _too_large(e):
    return jsonify({"error": "请求体过大，单次请求不得超过 32 MB"}), 413


# ════════════════════════════════════════════
# 工具函数
# ════════════════════════════════════════════

def _sq_flat(q, sq, qi: int, si: int) -> dict:
    own_opts    = list(sq.options or [])
    shared_opts = list(q.shared_options or [])
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
    if not raw:
        return None
    s = str(raw).strip().rstrip("%")
    try:
        v = float(s)
        return v if 0 <= v <= 100 else None
    except ValueError:
        return None


def _classify_group_difficulty(grp: list[dict]) -> str:
    rates = [r for sq in grp if (r := _parse_rate(sq.get("rate"))) is not None]
    if not rates:
        return "medium"
    avg = sum(rates) / len(rates)
    if avg >= 80: return "easy"
    if avg >= 60: return "medium"
    if avg >= 40: return "hard"
    return "extreme"


def _distribute_by_ratio(total: int, weights: dict) -> dict:
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
    available = list(pool)
    picked: list = []
    total = 0
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
            break
    if total < target and remaining:
        gap = target - total
        best_idx, best_size = -1, 0
        for i, grp in enumerate(remaining):
            c = len(grp)
            if c > gap and (best_idx == -1 or c < best_size):
                best_idx = i
                best_size = c
        if best_idx >= 0:
            picked.append(remaining[best_idx][:gap])
    return picked


def _sample_with_difficulty(pool: list, target: int, difficulty: dict, rng) -> list:
    by_diff: dict = defaultdict(list)
    for grp in pool:
        by_diff[_classify_group_difficulty(grp)].append(grp)
    targets = _distribute_by_ratio(target, difficulty)
    selected: list = []
    for level, need in targets.items():
        lvl_pool = list(by_diff.get(level, []))
        rng.shuffle(lvl_pool)
        selected.extend(_greedy_fill(lvl_pool, need))
    used_ids = {id(g) for g in selected}
    got = sum(len(g) for g in selected)
    if got < target:
        rest = [g for g in pool if id(g) not in used_ids]
        rng.shuffle(rest)
        selected.extend(_greedy_fill(rest, target - got))
    return selected


# ════════════════════════════════════════════
# API — 题库列表
# ════════════════════════════════════════════

@app.get("/api/banks")
def api_banks():
    """返回所有已加载题库的元信息列表。"""
    infos = []
    for i, b in enumerate(_banks):
        total_sq = sum(len(q.sub_questions) for q in b.questions)
        infos.append({
            "id":       i,
            "name":     b.name,
            "path":     str(b.bank_path),
            "total_sq": total_sq,
        })
    return jsonify({
        "banks":         infos,
        "session_token": _session_token,
        "asset_ver":     _asset_ver,
    })


# ════════════════════════════════════════════
# API — 答题记录
# ════════════════════════════════════════════

@app.post("/api/record")
def api_record():
    b, ok = _get_bank()
    if not ok:
        return jsonify({"error": "bank not found"}), 404
    if not b.record_enabled:
        return jsonify({"ok": True, "skipped": True})
    if b.db_path is None:
        return jsonify({"ok": False, "error": "进度数据库未初始化"}), 503
    try:
        from med_exam_toolkit.progress import record_session
        data = request.get_json(silent=True) or {}
        record_session(b.db_path, data, user_id=_get_user_id())
        return jsonify({"ok": True})
    except Exception as e:
        return jsonify({"ok": False, "error": str(e)}), 500


@app.get("/api/record/status")
def api_record_status():
    b, ok = _get_bank()
    if not ok:
        return jsonify({"error": "bank not found"}), 404
    uid = _get_user_id()
    return jsonify({
        "enabled":   b.record_enabled,
        "db_ready":  b.db_path is not None and b.db_path.exists(),
        "user_id":   uid,
        "is_legacy": uid == "_legacy",
    })


@app.post("/api/record/clear")
def api_record_clear():
    b, ok = _get_bank()
    if not ok:
        return jsonify({"error": "bank not found"}), 404
    if b.db_path is None or not b.db_path.exists():
        return jsonify({"ok": True, "deleted": {}}), 200
    try:
        from med_exam_toolkit.progress import clear_user_data
        deleted = clear_user_data(b.db_path, user_id=_get_user_id())
        return jsonify({"ok": True, "deleted": deleted})
    except Exception as e:
        return jsonify({"ok": False, "error": str(e)}), 500


@app.post("/api/record/migrate")
def api_record_migrate():
    """将旧 UID 的学习记录合并到当前 UID。
    入口刻意做得隐蔽（统计页用户 ID 旁的小链接），防止误操作。
    """
    b, ok = _get_bank()
    if not ok:
        return jsonify({"error": "bank not found"}), 404
    if not b.record_enabled or b.db_path is None:
        return jsonify({"error": "记录功能未启用"}), 503
    data     = request.get_json(silent=True) or {}
    from_uid = (data.get("from_uid") or "").strip()
    to_uid   = _get_user_id()
    if not from_uid or from_uid == to_uid:
        return jsonify({"error": "无效的 from_uid"}), 400
    try:
        from med_exam_toolkit.progress import migrate_user_data
        counts = migrate_user_data(b.db_path, from_uid=from_uid, to_uid=to_uid)
        return jsonify({"ok": True, "migrated": counts})
    except Exception as e:
        return jsonify({"ok": False, "error": str(e)}), 500


@app.get("/api/stats")
def api_stats():
    b, ok = _get_bank()
    if not ok:
        return jsonify({"error": "bank not found"}), 404
    if b.db_path is None or not b.db_path.exists():
        return jsonify({"overall": {}, "history": [], "units": []})
    from med_exam_toolkit.progress import get_overall_stats, get_history, get_unit_stats
    uid = _get_user_id()
    client_date = request.args.get("date", "")
    return jsonify({
        "overall": get_overall_stats(b.db_path, user_id=uid, client_date=client_date),
        "history": get_history(b.db_path, user_id=uid, limit=20),
        "units":   get_unit_stats(b.db_path, user_id=uid),
    })


@app.get("/api/review/due")
def api_review_due():
    b, ok = _get_bank()
    if not ok:
        return jsonify({"error": "bank not found"}), 404
    if b.db_path is None or not b.db_path.exists():
        return jsonify({"fingerprints": [], "count": 0})
    from med_exam_toolkit.progress import get_due_fingerprints
    # 接受客户端本地日期以修正时区偏差（中国用户在 UTC 午夜到 08:00 间跨天时尤为关键）
    client_date = request.args.get("date", "")
    fps = get_due_fingerprints(b.db_path, user_id=_get_user_id(), client_date=client_date)
    return jsonify({"fingerprints": fps, "count": len(fps)})


@app.get("/api/wrongbook")
def api_wrongbook():
    b, ok = _get_bank()
    if not ok:
        return jsonify({"error": "bank not found"}), 404
    if b.db_path is None or not b.db_path.exists():
        return jsonify({"items": [], "count": 0})
    from med_exam_toolkit.progress import get_wrong_fingerprints
    entries = get_wrong_fingerprints(b.db_path, user_id=_get_user_id())

    # 构建 fingerprint → question 索引，附上题目文字
    fp_idx = {q.fingerprint: q for q in (b.questions or [])}
    items = []
    for e in entries:
        item = dict(e)
        q = fp_idx.get(e.get("fingerprint", ""))
        if q and q.sub_questions:
            sq = q.sub_questions[0]
            item["text"]    = getattr(sq, "text", "") or ""
            item["stem"]    = getattr(q,  "stem", "") or ""
            item["answer"]  = sq.eff_answer() if hasattr(sq, "eff_answer") else (getattr(sq, "answer", "") or "")
            item["discuss"] = sq.eff_discuss() if hasattr(sq, "eff_discuss") else (getattr(sq, "discuss", "") or "")
            item["unit"]    = getattr(q, "unit", "") or ""
        items.append(item)
    return jsonify({"items": items, "count": len(items)})


@app.get("/api/history")
def api_history():
    """返回服务端完整历史记录（sessions 表）供主页展示。"""
    b, ok = _get_bank()
    if not ok:
        return jsonify({"error": "bank not found"}), 404
    if b.db_path is None or not b.db_path.exists():
        return jsonify({"items": []})
    try:
        limit = min(int(request.args.get("limit", 50)), 200)
    except (ValueError, TypeError):
        limit = 50
    from med_exam_toolkit.progress import get_history
    items = get_history(b.db_path, user_id=_get_user_id(), limit=limit)
    return jsonify({"items": items})


@app.delete("/api/session/<session_id>")
def api_delete_session(session_id: str):
    """删除指定会话记录（只能删自己的）。"""
    b, ok = _get_bank()
    if not ok:
        return jsonify({"error": "bank not found"}), 404
    # DB 不存在时视为「无需删除」而非错误（本地记录可能尚未同步到服务端）
    if b.db_path is None or not b.db_path.exists():
        return jsonify({"ok": True})
    from med_exam_toolkit.progress import delete_session
    deleted = delete_session(b.db_path, session_id, _get_user_id())
    return jsonify({"ok": deleted})


@app.post("/api/sync")
def api_sync():
    b, ok = _get_bank()
    if not ok:
        return jsonify({"error": "bank not found"}), 404
    if not b.record_enabled:
        return jsonify({"ok": True, "skipped_all": True, "processed": [], "skipped": [], "failed": []})
    if b.db_path is None:
        return jsonify({"ok": False, "error": "进度数据库未初始化"}), 503
    data     = request.get_json(silent=True) or {}
    sessions = data.get("sessions", [])
    if not isinstance(sessions, list) or not sessions:
        return jsonify({"ok": True, "processed": [], "skipped": [], "failed": []})
    if len(sessions) > 200:
        return jsonify({"ok": False, "error": "单次同步不超过 200 条"}), 400
    try:
        from med_exam_toolkit.progress import record_sessions_batch
        uid    = _get_user_id()
        result = record_sessions_batch(b.db_path, sessions, user_id=uid)
        return jsonify({"ok": True, "failed": [], **result})
    except Exception as e:
        return jsonify({"ok": False, "error": str(e)}), 500


@app.get("/api/sync/status")
def api_sync_status():
    b, ok = _get_bank()
    if not ok:
        return jsonify({"error": "bank not found"}), 404
    if b.db_path is None or not b.db_path.exists():
        return jsonify({"session_count": 0, "last_ts": None, "db_ready": False})
    try:
        from med_exam_toolkit.progress import get_sync_status
        status = get_sync_status(b.db_path, user_id=_get_user_id())
        return jsonify({"db_ready": True, **status})
    except Exception as e:
        return jsonify({"db_ready": False, "error": str(e)}), 500


# ════════════════════════════════════════════
# API — 考试防作弊 & 试卷分享
# ════════════════════════════════════════════

@app.get("/api/exam/reveal")
def api_exam_reveal():
    """考试模式提交后下发答案（一次性，消费后删除）。"""
    eid = request.args.get("id", "")
    if not eid:
        return jsonify({"error": "缺少 exam id"}), 400
    with _exam_lock:
        sess = _exam_sessions.pop(eid, None)
    if sess is None:
        return jsonify({"error": "考试会话已过期或不存在"}), 404
    return jsonify({"answers": sess["answers"]})


@app.post("/api/exam/share")
def api_exam_share():
    """生成试卷分享令牌（7天有效，内存存储）。"""
    try:
        body = request.get_json(force=True) or {}
    except Exception:
        return jsonify({"error": "invalid request"}), 400

    fps = body.get("fingerprints", [])
    if not fps:
        return jsonify({"error": "fingerprints required"}), 400

    # exam_done 是前端内部状态，统一规范为 'exam' 存储
    raw_mode   = body.get("mode", "exam")
    mode       = "exam" if raw_mode in ("exam", "exam_done") else raw_mode
    time_limit = int(body.get("time_limit", 90 * 60))
    if time_limit <= 0:
        time_limit = 90 * 60

    scoring          = bool(body.get("scoring", False))
    score_per_mode   = body.get("score_per_mode") or {}
    multi_score_mode = body.get("multi_score_mode") or "strict"
    sub_ids          = body.get("sub_ids") or []

    _, bank_idx, ok = _get_bank_with_idx()
    if not ok:
        return jsonify({"error": "bank not found"}), 404

    token      = secrets.token_hex(8)
    now        = int(time.time())
    expires_at = now + _SHARE_TTL

    cfg = {
        "fingerprints":     fps,
        "sub_ids":          sub_ids,
        "mode":             mode,
        "bank_idx":         bank_idx,
        "time_limit":       time_limit,
        "scoring":          scoring,
        "score_per_mode":   score_per_mode,
        "multi_score_mode": multi_score_mode,
        "ts":               now,
        "expires_at":       expires_at,
    }

    with _share_lock:
        # 清理已过期 token
        expired = [k for k, v in _share_tokens.items() if now > v.get("expires_at", 0)]
        for k in expired:
            del _share_tokens[k]
        _share_tokens[token] = cfg

    return jsonify({"ok": True, "token": token})


@app.get("/api/exam/join")
def api_exam_join():
    """用 token 加入分享试卷。服务端从 token 读取 bank_idx，客户端无需传 ?bank=N。"""
    token = request.args.get("token", "")
    if not token:
        return jsonify({"error": "缺少 token"}), 400

    now = int(time.time())
    with _share_lock:
        cfg = _share_tokens.get(token)

    if cfg is None:
        return jsonify({"error": "分享链接已过期或无效"}), 404

    # 校验 7 天有效期
    if now > cfg.get("expires_at", 0):
        with _share_lock:
            _share_tokens.pop(token, None)
        return jsonify({"error": "分享链接已过期（7天有效期）"}), 404

    bank_idx = cfg["bank_idx"]
    if bank_idx < 0 or bank_idx >= len(_banks):
        return jsonify({"error": "题库不存在"}), 404

    b = _banks[bank_idx]
    fp_set = set(cfg["fingerprints"])

    # 从该题库按 fingerprint 过滤题目并展开为 flat 列表
    rows = _select_questions_by_fp(b.questions, fp_set)

    # 精确到小题级别：若分享时提供了 sub_ids（"fingerprint:si" 对），
    # 只保留接收端明确指定的那些小题，防止服务端自动把同一题干下所有小题
    # 全部还原（导致 220 → 221 等多出题目的情况）。
    sub_ids = cfg.get("sub_ids") or []
    if sub_ids:
        allowed = set(sub_ids)
        rows = [
            r for r in rows
            if f"{r.get('fingerprint','')}:{r.get('si',0)}" in allowed
        ]

    # exam / exam_done 模式：剥离答案，服务端暂存密封
    # 返回给客户端统一使用 'exam'，让接收方直接进入考试模式
    is_exam_mode = cfg["mode"] in ("exam", "exam_done")
    eid = None
    if is_exam_mode and rows:
        eid = secrets.token_hex(16)
        answers = {r["fingerprint"]: {"answer": r["answer"], "discuss": r["discuss"]} for r in rows}
        for r in rows:
            r["answer"] = ""
            r["discuss"] = ""
        with _exam_lock:
            old = [k for k, v in _exam_sessions.items() if now - v["ts"] > 86400]
            for k in old:
                del _exam_sessions[k]
            _exam_sessions[eid] = {"answers": answers, "ts": now}

    out_mode   = "exam" if is_exam_mode else cfg["mode"]
    time_limit = cfg.get("time_limit", 90 * 60)
    return jsonify({
        "items":            rows,
        "total":            len(rows),
        "mode":             out_mode,
        "exam_id":          eid,
        "time_limit":       time_limit,
        "bank_idx":         bank_idx,
        "scoring":          cfg.get("scoring", False),
        "score_per_mode":   cfg.get("score_per_mode", {}),
        "multi_score_mode": cfg.get("multi_score_mode", "strict"),
    })


# ════════════════════════════════════════════
# API — 题库信息
# ════════════════════════════════════════════

@app.get("/api/info")
def api_info():
    b, ok = _get_bank()
    if not ok:
        return jsonify({"error": "bank not found"}), 404
    from collections import Counter
    questions = b.questions
    mc    = Counter(q.mode for q in questions)
    uc    = Counter(q.unit for q in questions)
    mc_sq = Counter()
    unit_mode_sq: dict = {}
    for q in questions:
        mc_sq[q.mode] += len(q.sub_questions)
        u = q.unit or ""
        m = q.mode or ""
        if u not in unit_mode_sq:
            unit_mode_sq[u] = {}
        unit_mode_sq[u][m] = unit_mode_sq[u].get(m, 0) + len(q.sub_questions)
    unit_sq = {u: sum(v.values()) for u, v in unit_mode_sq.items()}
    return jsonify({
        "bank_name":      b.name,
        "total_q":        len(questions),
        "total_sq":       sum(len(q.sub_questions) for q in questions),
        "modes":          sorted(k for k in mc if k),
        "units":          sorted(k for k in uc if k),
        "mode_counts":    dict(mc),
        "mode_counts_sq": dict(mc_sq),
        "unit_counts":    dict(uc),
        "unit_sq":        unit_sq,
        "unit_mode_sq":   unit_mode_sq,
        "ai_enabled":     _ai_client is not None,
    })


# ════════════════════════════════════════════
# API — 题目查询
# ════════════════════════════════════════════

@app.get("/api/questions")
def api_questions():
    b, ok = _get_bank()
    if not ok:
        return jsonify({"error": "bank not found"}), 404

    modes_filter  = request.args.getlist("mode")
    units_filter  = request.args.getlist("unit")
    limit         = int(request.args.get("limit", 0))
    shuffle       = request.args.get("shuffle", "0") == "1"
    seed          = request.args.get("seed", None)
    per_mode_raw  = request.args.get("per_mode",   None)
    per_unit_raw  = request.args.get("per_unit",   None)
    difficulty_raw= request.args.get("difficulty", None)
    fps_raw       = request.args.get("fingerprints", None)

    per_mode   = _json.loads(per_mode_raw)   if per_mode_raw   else None
    per_unit   = _json.loads(per_unit_raw)   if per_unit_raw   else None
    difficulty = _json.loads(difficulty_raw) if difficulty_raw else None
    fp_set     = set(fps_raw.split(","))     if fps_raw        else None

    rng = random.Random(int(seed)) if seed else random.Random()

    groups: list[list[dict]] = []
    for qi, q in enumerate(b.questions):
        if modes_filter and q.mode not in modes_filter:
            continue
        if units_filter and not any(u and u in (q.unit or "") for u in units_filter):
            continue
        if per_unit and (q.unit or "") not in per_unit:
            continue
        if fp_set is not None:
            fp = getattr(q, "fingerprint", "") or ""
            if fp not in fp_set:
                continue
        grp = [_sq_flat(q, sq, qi, si) for si, sq in enumerate(q.sub_questions)]
        if grp:
            groups.append(grp)

    # ── 按题型分组 ──
    mode_order: list[str] = []
    mode_map:  dict[str, list] = {}
    for grp in groups:
        mk = grp[0]["mode"]
        if mk not in mode_map:
            mode_order.append(mk)
            mode_map[mk] = []
        mode_map[mk].append(grp)

    result_groups: list[list[dict]] = []

    if per_unit:
        unit_order: list[str] = []
        unit_map: dict[str, list] = {}
        for grp in groups:
            uk = grp[0]["unit"]
            if uk not in unit_map:
                unit_order.append(uk)
                unit_map[uk] = []
            unit_map[uk].append(grp)
        reorder: dict[str, list] = {}
        for uk in unit_order:
            need = per_unit.get(uk, 0)
            if need <= 0:
                continue
            pool = list(unit_map[uk])
            rng.shuffle(pool)
            for grp in _greedy_fill(pool, need):
                mk = grp[0]["mode"]
                reorder.setdefault(mk, []).append(grp)
        for mk in mode_order:
            result_groups.extend(reorder.get(mk, []))

    elif not shuffle:
        for mk in mode_order:
            result_groups.extend(mode_map[mk])
        if limit > 0:
            cut, n = [], 0
            for grp in result_groups:
                c = len(grp)
                if n + c <= limit:
                    cut.append(grp)
                    n += c
                if n >= limit:
                    break
            result_groups = cut

    elif per_mode:
        total_need_pm = 0
        for mk in mode_order:
            need = per_mode.get(mk, 0)
            if need <= 0:
                continue
            total_need_pm += need
            pool = list(mode_map[mk])
            rng.shuffle(pool)
            result_groups.extend(_greedy_fill(pool, need))
        # shortfall recovery
        actual_pm = sum(len(g) for g in result_groups)
        if (shortfall := total_need_pm - actual_pm) > 0:
            picked_ids = {id(g) for g in result_groups}
            for mk in mode_order:
                if shortfall <= 0:
                    break
                for grp in mode_map[mk]:
                    if shortfall <= 0:
                        break
                    if id(grp) not in picked_ids and len(grp) <= shortfall:
                        result_groups.append(grp)
                        picked_ids.add(id(grp))
                        shortfall -= len(grp)

    else:
        mode_sq_total = {mk: sum(len(g) for g in mode_map[mk]) for mk in mode_order}
        total_sq_all  = sum(mode_sq_total.values())
        total_need    = limit if limit > 0 else total_sq_all
        quotas = _distribute_by_ratio(total_need, mode_sq_total)
        for mk, tot in mode_sq_total.items():
            if tot > 0 and quotas[mk] < 1:
                quotas[mk] = 1
        # trim overflow
        overflow = sum(quotas.values()) - total_need
        for mk in mode_order:
            if overflow <= 0:
                break
            red = min(quotas[mk] - 1, overflow)
            if red > 0:
                quotas[mk] -= red
                overflow   -= red

        for mk in mode_order:
            need = quotas.get(mk, 0)
            if need <= 0:
                continue
            pool = list(mode_map[mk])
            if difficulty:
                rng.shuffle(pool)
                result_groups.extend(_sample_with_difficulty(pool, need, difficulty, rng))
            else:
                rng.shuffle(pool)
                result_groups.extend(_greedy_fill(pool, need))

        # shortfall recovery
        actual = sum(len(g) for g in result_groups)
        if (shortfall := total_need - actual) > 0:
            picked_ids = {id(g) for g in result_groups}
            for mk in mode_order:
                if shortfall <= 0:
                    break
                for grp in mode_map[mk]:
                    if shortfall <= 0:
                        break
                    if id(grp) not in picked_ids and len(grp) <= shortfall:
                        result_groups.append(grp)
                        picked_ids.add(id(grp))
                        shortfall -= len(grp)

    items = [sq for grp in result_groups for sq in grp]
    return jsonify({"total": len(items), "items": items})


# ════════════════════════════════════════════
# 页面路由
# ════════════════════════════════════════════

@app.get("/")
def index():
    import secrets as _sec
    from flask import Response
    resp = make_response(render_template(
        "quiz.html",
        session_token=_session_token,
        asset_ver=_asset_ver,
    ))
    if not request.cookies.get("med_exam_uid"):
        resp.set_cookie(
            "med_exam_uid", _sec.token_hex(16),
            max_age=365 * 24 * 3600, httponly=False, samesite="Lax", path="/",
        )
    return resp


@app.get("/static/icon.svg")
def icon_svg():
    """SVG 应用图标：通过路由动态生成，无需预置静态图片文件。
    manifest.json 引用此路由，浏览器即可识别 PWA 安装条件。
    """
    from flask import Response
    svg = (
        '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 192 192">'
        '\n  <rect width="192" height="192" rx="36" fill="#0d1117"/>'
        '\n  <rect x="82" y="38" width="28" height="116" rx="14" fill="#3a82f6"/>'
        '\n  <rect x="38" y="82" width="116" height="28" rx="14" fill="#3a82f6"/>'
        '\n  <circle cx="96" cy="96" r="18" fill="#0d1117"/>'
        '\n  <circle cx="96" cy="96" r="10" fill="#3a82f6"/>'
        '\n</svg>'
    )
    resp = Response(svg, mimetype="image/svg+xml")
    resp.headers["Cache-Control"] = "public, max-age=86400"
    return resp


@app.get("/static/icon-192.png")
def icon_192_png():
    resp = make_response(app.send_static_file("icon-192.png"))
    resp.headers["Content-Type"] = "image/png"
    resp.headers["Cache-Control"] = "public, max-age=86400"
    return resp


@app.get("/static/icon-512.png")
def icon_512_png():
    resp = make_response(app.send_static_file("icon-512.png"))
    resp.headers["Content-Type"] = "image/png"
    resp.headers["Cache-Control"] = "public, max-age=86400"
    return resp


@app.get("/manifest.json")
def pwa_manifest():
    resp = make_response(app.send_static_file("manifest.json"))
    resp.headers["Content-Type"] = "application/manifest+json"
    return resp


# ── Web Push API ──────────────────────────────────────────────────────────

@app.get("/api/push/vapid-key")
def api_push_vapid_key():
    """返回服务端 VAPID 公钥（无需认证，PWA 初始化时调用）。"""
    key = _vapid_keys.public_key_b64 if _vapid_keys else ""
    return jsonify({"publicKey": key})


@app.post("/api/push/subscribe")
def api_push_subscribe():
    """保存用户 push 订阅。"""
    if _push_store is None:
        return jsonify({"error": "push not available"}), 503
    try:
        from med_exam_toolkit.push import PushSubscription
        data = request.get_json(silent=True) or {}
        sub  = PushSubscription.from_dict(data)
        uid  = (data.get("uid") or "").strip()
        _push_store.add(sub, uid)
        import logging
        logging.getLogger(__name__).info("[push] 新订阅: uid=%s ep=%s…", uid, sub.endpoint[:40])
        return jsonify({"ok": True})
    except (KeyError, Exception) as e:
        return jsonify({"error": str(e)}), 400


@app.delete("/api/push/subscribe")
def api_push_unsubscribe():
    """删除用户 push 订阅。"""
    if _push_store is None:
        return jsonify({"ok": True})
    data = request.get_json(silent=True) or {}
    ep   = data.get("endpoint", "")
    if ep:
        _push_store.remove(ep)
    return jsonify({"ok": True})


_push_test_buckets: dict = {}
_push_test_lock = __import__('threading').Lock()

@app.post("/api/push/test")
def api_push_test():
    """立即发送测试推送（调试用）。
    ?uid=<用户ID> → 只推给该用户；无参数 → 推给所有订阅者。
    每个 IP 限流：5次/小时，防止滥用。
    """
    import time as _time
    ip = _get_real_ip()
    with _push_test_lock:
        now    = _time.time()
        cutoff = now - 3600
        fresh  = [t for t in _push_test_buckets.get(ip, []) if t > cutoff]
        if len(fresh) >= 5:
            _push_test_buckets[ip] = fresh
            return jsonify({"error": "请求过于频繁，每小时最多测试 5 次"}), 429
        fresh.append(now)
        _push_test_buckets[ip] = fresh

    if _push_store is None or _vapid_keys is None:
        return jsonify({"error": "push not initialised"}), 503

    from med_exam_toolkit.push import send_push, SubscriptionGone
    import json as _json
    payload = _json.dumps({
        "title": "医考练习 · 测试通知",
        "body":  "推送功能正常 🎉 点击打开应用",
        "due":   0,
    }).encode()

    uid = request.args.get("uid", "").strip()
    if uid:
        sub = _push_store.for_uid(uid)
        if sub is None:
            return jsonify({"ok": False, "msg": "该用户暂无订阅"})
        subs = [sub]
    else:
        subs = _push_store.all()

    if not subs:
        return jsonify({"ok": True, "sent": 0, "msg": "暂无订阅者"})

    sent = failed = removed = 0
    for sub in subs:
        try:
            send_push(_vapid_keys, sub, payload)
            sent += 1
        except SubscriptionGone:
            _push_store.remove(sub.endpoint)
            removed += 1
        except Exception as e:
            import logging
            logging.getLogger(__name__).warning("[push/test] 失败: %s", e)
            failed += 1

    return jsonify({"ok": True, "sent": sent, "failed": failed, "removed": removed})


@app.post("/api/calculate")
def api_calculate():
    """服务端科学计算器 — 递归下降表达式解析器。"""
    data = request.get_json(silent=True) or {}
    expr = data.get("expr", "")
    deg = data.get("deg", True)
    result = _calc_evaluate(expr, deg)
    return jsonify({"result": result})


# ── 递归下降表达式解析器 ──────────────────────────────────────────────

class _CalcParser:
    def __init__(self, src: str, deg: bool):
        self.src = src
        self.pos = 0
        self.deg = deg

    def ws(self):
        while self.pos < len(self.src) and self.src[self.pos] == ' ':
            self.pos += 1

    def try_match(self, s: str) -> bool:
        self.ws()
        if self.src[self.pos:self.pos + len(s)] == s:
            self.pos += len(s)
            return True
        return False

    def peek(self) -> str:
        self.ws()
        return self.src[self.pos] if self.pos < len(self.src) else ''

    def parse_expr(self) -> float:
        v = self.parse_mul_div()
        while True:
            self.ws()
            if self.pos >= len(self.src):
                break
            if self.src[self.pos] == '+':
                self.pos += 1; v += self.parse_mul_div()
            elif self.src[self.pos] == '-':
                self.pos += 1; v -= self.parse_mul_div()
            else:
                break
        return v

    def parse_mul_div(self) -> float:
        v = self.parse_pow()
        while True:
            self.ws()
            if self.pos >= len(self.src):
                break
            if self.src[self.pos] == '*' and (self.pos + 1 >= len(self.src) or self.src[self.pos + 1] != '*'):
                self.pos += 1; v *= self.parse_pow()
            elif self.src[self.pos] == '/':
                self.pos += 1; v /= self.parse_pow()
            else:
                break
        return v

    def parse_pow(self) -> float:
        b = self.parse_unary()
        self.ws()
        if self.pos < len(self.src) and self.src[self.pos] == '^':
            self.pos += 1
            b = math.pow(b, self.parse_pow())
        elif self.pos + 1 < len(self.src) and self.src[self.pos:self.pos + 2] == '**':
            self.pos += 2
            b = math.pow(b, self.parse_pow())
        return b

    def parse_unary(self) -> float:
        self.ws()
        if self.pos < len(self.src) and self.src[self.pos] == '-':
            self.pos += 1; return -self.parse_postfix()
        if self.pos < len(self.src) and self.src[self.pos] == '+':
            self.pos += 1
        return self.parse_postfix()

    def parse_postfix(self) -> float:
        v = self.parse_atom()
        self.ws()
        while self.pos < len(self.src) and self.src[self.pos] == '!':
            self.pos += 1
            v = _factorial(v)
        return v

    def parse_atom(self) -> float:
        self.ws()
        for fn in ('asin', 'acos', 'atan', 'sqrt', 'sin', 'cos', 'tan', 'lg', 'ln'):
            if self.try_match(fn + '('):
                a = self.parse_expr()
                if self.pos < len(self.src) and self.src[self.pos] == ')':
                    self.pos += 1
                return self._apply_func(fn, a)
        if self.try_match('10^'):
            return math.pow(10, self.parse_unary())
        if self.try_match('e^'):
            return math.exp(self.parse_unary())
        if self.peek() == '(':
            self.pos += 1
            v = self.parse_expr()
            if self.pos < len(self.src) and self.src[self.pos] == ')':
                self.pos += 1
            return v
        # number
        start = self.pos
        if self.pos < len(self.src) and self.src[self.pos] in '-+':
            self.pos += 1
        while self.pos < len(self.src) and (self.src[self.pos].isdigit() or self.src[self.pos] == '.'):
            self.pos += 1
        # scientific notation
        if self.pos < len(self.src) and self.src[self.pos] in 'eE':
            self.pos += 1
            if self.pos < len(self.src) and self.src[self.pos] in '+-':
                self.pos += 1
            while self.pos < len(self.src) and self.src[self.pos].isdigit():
                self.pos += 1
        if self.pos == start:
            raise ValueError('unexpected token')
        return float(self.src[start:self.pos])

    def _apply_func(self, fn: str, a: float) -> float:
        to_rad = lambda x: math.radians(x) if self.deg else x
        from_rad = lambda x: math.degrees(x) if self.deg else x
        funcs = {
            'sin': lambda: math.sin(to_rad(a)),
            'cos': lambda: math.cos(to_rad(a)),
            'tan': lambda: math.tan(to_rad(a)),
            'asin': lambda: from_rad(math.asin(a)),
            'acos': lambda: from_rad(math.acos(a)),
            'atan': lambda: from_rad(math.atan(a)),
            'sqrt': lambda: math.sqrt(a),
            'lg': lambda: math.log10(a),
            'ln': lambda: math.log(a),
        }
        return funcs.get(fn, lambda: float('nan'))()


def _factorial(n: float) -> float:
    if n < 0 or n != math.floor(n) or n > 170:
        return float('nan')
    r = 1.0
    for i in range(2, int(n) + 1):
        r *= i
    return r


def _calc_evaluate(raw: str, deg: bool) -> str:
    try:
        import re
        s = raw.replace('×', '*').replace('÷', '/').replace('−', '-')
        s = s.replace('π', f'({math.pi})').replace('%', '/100')
        # standalone e constant
        s = re.sub(r'(?<![a-zA-Z])e(?!\^)(?![a-zA-Z])', f'({math.e})', s)
        p = _CalcParser(s, deg)
        val = p.parse_expr()
        if math.isinf(val):
            return '∞' if val > 0 else '-∞'
        if math.isnan(val):
            return 'Error'
        return f'{val:.12g}'
    except Exception:
        return 'Error'


@app.get("/sw.js")
def pwa_sw():
    resp = make_response(app.send_static_file("sw.js"))
    resp.headers["Content-Type"] = "application/javascript"
    resp.headers["Service-Worker-Allowed"] = "/"
    resp.headers["Cache-Control"] = "no-store"
    return resp


@app.post("/auth")
def auth():
    from med_exam_toolkit.auth import (
        render_pin_page, set_auth_cookie,
        needs_captcha, new_captcha, verify_captcha,
        record_success, record_failure,
    )
    from flask import Response
    ip   = _get_real_ip()
    code = request.form.get("code", "").strip().upper()

    # 如需验证码，先校验
    if _pin_enabled and needs_captcha(ip):
        token  = request.form.get("captcha_token", "")
        answer = request.form.get("captcha_answer", "")
        if not verify_captcha(token, answer, ip):
            tok, svg = new_captcha()
            return Response(
                render_pin_page("医考练习", error="验证码错误，请重新计算",
                               pin_len=_pin_len, captcha_token=tok, captcha_svg=svg),
                mimetype="text/html", status=200,
            )

    if _pin_enabled and secrets.compare_digest(code, _access_code):
        record_success(ip)
        resp = make_response("", 302)
        resp.headers["Location"] = "/"
        set_auth_cookie(resp, _cookie_secret, _access_code)
        return resp

    if _pin_enabled:
        record_failure(ip)
        tok, svg = (new_captcha() if needs_captcha(ip) else ("", ""))
        return Response(
            render_pin_page("医考练习", error="访问码不正确，请重试",
                           pin_len=_pin_len, captcha_token=tok, captcha_svg=svg),
            mimetype="text/html", status=200,
        )
    return Response("", 302, headers={"Location": "/"})


# ════════════════════════════════════════════
# API — AI 答疑（流式）
# ════════════════════════════════════════════

@app.post("/api/ai/chat")
def api_ai_chat():
    if _ai_client is None:
        return jsonify({"error": "AI 功能未配置"}), 503

    data = request.get_json(silent=True) or {}
    fingerprint = data.get("fingerprint", "")
    sq_index    = int(data.get("sq_index", 0))
    bank_idx    = int(data.get("bank", 0))
    user_answer = data.get("user_answer", "")
    history     = data.get("history", [])

    # Look up question
    if bank_idx < 0 or bank_idx >= len(_banks):
        bank_idx = 0
    question = None
    for b in _banks:
        for q in b.questions:
            if q.fingerprint == fingerprint:
                question = q
                break
        if question:
            break
    if question is None:
        return jsonify({"error": "题目未找到"}), 404
    if sq_index < 0 or sq_index >= len(question.sub_questions):
        return jsonify({"error": "小题索引无效"}), 400

    from med_exam_toolkit.ai.prompt import build_ai_chat_prompt
    from med_exam_toolkit.ai.client import chat_completion_stream

    messages = build_ai_chat_prompt(question, sq_index, user_answer)
    if history:
        messages.extend(history)

    def generate():
        for chunk in chat_completion_stream(
            client=_ai_client,
            model=_ai_model,
            messages=messages,
            temperature=0.7,
            max_tokens=2048,
            enable_thinking=_ai_enable_thinking,
            provider=_ai_provider,
        ):
            if chunk.get("done"):
                yield "data: [DONE]\n\n"
                return
            if chunk.get("error"):
                yield f"data: {_json.dumps({'error': chunk['error']})}\n\n"
                return
            yield f"data: {_json.dumps({'content': chunk.get('content', ''), 'reasoning': chunk.get('reasoning', '')})}\n\n"

    from flask import Response
    return Response(generate(), mimetype="text/event-stream",
                    headers={"Cache-Control": "no-cache", "X-Accel-Buffering": "no"})


# ════════════════════════════════════════════
# 启动函数
# ════════════════════════════════════════════

def start_quiz(
    bank_paths:  list[str] | str,
    port:       int  = 5174,
    host:       str  = "127.0.0.1",
    no_browser: bool = False,
    password:   str | None = None,
    no_record:  bool = False,
    no_pin:     bool = False,
    pin:        str | None = None,
    ai_provider: str = "",
    ai_model:    str = "",
    ai_api_key:  str = "",
    ai_base_url: str = "",
    ai_thinking: bool | None = None,
) -> None:
    """启动医考练习 Web 应用（支持多题库）。

    bank_paths 可以是单个路径字符串，也可以是路径列表。
    """
    from med_exam_toolkit.bank import load_bank
    # 让 Werkzeug 内置日志也显示真实 IP（nginx 反代场景）
    from werkzeug.middleware.proxy_fix import ProxyFix
    app.wsgi_app = ProxyFix(app.wsgi_app, x_for=1, x_proto=1, x_host=1)

    global _banks, _session_token, _asset_ver, \
           _server_port, _server_host, \
           _access_code, _cookie_secret, _pin_enabled, _pin_len, \
           _ai_client, _ai_model, _ai_provider, _ai_enable_thinking

    # 兼容旧的单路径传参
    if isinstance(bank_paths, (str, Path)):
        bank_paths = [bank_paths]

    _server_port    = port
    _server_host    = host
    _session_token  = secrets.token_hex(32)
    _asset_ver      = secrets.token_hex(8)
    _pin_enabled    = not no_pin or bool(pin)

    # ── AI 答疑初始化 ──
    _ai_client = None
    _ai_model  = ""
    _ai_provider = ""
    _ai_enable_thinking = ai_thinking
    if ai_provider and ai_api_key:
        try:
            from med_exam_toolkit.ai.client import make_client, default_model
            _ai_provider = ai_provider
            _ai_model    = ai_model or default_model(ai_provider)
            _ai_client   = make_client(
                provider=ai_provider,
                api_key=ai_api_key,
                base_url=ai_base_url,
                model=_ai_model,
            )
            print(f"[INFO] AI 答疑已启用: provider={ai_provider}  model={_ai_model}")
        except Exception as e:
            print(f"[WARN] AI 答疑初始化失败: {e}")
            _ai_client = None

    from med_exam_toolkit.auth import generate_access_code, derive_secret
    if pin:
        _access_code = pin.strip().upper()
        _pin_len = 0
        _pin_enabled = True
    elif _pin_enabled:
        _access_code, _ = generate_access_code()
        _pin_len = 8
    else:
        _access_code = ""
        _pin_len = 0
    # cookie secret 从访问码确定性派生，服务重启后 cookie 仍然有效
    _cookie_secret = derive_secret(_access_code) if _access_code else ""

    # ── 加载每个题库 ──────────────────────────────────────────────
    _banks = []
    for bp_str in bank_paths:
        bp = Path(bp_str).resolve()
        print(f"[INFO] 加载题库: {bp}")
        questions = load_bank(bp, password)
        sq_total = sum(len(q.sub_questions) for q in questions)
        print(f"[INFO]   共 {len(questions)} 大题 / {sq_total} 小题")

        db_path = None
        record_enabled = not no_record
        if record_enabled:
            from med_exam_toolkit.progress import db_path_for_bank, init_db
            db_path = db_path_for_bank(bp)
            init_db(db_path)
            print(f"[INFO]   进度数据库: {db_path}")
        else:
            print(f"[INFO]   学习记录已关闭（--no-record）")

        _banks.append(BankState(
            bank_path=bp,
            password=password,
            questions=questions,
            db_path=db_path,
            record_enabled=record_enabled,
        ))

    # ── 打印启动信息 ──────────────────────────────────────────────
    local_url = f"http://127.0.0.1:{port}"
    print(f"[INFO] 本机访问: {local_url}")
    if host == "0.0.0.0":
        lan_ip  = _get_lan_ip()
        lan_url = f"http://{lan_ip}:{port}"
        print(f"[INFO] 局域网访问: {lan_url}")
        from med_exam_toolkit.auth import print_public_internet_warning
        print_public_internet_warning(port)

    if _pin_enabled:
        if pin:
            display = _access_code
            label = "访问码（自定义）"
        else:
            display = _access_code[:4] + " " + _access_code[4:]
            label = "访问码"
        print(f"\n{'━'*40}")
        print(f"  🔑 {label}：  {display}")
        print(f"  首次打开浏览器时需要输入此码")
        if not pin:
            print(f"  重启服务后访问码会自动更新")
        print(f"{'━'*40}\n")
    else:
        print("\n  ⚠️  访问码验证已关闭（--no-pin），任何人可直接访问\n")

    print("[INFO] Ctrl+C 退出")

    if not no_browser:
        threading.Timer(0.9, lambda: webbrowser.open(local_url)).start()

    # 初始化 Web Push
    global _push_store, _vapid_keys
    try:
        from med_exam_toolkit.push import PushStore, generate_vapid_keys, start_daily_push_scheduler
        _push_store = PushStore()
        _vapid_keys = generate_vapid_keys()
        start_daily_push_scheduler(_vapid_keys, _push_store)
    except Exception as _e:
        import logging
        logging.getLogger(__name__).warning("[push] 初始化失败: %s", _e)

    app.run(host=host, port=port, debug=False, use_reloader=False, threaded=True)


if __name__ == "__main__":
    import argparse
    p = argparse.ArgumentParser(description="医考练习 Web 应用")
    p.add_argument("--bank", required=True, action="append", dest="banks")
    p.add_argument("--password",   default=None)
    p.add_argument("--port",       default=5174, type=int)
    p.add_argument("--no-browser", action="store_true")
    a = p.parse_args()
    start_quiz(a.banks, port=a.port, no_browser=a.no_browser, password=a.password)
