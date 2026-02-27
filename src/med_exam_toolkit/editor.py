"""本地题库编辑器 Web 服务"""
from __future__ import annotations

import copy
import json
import threading
import webbrowser
from pathlib import Path
from typing import Any

from flask import Flask, jsonify, request, render_template_string

# ── 全局状态 ──
_questions: list = []
_bank_path: Path | None = None
_dirty    = False
_password: str | None = None

app = Flask(__name__)
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
    return render_template_string(HTML)


# ═══════════════════════════════════════════════════════════════════
# HTML（单页应用）
# ═══════════════════════════════════════════════════════════════════

HTML = r"""<!DOCTYPE html>
<html lang="zh-CN" data-theme="dark">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1,maximum-scale=1">
<title>题库编辑器</title>
<style>
/* ══════════════════════════════════════════
   主题变量
══════════════════════════════════════════ */

/* 夜间主题（默认） */
:root,[data-theme="dark"]{
  --bg:        #0f1117;
  --surface:   #181c27;
  --panel:     #1e2333;
  --border:    #2a3045;
  --accent:    #4f9cf9;
  --accent2:   #f0a04b;
  --ai-color:  #f0a04b;
  --ok:        #3ecf8e;
  --danger:    #f56565;
  --text:      #e2e8f0;
  --muted:     #6b7a99;
  --fp-color:  #3a4a66;
  --active-bg: #1a2540;
  --input-bg:  #0f1117;
  --scrollbar: #2a3045;
}

/* 白天主题 */
[data-theme="light"]{
  --bg:        #f5f6fa;
  --surface:   #ffffff;
  --panel:     #f0f2f8;
  --border:    #d8dce8;
  --accent:    #2563eb;
  --accent2:   #d97706;
  --ai-color:  #d97706;
  --ok:        #16a34a;
  --danger:    #dc2626;
  --text:      #1e2535;
  --muted:     #6b7a99;
  --fp-color:  #a0aec0;
  --active-bg: #dbeafe;
  --input-bg:  #ffffff;
  --scrollbar: #d8dce8;
}

/* 暖调主题 */
[data-theme="warm"]{
  --bg:        #1a1510;
  --surface:   #231d16;
  --panel:     #2c2419;
  --border:    #3d3025;
  --accent:    #f59e0b;
  --accent2:   #fb923c;
  --ai-color:  #fb923c;
  --ok:        #4ade80;
  --danger:    #f87171;
  --text:      #f5e6d0;
  --muted:     #9a8070;
  --fp-color:  #5a4a38;
  --active-bg: #3d2e1a;
  --input-bg:  #1a1510;
  --scrollbar: #3d3025;
}

/* 绿色终端主题 */
[data-theme="terminal"]{
  --bg:        #0a0f0a;
  --surface:   #0d130d;
  --panel:     #111811;
  --border:    #1a2e1a;
  --accent:    #22c55e;
  --accent2:   #86efac;
  --ai-color:  #86efac;
  --ok:        #4ade80;
  --danger:    #f87171;
  --text:      #bbf7d0;
  --muted:     #4a7a4a;
  --fp-color:  #2a4a2a;
  --active-bg: #0f2a0f;
  --input-bg:  #0a0f0a;
  --scrollbar: #1a2e1a;
}

/* ══════════════════════════════════════════
   全局基础
══════════════════════════════════════════ */
*{box-sizing:border-box;margin:0;padding:0}
html,body{height:100%;overflow:hidden}
body{
  background:var(--bg);color:var(--text);
  font-family:'PingFang SC','Hiragino Sans GB','Microsoft YaHei',sans-serif;
  font-size:14px;display:flex;flex-direction:column;
  transition:background .2s,color .2s;
}

/* ── 顶栏 ── */
#topbar{
  display:flex;align-items:center;gap:8px;
  padding:0 16px;height:52px;
  background:var(--surface);border-bottom:1px solid var(--border);
  flex-shrink:0;min-width:0;
  transition:background .2s,border-color .2s;
}
.logo{font-size:12px;font-family:ui-monospace,'SF Mono',monospace;color:var(--accent);letter-spacing:.05em;white-space:nowrap;flex-shrink:0}
.bank-name{font-size:11px;color:var(--muted);font-family:ui-monospace,monospace;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;flex:1;min-width:0}
.top-stats{font-size:11px;color:var(--muted);font-family:ui-monospace,monospace;white-space:nowrap;flex-shrink:0}
.dirty-dot{width:7px;height:7px;border-radius:50%;background:var(--accent2);display:none;flex-shrink:0}
.dirty-dot.show{display:block}
.top-btn{
  padding:5px 12px;border-radius:6px;cursor:pointer;
  font-family:ui-monospace,monospace;font-size:12px;font-weight:600;
  border:none;white-space:nowrap;flex-shrink:0;transition:opacity .15s;
}
.top-btn:hover{opacity:.85}
#btnSave{background:var(--accent);color:#fff}
#btnSave:disabled{opacity:.35;cursor:default}
#btnReplace{background:var(--panel);color:var(--text);border:1px solid var(--border)}
#btnReplace:hover{border-color:var(--accent)}

/* 主题切换按钮 */
#btnTheme{
  background:var(--panel);color:var(--muted);
  border:1px solid var(--border);
  padding:5px 10px;border-radius:6px;cursor:pointer;
  font-size:14px;flex-shrink:0;line-height:1;
  transition:border-color .15s,color .15s;
}
#btnTheme:hover{border-color:var(--accent);color:var(--accent)}

/* 主题选择下拉 */
#themeMenu{
  position:absolute;top:calc(52px + 6px);right:16px;
  background:var(--surface);border:1px solid var(--border);
  border-radius:10px;padding:6px;
  display:none;flex-direction:column;gap:3px;
  z-index:300;box-shadow:0 8px 32px rgba(0,0,0,.4);
  min-width:150px;
}
#themeMenu.open{display:flex}
.theme-opt{
  display:flex;align-items:center;gap:10px;
  padding:8px 12px;border-radius:7px;cursor:pointer;
  font-size:13px;transition:background .1s;white-space:nowrap;
}
.theme-opt:hover{background:var(--panel)}
.theme-opt.active{background:var(--panel);color:var(--accent)}
.theme-swatch{
  width:28px;height:16px;border-radius:4px;flex-shrink:0;
  border:1px solid rgba(255,255,255,.1);
}

/* ── 主布局 ── */
#layout{display:flex;flex:1;min-height:0;height:calc(100vh - 52px);overflow:hidden}

/* ── 左栏 ── */
#sidebar{
  width:320px;flex-shrink:0;display:flex;flex-direction:column;
  border-right:1px solid var(--border);background:var(--surface);
  overflow:hidden;min-height:0;
  transition:background .2s,border-color .2s;
}
#search-area{padding:10px 12px;display:flex;flex-direction:column;gap:7px;border-bottom:1px solid var(--border);flex-shrink:0}
#search-area input,#search-area select{
  background:var(--panel);border:1px solid var(--border);color:var(--text);
  border-radius:6px;padding:7px 10px;font-size:12px;
  font-family:ui-monospace,monospace;width:100%;outline:none;
  transition:background .2s,border-color .15s,color .2s;
}
#search-area input:focus,#search-area select:focus{border-color:var(--accent)}
.filter-row{display:flex;gap:6px}
.filter-row select{flex:1;min-width:0}
.filter-checks{display:flex;gap:14px;flex-wrap:wrap}
.filter-checks label{display:flex;align-items:center;gap:5px;font-family:ui-monospace,monospace;font-size:11px;color:var(--muted);cursor:pointer}
#result-count{font-size:11px;color:var(--muted);font-family:ui-monospace,monospace;padding:5px 12px;border-bottom:1px solid var(--border);flex-shrink:0}
#qlist{flex:1;overflow-y:auto;min-height:0}
.q-item{padding:10px 14px;border-bottom:1px solid var(--border);cursor:pointer;transition:background .1s}
.q-item:hover{background:var(--panel)}
.q-item.active{background:var(--active-bg);border-left:3px solid var(--accent)}
.q-idx{font-family:ui-monospace,monospace;font-size:10px;color:var(--muted)}
.q-mode{font-size:10px;color:var(--accent);font-family:ui-monospace,monospace}
.q-fp{font-family:ui-monospace,monospace;font-size:9px;color:var(--fp-color);margin-left:6px}
.q-text{font-size:12px;margin-top:3px;line-height:1.5;overflow:hidden;display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical}
.q-badges{display:flex;gap:4px;margin-top:4px;flex-wrap:wrap}
.badge{font-size:10px;padding:1px 6px;border-radius:3px;font-family:ui-monospace,monospace}
.badge.ai{background:color-mix(in srgb,var(--ai-color) 15%,transparent);color:var(--ai-color)}
.badge.miss{background:color-mix(in srgb,var(--danger) 15%,transparent);color:var(--danger)}
#pagination{display:flex;align-items:center;justify-content:space-between;padding:6px 10px;border-top:1px solid var(--border);font-family:ui-monospace,monospace;font-size:11px;color:var(--muted);flex-shrink:0;gap:6px}
#pagination button{background:var(--panel);border:1px solid var(--border);color:var(--text);border-radius:4px;padding:3px 10px;cursor:pointer;font-family:ui-monospace,monospace;font-size:11px}
#pagination button:disabled{opacity:.35;cursor:default}
#jumpInput{width:42px;background:var(--panel);border:1px solid var(--border);color:var(--text);border-radius:4px;padding:3px 6px;font-family:ui-monospace,monospace;font-size:11px;text-align:center;outline:none}
#jumpInput:focus{border-color:var(--accent)}

/* ── 移动端：返回按钮 ── */
#mobileBack{display:none;align-items:center;gap:6px;padding:8px 14px;border-bottom:1px solid var(--border);font-family:ui-monospace,monospace;font-size:12px;color:var(--accent);cursor:pointer;background:var(--surface);flex-shrink:0}

/* ── 右栏 ── */
#editor{
  flex:1;min-width:0;min-height:0;
  overflow-y:auto;overflow-x:hidden;
  padding:20px 24px 32px;
  display:flex;flex-direction:column;gap:16px;
  transition:background .2s;
}
.empty{color:var(--muted);font-family:ui-monospace,monospace;text-align:center;margin-top:80px;font-size:13px}

/* ── 编辑区块 ── */
.section{background:var(--panel);border:1px solid var(--border);border-radius:10px;overflow:hidden;flex-shrink:0;transition:background .2s,border-color .2s}
.section-header{display:flex;align-items:center;justify-content:space-between;padding:9px 14px;border-bottom:1px solid var(--border);background:var(--surface);transition:background .2s}
.sec-title{font-size:10px;font-family:ui-monospace,monospace;color:var(--muted);text-transform:uppercase;letter-spacing:.08em}
.sec-fp{font-size:9px;font-family:ui-monospace,monospace;color:var(--fp-color);letter-spacing:.04em;cursor:pointer;user-select:all;padding:2px 6px;border-radius:3px;transition:background .15s,color .15s}
.sec-fp:hover{background:var(--border);color:var(--muted)}
.section-body{padding:14px}
.meta-grid{display:grid;grid-template-columns:1fr 1fr 1fr;gap:10px}
.field-group{display:flex;flex-direction:column;gap:4px}
.field-label{font-size:10px;font-family:ui-monospace,monospace;color:var(--muted);text-transform:uppercase;letter-spacing:.06em}
.field-input{
  background:var(--input-bg);border:1px solid var(--border);color:var(--text);
  border-radius:6px;padding:7px 10px;font-size:13px;
  font-family:'PingFang SC','Microsoft YaHei',sans-serif;
  outline:none;width:100%;
  transition:background .2s,border-color .15s,color .2s;
}
.field-input:focus{border-color:var(--accent)}
textarea.field-input{resize:vertical;min-height:72px;line-height:1.65}
textarea.field-input.tall{min-height:110px}
.options-list{display:flex;flex-direction:column;gap:6px}
.option-row{display:flex;align-items:center;gap:8px}
.option-label{font-family:ui-monospace,monospace;font-size:12px;color:var(--accent);width:20px;flex-shrink:0;text-align:center}
.option-input{flex:1;min-width:0;background:var(--input-bg);border:1px solid var(--border);color:var(--text);border-radius:6px;padding:6px 10px;font-size:13px;outline:none;transition:background .2s,border-color .15s}
.option-input:focus{border-color:var(--accent)}
.btn-icon{background:none;border:none;color:var(--muted);cursor:pointer;font-size:14px;padding:2px 5px;border-radius:3px}
.btn-icon:hover{color:var(--danger)}
.answer-row{display:grid;grid-template-columns:1fr 1fr;gap:12px;align-items:start}
.ai-badge{font-size:10px;font-family:ui-monospace,monospace;color:var(--ai-color);background:color-mix(in srgb,var(--ai-color) 15%,transparent);padding:2px 6px;border-radius:3px;margin-left:5px}
.action-bar{display:flex;gap:10px;align-items:center;flex-wrap:wrap}
.btn{padding:7px 16px;border-radius:6px;cursor:pointer;font-family:ui-monospace,monospace;font-size:12px;font-weight:600;border:none;transition:opacity .15s}
.btn:hover{opacity:.85}
.btn-primary{background:var(--accent);color:#fff}
.btn-danger{background:var(--danger);color:#fff}
.btn-ghost{background:var(--panel);color:var(--text);border:1px solid var(--border)}
.btn-ghost:hover{border-color:var(--accent);opacity:1}
.sub-tabs{display:flex;gap:4px;flex-wrap:wrap}
.sub-tab{padding:4px 12px;border-radius:5px;cursor:pointer;font-family:ui-monospace,monospace;font-size:11px;background:var(--panel);border:1px solid var(--border);color:var(--muted)}
.sub-tab.active{background:var(--accent);border-color:var(--accent);color:#fff}

/* ── 弹窗 ── */
.modal-backdrop{position:fixed;inset:0;background:rgba(0,0,0,.65);z-index:100;display:flex;align-items:center;justify-content:center;backdrop-filter:blur(2px);padding:16px}
.modal{background:var(--surface);border:1px solid var(--border);border-radius:12px;width:100%;max-width:520px;max-height:90vh;overflow-y:auto;padding:22px;display:flex;flex-direction:column;gap:14px;box-shadow:0 20px 60px rgba(0,0,0,.4);transition:background .2s}
.modal h3{font-size:15px;color:var(--text)}
.modal-btns{display:flex;gap:10px;justify-content:flex-end;flex-wrap:wrap}
.checkbox-group{display:flex;gap:10px;flex-wrap:wrap}
.checkbox-group label{display:flex;align-items:center;gap:4px;font-family:ui-monospace,monospace;font-size:12px;color:var(--muted);cursor:pointer}

/* ── Toast ── */
#toast{position:fixed;bottom:20px;right:20px;left:20px;max-width:340px;margin:0 auto;background:var(--surface);border:1px solid var(--border);border-radius:8px;padding:10px 16px;font-family:ui-monospace,monospace;font-size:12px;color:var(--text);z-index:200;opacity:0;transform:translateY(8px);transition:all .25s;pointer-events:none;text-align:center}
#toast.show{opacity:1;transform:translateY(0)}
#toast.err{border-color:var(--danger);color:var(--danger)}

/* ── 滚动条 ── */
::-webkit-scrollbar{width:4px;height:4px}
::-webkit-scrollbar-track{background:transparent}
::-webkit-scrollbar-thumb{background:var(--scrollbar);border-radius:3px}

/* ══════════════════════════════════════════
   移动端响应式  ≤ 768px
══════════════════════════════════════════ */
@media (max-width: 768px) {
  #topbar { padding:0 12px; gap:6px; }
  .top-stats { display:none; }
  .logo { font-size:11px; }
  #layout { position:relative; height:calc(100dvh - 52px); }
  #sidebar { position:absolute;inset:0;width:100%;border-right:none;z-index:10;transition:transform .25s ease }
  #editor  { position:absolute;inset:0;padding:14px 16px 24px;transform:translateX(100%);transition:transform .25s ease;background:var(--bg);overflow-y:auto;-webkit-overflow-scrolling:touch }
  body.editor-open #sidebar { transform:translateX(-100%); }
  body.editor-open #editor  { transform:translateX(0); }
  #mobileBack { display:flex; }
  .meta-grid  { grid-template-columns:1fr; }
  .answer-row { grid-template-columns:1fr; }
  .modal { padding:16px; }
  .modal-btns { justify-content:stretch; }
  .modal-btns .btn { flex:1; }
  #themeMenu { right:12px; }
}
</style>
</head>
<body>

<div id="topbar">
  <span class="logo">◈ 题库编辑器</span>
  <span class="bank-name" id="bankName">加载中…</span>
  <span class="top-stats" id="topStats"></span>
  <div class="dirty-dot" id="dirtyDot" title="有未保存的改动"></div>
  <button class="top-btn" id="btnReplace" onclick="openReplace()">批量替换</button>
  <button class="top-btn" id="btnSave" onclick="doSave()">保存</button>
  <button id="btnTheme" onclick="toggleThemeMenu()" title="切换主题">🎨</button>
</div>

<!-- 主题菜单 -->
<div id="themeMenu">
  <div class="theme-opt" data-t="dark">
    <span class="theme-swatch" style="background:linear-gradient(135deg,#0f1117 50%,#4f9cf9 50%)"></span>
    夜间
  </div>
  <div class="theme-opt" data-t="light">
    <span class="theme-swatch" style="background:linear-gradient(135deg,#f5f6fa 50%,#2563eb 50%)"></span>
    日间
  </div>
  <div class="theme-opt" data-t="warm">
    <span class="theme-swatch" style="background:linear-gradient(135deg,#1a1510 50%,#f59e0b 50%)"></span>
    暖调
  </div>
  <div class="theme-opt" data-t="terminal">
    <span class="theme-swatch" style="background:linear-gradient(135deg,#0a0f0a 50%,#22c55e 50%)"></span>
    终端
  </div>
</div>

<div id="layout">
  <!-- 左栏 -->
  <div id="sidebar">
    <div id="search-area">
      <input id="searchInput" placeholder="搜索题目文字、解析…" oninput="debounceSearch()">
      <input id="fpInput" placeholder="指纹搜索（前缀或完整MD5）" oninput="debounceSearch()"
             style="font-family:ui-monospace,monospace;font-size:11px">
      <div class="filter-row">
        <select id="filterMode" onchange="loadList()"><option value="">全部题型</option></select>
        <select id="filterUnit" onchange="loadList()"><option value="">全部章节</option></select>
      </div>
      <div class="filter-checks">
        <label><input type="checkbox" id="filterAI" onchange="loadList()"> 含AI内容</label>
        <label><input type="checkbox" id="filterMissing" onchange="loadList()"> 缺答案/解析</label>
      </div>
    </div>
    <div id="result-count"></div>
    <div id="qlist"></div>
    <div id="pagination">
      <button id="btnPrev" onclick="prevPage()">‹</button>
      <span id="pageInfo"></span>
      <button id="btnNext" onclick="nextPage()">›</button>
      <span style="color:var(--border)">|</span>
      <span style="color:var(--muted)">跳转</span>
      <input id="jumpInput" type="number" min="1" placeholder="页"
             onkeydown="if(event.key==='Enter')jumpPage(this.value)">
    </div>
  </div>

  <!-- 右栏 -->
  <div id="editor">
    <div id="mobileBack" onclick="closeMobileEditor()">← 返回列表</div>
    <div class="empty" id="emptyHint">← 从左侧选择一道题目进行编辑</div>
  </div>
</div>

<!-- 批量替换弹窗 -->
<div class="modal-backdrop" id="replaceModal" style="display:none" onclick="if(event.target===this)closeReplace()">
  <div class="modal">
    <h3>批量替换</h3>
    <div class="field-group">
      <span class="field-label">查找文本</span>
      <input class="field-input" id="rFind" placeholder="输入要查找的文本">
    </div>
    <div class="field-group">
      <span class="field-label">替换为</span>
      <input class="field-input" id="rReplace" placeholder="替换后的文本（留空即删除）">
    </div>
    <div class="field-group">
      <span class="field-label">作用字段</span>
      <div class="checkbox-group">
        <label><input type="checkbox" value="text" checked> 题目文字</label>
        <label><input type="checkbox" value="discuss" checked> 解析</label>
        <label><input type="checkbox" value="answer"> 答案</label>
        <label><input type="checkbox" value="point"> 考点</label>
      </div>
    </div>
    <div class="field-group">
      <span class="field-label">范围限制（可选）</span>
      <div style="display:flex;gap:8px;flex-wrap:wrap">
        <select class="field-input" id="rMode" style="flex:1;min-width:120px"><option value="">全部题型</option></select>
        <select class="field-input" id="rUnit" style="flex:1;min-width:120px"><option value="">全部章节</option></select>
      </div>
    </div>
    <div class="modal-btns">
      <button class="btn btn-ghost" onclick="closeReplace()">取消</button>
      <button class="btn btn-primary" onclick="doReplace()">执行替换</button>
    </div>
  </div>
</div>

<!-- 删除确认弹窗 -->
<div class="modal-backdrop" id="deleteModal" style="display:none">
  <div class="modal">
    <h3>确认删除</h3>
    <p style="color:var(--muted);font-size:13px;line-height:1.6">删除后不可撤销（保存前可关闭页面放弃更改）。确定要删除这道题吗？</p>
    <div class="modal-btns">
      <button class="btn btn-ghost" onclick="closeDelete()">取消</button>
      <button class="btn btn-danger" onclick="confirmDelete()">删除</button>
    </div>
  </div>
</div>

<div id="toast"></div>

<script>
// ── 状态 ──
let state = { page: 1, perPage: 50, total: 0, pages: 0 };
let currentQi = null, currentSi = 0;
let deleteTarget = null;
let searchTimer = null;
let info = {};

// ══════════════════════════════════════════
// 主题管理
// ══════════════════════════════════════════
const THEMES = ['dark','light','warm','terminal'];
const THEME_LABELS = { dark:'夜间', light:'日间', warm:'暖调', terminal:'终端' };

function applyTheme(t) {
  document.documentElement.setAttribute('data-theme', t);
  localStorage.setItem('editor-theme', t);
  // 更新菜单高亮
  document.querySelectorAll('.theme-opt').forEach(el => {
    el.classList.toggle('active', el.dataset.t === t);
  });
}

function toggleThemeMenu() {
  const menu = document.getElementById('themeMenu');
  menu.classList.toggle('open');
}

function closeThemeMenu() {
  document.getElementById('themeMenu').classList.remove('open');
}

// 点击菜单外关闭
document.addEventListener('click', e => {
  if (!e.target.closest('#themeMenu') && !e.target.closest('#btnTheme')) {
    closeThemeMenu();
  }
});

// 点击主题选项
document.querySelectorAll('.theme-opt').forEach(el => {
  el.onclick = () => {
    applyTheme(el.dataset.t);
    closeThemeMenu();
  };
});

// 初始化时恢复上次主题
(function() {
  const saved = localStorage.getItem('editor-theme') || 'dark';
  applyTheme(saved);
})();

// ══════════════════════════════════════════
// 初始化
// ══════════════════════════════════════════
async function init() {
  info = await fetch('/api/info').then(r => r.json());
  document.getElementById('bankName').textContent = info.bank_path.split(/[\\\/]/).pop();
  updateTopStats();
  populateFilters();
  loadList();
}

function populateFilters() {
  ['filterMode','rMode'].forEach(id => {
    const sel = document.getElementById(id);
    sel.innerHTML = '<option value="">全部题型</option>';
    info.modes.forEach(m => sel.add(new Option(m, m)));
  });
  ['filterUnit','rUnit'].forEach(id => {
    const sel = document.getElementById(id);
    sel.innerHTML = '<option value="">全部章节</option>';
    info.units.forEach(u => sel.add(new Option(u, u)));
  });
}

function updateTopStats() {
  document.getElementById('topStats').textContent =
    `${info.total_q} 大题 / ${info.total_sq} 小题`;
  document.getElementById('dirtyDot').className = 'dirty-dot' + (info.dirty ? ' show' : '');
  document.getElementById('btnSave').disabled = !info.dirty;
}

function openMobileEditor()  { if (window.innerWidth <= 768) document.body.classList.add('editor-open'); }
function closeMobileEditor() { document.body.classList.remove('editor-open'); }

// ── 列表 ──
async function loadList() {
  const q       = document.getElementById('searchInput').value;
  const fp      = document.getElementById('fpInput').value.trim();
  const mode    = document.getElementById('filterMode').value;
  const unit    = document.getElementById('filterUnit').value;
  const hasAI   = document.getElementById('filterAI').checked ? '1' : '';
  const missing = document.getElementById('filterMissing').checked ? '1' : '';

  const params = new URLSearchParams({
    q, fp, mode, unit, has_ai: hasAI, missing, page: state.page, per_page: state.perPage
  });
  const data = await fetch('/api/questions?' + params).then(r => r.json());

  state.total = data.total;
  state.pages = data.pages;

  document.getElementById('result-count').textContent = `共 ${data.total} 个小题`;
  document.getElementById('pageInfo').textContent = `${state.page} / ${Math.max(1, state.pages)}`;
  document.getElementById('btnPrev').disabled = state.page <= 1;
  document.getElementById('btnNext').disabled = state.page >= state.pages;

  const list = document.getElementById('qlist');
  list.innerHTML = '';
  data.items.forEach(item => {
    const div = document.createElement('div');
    div.className = 'q-item' + (item.qi === currentQi && item.si === currentSi ? ' active' : '');
    div.onclick = () => selectQuestion(item.qi, item.si);

    const hasAIBadge = item.ai_discuss || item.ai_answer;
    const badges = [
      hasAIBadge                      ? `<span class="badge ai">AI</span>`     : '',
      item.answer_source  === 'ai'    ? `<span class="badge ai">AI答案</span>` : '',
      item.discuss_source === 'ai'    ? `<span class="badge ai">AI解析</span>` : '',
      (!item.answer || !item.discuss) ? `<span class="badge miss">缺${!item.answer?'答案':''}${!item.discuss?'解析':''}</span>` : '',
    ].filter(Boolean).join('');

    const fpShort = item.fingerprint ? item.fingerprint.slice(0, 8) : '';
    div.innerHTML = `
      <div style="display:flex;justify-content:space-between;align-items:baseline">
        <span class="q-idx">[${item.qi+1}-${item.si+1}]</span>
        <span><span class="q-mode">${esc(item.mode)}</span><span class="q-fp">${fpShort}</span></span>
      </div>
      <div class="q-text">${esc(item.text || item.stem || '（无题目文本）')}</div>
      <div class="q-badges">${badges}</div>`;
    list.appendChild(div);
  });
}

function debounceSearch() {
  clearTimeout(searchTimer);
  state.page = 1;
  searchTimer = setTimeout(loadList, 280);
}
function prevPage() { if (state.page > 1)           { state.page--; loadList(); } }
function nextPage() { if (state.page < state.pages) { state.page++; loadList(); } }
function jumpPage(v) {
  const p = Math.max(1, Math.min(state.pages, parseInt(v) || 1));
  state.page = p;
  document.getElementById('jumpInput').value = '';
  loadList();
}

// ── 编辑面板 ──
async function selectQuestion(qi, si) {
  currentQi = qi; currentSi = si;
  document.querySelectorAll('.q-item').forEach(el => {
    el.classList.toggle('active',
      el.querySelector('.q-idx')?.textContent === `[${qi+1}-${si+1}]`);
  });
  const data = await fetch(`/api/question/${qi}`).then(r => r.json());
  renderEditor(data, si);
  openMobileEditor();
}

function renderEditor(data, activeSi = 0) {
  const ed = document.getElementById('editor');
  ed.innerHTML = '';

  const back = document.createElement('div');
  back.id = 'mobileBack';
  back.textContent = '← 返回列表';
  back.onclick = closeMobileEditor;
  ed.appendChild(back);

  // 元数据
  const metaSection = mkSection('元数据', `
    <div class="meta-grid">
      <div class="field-group">
        <span class="field-label">题型</span>
        <input class="field-input" id="f_mode" value="${esc(data.mode)}">
      </div>
      <div class="field-group">
        <span class="field-label">章节</span>
        <input class="field-input" id="f_unit" value="${esc(data.unit)}">
      </div>
      <div class="field-group">
        <span class="field-label">分类</span>
        <input class="field-input" id="f_cls" value="${esc(data.cls)}">
      </div>
    </div>
    ${data.stem ? `
    <div class="field-group" style="margin-top:10px">
      <span class="field-label">共享题干</span>
      <textarea class="field-input tall" id="f_stem">${esc(data.stem)}</textarea>
    </div>` : ''}
  `);
  const fp0 = data.sub_questions[0]?.fingerprint || '';
  if (fp0) {
    const fpEl = document.createElement('span');
    fpEl.className = 'sec-fp';
    fpEl.title = '点击复制完整指纹：' + fp0;
    fpEl.textContent = fp0.slice(0, 12) + '…';
    fpEl.onclick = () => navigator.clipboard?.writeText(fp0).then(() => toast('已复制指纹'));
    metaSection.querySelector('.section-header').appendChild(fpEl);
  }
  ed.appendChild(metaSection);

  // 子题切换
  if (data.sub_questions.length > 1) {
    const tabsDiv = document.createElement('div');
    tabsDiv.className = 'section';
    tabsDiv.innerHTML = `<div class="section-body"><div class="sub-tabs">
      ${data.sub_questions.map((sq, i) => `
        <div class="sub-tab ${i === activeSi ? 'active' : ''}" onclick="selectSub(${data.qi},${i})">
          子题 ${i+1}${!sq.answer ? ' ❓' : ''}
        </div>`).join('')}
    </div></div>`;
    ed.appendChild(tabsDiv);
  }

  const sq = data.sub_questions[activeSi];
  currentSi = activeSi;

  ed.appendChild(mkSection('题目文字', `
    <textarea class="field-input tall" id="f_text">${esc(sq.text)}</textarea>
  `));

  const optHtml = sq.options.map((o, i) =>
    `<div class="option-row">
      <span class="option-label">${String.fromCharCode(65+i)}</span>
      <input class="option-input" value="${esc(o)}" data-oi="${i}">
      <button class="btn-icon" onclick="removeOption(${i})" title="删除">✕</button>
    </div>`
  ).join('');
  ed.appendChild(mkSection('选项', `
    <div class="options-list" id="optionsList">${optHtml}</div>
    <button class="btn btn-ghost" style="margin-top:10px;font-size:11px" onclick="addOption()">＋ 添加选项</button>
  `));

  const aiAnsBadge = sq.ai_answer  ? `<span class="ai-badge">AI: ${esc(sq.ai_answer)}</span>`  : '';
  const aiDisBadge = sq.ai_discuss ? `<span class="ai-badge">AI</span>` : '';
  ed.appendChild(mkSection('答案与解析', `
    <div class="answer-row">
      <div class="field-group">
        <span class="field-label">答案 ${aiAnsBadge}</span>
        <input class="field-input" id="f_answer" value="${esc(sq.answer)}"
               style="${sq.answer_source==='ai' ? 'border-color:var(--ai-color)' : ''}">
      </div>
      <div class="field-group">
        <span class="field-label">考点</span>
        <input class="field-input" id="f_point" value="${esc(sq.point)}">
      </div>
    </div>
    <div class="field-group" style="margin-top:12px">
      <span class="field-label">解析 ${aiDisBadge}</span>
      <textarea class="field-input tall" id="f_discuss"
        style="${sq.discuss_source==='ai' ? 'border-color:var(--ai-color)' : ''}">${esc(sq.discuss)}</textarea>
    </div>
    ${sq.ai_discuss ? `
    <div class="field-group" style="margin-top:10px">
      <span class="field-label" style="color:var(--ai-color)">AI 解析原文（只读参考）</span>
      <textarea class="field-input tall" readonly
        style="color:var(--ai-color);opacity:.7;cursor:default">${esc(sq.ai_discuss)}</textarea>
    </div>` : ''}
  `));

  const actDiv = document.createElement('div');
  actDiv.className = 'section';
  actDiv.innerHTML = `<div class="section-body"><div class="action-bar">
    <button class="btn btn-primary" onclick="saveSubQuestion()">保存此题</button>
    <button class="btn btn-danger"  onclick="openDelete(${data.qi})">删除整道题</button>
  </div></div>`;
  ed.appendChild(actDiv);
}

function mkSection(title, bodyHtml) {
  const div = document.createElement('div');
  div.className = 'section';
  div.innerHTML = `
    <div class="section-header"><span class="sec-title">${title}</span></div>
    <div class="section-body">${bodyHtml}</div>`;
  return div;
}

async function selectSub(qi, si) {
  const data = await fetch(`/api/question/${qi}`).then(r => r.json());
  renderEditor(data, si);
}

function getOptions() { return [...document.querySelectorAll('.option-input')].map(i => i.value); }
function addOption() {
  const list = document.getElementById('optionsList');
  const i = list.children.length;
  const row = document.createElement('div');
  row.className = 'option-row';
  row.innerHTML = `
    <span class="option-label">${String.fromCharCode(65+i)}</span>
    <input class="option-input" value="" data-oi="${i}">
    <button class="btn-icon" onclick="removeOption(${i})" title="删除">✕</button>`;
  list.appendChild(row);
}
function removeOption(idx) {
  document.getElementById('optionsList').children[idx]?.remove();
  [...document.getElementById('optionsList').children].forEach((row, i) => {
    row.querySelector('.option-label').textContent = String.fromCharCode(65+i);
    row.querySelector('.option-input').dataset.oi = i;
    row.querySelector('.btn-icon').setAttribute('onclick', `removeOption(${i})`);
  });
}

async function saveSubQuestion() {
  const payload = {
    mode:    document.getElementById('f_mode')?.value  ?? '',
    unit:    document.getElementById('f_unit')?.value  ?? '',
    cls:     document.getElementById('f_cls')?.value   ?? '',
    stem:    document.getElementById('f_stem')?.value  ?? '',
    text:    document.getElementById('f_text').value,
    answer:  document.getElementById('f_answer').value,
    discuss: document.getElementById('f_discuss').value,
    point:   document.getElementById('f_point').value,
    options: getOptions(),
  };
  const res = await fetch(`/api/subquestion/${currentQi}/${currentSi}`, {
    method: 'PUT', headers: {'Content-Type':'application/json'}, body: JSON.stringify(payload),
  }).then(r => r.json());
  if (res.ok) { info.dirty = true; updateTopStats(); loadList(); toast('✓ 已保存'); }
  else toast('保存失败: ' + (res.error || ''), true);
}

function openDelete(qi) { deleteTarget = qi; document.getElementById('deleteModal').style.display = 'flex'; }
function closeDelete()  { document.getElementById('deleteModal').style.display = 'none'; deleteTarget = null; }
async function confirmDelete() {
  if (deleteTarget === null) return;
  const res = await fetch(`/api/question/${deleteTarget}`, { method:'DELETE' }).then(r => r.json());
  closeDelete();
  if (res.ok) {
    currentQi = null; info.dirty = true; info.total_q = res.total;
    updateTopStats(); loadList(); closeMobileEditor();
    document.getElementById('editor').innerHTML =
      '<div id="mobileBack" onclick="closeMobileEditor()">← 返回列表</div>' +
      '<div class="empty">← 从左侧选择一道题目进行编辑</div>';
    toast(`已删除，剩余 ${res.total} 大题`);
  } else toast('删除失败', true);
}

function openReplace()  { document.getElementById('replaceModal').style.display = 'flex'; }
function closeReplace() { document.getElementById('replaceModal').style.display = 'none'; }
async function doReplace() {
  const find   = document.getElementById('rFind').value;
  const repl   = document.getElementById('rReplace').value;
  const fields = [...document.querySelectorAll('.checkbox-group input:checked')].map(c => c.value);
  const mode   = document.getElementById('rMode').value;
  const unit   = document.getElementById('rUnit').value;
  if (!find)          { toast('查找文本不能为空', true); return; }
  if (!fields.length) { toast('请至少选择一个作用字段', true); return; }
  const res = await fetch('/api/replace', {
    method:'POST', headers:{'Content-Type':'application/json'},
    body: JSON.stringify({ find, replace: repl, fields, mode, unit }),
  }).then(r => r.json());
  if (res.ok) { info.dirty = true; updateTopStats(); closeReplace(); loadList(); toast(`替换完成：共 ${res.replaced} 处`); }
  else toast('替换失败: ' + (res.error || ''), true);
}

async function doSave() {
  const res = await fetch('/api/save', {
    method:'POST', headers:{'Content-Type':'application/json'}, body: JSON.stringify({}),
  }).then(r => r.json());
  if (res.ok) { info.dirty = false; updateTopStats(); toast('✓ 已保存至 ' + res.path.split(/[\\\/]/).pop()); }
  else toast('保存失败: ' + (res.error || ''), true);
}

function esc(s) {
  return (s||'').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;')
                .replace(/"/g,'&quot;').replace(/'/g,'&#39;');
}
let toastTimer;
function toast(msg, err=false) {
  const t = document.getElementById('toast');
  t.textContent = msg;
  t.className = 'show' + (err ? ' err' : '');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => t.className = '', 3000);
}

document.addEventListener('keydown', e => {
  if ((e.ctrlKey || e.metaKey) && e.key === 's') { e.preventDefault(); if (info.dirty) doSave(); }
});

init();
</script>
</body>
</html>
"""



# ═══════════════════════════════════════════════════════════════════
# 启动入口
# ═══════════════════════════════════════════════════════════════════

def start_editor(bank_path: str, port: int = 5173, no_browser: bool = False,
                 password: str | None = None) -> None:
    from med_exam_toolkit.bank import load_bank

    global _questions, _bank_path, _password
    _bank_path = Path(bank_path).resolve()
    _password  = password
    print(f"[INFO] 加载题库: {_bank_path}")
    _questions = load_bank(_bank_path, password)
    print(f"[INFO] 已加载 {len(_questions)} 道大题")

    url = f"http://127.0.0.1:{port}"
    print(f"[INFO] 编辑器启动: {url}")
    print(f"[INFO] 按 Ctrl+C 退出")

    if not no_browser:
        threading.Timer(0.8, lambda: webbrowser.open(url)).start()

    app.run(host="127.0.0.1", port=port, debug=False, use_reloader=False)