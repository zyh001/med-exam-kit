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
_dirty = False          # 是否有未保存的改动

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
        "sub_total": len(q.sub_questions),
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
    q_kw   = request.args.get("q", "").strip()
    mode   = request.args.get("mode", "")
    unit   = request.args.get("unit", "")
    has_ai = request.args.get("has_ai", "") == "1"
    missing = request.args.get("missing", "") == "1"
    page   = max(1, int(request.args.get("page", 1)))
    per    = min(100, max(1, int(request.args.get("per_page", 50))))

    rows = []
    for qi, q in enumerate(_questions):
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
        pwd = request.get_json(silent=True) or {}
        save_bank(_questions, _bank_path, pwd.get("password"))
        _dirty = False
        return jsonify({"ok": True, "path": str(_bank_path)})
    except Exception as e:
        return jsonify({"error": str(e)}), 500


@app.post("/api/shutdown")
def api_shutdown():
    func = request.environ.get("werkzeug.server.shutdown")
    if func:
        func()
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
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1,maximum-scale=1">
<title>题库编辑器</title>
<style>
:root {
  --bg:       #0f1117;
  --surface:  #181c27;
  --panel:    #1e2333;
  --border:   #2a3045;
  --accent:   #4f9cf9;
  --accent2:  #f0a04b;
  --ai-color: #f0a04b;
  --ok:       #3ecf8e;
  --danger:   #f56565;
  --text:     #e2e8f0;
  --muted:    #6b7a99;
  --mono:     ui-monospace, 'Cascadia Code', 'SF Mono', monospace;
  --serif:    'PingFang SC', 'Hiragino Sans GB', 'Microsoft YaHei', sans-serif;
  --sidebar-w: 320px;
  --topbar-h:  52px;
}
*{box-sizing:border-box;margin:0;padding:0}
html,body{height:100%;overflow:hidden}
body{
  background:var(--bg);color:var(--text);font-family:var(--serif);font-size:14px;
  display:flex;flex-direction:column;min-height:100vh;
}

/* ── 顶栏 ── */
#topbar{
  display:flex;align-items:center;gap:8px;
  padding:0 16px;height:var(--topbar-h);
  background:var(--surface);border-bottom:1px solid var(--border);
  flex-shrink:0;min-width:0;
}
.logo{font-size:12px;font-family:var(--mono);color:var(--accent);letter-spacing:.05em;white-space:nowrap;flex-shrink:0}
.bank-name{font-size:11px;color:var(--muted);font-family:var(--mono);
  overflow:hidden;text-overflow:ellipsis;white-space:nowrap;flex:1;min-width:0}
.top-stats{font-size:11px;color:var(--muted);font-family:var(--mono);white-space:nowrap;flex-shrink:0}
.dirty-dot{width:7px;height:7px;border-radius:50%;background:var(--accent2);display:none;flex-shrink:0}
.dirty-dot.show{display:block}
.top-btn{padding:5px 12px;border-radius:6px;cursor:pointer;font-family:var(--mono);
  font-size:12px;font-weight:600;border:none;white-space:nowrap;flex-shrink:0;transition:opacity .15s}
.top-btn:hover{opacity:.85}
#btnSave{background:var(--accent);color:#fff}
#btnSave:disabled{opacity:.35;cursor:default}
#btnReplace{background:var(--panel);color:var(--text);border:1px solid var(--border)}
#btnReplace:hover{border-color:var(--accent)}

/* ── 主布局 ── */
#layout{
  display:flex;
  flex:1;
  min-height:0;
  height:calc(100vh - var(--topbar-h));
  overflow:hidden;
}

/* ── 左栏 ── */
#sidebar{
  width:var(--sidebar-w);flex-shrink:0;
  display:flex;flex-direction:column;
  border-right:1px solid var(--border);
  background:var(--surface);
  overflow:hidden;
  min-height:0;
}
#search-area{
  padding:10px 12px;display:flex;flex-direction:column;gap:7px;
  border-bottom:1px solid var(--border);flex-shrink:0;
}
#search-area input,#search-area select{
  background:var(--panel);border:1px solid var(--border);color:var(--text);
  border-radius:6px;padding:7px 10px;font-size:12px;font-family:var(--mono);
  width:100%;outline:none;
}
#search-area input:focus,#search-area select:focus{border-color:var(--accent)}
.filter-row{display:flex;gap:6px}
.filter-row select{flex:1;min-width:0}
.filter-checks{display:flex;gap:14px;flex-wrap:wrap}
.filter-checks label{
  display:flex;align-items:center;gap:5px;
  font-family:var(--mono);font-size:11px;color:var(--muted);cursor:pointer;
}
#result-count{
  font-size:11px;color:var(--muted);font-family:var(--mono);
  padding:5px 12px;border-bottom:1px solid var(--border);flex-shrink:0;
}
#qlist{flex:1;overflow-y:auto;min-height:0}
.q-item{
  padding:10px 14px;border-bottom:1px solid var(--border);
  cursor:pointer;transition:background .1s;
}
.q-item:hover{background:var(--panel)}
.q-item.active{background:#1a2540;border-left:3px solid var(--accent)}
.q-idx{font-family:var(--mono);font-size:10px;color:var(--muted)}
.q-mode{font-size:10px;color:var(--accent);font-family:var(--mono)}
.q-text{
  font-size:12px;margin-top:3px;line-height:1.5;
  overflow:hidden;display:-webkit-box;
  -webkit-line-clamp:2;-webkit-box-orient:vertical;
}
.q-badges{display:flex;gap:4px;margin-top:4px;flex-wrap:wrap}
.badge{font-size:10px;padding:1px 6px;border-radius:3px;font-family:var(--mono)}
.badge.ai{background:#2d2010;color:var(--ai-color)}
.badge.miss{background:#2d1010;color:var(--danger)}
#pagination{
  display:flex;align-items:center;justify-content:space-between;
  padding:8px 12px;border-top:1px solid var(--border);
  font-family:var(--mono);font-size:11px;color:var(--muted);flex-shrink:0;
}
#pagination button{
  background:var(--panel);border:1px solid var(--border);color:var(--text);
  border-radius:4px;padding:3px 10px;cursor:pointer;font-family:var(--mono);font-size:11px;
}
#pagination button:disabled{opacity:.35;cursor:default}

/* ── 移动端：返回按钮 ── */
#mobileBack{
  display:none;align-items:center;gap:6px;
  padding:8px 14px;border-bottom:1px solid var(--border);
  font-family:var(--mono);font-size:12px;color:var(--accent);
  cursor:pointer;background:var(--surface);flex-shrink:0;
}

/* ── 右栏 ── */
#editor{
  flex:1;
  min-width:0;
  min-height:0;
  overflow-y:auto;
  overflow-x:hidden;
  padding:20px 24px 28px;
  display:flex;flex-direction:column;gap:16px;
}
.empty{color:var(--muted);font-family:var(--mono);text-align:center;margin-top:80px;font-size:13px}

/* ── 编辑区块 ── */
.section{background:var(--panel);border:1px solid var(--border);border-radius:10px;overflow:hidden}
.section-header{
  display:flex;align-items:center;justify-content:space-between;
  padding:9px 14px;border-bottom:1px solid var(--border);background:var(--surface);
}
.sec-title{font-size:10px;font-family:var(--mono);color:var(--muted);text-transform:uppercase;letter-spacing:.08em}
.section-body{padding:14px}

.meta-grid{display:grid;grid-template-columns:1fr 1fr 1fr;gap:10px}
.field-group{display:flex;flex-direction:column;gap:4px}
.field-label{font-size:10px;font-family:var(--mono);color:var(--muted);text-transform:uppercase;letter-spacing:.06em}
.field-input{
  background:var(--bg);border:1px solid var(--border);color:var(--text);
  border-radius:6px;padding:7px 10px;font-size:13px;font-family:var(--serif);
  outline:none;width:100%;transition:border-color .15s;
}
.field-input:focus{border-color:var(--accent)}
textarea.field-input{resize:vertical;min-height:72px;line-height:1.65}
textarea.field-input.tall{min-height:110px}

.options-list{display:flex;flex-direction:column;gap:6px}
.option-row{display:flex;align-items:center;gap:8px}
.option-label{font-family:var(--mono);font-size:12px;color:var(--accent);width:20px;flex-shrink:0;text-align:center}
.option-input{
  flex:1;min-width:0;background:var(--bg);border:1px solid var(--border);
  color:var(--text);border-radius:6px;padding:6px 10px;font-size:13px;outline:none;
}
.option-input:focus{border-color:var(--accent)}
.btn-icon{background:none;border:none;color:var(--muted);cursor:pointer;font-size:14px;padding:2px 5px;border-radius:3px}
.btn-icon:hover{color:var(--danger)}

.answer-row{display:grid;grid-template-columns:1fr 1fr;gap:12px;align-items:start}
.ai-badge{font-size:10px;font-family:var(--mono);color:var(--ai-color);background:#2d2010;padding:2px 6px;border-radius:3px;margin-left:5px}

.action-bar{display:flex;gap:10px;align-items:center;flex-wrap:wrap}
.btn{padding:7px 16px;border-radius:6px;cursor:pointer;font-family:var(--mono);font-size:12px;font-weight:600;border:none;transition:opacity .15s}
.btn:hover{opacity:.85}
.btn-primary{background:var(--accent);color:#fff}
.btn-danger{background:var(--danger);color:#fff}
.btn-ghost{background:var(--panel);color:var(--text);border:1px solid var(--border)}
.btn-ghost:hover{border-color:var(--accent);opacity:1}

.sub-tabs{display:flex;gap:4px;flex-wrap:wrap}
.sub-tab{padding:4px 12px;border-radius:5px;cursor:pointer;font-family:var(--mono);font-size:11px;background:var(--panel);border:1px solid var(--border);color:var(--muted)}
.sub-tab.active{background:var(--accent);border-color:var(--accent);color:#fff}

/* ── 弹窗 ── */
.modal-backdrop{
  position:fixed;inset:0;background:rgba(0,0,0,.65);z-index:100;
  display:flex;align-items:center;justify-content:center;
  backdrop-filter:blur(2px);padding:16px;
}
.modal{
  background:var(--surface);border:1px solid var(--border);border-radius:12px;
  width:100%;max-width:520px;max-height:90vh;overflow-y:auto;
  padding:22px;display:flex;flex-direction:column;gap:14px;
  box-shadow:0 20px 60px rgba(0,0,0,.5);
}
.modal h3{font-size:15px;color:var(--text)}
.modal-btns{display:flex;gap:10px;justify-content:flex-end;flex-wrap:wrap}
.checkbox-group{display:flex;gap:10px;flex-wrap:wrap}
.checkbox-group label{display:flex;align-items:center;gap:4px;font-family:var(--mono);font-size:12px;color:var(--muted);cursor:pointer}

/* ── Toast ── */
#toast{
  position:fixed;bottom:20px;right:20px;left:20px;max-width:340px;margin:0 auto;
  background:var(--surface);border:1px solid var(--border);border-radius:8px;
  padding:10px 16px;font-family:var(--mono);font-size:12px;color:var(--text);
  z-index:200;opacity:0;transform:translateY(8px);transition:all .25s;pointer-events:none;
  text-align:center;
}
#toast.show{opacity:1;transform:translateY(0)}
#toast.err{border-color:var(--danger);color:var(--danger)}

/* ── 滚动条 ── */
::-webkit-scrollbar{width:4px;height:4px}
::-webkit-scrollbar-track{background:transparent}
::-webkit-scrollbar-thumb{background:var(--border);border-radius:3px}

/* ══════════════════════════════════════════
   移动端响应式  ≤ 768px
══════════════════════════════════════════ */
@media (max-width: 768px) {
  :root { --sidebar-w: 100%; }

  #topbar { padding:0 12px; gap:6px; }
  .top-stats { display:none; }
  .logo { font-size:11px; }

  #layout {
    position:relative;
    height:calc(100dvh - var(--topbar-h));
  }

  #sidebar {
    position:absolute;inset:0;
    width:100%;border-right:none;
    z-index:10;
    transition:transform .25s ease;
    min-height:0;
  }
  #editor {
    position:absolute;inset:0;
    padding:14px 16px 24px;
    transform:translateX(100%);
    transition:transform .25s ease;
    background:var(--bg);
    min-height:0;
    overflow-y:auto;
    -webkit-overflow-scrolling:touch;
  }

  body.editor-open #sidebar { transform:translateX(-100%); }
  body.editor-open #editor  { transform:translateX(0); }

  #mobileBack { display:flex; }

  .meta-grid { grid-template-columns:1fr; }
  .answer-row { grid-template-columns:1fr; }

  .modal { padding:16px; }
  .modal-btns { justify-content:stretch; }
  .modal-btns .btn { flex:1; justify-content:center; }
}
/* ===== 修复右侧编辑区显示不全：让右栏独立滚动 ===== */

/* 顶栏之外的可用高度 */
#layout{
  height: calc(100vh - var(--topbar-h));
  min-height: 0;
  overflow: hidden;
}

/* 两栏在 flex 里都允许收缩，否则子元素滚动会失效 */
#sidebar,
#editor{
  min-height: 0;
}

/* 关键：右栏自身滚动 */
#editor{
  height: 100%;
  overflow-y: auto !important;
  overflow-x: hidden;
  -webkit-overflow-scrolling: touch;
  padding-bottom: 32px; /* 防止最后一块“看起来被截断” */
}

/* 某些浏览器下 section 也会撑破，兜底 */
#editor .section{
  flex-shrink: 0;
}

/* 移动端地址栏伸缩时，100vh不准，用dvh */
@media (max-width: 768px){
  #layout{
    height: calc(100dvh - var(--topbar-h));
  }
  #editor{
    min-height: 0;
    overflow-y: auto !important;
  }
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
</div>

<div id="layout">
  <!-- 左栏 -->
  <div id="sidebar">
    <div id="search-area">
      <input id="searchInput" placeholder="搜索题目文字、解析…" oninput="debounceSearch()">
      <div class="filter-row">
        <select id="filterMode" onchange="loadList()">
          <option value="">全部题型</option>
        </select>
        <select id="filterUnit" onchange="loadList()">
          <option value="">全部章节</option>
        </select>
      </div>
      <div class="filter-checks">
        <label><input type="checkbox" id="filterAI" onchange="loadList()"> 含AI内容</label>
        <label><input type="checkbox" id="filterMissing" onchange="loadList()"> 缺答案/解析</label>
      </div>
    </div>
    <div id="result-count"></div>
    <div id="qlist"></div>
    <div id="pagination">
      <button id="btnPrev" onclick="prevPage()">‹ 上页</button>
      <span id="pageInfo"></span>
      <button id="btnNext" onclick="nextPage()">下页 ›</button>
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
        <select class="field-input" id="rMode" style="flex:1;min-width:120px">
          <option value="">全部题型</option>
        </select>
        <select class="field-input" id="rUnit" style="flex:1;min-width:120px">
          <option value="">全部章节</option>
        </select>
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
let state = { page: 1, perPage: 50, total: 0, pages: 0 };
let currentQi = null, currentSi = 0;
let deleteTarget = null;
let searchTimer = null;
let info = {};

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
  const dot = document.getElementById('dirtyDot');
  dot.className = 'dirty-dot' + (info.dirty ? ' show' : '');
  document.getElementById('btnSave').disabled = !info.dirty;
}

function openMobileEditor() {
  if (window.innerWidth <= 768) document.body.classList.add('editor-open');
}
function closeMobileEditor() {
  document.body.classList.remove('editor-open');
}

async function loadList() {
  const q       = document.getElementById('searchInput').value;
  const mode    = document.getElementById('filterMode').value;
  const unit    = document.getElementById('filterUnit').value;
  const hasAI   = document.getElementById('filterAI').checked ? '1' : '';
  const missing = document.getElementById('filterMissing').checked ? '1' : '';

  const params = new URLSearchParams({
    q, mode, unit, has_ai: hasAI, missing, page: state.page, per_page: state.perPage
  });
  const data = await fetch('/api/questions?' + params).then(r => r.json());

  state.total = data.total;
  state.pages = data.pages;

  document.getElementById('result-count').textContent = `共 ${data.total} 个小题`;
  document.getElementById('pageInfo').textContent =
    `${state.page} / ${Math.max(1, state.pages)}`;
  document.getElementById('btnPrev').disabled = state.page <= 1;
  document.getElementById('btnNext').disabled = state.page >= state.pages;

  const list = document.getElementById('qlist');
  list.innerHTML = '';
  data.items.forEach(item => {
    const div = document.createElement('div');
    div.className = 'q-item' + (item.qi === currentQi && item.si === currentSi ? ' active' : '');
    div.onclick = () => selectQuestion(item.qi, item.si);

    const hasAIBadge  = item.ai_discuss || item.ai_answer;
    const missingAns  = !item.answer;
    const missingDis  = !item.discuss;
    const badges = [
      hasAIBadge ? `<span class="badge ai">AI</span>` : '',
      item.answer_source  === 'ai' ? `<span class="badge ai">AI答案</span>` : '',
      item.discuss_source === 'ai' ? `<span class="badge ai">AI解析</span>` : '',
      (missingAns || missingDis) ? `<span class="badge miss">缺${missingAns?'答案':''}${missingDis?'解析':''}</span>` : '',
    ].filter(Boolean).join('');

    div.innerHTML = `
      <div style="display:flex;justify-content:space-between;align-items:baseline">
        <span class="q-idx">[${item.qi+1}-${item.si+1}]</span>
        <span class="q-mode">${esc(item.mode)}</span>
      </div>
      <div class="q-text">${esc(item.text || item.stem || '（无题目文本）')}</div>
      <div class="q-badges">${badges}</div>`;
    list.appendChild(div);
  });
}

function debounceSearch() {
  clearTimeout(searchTimer);
  state.page = 1;
  searchTimer = setTimeout(loadList, 300);
}
function prevPage() { if (state.page > 1)           { state.page--; loadList(); } }
function nextPage() { if (state.page < state.pages) { state.page++; loadList(); } }

async function selectQuestion(qi, si) {
  currentQi = qi;
  currentSi = si;
  document.querySelectorAll('.q-item').forEach(el => {
    const idx = el.querySelector('.q-idx')?.textContent;
    el.classList.toggle('active', idx === `[${qi+1}-${si+1}]`);
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
  back.innerHTML = '← 返回列表';
  back.onclick = closeMobileEditor;
  ed.appendChild(back);

  const meta = mkSection('元数据', `
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
  ed.appendChild(meta);

  if (data.sub_questions.length > 1) {
    const tabsDiv = document.createElement('div');
    tabsDiv.className = 'section';
    tabsDiv.innerHTML = `<div class="section-body">
      <div class="sub-tabs">
        ${data.sub_questions.map((sq, i) => `
          <div class="sub-tab ${i === activeSi ? 'active' : ''}"
               onclick="selectSub(${data.qi}, ${i})">
            子题 ${i+1}${!sq.answer ? ' ❓' : ''}
          </div>`).join('')}
      </div>
    </div>`;
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
      <button class="btn-icon" onclick="removeOption(${i})" title="删除此选项">✕</button>
    </div>`
  ).join('');
  ed.appendChild(mkSection('选项', `
    <div class="options-list" id="optionsList">${optHtml}</div>
    <button class="btn btn-ghost" style="margin-top:10px;font-size:11px" onclick="addOption()">＋ 添加选项</button>
  `));

  const aiAnsBadge = sq.ai_answer ? `<span class="ai-badge">AI: ${esc(sq.ai_answer)}</span>` : '';
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
        style="color:var(--ai-color);opacity:.75;cursor:default">${esc(sq.ai_discuss)}</textarea>
    </div>` : ''}
  `));

  const actDiv = document.createElement('div');
  actDiv.className = 'section';
  actDiv.innerHTML = `<div class="section-body">
    <div class="action-bar">
      <button class="btn btn-primary" onclick="saveSubQuestion()">保存此题</button>
      <button class="btn btn-danger" onclick="openDelete(${data.qi})">删除整道题</button>
    </div>
  </div>`;
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

function getOptions() {
  return [...document.querySelectorAll('.option-input')].map(i => i.value);
}
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
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  }).then(r => r.json());

  if (res.ok) {
    info.dirty = true;
    updateTopStats();
    loadList();
    toast('✓ 已保存');
  } else {
    toast('保存失败: ' + (res.error || ''), true);
  }
}

function openDelete(qi) {
  deleteTarget = qi;
  document.getElementById('deleteModal').style.display = 'flex';
}
function closeDelete() {
  document.getElementById('deleteModal').style.display = 'none';
  deleteTarget = null;
}
async function confirmDelete() {
  if (deleteTarget === null) return;
  const res = await fetch(`/api/question/${deleteTarget}`, { method: 'DELETE' }).then(r => r.json());
  closeDelete();
  if (res.ok) {
    currentQi = null;
    info.dirty = true;
    info.total_q = res.total;
    updateTopStats();
    loadList();
    closeMobileEditor();
    const ed = document.getElementById('editor');
    ed.innerHTML = '<div id="mobileBack" onclick="closeMobileEditor()" style="display:none">← 返回列表</div><div class="empty">← 从左侧选择一道题目进行编辑</div>';
    document.getElementById('mobileBack').style.display = '';
    toast(`已删除，剩余 ${res.total} 大题`);
  } else {
    toast('删除失败', true);
  }
}

function openReplace() {
  document.getElementById('replaceModal').style.display = 'flex';
}
function closeReplace() {
  document.getElementById('replaceModal').style.display = 'none';
}
async function doReplace() {
  const find    = document.getElementById('rFind').value;
  const replace = document.getElementById('rReplace').value;
  const fields  = [...document.querySelectorAll('.checkbox-group input:checked')].map(c => c.value);
  const mode    = document.getElementById('rMode').value;
  const unit    = document.getElementById('rUnit').value;

  if (!find)          { toast('查找文本不能为空', true); return; }
  if (!fields.length) { toast('请至少选择一个作用字段', true); return; }

  const res = await fetch('/api/replace', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ find, replace, fields, mode, unit }),
  }).then(r => r.json());

  if (res.ok) {
    info.dirty = true;
    updateTopStats();
    closeReplace();
    loadList();
    toast(`替换完成：共 ${res.replaced} 处`);
  } else {
    toast('替换失败: ' + (res.error || ''), true);
  }
}

async function doSave() {
  const res = await fetch('/api/save', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({}),
  }).then(r => r.json());

  if (res.ok) {
    info.dirty = false;
    updateTopStats();
    toast('✓ 已保存至 ' + res.path.split(/[\\\/]/).pop());
  } else {
    toast('保存失败: ' + (res.error || ''), true);
  }
}

function esc(s) {
  return (s || '').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;')
                  .replace(/"/g,'&quot;').replace(/'/g,'&#39;');
}

let toastTimer;
function toast(msg, err = false) {
  const t = document.getElementById('toast');
  t.textContent = msg;
  t.className = 'show' + (err ? ' err' : '');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => t.className = '', 3000);
}

document.addEventListener('keydown', e => {
  if ((e.ctrlKey || e.metaKey) && e.key === 's') {
    e.preventDefault();
    if (info.dirty) doSave();
  }
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

    global _questions, _bank_path
    _bank_path = Path(bank_path).resolve()
    print(f"[INFO] 加载题库: {_bank_path}")
    _questions = load_bank(_bank_path, password)
    print(f"[INFO] 已加载 {len(_questions)} 道大题")

    url = f"http://127.0.0.1:{port}"
    print(f"[INFO] 编辑器启动: {url}")
    print(f"[INFO] 按 Ctrl+C 退出")

    if not no_browser:
        threading.Timer(0.8, lambda: webbrowser.open(url)).start()

    app.run(host="127.0.0.1", port=port, debug=False, use_reloader=False)