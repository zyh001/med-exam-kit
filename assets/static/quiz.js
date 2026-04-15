// ── Session Token（由服务端启动时生成，嵌入此页面）──────────────────────
// 所有 /api/* 请求必须通过 apiFetch() 发出，它会自动附加此 Token。
// 外部脚本/页面无法获知 Token，因此无法伪造 API 请求。

// ── quiz_ai.js 按需加载 ──────────────────────────────────────────
let _quizAILoaded = false;
let _quizAIPromise = null;
function _loadQuizAI() {
  if (_quizAILoaded) return Promise.resolve();
  if (_quizAIPromise) return _quizAIPromise;
  _quizAIPromise = new Promise((resolve, reject) => {
    const sc = document.createElement('script');
    sc.src = '/static/quiz_ai.js';
    sc.onload  = () => { _quizAILoaded = true; resolve(); };
    sc.onerror = () => reject(new Error('Failed to load quiz_ai.js'));
    document.body.appendChild(sc);
  });
  return _quizAIPromise;
}


// ════════════════════════════════════════════
// 状态
// ════════════════════════════════════════════
/** 返回用户本地日期字符串 YYYY-MM-DD，避免 toISOString() 的 UTC 偏差。
 *  用于 SM-2 复习日期计算和 API 请求中的 ?date= 参数，确保中国用户
 *  在 UTC 自然日午夜到 08:00 之间仍能正确看到当日复习任务。 */
function _localDate() {
  const d = new Date();
  return `${d.getFullYear()}-${String(d.getMonth()+1).padStart(2,'0')}-${String(d.getDate()).padStart(2,'0')}`;
}

const S = {
  mode: 'practice',
  questions: [],
  cur: 0,
  ans: {},          // idx -> letter
  marked: new Set(),
  revealed: new Set(),
  memoQueue: [],
  memoKnown: new Set(),
  memoCur: 0,
  memoFlipped: false,
  examStart: null,
  examLimit: 90 * 60,
  examSubmitted: false,
  examReviewMode: false,  // 全部答完后进入"回看"模式：自由导航但不可修改
  timerInterval: null,
  bankInfo: null,
  results: null,
  history: [],      // stored in localStorage
  // ── 考试模式题型分组 ──
  modeGroups: [],          // [{mode, startIdx, endIdx, allowBack}, ...]
  currentGroupIdx: 0,      // 当前所在题型组索引（只增不减）
  caseMaxReached: {},      // 案例分析组内已到达的最远题目索引
  _groupModalCb: null,     // 题型切换确认框回调
  practiceSessionId: null, // 当前练习进度 ID
  streak: 0,              // 练习模式连续答对计数
  bankID: 0,              // 当前选中的题库索引
  banksInfo: [],          // 所有题库列表（从 /api/banks 获取）
  questionTimes: {},      // {idx: seconds} 每题实际作答耗时
  _qStartTime: null,      // 当前题目开始作答的时间戳
};

// 返回当前题库的 query string 参数 (bank=N)
function bankQS() { return 'bank=' + S.bankID; }

// ── 图片灯箱（支持滚轮/捏合缩放 · 拖拽平移 · 双击还原）────────────
(function() {
  var _lb = null, _lbWrap = null, _lbImg = null;

  // 变换状态
  var _scale = 1, _minScale = 1, _maxScale = 12;
  var _panX = 0, _panY = 0;

  // 拖拽状态
  var _dragging = false, _didMove = false;
  var _dragX0 = 0, _dragY0 = 0, _panX0 = 0, _panY0 = 0;

  // 双击 / 双指点击状态
  var _lastTap = 0;

  // 捏合状态
  var _pinchDist0 = 0, _pinchMidX = 0, _pinchMidY = 0, _pinchPanX0 = 0, _pinchPanY0 = 0, _pinchScale0 = 1;
  // 单指平移状态
  var _touchPanX0 = 0, _touchPanY0 = 0, _touchPanPX0 = 0, _touchPanPY0 = 0;

  var _zoomTimer = null;

  // ── 初始化 DOM ────────────────────────────────────────────────────
  function _init() {
    if (_lb) return;

    _lb = document.createElement('div');
    _lb.id = 'img-lightbox';
    _lb.setAttribute('role', 'dialog');
    _lb.setAttribute('aria-modal', 'true');

    // 关闭按钮
    var closeBtn = document.createElement('button');
    closeBtn.id = 'img-lightbox-close';
    closeBtn.setAttribute('aria-label', '关闭');
    closeBtn.innerHTML = '&#x2715;';
    closeBtn.addEventListener('click', function(e) { e.stopPropagation(); _close(); });

    // 缩放工具栏
    var toolbar = document.createElement('div');
    toolbar.id = 'lb-toolbar';
    toolbar.innerHTML =
      '<button class="lb-tbtn" id="lb-zout" title="缩小 (-)">&#x2212;</button>' +
      '<span id="lb-zpct">100%</span>' +
      '<button class="lb-tbtn" id="lb-zin"  title="放大 (+)">&#x2b;</button>' +
      '<button class="lb-tbtn" id="lb-zrst" title="重置 (0)">⊙</button>';
    toolbar.addEventListener('click', function(e) { e.stopPropagation(); });

    // 缩放百分比浮动提示
    var zInd = document.createElement('div');
    zInd.id = 'lb-zoom-ind';

    // 图片容器（承载 transform）
    _lbWrap = document.createElement('div');
    _lbWrap.id = 'lb-wrap';

    _lbImg = document.createElement('img');
    _lbImg.alt = '图片预览';
    _lbImg.draggable = false;
    _lbImg.addEventListener('dragstart', function(e) { e.preventDefault(); });

    _lbWrap.appendChild(_lbImg);
    _lb.appendChild(closeBtn);
    _lb.appendChild(toolbar);
    _lb.appendChild(_lbWrap);
    _lb.appendChild(zInd);
    document.body.appendChild(_lb);

    // 工具栏按钮
    document.getElementById('lb-zin').addEventListener('click',  function() { _zoomStep(1.3); });
    document.getElementById('lb-zout').addEventListener('click', function() { _zoomStep(1/1.3); });
    document.getElementById('lb-zrst').addEventListener('click', function() { _resetAnim(); });

    // 鼠标事件
    _lb.addEventListener('mousedown', _mdDown);
    window.addEventListener('mousemove', _mdMove);
    window.addEventListener('mouseup',   _mdUp);

    // 滚轮缩放
    _lb.addEventListener('wheel', _onWheel, { passive: false });

    // 双击缩放
    _lb.addEventListener('dblclick', _onDblClick);

    // 触摸事件
    _lb.addEventListener('touchstart',  _onTouchStart,  { passive: false });
    _lb.addEventListener('touchmove',   _onTouchMove,   { passive: false });
    _lb.addEventListener('touchend',    _onTouchEnd);
    _lb.addEventListener('touchcancel', _onTouchEnd);

    // 键盘
    document.addEventListener('keydown', function(e) {
      if (!_lb.classList.contains('open')) return;
      if (e.key === 'Escape')             { _close(); }
      if (e.key === '+' || e.key === '=') { e.preventDefault(); _zoomStep(1.3); }
      if (e.key === '-')                  { e.preventDefault(); _zoomStep(1/1.3); }
      if (e.key === '0')                  { e.preventDefault(); _resetAnim(); }
      if (e.key === 'ArrowLeft')  { e.preventDefault(); _movePan( 60,  0); }
      if (e.key === 'ArrowRight') { e.preventDefault(); _movePan(-60,  0); }
      if (e.key === 'ArrowUp')    { e.preventDefault(); _movePan(  0, 60); }
      if (e.key === 'ArrowDown')  { e.preventDefault(); _movePan(  0,-60); }
    });
  }

  // ── 变换应用 ─────────────────────────────────────────────────────
  function _applyTransform() {
    _lbWrap.style.transform = 'translate3d(' + _panX + 'px,' + _panY + 'px,0) scale(' + _scale + ')';
    _lb.style.cursor = _scale > 1.01 ? (_dragging ? 'grabbing' : 'grab') : 'zoom-out';
    var pct = document.getElementById('lb-zpct');
    if (pct) pct.textContent = Math.round(_scale * 100) + '%';
  }

  function _showZoomInd() {
    var el = document.getElementById('lb-zoom-ind');
    if (!el) return;
    el.textContent = Math.round(_scale * 100) + '%';
    el.classList.add('visible');
    clearTimeout(_zoomTimer);
    _zoomTimer = setTimeout(function() { el.classList.remove('visible'); }, 1000);
  }

  // 限制平移范围（允许图片边缘最多超出 80px）
  function _clampPan() {
    if (!_lbImg || !_lb) return;
    var lw = _lb.clientWidth, lh = _lb.clientHeight;
    // 图片在 scale=1 时的显示尺寸
    var iw = _lbImg.naturalWidth  || _lbImg.clientWidth;
    var ih = _lbImg.naturalHeight || _lbImg.clientHeight;
    var dw = Math.min(iw, lw * 0.92) * _scale;
    var dh = Math.min(ih, lh * 0.88) * _scale;
    var mx = Math.max(0, (dw - lw) / 2 + 80);
    var my = Math.max(0, (dh - lh) / 2 + 80);
    _panX = Math.max(-mx, Math.min(mx, _panX));
    _panY = Math.max(-my, Math.min(my, _panY));
  }

  // 以视口坐标 (cx,cy) 为中心，缩放 ratio 倍
  function _zoomAt(cx, cy, ratio) {
    var rect = _lbWrap.getBoundingClientRect();
    var wcx = rect.left + rect.width  / 2;
    var wcy = rect.top  + rect.height / 2;
    var dx = cx - wcx, dy = cy - wcy;
    var newScale = Math.max(_minScale * 0.95, Math.min(_maxScale, _scale * ratio));
    var r = newScale / _scale;
    _scale = newScale;
    _panX = _panX * r + dx * (1 - r);
    _panY = _panY * r + dy * (1 - r);
    _clampPan();
    _applyTransform();
    _showZoomInd();
  }

  function _zoomStep(ratio) {
    var lw = _lb ? _lb.clientWidth  : window.innerWidth;
    var lh = _lb ? _lb.clientHeight : window.innerHeight;
    _zoomAt(lw / 2, lh / 2, ratio);
  }

  function _movePan(dx, dy) {
    _panX += dx; _panY += dy;
    _clampPan();
    _applyTransform();
  }

  function _resetAnim() {
    _lbWrap.style.transition = 'transform .22s cubic-bezier(.4,0,.2,1)';
    _scale = 1; _panX = 0; _panY = 0;
    _applyTransform();
    _showZoomInd();
    setTimeout(function() { if (_lbWrap) _lbWrap.style.transition = ''; }, 240);
  }

  // ── 鼠标事件 ─────────────────────────────────────────────────────
  function _mdDown(e) {
    if (e.button !== 0) return;
    e.preventDefault();
    _dragging = true; _didMove = false;
    _dragX0 = e.clientX; _dragY0 = e.clientY;
    _panX0 = _panX; _panY0 = _panY;
    _applyTransform();
  }
  function _mdMove(e) {
    if (!_dragging) return;
    var dx = e.clientX - _dragX0, dy = e.clientY - _dragY0;
    if (Math.abs(dx) > 3 || Math.abs(dy) > 3) _didMove = true;
    _panX = _panX0 + dx; _panY = _panY0 + dy;
    _clampPan();
    _applyTransform();
  }
  function _mdUp(e) {
    if (!_dragging) return;
    _dragging = false;
    _applyTransform();
    // 没拖动 + 在背景上 + 未放大 → 关闭
    if (!_didMove && e.target === _lb && _scale <= 1.01) _close();
    _didMove = false;
  }

  // ── 滚轮 ─────────────────────────────────────────────────────────
  function _onWheel(e) {
    e.preventDefault();
    // ctrlKey = 触控板捏合（已经是 pinch delta），普通滚轮取 deltaY
    var factor = e.ctrlKey ? Math.exp(-e.deltaY / 100) : (e.deltaY > 0 ? 0.85 : 1.18);
    _zoomAt(e.clientX, e.clientY, factor);
  }

  // ── 双击 ─────────────────────────────────────────────────────────
  function _onDblClick(e) {
    e.preventDefault();
    if (_scale > 1.1) { _resetAnim(); }
    else { _zoomAt(e.clientX, e.clientY, 2.5); }
  }

  // ── 触摸（捏合 + 单指平移 + 双击）──────────────────────────────────
  function _dist(a, b) {
    var dx = a.clientX - b.clientX, dy = a.clientY - b.clientY;
    return Math.sqrt(dx*dx + dy*dy);
  }

  function _onTouchStart(e) {
    // 关闭按钮 / 工具栏：不阻止默认行为，让 click 事件正常触发
    var tgt = e.target;
    if (tgt && (tgt.id === 'img-lightbox-close' ||
        tgt.closest && (tgt.closest('#img-lightbox-close') || tgt.closest('#lb-toolbar')))) {
      return; // 不 preventDefault，让 click 触发
    }
    e.preventDefault();
    var ts = e.touches;
    if (ts.length === 2) {
      // 捏合开始
      _pinchDist0   = _dist(ts[0], ts[1]);
      _pinchMidX    = (ts[0].clientX + ts[1].clientX) / 2;
      _pinchMidY    = (ts[0].clientY + ts[1].clientY) / 2;
      _pinchScale0  = _scale;
      _pinchPanX0   = _panX;
      _pinchPanY0   = _panY;
    } else if (ts.length === 1) {
      // 单指：记录起点
      _touchPanX0  = ts[0].clientX;
      _touchPanY0  = ts[0].clientY;
      _touchPanPX0 = _panX;
      _touchPanPY0 = _panY;
      // 双击检测
      var now = Date.now();
      if (now - _lastTap < 300) {
        _onDblClick({ preventDefault: function(){}, clientX: ts[0].clientX, clientY: ts[0].clientY });
      }
      _lastTap = now;
    }
  }

  function _onTouchMove(e) {
    e.preventDefault();
    var ts = e.touches;
    if (ts.length === 2) {
      // 捏合缩放：同时支持平移（两指中心移动）
      var curDist = _dist(ts[0], ts[1]);
      var curMidX = (ts[0].clientX + ts[1].clientX) / 2;
      var curMidY = (ts[0].clientY + ts[1].clientY) / 2;
      if (_pinchDist0 > 0) {
        var ratio = curDist / _pinchDist0;
        var newScale = Math.max(_minScale * 0.95, Math.min(_maxScale, _pinchScale0 * ratio));
        // 两指中心移动产生的平移量
        var midDX = curMidX - _pinchMidX;
        var midDY = curMidY - _pinchMidY;
        // 缩放中心修正
        var rect = _lbWrap.getBoundingClientRect();
        var wcx = rect.left + rect.width / 2, wcy = rect.top + rect.height / 2;
        var dx = _pinchMidX - wcx, dy = _pinchMidY - wcy;
        var r = newScale / _pinchScale0;
        _scale = newScale;
        _panX = _pinchPanX0 * r + dx * (1 - r) + midDX;
        _panY = _pinchPanY0 * r + dy * (1 - r) + midDY;
        _clampPan();
        _applyTransform();
        _showZoomInd();
      }
    } else if (ts.length === 1) {
      // 单指平移（缩放时才允许，否则让页面正常滚动）
      if (_scale > 1.01) {
        var dx = ts[0].clientX - _touchPanX0;
        var dy = ts[0].clientY - _touchPanY0;
        _panX = _touchPanPX0 + dx;
        _panY = _touchPanPY0 + dy;
        _clampPan();
        _applyTransform();
      }
    }
  }

  function _onTouchEnd(e) {
    // 捏合结束后重置参考距离
    if (e.touches.length < 2) _pinchDist0 = 0;
    if (e.touches.length === 0) {
      var tgt = e.changedTouches[0];
      // 触摸关闭按钮 → 关闭（补充 click 的保障）
      var el = document.elementFromPoint(tgt.clientX, tgt.clientY);
      if (el && (el.id === 'img-lightbox-close' || (el.closest && el.closest('#img-lightbox-close')))) {
        _close();
        return;
      }
      // 未放大时，点击背景区域（非图片/工具栏）→ 关闭
      if (_scale <= 1.01 && el) {
        var isBackground = (el === _lb) ||
          (el.id === 'lb-wrap') ||
          (el === _lbWrap);
        // 排除工具栏和关闭按钮本身
        var isToolbar = el.closest && (el.closest('#lb-toolbar') || el.closest('#img-lightbox-close'));
        if (isBackground && !isToolbar) {
          _close();
        }
      }
    }
  }

  // ── 开 / 关 ──────────────────────────────────────────────────────
  function _open(src) {
    _init();
    _scale = 1; _panX = 0; _panY = 0;
    _lbWrap.style.transition = '';
    _lbImg.src = src;
    _applyTransform();
    _lb.classList.add('open');
    document.body.style.overflow = 'hidden';
  }

  function _close() {
    if (!_lb) return;
    _lb.classList.remove('open');
    document.body.style.overflow = '';
    clearTimeout(_zoomTimer);
    setTimeout(function() { if (_lb && !_lb.classList.contains('open')) _lbImg.src = ''; }, 200);
  }

  window._openLightbox = _open;

  // 事件委托：data-lb 图片点击打开
  document.addEventListener('click', function(e) {
    var t = e.target;
    if (t && t.tagName === 'IMG' && t.dataset && t.dataset.lb) _open(t.src);
  }, true);
})();

// 安全渲染 HTML：允许 <img> 标签，过滤脚本等危险元素
function renderHTML(s) {
  if (!s) return '';
  // 使用 DOMParser 解析，只保留安全节点
  const div = document.createElement('div');
  // 先 esc 普通文本，再还原允许的 img 标签
  // 策略：直接用 innerHTML 赋值，但删除危险标签
  const tmp = document.createElement('div');
  tmp.innerHTML = s;
  // 移除所有 script / iframe / object / form 节点
  tmp.querySelectorAll('script,iframe,object,embed,form,link,meta,style').forEach(el => el.remove());
  // 对 img 标签：只保留安全属性，加响应式样式和加载失败提示
  tmp.querySelectorAll('img').forEach(img => {
    const allowed = ['src','alt','width','height','style','class','title'];
    Array.from(img.attributes).forEach(attr => {
      if (!allowed.includes(attr.name.toLowerCase())) img.removeAttribute(attr.name);
    });
    // 外链图片通过后端代理转发，解决跨域问题
    const src = img.getAttribute('src') || '';
    if (/^https?:\/\//i.test(src)) {
      img.setAttribute('src', '/api/img/proxy?url=' + encodeURIComponent(src));
    }
    // 响应式样式
    img.style.maxWidth = '100%';
    img.style.height = 'auto';
    img.style.borderRadius = '6px';
    img.style.margin = '8px 0';
    img.style.display = 'block';
    // 加载失败时显示提示，避免破碎图标
    if (!img.hasAttribute('alt') || !img.alt) img.alt = '题目图片';
    img.setAttribute('onerror',
      "this.onerror=null;this.style.display='none';" +
      "var p=document.createElement('span');" +
      "p.className='img-load-err';" +
      "p.textContent='⚠ 图片加载失败：' + (this.dataset.origSrc||this.src||'').slice(0,60);" +
      "this.parentNode.insertBefore(p,this.nextSibling);"
    );
    // 记录原始地址方便错误提示
    img.dataset.origSrc = src;
    // 标记图片可点击放大（onclick 在 sanitizer 之后通过事件委托处理）
    img.style.cursor = 'zoom-in';
    img.dataset.lb = '1';
  });
  // 移除所有元素上的事件属性（on*）
  tmp.querySelectorAll('*').forEach(el => {
    Array.from(el.attributes).forEach(attr => {
      if (attr.name.startsWith('on')) el.removeAttribute(attr.name);
    });
  });
  return tmp.innerHTML;
}

const CFG = {
  units: new Set(['__all__']),
  modes: new Set(['__all__']),
  count: 50,
  shuffle: true,
  examTime: 90,
  perMode:    null,   // null = 关闭；{mode: sqCount} = 精细配额
  perUnit:    null,   // null = 关闭；{unit: sqCount} = 章节配额
  difficulty: null,   // null = 关闭；{easy,medium,hard,extreme} = 权重
  countRatio: null,   // null = 关闭；{mode: weight} = 数量精细比例（与 perMode 互斥）
  scoring:    false,  // 是否开启计分
  scorePerMode: {},   // {mode: 每小题分值}
  multiScoreMode: 'strict', // 'strict' | 'loose' — 案例/X型计分规则
};

// ════════════════════════════════════════════
// 练习进度持久化
// ════════════════════════════════════════════
// ── 每个题库独立的 localStorage key ──────────────────────────────────
// 以 bankID 作为后缀，切换题库后历史/进度/存档完全隔离
function _bankSuffix() { return '-b' + S.bankID; }
function practiceSessionsKey() { return 'quiz_practice_sessions_v1' + _bankSuffix(); }
function examSessionKey()      { return 'quiz_exam_session_v1'      + _bankSuffix(); }
function historyKey()          { return 'quiz-history'              + _bankSuffix(); }
function deletedIdsKey()       { return 'quiz-deleted-ids'        + _bankSuffix(); }
function _reviewCacheKey()     { return 'quiz-review-cache'         + _bankSuffix(); }

const MAX_PRACTICE_SESSIONS = 5;

function _getPracticeSessions() {
  try { return JSON.parse(localStorage.getItem(practiceSessionsKey()) || '[]'); }
  catch { return []; }
}
function _setPracticeSessions(arr) {
  try { localStorage.setItem(practiceSessionsKey(), JSON.stringify(arr)); } catch(e) {}
}

/** 生成当前练习的可读标题 */
function _practiceTitle() {
  const units = [...new Set(S.questions.map(q => q.unit).filter(Boolean))];
  const unitLabel = units.length === 0 ? '全部章节'
      : units.length <= 2  ? units.join('、')
          : units.slice(0,2).join('、') + ' 等';
  return `${unitLabel} · ${S.questions.length} 题`;
}

/** 保存当前练习进度 */
function savePracticeSession() {
  if (S.mode !== 'practice' || !S.questions.length) return;
  const sessions = _getPracticeSessions();
  const existIdx = sessions.findIndex(s => s.id === S.practiceSessionId);

  const answered = Object.keys(S.ans).length;
  const session = {
    v:         1,
    id:        S.practiceSessionId,
    savedAt:   Date.now(),
    mode:      'practice',
    cur:       S.cur,
    questions: S.questions,
    ans:       _serializeAns(S.ans),
    revealed:  [...S.revealed],
    marked:    [...S.marked],
    title:     _practiceTitle(),
    answered,
    total:     S.questions.length,
    startedAt: S.examStart,
  };

  if (existIdx >= 0) sessions[existIdx] = session;
  else sessions.unshift(session);

  _setPracticeSessions(sessions.slice(0, MAX_PRACTICE_SESSIONS));
}

/** 练习完成时清除该进度（或标记为已完成） */
function clearPracticeSession(sessionId) {
  const targetId = sessionId || S.practiceSessionId;
  if (!targetId) return;
  const sessions = _getPracticeSessions().filter(s => s.id !== targetId);
  _setPracticeSessions(sessions);
  if (targetId === S.practiceSessionId) S.practiceSessionId = null;
}

/** 从存档恢复练习进度 */
function resumePracticeSession(id) {
  const sessions = _getPracticeSessions();
  const s = sessions.find(s => s.id === id);
  if (!s) return;

  S.mode             = 'practice';
  S.questions        = s.questions;
  S.ans              = _deserializeAns(s.ans);
  S.revealed         = new Set(s.revealed || []);
  S.marked           = new Set(s.marked   || []);
  S.cur              = s.cur ?? 0;
  S.examStart        = s.startedAt || Date.now();
  S.modeGroups       = buildModeGroups(s.questions);
  S.currentGroupIdx  = 0;
  S.caseMaxReached   = {};
  S.practiceSessionId= id;

  startQuiz();
}

// ════════════════════════════════════════════
// 考试进度持久化
// ════════════════════════════════════════════
/** 序列化 S.ans（值可能是 Set） */
function _serializeAns(ans) {
  const out = {};
  for (const [k, v] of Object.entries(ans)) {
    out[k] = (v instanceof Set) ? { __set: true, v: [...v] } : v;
  }
  return out;
}

/** 反序列化 ans */
function _deserializeAns(raw) {
  const out = {};
  for (const [k, v] of Object.entries(raw)) {
    out[k] = (v && v.__set) ? new Set(v.v) : v;
  }
  return out;
}

/** 保存当前考试状态到 localStorage */
function saveExamSession() {
  if (S.mode !== 'exam' || !S.questions.length || S.examSubmitted) return;
  const elapsedSec = Math.floor((Date.now() - S.examStart) / 1000);
  const remaining  = Math.max(0, S.examLimit - elapsedSec);
  const session = {
    v: 1,
    savedAt:       Date.now(),
    remaining,
    examLimit:     S.examLimit,
    cur:           S.cur,
    questions:     S.questions,
    ans:           _serializeAns(S.ans),
    marked:        [...S.marked],
    modeGroups:    S.modeGroups,
    currentGroupIdx: S.currentGroupIdx,
    caseMaxReached:  S.caseMaxReached,
    // 保存计分配置，确保刷新恢复后的分享也能带上正确的配置
    scoring:        !!CFG.scoring,
    scorePerMode:   CFG.scorePerMode   || {},
    multiScoreMode: CFG.multiScoreMode || 'strict',
    // 分享考试的密封 exam_id：刷新后仍能向服务端取回答案
    exam_id:        S.examId || null,
  };
  try {
    localStorage.setItem(examSessionKey(), JSON.stringify(session));
  } catch(e) {
    console.warn('[Quiz] 保存考试进度失败（可能 localStorage 已满）', e);
  }
}

/** 清除已保存的考试进度 */
function clearExamSession() {
  localStorage.removeItem(examSessionKey());
}

/** 读取已保存的考试进度，返回 session 对象或 null */
function _loadRawSession() {
  try {
    const raw = localStorage.getItem(examSessionKey());
    if (!raw) return null;
    const s = JSON.parse(raw);
    // 基本合法性校验
    if (!s || !Array.isArray(s.questions) || !s.questions.length) return null;
    return s;
  } catch(e) { return null; }
}

/** 格式化秒数为 mm:ss */
function _fmtSec(sec) {
  const m = String(Math.floor(sec / 60)).padStart(2, '0');
  const s = String(sec % 60).padStart(2, '0');
  return `${m}:${s}`;
}

/** 格式化存档时间 */
function _fmtSavedAt(ts) {
  const d = new Date(ts);
  const pad = n => String(n).padStart(2, '0');
  return `${d.getMonth()+1}/${d.getDate()} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** init 时检测是否有未完成的考试，有则弹出恢复弹窗 */
function checkResumeSession() {
  const s = _loadRawSession();
  if (!s) return false;

  // 基础字段校验：必须有 remaining 和 savedAt
  if (typeof s.remaining !== 'number' || typeof s.savedAt !== 'number') {
    clearExamSession(); return false;
  }

  // 超过 24 小时的存档直接丢弃
  if (Date.now() - s.savedAt > 24 * 3600 * 1000) {
    clearExamSession(); return false;
  }

  // 若剩余时间已耗尽（超时超过 10 秒缓冲），丢弃
  const elapsed = Math.floor((Date.now() - s.savedAt) / 1000);
  const remaining = s.remaining - elapsed;
  if (remaining <= -10) { clearExamSession(); return false; }

  // 填充弹窗数据
  const answered = Object.keys(s.ans).length;
  document.getElementById('rm-answered').textContent = answered;
  document.getElementById('rm-total').textContent    = s.questions.length;
  document.getElementById('rm-time').textContent     = remaining > 0 ? _fmtSec(remaining) : '已超时';
  document.getElementById('rm-saved').textContent    = _fmtSavedAt(s.savedAt);
  document.getElementById('resume-modal').style.display = 'flex';
  return true;
}

/** 用户选择「放弃」 */
function discardSession() {
  clearExamSession();
  document.getElementById('resume-modal').style.display = 'none';
}

/** 用户选择「继续作答」——从 localStorage 恢复完整考试状态 */
function restoreSession() {
  const s = _loadRawSession();
  document.getElementById('resume-modal').style.display = 'none';
  if (!s) return;

  const elapsed   = Math.floor((Date.now() - s.savedAt) / 1000);
  const remaining = Math.max(0, s.remaining - elapsed);

  // 恢复状态
  S.mode             = 'exam';
  S.questions        = s.questions;
  S.ans              = _deserializeAns(s.ans);
  S.marked           = new Set(s.marked || []);
  S.modeGroups       = s.modeGroups       || buildModeGroups(s.questions);
  S.currentGroupIdx  = s.currentGroupIdx  ?? 0;
  S.caseMaxReached   = s.caseMaxReached   || {};
  S.examLimit        = s.examLimit;
  S.revealed         = new Set();
  S.cur              = s.cur ?? 0;
  // 恢复计分配置（如果 session 内存的话）
  if (s.scoring != null)      CFG.scoring        = !!s.scoring;
  if (s.scorePerMode)         CFG.scorePerMode   = s.scorePerMode;
  if (s.multiScoreMode)       CFG.multiScoreMode = s.multiScoreMode;
  // 恢复密封考试的 exam_id，刷新后仍可向服务端取回答案
  S.examId = s.exam_id || null;
  // 同步 CFG.examTime，使后续"重新考"的默认时间也一致
  if (S.examLimit) CFG.examTime = Math.round(S.examLimit / 60);
  // examStart 反推，让计时器正确
  S.examStart        = Date.now() - (S.examLimit - remaining) * 1000;

  startQuiz(remaining);
}

// ════════════════════════════════════════════
// Init
// ════════════════════════════════════════════
const SELECTED_BANK_KEY = 'quiz-selected-bank';

async function init() {
  loadTheme();
  try {
    const banksData = await apiFetch('/api/banks').then(r => r.json());
    S.banksInfo = banksData.banks || [];

    if (S.banksInfo.length <= 1) {
      // 单题库：直接进入
      await selectBankAndEnter(0);
    } else {
      // 多题库：优先恢复上次选择；携带分享链接时自动用 bank 0 进入（join 接口从 token 读真实 bankIdx）
      const saved = parseInt(localStorage.getItem(SELECTED_BANK_KEY) ?? '', 10);
      const validSaved = Number.isFinite(saved) && saved >= 0 && saved < S.banksInfo.length;
      const hasShareToken = /[#&]share=[a-f0-9]+/.test(location.hash + location.search);
      if (validSaved) {
        await selectBankAndEnter(saved);
      } else if (hasShareToken) {
        // 直接用 bank 0 进入，_checkShareToken 会调 /api/exam/join，服务端从 token 取真正的 bankIdx
        await selectBankAndEnter(0);
      } else {
        renderBankSelectPage();
      }
    }
  } catch(e) {
    document.getElementById('home-bank-name').textContent = '无法连接到服务器';
  }
  // 异步加载徽章（不阻塞主界面显示）
  _refreshProgressBadges();

  // 注册离线同步状态回调：pending 数变化时更新角标
  if (typeof SyncManager !== 'undefined') {
    SyncManager.onStateChange(syncState => {
      _updateSyncBadge(syncState);
    });
    // 初始渲染一次
    _updateSyncBadge(SyncManager.getState());
  }
}

// 加载题库信息并渲染主页（内部使用，selectBankAndEnter 调用）
async function loadBankAndRenderHome() {
  S.bankInfo = await apiFetch('/api/info?' + bankQS()).then(r => r.json());
  renderHome();
}

// 每次显示主页时调用，刷新所有动态数据（历史记录、徽章、进度）
async function refreshHomeData() {
  try {
    await Promise.all([
      _fetchServerHistory(),       // 从服务端拉最新历史并重渲染
      _refreshProgressBadges(),    // 刷新徽章
    ]);
  } catch (e) { /* 静默失败 */ }
  refreshFavBadge();  // 刷新收藏徽章（本地，无需 await）
}

function renderHome() {
  const info = S.bankInfo;
  document.getElementById('home-bank-name').textContent = info.bank_name || '题库';
  document.getElementById('stat-sq').textContent    = info.total_sq.toLocaleString();
  document.getElementById('stat-units').textContent = info.units.length;
  document.getElementById('stat-modes').textContent = info.modes.length;
  renderHistorySection();
}

// ════════════════════════════════════════════
// 题库选择页面（卡片版）
// ════════════════════════════════════════════

const BANK_PALETTES = [
  { hue: '210' }, // 蓝
  { hue: '145' }, // 绿
  { hue: '32'  }, // 橙
  { hue: '270' }, // 紫
  { hue: '350' }, // 红
  { hue: '185' }, // 青
];

// 从题库名称中提取 1-2 个汉字作为头像文字
function _bankInitials(name) {
  const cjk = name.match(/[\u4e00-\u9fff\u3400-\u4dbf]+/g);
  if (cjk && cjk.length) {
    const joined = cjk.join('');
    return joined.length <= 2 ? joined : joined.slice(0, 1) + joined.slice(-1);
  }
  return name.slice(0, 2).toUpperCase();
}

function renderBankSelectPage() {
  const container = document.getElementById('bank-select-list');
  container.innerHTML = '';
  S.banksInfo.forEach((b, idx) => {
    const hue  = BANK_PALETTES[idx % BANK_PALETTES.length].hue;
    const card = document.createElement('button');
    card.className = 'bank-card-item';
    card.style.setProperty('--bh', hue);
    card.innerHTML = `
      <div class="bci-avatar">${esc(_bankInitials(b.name))}</div>
      <div class="bci-body">
        <div class="bci-name">${esc(b.name)}</div>
        <div class="bci-meta">
          <span class="bci-tag">${b.total_sq.toLocaleString()} 题</span>
        </div>
      </div>
      <div class="bci-arrow">›</div>
    `;
    card.onclick = () => selectBankAndEnter(idx);
    container.appendChild(card);
  });
  showScreen('s-bank-select');
}

// 多题库时让左上角品牌区域变成可点击的题库切换入口
function _updateBrandClickable() {
  const brand = document.querySelector('.home-brand');
  if (!brand) return;

  // 清除旧的单独切换按钮（兼容旧逻辑）
  const old = document.getElementById('switch-bank-btn');
  if (old) old.remove();

  if (S.banksInfo.length <= 1) {
    // 单题库：不可点击，移除所有交互样式
    brand.classList.remove('home-brand--switchable');
    brand.onclick = null;
    const tip = brand.querySelector('.brand-switch-tip');
    if (tip) tip.remove();
    return;
  }

  // 多题库：品牌区可点击
  brand.classList.add('home-brand--switchable');
  brand.onclick = () => renderBankSelectPage();

  // 若没有指示符则插入
  if (!brand.querySelector('.brand-switch-tip')) {
    const tip = document.createElement('span');
    tip.className = 'brand-switch-tip';
    tip.title = '点击切换题库';
    tip.innerHTML = '▾';
    brand.appendChild(tip);
  }
}

// 选择题库并进入主页：完全重置状态 + 重载历史 + 检查未完成考试
async function selectBankAndEnter(idx) {
  S.bankID = idx;

  // ── 完整重置 CFG（每个题库独立配置）────────────────
  CFG.units         = new Set(['__all__']);
  CFG.modes         = new Set(['__all__']);
  CFG.count         = 50;
  CFG.shuffle       = true;
  CFG.examTime      = 90;
  CFG.perMode       = null;
  CFG.perUnit       = null;
  CFG.difficulty    = null;
  CFG.countRatio    = null;
  CFG.scoring       = false;
  CFG.scorePerMode  = {};
  CFG.multiScoreMode = 'strict';

  // ── 重置题目状态 ──────────────────────────────────
  S.questions     = [];
  S.cur           = 0;
  S.ans           = {};
  S.marked        = new Set();
  S.revealed      = new Set();
  S.results       = null;
  S.modeGroups    = [];
  S.currentGroupIdx = 0;
  S.caseMaxReached  = {};
  S.streak        = 0;
  S.questionTimes = {};
  S._qStartTime   = null;

  // ── 持久化选择（刷新后自动恢复）────────────────────
  localStorage.setItem(SELECTED_BANK_KEY, String(idx));

  // ── 加载此题库的历史记录 ─────────────────────────
  S.serverHistory    = null;
  S.localOnlyHistory = null;
  S.deletedIds       = new Set();
  loadHistory();

  try {
    // 分享链接进入：未验证用户需要锁定在考试页面
    // 逻辑：任何不含 #share= 的正常访问都视为"已验证"
    // 首次通过分享链接进入的用户会被锁定；已经访问过（已验证）的用户正常
    const isShareEntry = /share=[a-f0-9]+/.test(location.hash);
    if (!isShareEntry) {
      try { localStorage.setItem('med_exam_verified', '1'); } catch(_) {}
      S.sharedLocked = false;
    } else {
      let alreadyVerified = false;
      try { alreadyVerified = localStorage.getItem('med_exam_verified') === '1'; } catch(_) {}
      S.sharedLocked = !alreadyVerified;
    }

    await loadBankAndRenderHome();
    showScreen('s-home');

    // 检查此题库是否有未完成的考试（弹窗覆盖在主页上方）
    // 锁定模式下跳过：防止分享接收者看到与他无关的历史存档
    if (!S.sharedLocked) checkResumeSession();

    // 检查 URL hash 是否包含分享试卷令牌
    _checkShareToken();

    // 尝试补取上次网络异常时未能取回的密封答案
    _processPendingReveals();

    // 多题库时，让左上角题库名称变成可点击的切换入口
    _updateBrandClickable();
  } catch(e) {
    toast('加载题库失败', true);
  }
}

// ════════════════════════════════════════════
// Screen transitions
// ════════════════════════════════════════════
function showScreen(id, dir = 'forward') {
  const cur  = document.querySelector('.screen.active');
  const next = document.getElementById(id);
  if (!next || next === cur) return;

  // 每次切换回主页：先用本地数据即时渲染，再异步拉服务端最新数据
  if (id === 's-home') {
    renderHistorySection();   // 本地数据，无网络延迟
    refreshHomeData();        // 异步拉服务端记录、刷新徽章
  }
  // 离开做题页时隐藏回看提示条
  if (id !== 's-quiz') {
    const bar = document.getElementById('exam-review-bar');
    if (bar) bar.style.display = 'none';
  }

  // 新屏：先标记为 sliding（visibility:visible），再移除偏移类，最后 active
  next.classList.add('sliding');
  next.classList.remove('slide-left', 'slide-right');
  // 强制 reflow 让初始 transform 生效，否则动画起点不正确
  next.offsetHeight;
  next.classList.add('active');

  if (cur) {
    cur.classList.remove('active');
    cur.classList.add('sliding');
    cur.classList.add(dir === 'forward' ? 'slide-left' : 'slide-right');
    setTimeout(() => {
      // 动画结束：旧屏彻底隐藏，不再占据任何视口空间
      cur.classList.remove('slide-left', 'slide-right', 'sliding');
    }, 260);
  }

  // 新屏动画结束后清理 sliding 标记
  setTimeout(() => next.classList.remove('sliding'), 260);
}

// ════════════════════════════════════════════
// Config screen
// ════════════════════════════════════════════
function openConfig(mode) {
  S.mode = mode;
  const titles = { practice:'练习模式', exam:'考试模式', memo:'背题模式' };
  document.getElementById('cfg-title').textContent = titles[mode];
  const badge = document.getElementById('cfg-badge');
  badge.textContent = titles[mode];
  badge.className = `mode-badge ${mode}`;

  const startBtn = document.getElementById('start-btn');
  const examLabel = { practice:'开始练习', exam:'开始考试', memo:'开始背题' };
  startBtn.textContent = examLabel[mode];
  startBtn.className = `start-btn ${mode}-start`;

  document.getElementById('exam-opts').style.display    = mode === 'exam' ? '' : 'none';
  document.getElementById('scoring-section').style.display = mode === 'exam' ? '' : 'none';

  // 重置所有可选项
  CFG.units = new Set(['__all__']);
  CFG.modes = new Set(['__all__']);
  CFG.perMode    = null;
  CFG.perUnit    = null;
  CFG.difficulty = null;
  CFG.countRatio = null;
  CFG.scoring    = false;
  CFG.scorePerMode = {};
  CFG.multiScoreMode = 'strict';

  // 重置计分开关
  const scCk = document.getElementById('cfg-scoring');
  if (scCk) scCk.checked = false;
  document.getElementById('scoring-panel').style.display = 'none';
  document.getElementById('scoring-panel').innerHTML = '';

  // 重置精细配额 / 难度面板开关
  const pmCk = document.getElementById('cfg-per-mode');
  if (pmCk) pmCk.checked = false;
  document.getElementById('mode-filter-panel').style.display = '';
  document.getElementById('mode-quota-panel').style.display = 'none';
  document.getElementById('mode-quota-panel').innerHTML = '';
  document.getElementById('count-section').style.display = '';

  // 重置章节配额
  const puCk = document.getElementById('cfg-per-unit');
  if (puCk) puCk.checked = false;
  document.getElementById('unit-quota-panel').style.display = 'none';
  document.getElementById('unit-quota-panel').innerHTML = '';

  // 重置数量精细比例
  const crCk = document.getElementById('cfg-count-ratio');
  if (crCk) crCk.checked = false;
  document.getElementById('count-ratio-panel').style.display = 'none';
  document.getElementById('count-ratio-panel').innerHTML = '';

  const diffCk = document.getElementById('cfg-difficulty');
  if (diffCk) diffCk.checked = false;
  document.getElementById('difficulty-panel').style.display = 'none';
  document.getElementById('difficulty-panel').innerHTML = '';

  // 清空章节搜索
  const search = document.getElementById('unit-search');
  if (search) search.value = '';

  // 构建 chips
  buildUnitChips(S.bankInfo.units, mode);
  buildModeChips(S.bankInfo.modes, mode);
  updateUnitSelCount();

  const maxQ = S.bankInfo.total_sq;
  const slider = document.getElementById('count-slider');
  slider.max = Math.min(maxQ, 300);
  slider.value = Math.min(CFG.count, Number(slider.max));
  updateCount(slider.value);

  showScreen('s-config');
}

// ── 章节 chips（带搜索 + 选中计数）──────────────────────────────────
function buildUnitChips(items, mode) {
  const wrap = document.getElementById('unit-chips');
  wrap.innerHTML = '';
  const unitSq = S.bankInfo.unit_sq || {};

  const all = document.createElement('button');
  all.className = 'chip active';
  all.textContent = '全部';
  all.dataset.val = '__all__';
  all.onclick = () => toggleChip(all, 'unit', mode);
  wrap.appendChild(all);

  items.forEach(item => {
    const cnt = unitSq[item] || 0;
    const btn = document.createElement('button');
    btn.className = 'chip';
    btn.dataset.val = item;
    btn.innerHTML = `${esc(item)}<span class="chip-cnt">${cnt}</span>`;
    btn.onclick = () => toggleChip(btn, 'unit', mode);
    wrap.appendChild(btn);
  });
}

function buildModeChips(items, mode) {
  const wrap = document.getElementById('mode-chips');
  wrap.innerHTML = '';
  const all = document.createElement('button');
  all.className = 'chip active';
  all.textContent = '全部';
  all.dataset.val = '__all__';
  all.onclick = () => toggleChip(all, 'mode', mode);
  wrap.appendChild(all);
  items.forEach(item => {
    const btn = document.createElement('button');
    btn.className = 'chip';
    btn.textContent = item;
    btn.dataset.val = item;
    btn.onclick = () => toggleChip(btn, 'mode', mode);
    wrap.appendChild(btn);
  });
}

/** 章节搜索实时过滤 */
function filterUnitChips(query) {
  const q = query.trim().toLowerCase();
  document.querySelectorAll('#unit-chips .chip').forEach(c => {
    if (c.dataset.val === '__all__') { c.style.display = ''; return; }
    c.style.display = c.textContent.toLowerCase().includes(q) ? '' : 'none';
  });
}

/** 更新章节已选数量徽章 */
function updateUnitSelCount() {
  const el = document.getElementById('unit-sel-count');
  if (!el) return;
  el.textContent = CFG.units.has('__all__') ? '全部' : `已选 ${CFG.units.size} 个`;
}

function toggleChip(btn, type, mode) {
  const val = btn.dataset.val;
  const set = type === 'unit' ? CFG.units : CFG.modes;
  const container = type === 'unit' ? 'unit-chips' : 'mode-chips';
  const chips = document.querySelectorAll(`#${container} .chip`);

  if (val === '__all__') {
    set.clear(); set.add('__all__');
    chips.forEach(c => c.classList.toggle('active', c.dataset.val === '__all__'));
  } else {
    set.delete('__all__');
    if (set.has(val)) { set.delete(val); btn.classList.remove('active'); }
    else              { set.add(val);    btn.classList.add('active'); }
    if (set.size === 0) {
      set.add('__all__');
      chips.forEach(c => c.classList.toggle('active', c.dataset.val === '__all__'));
    } else {
      chips.forEach(c => c.classList.toggle('active',
          c.dataset.val !== '__all__' && set.has(c.dataset.val)));
      document.querySelector(`#${container} .chip[data-val="__all__"]`)?.classList.remove('active');
    }
  }
  if (type === 'unit') {
    updateUnitSelCount();
    _refreshQuotaFromUnits();
    _onUnitSelectionChange();
  }
}

/** 全选所有章节 */
function selectAllUnits() {
  const chips = document.querySelectorAll('#unit-chips .chip');
  CFG.units.clear();
  chips.forEach(c => {
    if (c.dataset.val !== '__all__') {
      CFG.units.add(c.dataset.val);
      c.style.display !== 'none' && c.classList.add('active');
    }
  });
  document.querySelector('#unit-chips .chip[data-val="__all__"]')?.classList.remove('active');
  updateUnitSelCount();
  _refreshQuotaFromUnits();
  _onUnitSelectionChange();
}

/** 反选：未选的选上，已选的取消 */
function invertUnits() {
  const chips = [...document.querySelectorAll('#unit-chips .chip')]
      .filter(c => c.dataset.val !== '__all__' && c.style.display !== 'none');
  const wasAll = CFG.units.has('__all__');
  CFG.units.clear();
  chips.forEach(c => {
    const v = c.dataset.val;
    if (wasAll || !c.classList.contains('active')) {
      CFG.units.add(v);
      c.classList.add('active');
    } else {
      c.classList.remove('active');
    }
  });
  if (CFG.units.size === 0) {
    CFG.units.add('__all__');
    document.querySelector('#unit-chips .chip[data-val="__all__"]')?.classList.add('active');
  } else {
    document.querySelector('#unit-chips .chip[data-val="__all__"]')?.classList.remove('active');
  }
  updateUnitSelCount();
  _refreshQuotaFromUnits();
  _onUnitSelectionChange();
}

/** 清空选择（回到"全部"）*/
function clearUnits() {
  CFG.units.clear();
  CFG.units.add('__all__');
  document.querySelectorAll('#unit-chips .chip').forEach(c => {
    c.classList.toggle('active', c.dataset.val === '__all__');
  });
  updateUnitSelCount();
  _refreshQuotaFromUnits();
  _onUnitSelectionChange();
}

// ── 精细配额 ──────────────────────────────────────────────────────
// ── 计分配置 ──────────────────────────────────────────────────────
function toggleScoring() {
  CFG.scoring = document.getElementById('cfg-scoring').checked;
  const panel = document.getElementById('scoring-panel');
  panel.style.display = CFG.scoring ? '' : 'none';
  if (!CFG.scoring) { panel.innerHTML = ''; return; }

  // 开启计分时强制打开题型精细配额（精确分配才能准确计分）
  const pmCk = document.getElementById('cfg-per-mode');
  if (pmCk && !pmCk.checked) {
    pmCk.checked = true;
    togglePerMode();
  }
  buildScoringPanel();
}

function buildScoringPanel() {
  const panel = document.getElementById('scoring-panel');
  panel.innerHTML = '';
  const modes = S.bankInfo.modes;
  if (!modes.length) return;

  // 判断是否有需要多选规则的题型
  const hasMultiMode = modes.some(m =>
      m.includes('X型') || m.includes('不定项') || m.includes('案例分析')
  );

  const wrap = document.createElement('div');
  wrap.className = 'scoring-panel';

  // 每题型分值输入
  modes.forEach(mode => {
    if (!CFG.scorePerMode[mode]) CFG.scorePerMode[mode] = 1;
    const row = document.createElement('div');
    row.className = 'scoring-rule-row';
    row.innerHTML = `
      <span class="scoring-rule-label">${esc(mode)}</span>
      <input class="scoring-rule-input" type="number" min="0.5" max="99" step="0.5"
             value="${CFG.scorePerMode[mode]}" data-mode="${esc(mode)}"
             oninput="updateScorePerMode(this)">
      <span class="scoring-rule-unit">分/小题</span>`;
    wrap.appendChild(row);
  });

  // 多选 / 案例分析计分规则
  if (hasMultiMode) {
    const ruleBox = document.createElement('div');
    ruleBox.className = 'scoring-multi-rule';
    ruleBox.innerHTML = `
      <div class="scoring-multi-rule-title">⚡ 案例分析 / X型题计分规则</div>
      <div class="scoring-multi-chips">
        <button class="scoring-multi-chip${CFG.multiScoreMode==='strict'?' active':''}"
                onclick="setMultiScoreMode('strict',this)">
          严格计分<br><span style="font-size:10px;font-weight:400">全对才得分</span>
        </button>
        <button class="scoring-multi-chip${CFG.multiScoreMode==='loose'?' active':''}"
                onclick="setMultiScoreMode('loose',this)">
          宽松计分<br><span style="font-size:10px;font-weight:400">按选项比例得分</span>
        </button>
      </div>`;
    wrap.appendChild(ruleBox);
  }

  // 总分预览行
  const totalRow = document.createElement('div');
  totalRow.className = 'mode-quota-total-row';
  totalRow.style.padding = '10px 14px 2px';
  totalRow.innerHTML = `<span>预计总分</span><strong id="scoring-total-val" style="font-size:18px">—</strong>`;
  wrap.appendChild(totalRow);

  panel.appendChild(wrap);
  _refreshScoringTotal();
}

function _refreshScoringTotal() {
  const el = document.getElementById('scoring-total-val');
  if (!el) return;
  if (!CFG.perMode || !Object.keys(CFG.perMode).length) {
    el.textContent = '—';
    el.style.color = 'var(--muted)';
    return;
  }
  let total = 0;
  Object.entries(CFG.perMode).forEach(([mode, sqCount]) => {
    total += sqCount * (CFG.scorePerMode[mode] || 1);
  });
  total = Math.round(total * 10) / 10;
  el.textContent = total + ' 分';
  el.style.color = 'var(--accent)';
}

function updateScorePerMode(input) {
  const mode = input.dataset.mode;
  const val  = Math.max(0.5, parseFloat(input.value) || 1);
  CFG.scorePerMode[mode] = val;
  _refreshScoringTotal();
}

function setMultiScoreMode(mode, btn) {
  CFG.multiScoreMode = mode;
  btn.closest('.scoring-multi-chips').querySelectorAll('.scoring-multi-chip')
      .forEach(b => b.classList.toggle('active', b === btn));
}

// ── 章节配额 ──────────────────────────────────────────────────────
function togglePerUnit() {
  const on = document.getElementById('cfg-per-unit').checked;
  const panel = document.getElementById('unit-quota-panel');
  panel.style.display = on ? '' : 'none';
  if (on) {
    CFG.perUnit = {};
    // 开启时若未选章节，自动全选所有章节方便配置
    if (CFG.units.has('__all__')) {
      // 全部状态下，使用所有章节
    }
    buildUnitQuotaPanel();
    // 开启章节配额时隐藏数量区（配额决定总数）
    document.getElementById('count-section').style.display = 'none';
  } else {
    CFG.perUnit = null;
    document.getElementById('count-section').style.display = '';
    panel.innerHTML = '';
  }
}

function _getActiveUnits() {
  // 返回当前生效的章节列表
  if (CFG.units.has('__all__')) return S.bankInfo.units;
  return [...CFG.units].filter(u => S.bankInfo.units.includes(u));
}

function buildUnitQuotaPanel() {
  const panel = document.getElementById('unit-quota-panel');
  panel.innerHTML = '';
  const units  = _getActiveUnits();
  const unitSq = S.bankInfo.unit_sq || {};
  if (!units.length) { panel.innerHTML = '<div style="font-size:12px;color:var(--muted);padding:8px 0">请先选择章节</div>'; return; }

  const wrap = document.createElement('div');
  wrap.className = 'unit-quota-panel';

  // 批量设置栏
  const batchBar = document.createElement('div');
  batchBar.className = 'unit-quota-batch-bar';
  batchBar.innerHTML = `
    <span>批量设置</span>
    <input class="unit-quota-batch-input" id="unit-batch-val" type="number"
           min="1" value="20" placeholder="题数">
    <button class="unit-quota-batch-btn" onclick="applyUnitQuotaBatch('fixed')">每章固定题数</button>
    <button class="unit-quota-batch-btn ratio" onclick="applyUnitQuotaBatch('ratio')">按比例分配</button>`;
  wrap.appendChild(batchBar);

  // 章节列表
  const list = document.createElement('div');
  list.className = 'unit-quota-list';
  units.forEach(unit => {
    const avail = unitSq[unit] || 0;
    const def   = Math.min(20, avail);
    CFG.perUnit[unit] = def;
    const row = document.createElement('div');
    row.className = 'unit-quota-row';
    row.innerHTML = `
      <span class="unit-quota-name" title="${esc(unit)}">${esc(unit)}</span>
      <span class="unit-quota-avail">共${avail}</span>
      <input class="unit-quota-input" type="number" min="0" max="${avail}"
             value="${def}" data-unit="${esc(unit)}"
             oninput="updateUnitQuota(this)">`;
    list.appendChild(row);
  });
  wrap.appendChild(list);

  // 合计行
  const totalRow = document.createElement('div');
  totalRow.className = 'unit-quota-total-row';
  totalRow.innerHTML = `<span>合计小题数</span><strong id="unit-quota-total-val">0</strong>`;
  wrap.appendChild(totalRow);

  panel.appendChild(wrap);
  _refreshUnitQuotaTotal();
}

function updateUnitQuota(input) {
  const unit = input.dataset.unit;
  const max  = parseInt(input.max) || 9999;
  const val  = Math.max(0, Math.min(parseInt(input.value) || 0, max));
  input.value = val;
  if (CFG.perUnit) CFG.perUnit[unit] = val;
  _refreshUnitQuotaTotal();
}

function _refreshUnitQuotaTotal() {
  if (!CFG.perUnit) return;
  const total = Object.values(CFG.perUnit).reduce((a, b) => a + b, 0);
  const el = document.getElementById('unit-quota-total-val');
  if (el) el.textContent = total;
}

function applyUnitQuotaBatch(mode) {
  if (!CFG.perUnit) return;
  const batchVal = parseInt(document.getElementById('unit-batch-val').value) || 20;
  const units    = _getActiveUnits();
  const unitSq   = S.bankInfo.unit_sq || {};

  if (mode === 'fixed') {
    // 每章固定题数（不超过该章上限）
    units.forEach(unit => {
      const avail = unitSq[unit] || 0;
      const val   = Math.min(batchVal, avail);
      CFG.perUnit[unit] = val;
      const input = document.querySelector(`.unit-quota-input[data-unit="${CSS.escape(unit)}"]`);
      if (input) input.value = val;
    });
  } else {
    // 按比例：以 batchVal 为总题数，按各章可用数等比分配
    const totalAvail = units.reduce((s, u) => s + (unitSq[u] || 0), 0);
    if (!totalAvail) return;
    let allocated = 0;
    units.forEach((unit, i) => {
      const avail = unitSq[unit] || 0;
      let val;
      if (i === units.length - 1) {
        val = Math.max(0, batchVal - allocated);
      } else {
        val = Math.round(batchVal * avail / totalAvail);
        allocated += val;
      }
      val = Math.min(val, avail);
      CFG.perUnit[unit] = val;
      const input = document.querySelector(`.unit-quota-input[data-unit="${CSS.escape(unit)}"]`);
      if (input) input.value = val;
    });
  }
  _refreshUnitQuotaTotal();
}

// 章节变化时重建配额面板（只对已勾选的章节显示行）
function _onUnitSelectionChange() {
  if (CFG.perUnit !== null) {
    buildUnitQuotaPanel();
  }
}

function togglePerMode() {
  const on = document.getElementById('cfg-per-mode').checked;

  // 互斥：关闭数量精细比例
  if (on) {
    const crCk = document.getElementById('cfg-count-ratio');
    if (crCk) { crCk.checked = false; toggleCountRatio(); }
  }

  document.getElementById('mode-filter-panel').style.display = on ? 'none' : '';
  document.getElementById('mode-quota-panel').style.display  = on ? '' : 'none';
  document.getElementById('count-section').style.display     = on ? 'none' : '';
  if (on) {
    CFG.perMode    = {};
    CFG.countRatio = null;
    buildModeQuota();
  } else {
    CFG.perMode = null;
  }
}

function toggleCountRatio() {
  const on = document.getElementById('cfg-count-ratio').checked;

  // 互斥：关闭题型精细配额
  if (on) {
    const pmCk = document.getElementById('cfg-per-mode');
    if (pmCk && pmCk.checked) {
      pmCk.checked = false;
      togglePerMode();
    }
  }

  document.getElementById('count-ratio-panel').style.display = on ? '' : 'none';
  if (on) {
    CFG.countRatio = {};
    CFG.perMode    = null;
    buildCountRatioPanel();
  } else {
    CFG.countRatio = null;
  }
}

function buildCountRatioPanel() {
  const panel = document.getElementById('count-ratio-panel');
  panel.innerHTML = '';
  const modes  = S.bankInfo.modes;
  if (!modes.length) return;

  // 默认等比例
  const equalPct = Math.floor(100 / modes.length);
  modes.forEach((m, i) => {
    CFG.countRatio[m] = i < modes.length - 1 ? equalPct : 100 - equalPct * (modes.length - 1);
  });

  const list = document.createElement('div');
  list.className = 'count-ratio-list';

  modes.forEach(mode => {
    const pct = CFG.countRatio[mode];
    const row = document.createElement('div');
    row.className = 'count-ratio-row';
    row.innerHTML = `
      <span class="count-ratio-label" title="${esc(mode)}">${esc(mode)}</span>
      <input type="range" min="0" max="100" value="${pct}" style="flex:1.5"
             data-mode="${esc(mode)}" oninput="updateCountRatio(this)">
      <span class="count-ratio-pct" id="crp-pct-${_modeId(mode)}">${pct}%</span>
      <span class="count-ratio-expected" id="crp-exp-${_modeId(mode)}">~0</span>`;
    list.appendChild(row);
  });

  const totalRow = document.createElement('div');
  totalRow.className = 'count-ratio-total-row';
  totalRow.innerHTML = `<span>合计比例 <span id="cr-total-pct" style="color:var(--text)">0%</span></span>
    <span>预计 <strong id="cr-total-exp">0</strong> 题</span>`;
  list.appendChild(totalRow);

  panel.appendChild(list);
  _refreshCountRatioDisplay();
}

/** mode 名称转安全 id 片段 */
function _modeId(mode) {
  return mode.replace(/[^a-zA-Z0-9\u4e00-\u9fff]/g, '_');
}

function updateCountRatio(slider) {
  const mode = slider.dataset.mode;
  CFG.countRatio[mode] = parseInt(slider.value);
  _refreshCountRatioDisplay();
}

function _refreshCountRatioDisplay() {
  const total = CFG.count;
  const ratioSum = Object.values(CFG.countRatio).reduce((a, b) => a + b, 0);
  document.getElementById('cr-total-pct').textContent = ratioSum + '%';
  document.getElementById('cr-total-pct').style.color =
      ratioSum === 100 ? 'var(--success)' : ratioSum > 100 ? 'var(--danger)' : 'var(--warning)';

  let totalExp = 0;
  S.bankInfo.modes.forEach(mode => {
    const pct = CFG.countRatio[mode] || 0;
    const exp = ratioSum > 0 ? Math.round(total * pct / ratioSum) : 0;
    totalExp += exp;
    const pctEl = document.getElementById(`crp-pct-${_modeId(mode)}`);
    const expEl = document.getElementById(`crp-exp-${_modeId(mode)}`);
    if (pctEl) pctEl.textContent = pct + '%';
    if (expEl) expEl.textContent = '~' + exp;
  });
  const totalEl = document.getElementById('cr-total-exp');
  if (totalEl) totalEl.textContent = totalExp;
}


// ── 精细配额 ──────────────────────────────────────────────────────

/**
 * 根据当前选中章节，计算各题型可用小题数，
 * 并刷新精细配额面板的「共 X」显示、input max、以及按比例重新建议配额值。
 */
function _getUnitModeSq() {
  const unitModeSq = S.bankInfo.unit_mode_sq || {};
  const isAll = CFG.units.has('__all__');
  const result = {};  // {mode: available_sq}
  S.bankInfo.modes.forEach(m => result[m] = 0);

  const units = isAll ? S.bankInfo.units : [...CFG.units];
  units.forEach(u => {
    const modeMap = unitModeSq[u] || {};
    Object.entries(modeMap).forEach(([m, n]) => {
      if (result[m] !== undefined) result[m] += n;
    });
  });
  return result;
}

function _refreshQuotaFromUnits() {
  // 同步更新数量滑杆的 max
  const avail = _getUnitModeSq();
  const totalAvail = Object.values(avail).reduce((a, b) => a + b, 0);
  const slider = document.getElementById('count-slider');
  if (slider) {
    const newMax = Math.min(totalAvail || S.bankInfo.total_sq, 300);
    slider.max = newMax;
    if (parseInt(slider.value) > newMax) {
      slider.value = newMax;
      updateCount(newMax);
    }
  }

  if (!CFG.perMode) return;   // 精细配额未开启，不需要刷新

  S.bankInfo.modes.forEach(mode => {
    const a = avail[mode] || 0;
    // 更新「共 X」文字
    const availEl = document.querySelector(`.mode-quota-avail[data-mode-avail="${CSS.escape(mode)}"]`);
    if (availEl) availEl.textContent = `共 ${a}`;
    // 更新 input max，并把超出的值截断
    const input = document.querySelector(`.mode-quota-input[data-mode="${CSS.escape(mode)}"]`);
    if (input) {
      input.max = a;
      const cur = parseInt(input.value) || 0;
      if (cur > a) {
        input.value = a;
        CFG.perMode[mode] = a;
      }
    }
  });
  updatePerModeTotal();
  _refreshScoringTotal();
}

function buildModeQuota() {
  const panel = document.getElementById('mode-quota-panel');
  panel.innerHTML = '';
  const modes = S.bankInfo.modes;
  const avail = _getUnitModeSq();   // 按当前章节计算

  const list = document.createElement('div');
  list.className = 'mode-quota-list';

  modes.forEach(mode => {
    const available = avail[mode] || 0;
    const def = Math.max(1, Math.round(available * 0.4));
    CFG.perMode[mode] = def;

    const row = document.createElement('div');
    row.className = 'mode-quota-row';
    row.innerHTML = `
      <span class="mode-quota-label" title="${esc(mode)}">${esc(mode)}</span>
      <span class="mode-quota-avail" data-mode-avail="${esc(mode)}">共 ${available}</span>
      <input class="mode-quota-input" type="number" min="0" max="${available}"
             value="${def}" data-mode="${esc(mode)}"
             oninput="updatePerModeCount(this)">`;
    list.appendChild(row);
  });

  const totalRow = document.createElement('div');
  totalRow.className = 'mode-quota-total-row';
  totalRow.innerHTML = `<span>合计小题数</span><strong id="mode-quota-total-val">0</strong>`;
  list.appendChild(totalRow);

  panel.appendChild(list);
  updatePerModeTotal();
}

function updatePerModeCount(input) {
  const mode = input.dataset.mode;
  const max  = parseInt(input.max) || 9999;
  const val  = Math.max(0, Math.min(parseInt(input.value) || 0, max));
  input.value = val;
  if (CFG.perMode) CFG.perMode[mode] = val;
  updatePerModeTotal();
  _refreshScoringTotal();
}

function updatePerModeTotal() {
  if (!CFG.perMode) return;
  const total = Object.values(CFG.perMode).reduce((a, b) => a + b, 0);
  const el = document.getElementById('mode-quota-total-val');
  if (el) el.textContent = total;
}

// ── 难度分布 ──────────────────────────────────────────────────────
const DIFF_PRESETS = [
  { label:'均衡', v:{easy:25, medium:45, hard:20, extreme:10} },
  { label:'偏易', v:{easy:45, medium:35, hard:15, extreme:5}  },
  { label:'偏难', v:{easy:10, medium:30, hard:40, extreme:20} },
  { label:'拉满', v:{easy:5,  medium:15, hard:40, extreme:40} },
];
const DIFF_INFO = [
  {key:'easy',    label:'简单', cls:'easy',    desc:'正确率 ≥80%'},
  {key:'medium',  label:'中等', cls:'medium',  desc:'60–80%'},
  {key:'hard',    label:'较难', cls:'hard',    desc:'40–60%'},
  {key:'extreme', label:'困难', cls:'extreme', desc:'＜40%'},
];

function toggleDifficulty() {
  const on = document.getElementById('cfg-difficulty').checked;
  document.getElementById('difficulty-panel').style.display = on ? '' : 'none';
  if (!on) { CFG.difficulty = null; return; }
  if (!document.getElementById('difficulty-panel').hasChildNodes()) buildDifficultyPanel();
  else CFG.difficulty = CFG.difficulty || {easy:25, medium:45, hard:20, extreme:10};
}

function buildDifficultyPanel() {
  CFG.difficulty = {easy:25, medium:45, hard:20, extreme:10};
  const panel = document.getElementById('difficulty-panel');
  panel.innerHTML = '';

  // 预设按钮
  const presets = document.createElement('div');
  presets.className = 'difficulty-presets';
  DIFF_PRESETS.forEach((p, i) => {
    const btn = document.createElement('button');
    btn.className = 'diff-preset' + (i === 0 ? ' active' : '');
    btn.textContent = p.label;
    btn.onclick = () => applyDiffPreset(p.v, btn);
    presets.appendChild(btn);
  });
  panel.appendChild(presets);

  // 四档滑杆
  const sliders = document.createElement('div');
  sliders.className = 'difficulty-sliders';
  DIFF_INFO.forEach(d => {
    const row = document.createElement('div');
    row.className = 'diff-row';
    row.innerHTML = `
      <span class="diff-dot ${d.cls}"></span>
      <span class="diff-label" title="${d.desc}">${d.label}</span>
      <input type="range" min="0" max="100" value="${CFG.difficulty[d.key]}"
             style="flex:1" data-key="${d.key}" oninput="updateDiffSlider(this)">
      <span class="diff-pct" id="diff-pct-${d.key}">${CFG.difficulty[d.key]}%</span>`;
    sliders.appendChild(row);
  });
  panel.appendChild(sliders);
}

function applyDiffPreset(v, btn) {
  CFG.difficulty = {...v};
  document.querySelectorAll('.diff-preset').forEach(b => b.classList.remove('active'));
  btn.classList.add('active');
  DIFF_INFO.forEach(d => {
    const slider = document.querySelector(`input[data-key="${d.key}"]`);
    if (slider) slider.value = v[d.key];
    const pct = document.getElementById(`diff-pct-${d.key}`);
    if (pct) pct.textContent = v[d.key] + '%';
  });
}

function updateDiffSlider(input) {
  if (!CFG.difficulty) CFG.difficulty = {};
  CFG.difficulty[input.dataset.key] = parseInt(input.value);
  const pct = document.getElementById(`diff-pct-${input.dataset.key}`);
  if (pct) pct.textContent = input.value + '%';
  document.querySelectorAll('.diff-preset').forEach(b => b.classList.remove('active'));
}

function selectTime(btn, val) {
  CFG.examTime = val;
  document.querySelectorAll('#time-chips .chip').forEach(c => c.classList.toggle('active', Number(c.dataset.val) === val));
}

function updateCount(v) {
  CFG.count = Number(v);
  const display = document.getElementById('count-display');
  display.textContent = v >= Number(document.getElementById('count-slider').max) ? '全部' : v;
  if (CFG.countRatio) _refreshCountRatioDisplay();
}

// ════════════════════════════════════════════
// 题型分组工具（考试模式导航限制核心）
// ════════════════════════════════════════════

/**
 * 根据题目列表构建题型分组信息。
 * allowBack: 组内是否允许回退（案例分析题不允许）
 */
/** 判断是否为多选题：X型题 / 案例分析 强制多选；其他题型看答案长度 */
function isMultiQ(q) {
  if (!q) return false;
  const mode = (q.mode || '').trim();
  if (mode.includes('X型') || mode.includes('不定项') || mode.includes('案例分析')) return true;
  return q.answer && q.answer.length > 1;
}

function buildModeGroups(questions) {
  const groups = [];
  questions.forEach((q, i) => {
    const mode = q.mode || '';
    if (groups.length === 0 || groups[groups.length - 1].mode !== mode) {
      // 不允许回退的题型：A3/A4型、案例分析
      // A1/A2型 是合并题型，允许回退（视同 A1/A2 的混合）
      const mu = mode.toUpperCase();
      const isNoBack = (mu.includes('案例') ||
        (mu.includes('A3') && !mu.includes('A1') && !mu.includes('A2')) ||
        (mu.includes('A4') && !mu.includes('A1') && !mu.includes('A2')));
      const allowBack = !isNoBack;
      groups.push({ mode, startIdx: i, endIdx: i, allowBack });
    } else {
      groups[groups.length - 1].endIdx = i;
    }
  });
  return groups;
}

/** 返回题目索引 qIdx 所在的组索引 */
function getGroupIdxForQ(qIdx) {
  for (let i = 0; i < S.modeGroups.length; i++) {
    if (qIdx >= S.modeGroups[i].startIdx && qIdx <= S.modeGroups[i].endIdx) return i;
  }
  return 0;
}

/**
 * 考试模式：判断能否向前（+1）导航。
 * 跨题型组时不能直接导航——需要弹窗确认（由调用方处理）。
 */
function examCanGoBack(fromIdx) {
  if (fromIdx <= 0) return false;
  const gIdx    = getGroupIdxForQ(fromIdx);
  const prevGIdx= getGroupIdxForQ(fromIdx - 1);
  if (prevGIdx !== gIdx) return false;            // 跨组禁止
  if (!S.modeGroups[gIdx].allowBack) return false; // 案例分析禁止
  return true;
}

/** 弹出题型切换确认框，cb 为用户点「确认」后的回调 */
function showGroupTransitionDialog(targetMode, cb) {
  S._groupModalCb = cb;
  document.getElementById('gtm-mode').textContent = targetMode || '下一题型';
  document.getElementById('group-transition-modal').style.display = 'flex';
}
function confirmGroupModal() {
  document.getElementById('group-transition-modal').style.display = 'none';
  if (S._groupModalCb) { S._groupModalCb(); S._groupModalCb = null; }
}
function dismissGroupModal() {
  document.getElementById('group-transition-modal').style.display = 'none';
  S._groupModalCb = null;
}

// ════════════════════════════════════════════
// Start session
// ════════════════════════════════════════════
async function startSession() {
  const startBtn = document.getElementById('start-btn');
  const origText = startBtn.textContent;

  // 防止重复点击：按钮变为加载状态
  startBtn.disabled = true;
  startBtn.innerHTML = '<span class="btn-spinner"></span> 正在出题…';

  // 超时控制（15 秒）
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 15000);

  try {
  const params = new URLSearchParams();

  // 章节筛选
  if (!CFG.units.has('__all__')) CFG.units.forEach(u => params.append('unit', u));

  const isPU    = document.getElementById('cfg-per-unit')?.checked;
  const isPM    = document.getElementById('cfg-per-mode')?.checked;
  const isCR    = document.getElementById('cfg-count-ratio')?.checked;
  const isDiff  = document.getElementById('cfg-difficulty')?.checked;

  if (isPU && CFG.perUnit) {
    // ── 章节配额：传 per_unit JSON，章节 unit 参数由后端从 per_unit key 中推断 ──
    const nonZero = Object.fromEntries(
        Object.entries(CFG.perUnit).filter(([, v]) => v > 0)
    );
    if (!Object.keys(nonZero).length) { toast('请至少为一个章节设置题目数量'); return; }
    params.set('per_unit', JSON.stringify(nonZero));
    params.set('shuffle', '1');

  } else if (isPM && CFG.perMode) {
    // ── 题型精细配额：传 per_mode JSON，忽略题型 chips 和数量滑杆 ──
    const nonZero = Object.fromEntries(
        Object.entries(CFG.perMode).filter(([, v]) => v > 0)
    );
    if (!Object.keys(nonZero).length) { toast('请至少为一种题型设置题目数量'); return; }
    params.set('per_mode', JSON.stringify(nonZero));
    params.set('shuffle', '1');

  } else if (isCR && CFG.countRatio) {
    // ── 数量精细比例：按比例换算为 per_mode 传给后端 ──
    const total    = CFG.count;
    const ratioSum = Object.values(CFG.countRatio).reduce((a, b) => a + b, 0);
    if (!ratioSum) { toast('请至少为一种题型设置比例'); return; }
    const perMode = {};
    const modes   = Object.keys(CFG.countRatio).filter(m => CFG.countRatio[m] > 0);
    let allocated = 0;
    modes.forEach((m, i) => {
      const n = i < modes.length - 1
          ? Math.round(total * CFG.countRatio[m] / ratioSum)
          : total - allocated;
      if (n > 0) perMode[m] = n;
      allocated += n;
    });
    if (!Object.keys(perMode).length) { toast('请调整比例后重试'); return; }
    params.set('per_mode', JSON.stringify(perMode));
    params.set('shuffle', '1');

  } else {
    // ── 普通模式 ──
    if (!CFG.modes.has('__all__')) CFG.modes.forEach(m => params.append('mode', m));
    params.set('shuffle', document.getElementById('cfg-shuffle').checked ? '1' : '0');
    const maxSlider = Number(document.getElementById('count-slider').max);
    if (CFG.count < maxSlider) params.set('limit', CFG.count);
  }

  // 难度分布（两种模式均可叠加）
  if (isDiff && CFG.difficulty) {
    const nonZeroDiff = Object.fromEntries(
        Object.entries(CFG.difficulty).filter(([, v]) => v > 0)
    );
    if (Object.keys(nonZeroDiff).length) {
      params.set('difficulty', JSON.stringify(nonZeroDiff));
    }
  }

  // 考试模式：防作弊，服务端不下发答案
  if (S.mode === 'exam') params.set('sealed', '1');

  const data = await apiFetch('/api/questions?' + params + '&' + bankQS(), { signal: controller.signal }).then(r => r.json());
  clearTimeout(timeout);
  if (!data.items.length) { toast('没有符合条件的题目'); return; }

  S.questions = data.items;
  S.examId    = data.exam_id || null; // sealed 模式下服务端返回的 exam ID
  S.cur = 0;
  S.ans = {};
  S.marked = new Set();
  S.revealed = new Set();
  S.examStart = Date.now();
  S.examSubmitted = false;
  S.examReviewMode = false;
  S.streak = 0;
  S.questionTimes = {};
  S._qStartTime   = Date.now();
  S.modeGroups       = buildModeGroups(S.questions);
  S.currentGroupIdx  = 0;
  S.caseMaxReached   = {};
  // 练习模式：生成唯一 session ID 用于持久化
  S.practiceSessionId = S.mode === 'practice' ? String(Date.now()) : null;

  if (S.mode === 'memo') startMemo();
  else {
    startQuiz();
    // 考试模式：进入后立即保存初始快照，确保题目有记录
    if (S.mode === 'exam') saveExamSession();
  }

  } catch (e) {
    clearTimeout(timeout);
    if (e.name === 'AbortError') {
      toast('出题超时，请减少题目数量后重试');
    } else {
      toast('出题失败，请刷新页面重试');
    }
  } finally {
    startBtn.disabled = false;
    startBtn.textContent = origText;
  }
}

// ════════════════════════════════════════════
// QUIZ (practice + exam)
// ════════════════════════════════════════════
function startQuiz(remainingSeconds) {
  const isExam = S.mode === 'exam';
  clearInterval(S.timerInterval);
  if (typeof clearAICache === 'function') clearAICache();

  const timer = document.getElementById('quiz-timer');
  const gridToggle = document.getElementById('grid-toggle');
  const fill = document.getElementById('progress-fill');

  if (isExam) {
    if (remainingSeconds === undefined) {
      S.examLimit = CFG.examTime * 60;
      remainingSeconds = S.examLimit;
    }
    timer.style.display = '';
    timer.classList.remove('urgent');
    gridToggle.style.display = '';
    fill.className = 'progress-fill exam-fill';
    startTimer(remainingSeconds);
    buildGrid();
    _showCalcBtn(true);
  } else {
    timer.style.display = 'none';
    // 练习模式也显示题目列表按钮
    gridToggle.style.display = '';
    fill.className = 'progress-fill';
    document.getElementById('q-grid-panel').classList.remove('open');
    buildGrid();
    _showCalcBtn(false);
  }

  showScreen('s-quiz');
  renderQ('none');
}

// ─── 通用滑动切题助手 ─────────────────────────────────────────────
// dir: 'forward'(→) | 'back'(←) | 'none'(无动画，初次渲染)
let _slideCleanTimer = null;   // 追踪上一次动画的清理 setTimeout
let _autoAdvanceTimer = null;  // 练习模式答对后自动跳转计时器

function _slideQ(dir, buildFn) {
  const body = document.querySelector('.quiz-body');

  // ① 若上一次动画还没结束，立即取消清理计划并强制移除所有旧 stage，
  //    防止快速连点时多个 stage 堆叠。
  if (_slideCleanTimer) {
    clearTimeout(_slideCleanTimer);
    _slideCleanTimer = null;
  }
  body.querySelectorAll('.q-stage').forEach(el => el.remove());

  // ② 构建新 stage
  const newStage = document.createElement('div');
  newStage.className = 'q-stage';
  const wrap = document.createElement('div');
  wrap.className = 'question-wrap';
  wrap.id = 'question-wrap';
  newStage.appendChild(wrap);
  buildFn(wrap);

  if (dir === 'none') {
    body.appendChild(newStage);
    return;
  }

  // ③ 构建一个纯用于退出动画的"幽灵"占位 stage（空内容，仅负责滑出）
  //    这样新 stage 的内容就绝对不会和旧内容混在一起。
  const ghostStage = document.createElement('div');
  ghostStage.className = 'q-stage';
  ghostStage.style.background = 'var(--bg)';   // 用背景色盖住，干净退出
  body.appendChild(ghostStage);
  body.appendChild(newStage);

  // ④ 旧内容（ghost）滑出，新 stage 从对侧滑入
  const exitCls  = dir === 'forward' ? 'exit-left'  : 'exit-right';
  const enterCls = dir === 'forward' ? 'enter-right' : 'enter-left';
  ghostStage.classList.add(exitCls);
  newStage.classList.add(enterCls);

  const DUR = 230;
  _slideCleanTimer = setTimeout(() => {
    ghostStage.remove();
    newStage.classList.remove(enterCls);
    _slideCleanTimer = null;
  }, DUR);
}

function renderQ(dir = 'none') {
  // 记录上一题耗时（切题时才记录，初始渲染跳过）
  if (dir !== 'none' && S._qStartTime != null) {
    const prev = S.cur - (dir === 'forward' ? 1 : -1);
    if (prev >= 0 && prev < S.questions.length) {
      const elapsed = Math.round((Date.now() - S._qStartTime) / 1000);
      S.questionTimes[prev] = (S.questionTimes[prev] || 0) + elapsed;
    }
  }
  S._qStartTime = Date.now();

  // 取消上一题的自动跳转计时器，防止手动导航后计时器仍触发导致跳题或白屏
  if (_autoAdvanceTimer) { clearTimeout(_autoAdvanceTimer); _autoAdvanceTimer = null; }

  const q       = S.questions[S.cur];
  const total   = S.questions.length;
  const isExam  = S.mode === 'exam';
  const isPractice = S.mode === 'practice';

  if (!q) { toast('题目加载失败，请刷新重试'); return; }

  // ── 更新 Header（不随卡片动画，即时更新）──
  document.getElementById('q-cur').textContent = S.cur + 1;
  document.getElementById('q-total').textContent = total;
  document.getElementById('q-unit-tag').textContent = (S.mode === 'exam') ? '' : (q.unit || '');
  document.getElementById('progress-fill').style.width = ((S.cur + 1) / total * 100).toFixed(1) + '%';
  _updateFlagBtn();

  // ── Footer 按钮 ──
  const btnPrev = document.getElementById('btn-prev');
  const btnNext = document.getElementById('btn-next');
  if (isExam) {
    btnPrev.disabled = !examCanGoBack(S.cur);
    btnNext.textContent = S.cur === total - 1 ? '交卷 ✓' : '下一题 →';
    btnNext.className = 'nav-btn primary exam';
    btnNext.disabled  = false;
  } else {
    // 练习模式：完全自由导航，两端禁用即可
    btnPrev.disabled = S.cur === 0;
    btnNext.textContent = S.cur === total - 1 ? '完成 ✓' : '下一题 →';
    btnNext.className = 'nav-btn primary';
    btnNext.disabled  = false;
    updateGridDot();  // 练习模式也更新网格颜色
  }

  // ── 滑动切换，实际渲染在回调里 ──
  _slideQ(dir, wrap => _fillQ(wrap, q, isExam, isPractice));
}

function _fillQ(wrap, q, isExam, isPractice) {
  const isMulti    = isMultiQ(q);
  const correctSet = new Set(isMulti ? q.answer.split('') : [q.answer]);
  const curSel     = S.ans[S.cur];
  const isRevealed = S.revealed.has(S.cur);

  // Tags
  const tags = document.createElement('div');
  tags.className = 'q-tags';
  if (q.mode) tags.innerHTML += `<span class="q-tag mode-tag">${esc(q.mode)}</span>`;
  // 考试模式不显示章节（防止泄露分组信息），其他模式正常显示
  if (q.unit && !isExam) tags.innerHTML += `<span class="q-tag unit-tag">${esc(q.unit)}</span>`;
  if (q.rate != null && q.rate !== '' && q.rate !== undefined && !isExam) {
    let rateVal = q.rate;
    if (typeof rateVal === 'string') rateVal = parseFloat(rateVal.replace('%',''));
    if (!isNaN(rateVal) && rateVal > 0) {
      const rateClass = rateVal >= 70 ? 'easy' : '';
      tags.innerHTML += `<span class="q-tag rate-tag ${rateClass}">正确率 ${rateVal.toFixed(0)}%</span>`;
    }
  }
  wrap.appendChild(tags);

  // Stem
  if (q.stem) {
    const siblingCount = S.questions.filter(sq => sq.qi === q.qi).length;
    const label = siblingCount > 1
        ? `共用题干（共 ${siblingCount} 小题 · 第 ${q.si + 1} 题）`
        : '共用题干';
    const stemDiv = document.createElement('div');
    stemDiv.className = 'q-stem q-stem-collapsible';
    stemDiv.innerHTML = `
      <div class="q-stem-label">${label}<span class="stem-toggle-hint">▴ 点击收起</span></div>
      <div class="q-stem-content">${renderHTML(q.stem)}</div>`;
    stemDiv.querySelector('.q-stem-label').addEventListener('click', () => {
      const c = stemDiv.classList.toggle('is-collapsed');
      stemDiv.querySelector('.stem-toggle-hint').textContent = c ? '▾ 点击展开' : '▴ 点击收起';
    });
    wrap.appendChild(stemDiv);
  }

  // B型题共享选项块（渲染在题目文字上方，可折叠）
  if (q.shared_options && q.shared_options.length > 0) {
    const siblingCount = S.questions.filter(sq => sq.qi === q.qi).length;
    const soDiv = document.createElement('div');
    soDiv.className = 'q-shared-opts';
    const soLabel = siblingCount > 1
        ? `B型题共享选项（共 ${siblingCount} 小题 · 第 ${q.si + 1} 题）`
        : 'B型题共享选项';
    const soItems = q.shared_options.map((o, i) => {
      const l = String.fromCharCode(65 + i);
      const clean = o.replace(/^[A-Za-z]\s*[.．、·）)\s]\s*/u, '').trim();
      return `<div class="q-shared-opt-row">
        <span class="q-shared-opt-lbl">${l}</span>
        <span>${esc(clean)}</span>
      </div>`;
    }).join('');
    soDiv.innerHTML = `
      <div class="q-shared-opts-label" style="cursor:pointer">${soLabel}
        <span class="stem-toggle-hint">▴ 点击收起</span></div>
      <div class="q-shared-opts-content">${soItems}</div>`;
    soDiv.querySelector('.q-shared-opts-label').addEventListener('click', () => {
      const c = soDiv.classList.toggle('is-collapsed');
      soDiv.querySelector('.stem-toggle-hint').textContent = c ? '▾ 点击展开' : '▴ 点击收起';
    });
    wrap.appendChild(soDiv);
  }

  // Question text
  const qtxt = document.createElement('div');
  qtxt.className = 'q-text';
  const qContent = q.text || (q.options?.length ? '（请查看下方选项）' : '题目内容为空');
  // 应用诱导性关键词高亮
  const highlightedContent = highlightInductiveWords(renderHTML(qContent));
  qtxt.innerHTML = `<span class="q-num">${S.cur + 1}</span>${highlightedContent}`;
  wrap.appendChild(qtxt);

  // Multi badge
  if (isMulti) {
    const badge = document.createElement('div');
    badge.className = 'multi-badge';
    badge.innerHTML = '不定项';
    wrap.appendChild(badge);
  }

  // Options
  const opts = document.createElement('div');
  opts.className = 'options-list';
  q.options.forEach((opt, i) => {
    const letter = String.fromCharCode(65 + i);
    const btn = document.createElement('button');
    btn.className = 'opt';
    if (isRevealed) {
      const inCorrect = correctSet.has(letter);
      const wasSelected = isMulti
          ? (curSel instanceof Set ? curSel.has(letter) : false)
          : curSel === letter;
      if (inCorrect) btn.classList.add('correct');
      else if (wasSelected) btn.classList.add('wrong');
      else btn.classList.add('dim');
      btn.disabled = true;
    } else if (isExam) {
      const sel = isMulti ? (curSel instanceof Set && curSel.has(letter)) : curSel === letter;
      if (sel) btn.classList.add(isMulti ? 'multi-selected' : 'selected');
      btn.onclick = () => selectOpt(letter, btn);
    } else {
      const sel = isMulti ? (curSel instanceof Set && curSel.has(letter)) : curSel === letter;
      if (sel) btn.classList.add(isMulti ? 'multi-selected' : 'selected');
      btn.onclick = () => selectOpt(letter, btn);
    }
    // 剥离选项文本里可能自带的字母前缀（"A." "A、" "A．" 等），避免字母重复显示
    const cleanOpt = opt.replace(/^[A-Za-z]\s*[.．、·）)\s]\s*/u, '').trim();
    btn.innerHTML = `<span class="opt-label">${letter}</span><span class="opt-text">${esc(cleanOpt)}</span>`;
    opts.appendChild(btn);
  });
  wrap.appendChild(opts);

  // 多选确认按钮
  if (isMulti && !isRevealed) {
    const cfm = document.createElement('button');
    cfm.className = 'confirm-btn' + (isExam ? ' exam' : '');
    cfm.textContent = isExam ? '确认选择' : '提交答案';
    cfm.disabled = !(curSel instanceof Set && curSel.size > 0);
    cfm.onclick = () => submitMulti();
    wrap.appendChild(cfm);
  }

  // Explanation
  if (isPractice && isRevealed) {
    wrap.appendChild(buildExplain(q, curSel));
  } else if (isPractice) {
    const p = document.createElement('div');
    p.className = 'explain-panel';
    p.id = 'explain-panel';
    wrap.appendChild(p);
  }
}


function selectOpt(letter, btn) {
  const q = S.questions[S.cur];
  const isExam    = S.mode === 'exam';
  const isPractice= S.mode === 'practice';
  const isMulti   = isMultiQ(q);

  if (isMulti) {
    // ── 多选：toggle 当前项，更新视觉，激活确认按钮 ──
    if (!S.ans[S.cur] || !(S.ans[S.cur] instanceof Set)) {
      S.ans[S.cur] = new Set();
    }
    const sel = S.ans[S.cur];
    if (sel.has(letter)) {
      sel.delete(letter);
      btn.classList.remove('multi-selected');
      btn.querySelector('.opt-label').style.background = '';
      btn.querySelector('.opt-label').style.color = '';
    } else {
      sel.add(letter);
      btn.classList.add('multi-selected');
    }
    // 更新确认按钮状态
    const cfm = document.querySelector('.confirm-btn');
    if (cfm) cfm.disabled = sel.size === 0;

  } else {
    // ── 单选 ──
    S.ans[S.cur] = letter;

    if (isExam) {
      // 回看模式：不允许修改答案
      if (S.examReviewMode) { toast('回看模式，答案不可修改'); return; }
      // 考试：标记已选
      document.querySelectorAll('.opt').forEach(b => b.classList.remove('selected'));
      btn.classList.add('selected');
      const dot = document.querySelector(`.q-dot[data-idx="${S.cur}"]`);
      if (dot) { dot.classList.add('answered'); }
      saveExamSession();   // 答题后立即保存

      // A3/A4/案例分析题不自动跳转，需要用户手动点下一题
      const _curGIdx = getGroupIdxForQ(S.cur);
      const _curGroup = S.modeGroups[_curGIdx];
      if (_curGroup && !_curGroup.allowBack) {
        updateGridDot();
        return; // 不自动前进
      }

      // 其他题型：自动前进（短暂延迟让用户看到选中态）
      setTimeout(() => {
        const total = S.questions.length;
        if (S.cur < total - 1) {
          const nextIdx  = S.cur + 1;
          const curGIdx  = getGroupIdxForQ(S.cur);
          const nextGIdx = getGroupIdxForQ(nextIdx);
          const curGroup = S.modeGroups[curGIdx];
          if (!curGroup.allowBack) {
            S.caseMaxReached[curGIdx] = Math.max(S.caseMaxReached[curGIdx] ?? nextIdx, nextIdx);
          }
          if (nextGIdx !== curGIdx) {
            const nextGroup = S.modeGroups[nextGIdx];
            showGroupTransitionDialog(nextGroup.mode, () => {
              S.currentGroupIdx = nextGIdx;
              if (!nextGroup.allowBack) S.caseMaxReached[nextGIdx] = S.caseMaxReached[nextGIdx] ?? nextIdx;
              S.cur = nextIdx; renderQ('forward'); updateGridDot();
            });
          } else {
            S.cur++; renderQ('forward'); updateGridDot();
          }
        } else { document.getElementById('btn-next').textContent = '交卷 ✓'; document.getElementById('btn-next').disabled = false; }
      }, 250);

    } else {
      // 练习：立即揭示答案，原地重渲
      document.querySelectorAll('.opt').forEach(b => b.disabled = true);
      S.revealed.add(S.cur);
      const isCorrectAns = (letter === q.answer);
      _trackStreak(isCorrectAns);
      savePracticeSession();
      const answeredIdx = S.cur;
      if (_zenMode) {
        // 极简模式：直接渲染结果，无闪动
        setTimeout(() => {
          if (S.cur !== answeredIdx) return;
          renderQ('none');  // 更新选项颜色（字母圈绿/红）
          if (isCorrectAns) {
            // 答对：250ms 后自动切下一题（快速）
            _autoAdvanceTimer = setTimeout(() => {
              _autoAdvanceTimer = null;
              if (S.cur !== answeredIdx) return;
              const total = S.questions.length;
              if (S.cur < total - 1) { S.cur++; renderQ('forward'); savePracticeSession(); }
              else finishPractice();
            }, 250);
          }
          // 答错：停留，用户自行左右滑动切题
        }, 130);
      } else {
        // 普通练习模式
        const capturedIdx = S.cur;
        setTimeout(() => {
          if (S.cur !== capturedIdx) return;
          renderQ('none');
          if (letter === q.answer) {
            _autoAdvanceTimer = setTimeout(() => {
              _autoAdvanceTimer = null;
              if (S.cur !== capturedIdx) return;
              const total = S.questions.length;
              if (S.cur < total - 1) { S.cur++; renderQ('forward'); savePracticeSession(); }
              else finishPractice();
            }, 1000);
          }
          setTimeout(() => {
            const explain = document.getElementById('explain-panel');
            if (explain) explain.scrollIntoView({ behavior:'smooth', block:'nearest' });
          }, 100);
        }, 180);
      }
    }
  }
}

// 多选提交（练习 + 考试共用）
function submitMulti() {
  const q        = S.questions[S.cur];
  const isExam   = S.mode === 'exam';
  const isPractice = S.mode === 'practice';
  const sel      = S.ans[S.cur]; // Set<string>
  if (!sel || sel.size === 0) return;
  if (isExam && S.examReviewMode) { toast('回看模式，答案不可修改'); return; }

  if (isExam) {
    // 考试模式：标记已答，更新小地图，直接跳下一题
    const dot = document.querySelector(`.q-dot[data-idx="${S.cur}"]`);
    if (dot) dot.classList.add('answered');
    saveExamSession();   // 多选提交后立即保存
    setTimeout(() => {
      const total = S.questions.length;
      if (S.cur < total - 1) {
        const nextIdx  = S.cur + 1;
        const curGIdx  = getGroupIdxForQ(S.cur);
        const nextGIdx = getGroupIdxForQ(nextIdx);
        // 案例分析组内推进：记录最远到达
        const curGroup = S.modeGroups[curGIdx];
        if (!curGroup.allowBack) {
          S.caseMaxReached[curGIdx] = Math.max(S.caseMaxReached[curGIdx] ?? nextIdx, nextIdx);
        }
        if (nextGIdx !== curGIdx) {
          const nextGroup = S.modeGroups[nextGIdx];
          showGroupTransitionDialog(nextGroup.mode, () => {
            S.currentGroupIdx = nextGIdx;
            if (!nextGroup.allowBack) S.caseMaxReached[nextGIdx] = S.caseMaxReached[nextGIdx] ?? nextIdx;
            S.cur = nextIdx; renderQ('forward'); updateGridDot();
          });
        } else {
          S.cur++; renderQ('forward'); updateGridDot();
        }
      } else { document.getElementById('btn-next').textContent = '交卷 ✓'; document.getElementById('btn-next').disabled = false; }
    }, 120);

  } else {
    // 练习模式：揭示答案，原地重渲
    S.revealed.add(S.cur);
    // 多选正确判定：选中集合与正确集合完全一致
    const correctSet = new Set(q.answer.split(''));
    const isCorrect = sel.size === correctSet.size && [...correctSet].every(l => sel.has(l));
    _trackStreak(isCorrect);
    savePracticeSession();  // 保存进度
    const answeredIdx = S.cur;
    setTimeout(() => {
      if (S.cur !== answeredIdx) return;
      renderQ('none');
      setTimeout(() => {
        const explain = document.getElementById('explain-panel');
        if (explain) explain.scrollIntoView({ behavior:'smooth', block:'nearest' });
      }, 100);
      document.getElementById('btn-next').disabled = false;
    }, 180);
  }
}

function buildExplain(q, selected) {
  const isMulti   = isMultiQ(q);
  const correctSet= new Set(isMulti ? q.answer.split('') : [q.answer]);
  // 判断是否完全正确
  let isCorrect;
  if (isMulti) {
    const selSet = selected instanceof Set ? selected : new Set();
    isCorrect = selSet.size === correctSet.size &&
        [...correctSet].every(l => selSet.has(l));
  } else {
    isCorrect = selected === q.answer;
  }

  const panel = document.createElement('div');
  panel.className = 'explain-panel open';
  panel.id = 'explain-panel';

  const inner = document.createElement('div');
  inner.className = 'explain-inner';

  const resultRow = document.createElement('div');
  resultRow.className = `explain-result ${isCorrect ? 'ok' : 'err'}`;
  resultRow.innerHTML = `
    <span class="result-icon">${isCorrect ? '✅' : '❌'}</span>
    <span class="result-title ${isCorrect ? 'ok' : 'err'}">${isCorrect ? '回答正确！' : (isMulti ? '答案不完整或有误' : '回答错误')}</span>`;
  inner.appendChild(resultRow);

  if (!isCorrect) {
    const corrRow = document.createElement('div');
    corrRow.className = 'explain-correct-row';
    if (isMulti) {
      corrRow.innerHTML = `正确答案：<span class="multi-ans">${q.answer.split('').join(' ')}</span>`;
    } else {
      corrRow.innerHTML = `正确答案：<span>${q.answer}</span>`;
    }
    inner.appendChild(corrRow);
  }

  // B型题：在解析前展示共享选项，高亮正确答案
  if (q.shared_options && q.shared_options.length > 0) {
    const soBox = document.createElement('div');
    soBox.className = 'explain-shared-opts';
    const soTitle = document.createElement('div');
    soTitle.className = 'explain-shared-opts-title';
    soTitle.textContent = 'B型题共享选项';
    soBox.appendChild(soTitle);
    q.shared_options.forEach((o, i) => {
      const l = String.fromCharCode(65 + i);
      const clean = o.replace(/^[A-Za-z]\s*[.．、·）)\s]\s*/u, '').trim();
      const row = document.createElement('div');
      row.className = 'explain-shared-opt-row' + (correctSet.has(l) ? ' is-ans' : '');
      row.innerHTML = `<span class="opt-lbl">${l}</span><span>${esc(clean)}</span>`;
      soBox.appendChild(row);
    });
    inner.appendChild(soBox);
  }

  const body = document.createElement('div');
  body.className = 'explain-body';
  if (q.discuss) {
    body.innerHTML = renderHTML(q.discuss);
    if (q.point) body.innerHTML += `<div class="explain-point"><span>考点：</span>${esc(q.point)}</div>`;
  } else {
    body.innerHTML = '<span style="color:var(--muted)">暂无解析</span>';
  }
  inner.appendChild(body);
  panel.appendChild(inner);
  // AI Q&A panel — placed below the explain box, not inside it
  const userAns = isMulti ? (selected instanceof Set ? [...selected].sort().join('') : '') : (selected || '');
  _loadQuizAI().then(() => initAIPanel(panel, q, q.si || 0, userAns)).catch(() => {});
  return panel;
}

function nextOrSubmit() {
  const total = S.questions.length;
  if (S.mode === 'exam' && S.cur === total - 1) {
    // 检查是否全部作答
    const unanswered = S.questions.filter((_, i) => {
      const sel = S.ans[i];
      return !sel || (sel instanceof Set && sel.size === 0);
    }).length;
    if (unanswered > 0) {
      // 还有未答题目，显示确认弹窗
      showExamSubmitConfirm(unanswered, total);
    } else {
      // 全部答完：进入回看模式（可自由浏览但不可修改）
      _enterExamReview();
    }
    return;
  }
  if (S.mode === 'practice' && S.cur === total - 1) { finishPractice(); return; }

  if (S.mode === 'exam') {
    const nextIdx  = S.cur + 1;
    const curGIdx  = getGroupIdxForQ(S.cur);
    const nextGIdx = getGroupIdxForQ(nextIdx);
    const curGroup = S.modeGroups[curGIdx];

    // A3/A4/案例分析组：当前题未作答不允许前进
    if (curGroup && !curGroup.allowBack) {
      const sel = S.ans[S.cur];
      const answered = sel instanceof Set ? sel.size > 0 : (sel !== undefined && sel !== null && sel !== '');
      if (!answered) {
        toast('请先回答本题再前进');
        return;
      }
    }

    if (nextGIdx !== curGIdx) {
      // 跨题型组——弹窗提示
      const nextGroup = S.modeGroups[nextGIdx];
      showGroupTransitionDialog(nextGroup.mode, () => {
        S.currentGroupIdx = nextGIdx;
        // 案例分析组：记录最远到达位置
        if (!nextGroup.allowBack) {
          S.caseMaxReached[nextGIdx] = S.caseMaxReached[nextGIdx] ?? nextIdx;
        }
        S.cur = nextIdx;
        renderQ('forward');
        updateGridDot();
      });
      return;
    }

    // 同组内：若为案例分析，更新最远到达位置
    if (curGroup && !curGroup.allowBack) {
      S.caseMaxReached[curGIdx] = Math.max(S.caseMaxReached[curGIdx] ?? nextIdx, nextIdx);
    }
  }

  S.cur++;
  renderQ('forward');
  if (S.mode === 'exam') { updateGridDot(); saveExamSession(); }
  else                     savePracticeSession();
}

function prevQ() {
  if (S.mode === 'exam') {
    if (!examCanGoBack(S.cur)) {
      const gIdx = getGroupIdxForQ(S.cur);
      const g    = S.modeGroups[gIdx];
      if (!g.allowBack) toast('案例分析题不能回退');
      else               toast('已进入「' + g.mode + '」，不能返回上一题型');
      return;
    }
  }
  if (S.cur > 0) {
    S.cur--;
    renderQ('back');
    if (S.mode === 'exam') { updateGridDot(); saveExamSession(); }
    else                     savePracticeSession();
  }
}

// ── 极简刷题模式 ────────────────────────────────────────────────
var _zenMode = false;
var _zenHintTimer = null;
var _zenHintEl = null;

function _ensureZenHint() {
  if (!_zenHintEl) {
    _zenHintEl = document.createElement('div');
    _zenHintEl.className = 'zen-mode-hint';
    document.body.appendChild(_zenHintEl);
  }
  return _zenHintEl;
}
function _showZenHint(text) {
  var el = _ensureZenHint();
  el.textContent = text;
  el.classList.add('show');
  clearTimeout(_zenHintTimer);
  _zenHintTimer = setTimeout(function() { el.classList.remove('show'); }, 1300);
}
function enterZenMode() {
  _zenMode = true;
  var s = document.getElementById('s-quiz');
  if (s) s.classList.add('s-quiz-zen');
  _showZenHint('⚡ 极简模式  长按再次退出');
}
function exitZenMode() {
  _zenMode = false;
  var s = document.getElementById('s-quiz');
  if (s) s.classList.remove('s-quiz-zen');
  _showZenHint('已退出极简模式');
}

// 长按进度区 ≥500ms 进入/退出极简模式
(function() {
  var _zt = null, _zFired = false;
  function _start() {
    _zFired = false;
    _zt = setTimeout(function() {
      _zt = null; _zFired = true;
      if (S.mode === 'practice' || S.mode === 'exam') {
        if (_zenMode) exitZenMode(); else enterZenMode();
      }
    }, 500);
  }
  function _cancel() { clearTimeout(_zt); _zt = null; }
  document.addEventListener('DOMContentLoaded', function() {
    var el = document.getElementById('quiz-progress-wrap');
    if (!el) return;
    el.addEventListener('mousedown',   _start);
    el.addEventListener('touchstart',  _start, { passive: true });
    el.addEventListener('mouseup',     _cancel);
    el.addEventListener('mouseleave',  _cancel);
    el.addEventListener('touchend',    _cancel);
    el.addEventListener('touchcancel', _cancel);
  });
})();

// _zenFlash 已废弃：极简模式不再使用背景闪动效果

// ── 收藏系统 ────────────────────────────────────────────────────
const FAV_KEY      = 'med_exam_favorites';
const FAV_DATA_KEY = 'med_exam_fav_data';  // {fp:si → questionObj}

function _loadFavorites() {
  try { return new Set(JSON.parse(localStorage.getItem(FAV_KEY) || '[]')); }
  catch(_) { return new Set(); }
}
function _saveFavorites(set) {
  try { localStorage.setItem(FAV_KEY, JSON.stringify([...set])); } catch(_) {}
}

/** 读取收藏题目完整数据 */
function _loadFavData() {
  try { return JSON.parse(localStorage.getItem(FAV_DATA_KEY) || '{}'); }
  catch(_) { return {}; }
}
/** 保存一道题的完整数据（收藏时调用） */
function _saveFavQuestion(fp, q) {
  try {
    const data = _loadFavData();
    data[fp] = {
      fingerprint: q.fingerprint,
      si:      q.si ?? 0,
      mode:    q.mode    || '',
      unit:    q.unit    || '',
      text:    q.text    || q.stem || '',
      stem:    q.stem    || '',
      options: q.options || [],
      shared_options: q.shared_options || q.sharedOptions || [],
      answer:  q.answer  || '',
      discuss: q.discuss || '',
      rate:    q.rate    || '',
    };
    localStorage.setItem(FAV_DATA_KEY, JSON.stringify(data));
  } catch(_) {}
}
/** 删除一道题的缓存数据（取消收藏时调用） */
function _deleteFavQuestion(fp) {
  try {
    const data = _loadFavData();
    delete data[fp];
    localStorage.setItem(FAV_DATA_KEY, JSON.stringify(data));
  } catch(_) {}
}

function _curFingerprint() {
  const q = S.questions && S.questions[S.cur];
  return q ? (q.fingerprint + ':' + (q.si ?? 0)) : null;
}

function toggleFavorite() {
  const fp = _curFingerprint();
  if (!fp) return;
  const favs = _loadFavorites();
  const adding = !favs.has(fp);
  if (adding) {
    favs.add(fp);
    // 同时把完整题目数据缓存到本地，保证跨 session 可以刷题
    const q = S.questions && S.questions[S.cur];
    if (q) _saveFavQuestion(fp, q);
  } else {
    favs.delete(fp);
    _deleteFavQuestion(fp);
  }
  _saveFavorites(favs);
  _recordFavTimestamp(fp, adding);  // 记录收藏时间戳
  refreshFavBadge();                // 刷新主页徽章
  _updateFlagBtn();
  // 果冻动画
  const btn = document.getElementById('flag-btn');
  if (btn) {
    btn.classList.remove('jelly');
    void btn.offsetWidth; // reflow 触发重播
    btn.classList.add('jelly');
    btn.addEventListener('animationend', () => btn.classList.remove('jelly'), { once: true });
  }
  toast(adding ? '⭐ 已收藏' : '已取消收藏');
}

/** 更新 flag-btn 的标记(active) + 收藏(fav) 状态及图标 */
function _updateFlagBtn() {
  const btn = document.getElementById('flag-btn');
  if (!btn) return;
  const isMarked = S.marked.has(S.cur);
  const fp = _curFingerprint();
  const isFav = fp ? _loadFavorites().has(fp) : false;
  btn.classList.toggle('active', isMarked && !isFav);
  btn.classList.toggle('fav', isFav);
  btn.textContent = isFav ? '★' : '⚑';
  btn.title = isFav ? '已收藏（长按取消）' : '标记（长按收藏）';
}

// 长按检测（移动端 + PC 均支持）
(function _bindFlagLongPress() {
  let _timer = null;
  let _longFired = false;
  const LONG_MS = 450;

  function _start(e) {
    if (!e.currentTarget || e.currentTarget.id !== 'flag-btn') return;
    _longFired = false;
    _timer = setTimeout(() => {
      _timer = null;
      _longFired = true;
      toggleFavorite();
    }, LONG_MS);
  }
  function _cancel() { clearTimeout(_timer); _timer = null; }
  function _click(e) {
    _cancel();
    if (_longFired) {
      // 长按已触发，吃掉这次 click，重置标记
      _longFired = false;
      e.preventDefault();
      return;
    }
    // 短按：走原有标记逻辑
    toggleFlag();
  }

  document.addEventListener('DOMContentLoaded', function() {
    const btn = document.getElementById('flag-btn');
    if (!btn) return;
    btn.removeAttribute('onclick');
    btn.addEventListener('mousedown',   _start);
    btn.addEventListener('touchstart',  _start,  { passive: true });
    btn.addEventListener('mouseup',     _cancel);
    btn.addEventListener('mouseleave',  _cancel);
    btn.addEventListener('touchend',    _cancel);
    btn.addEventListener('touchcancel', _cancel);
    btn.addEventListener('click',       _click);
  });
})();

function toggleFlag() {
  if (S.marked.has(S.cur)) S.marked.delete(S.cur); else S.marked.add(S.cur);
  _updateFlagBtn();
  updateGridDot();
  if (S.mode === 'exam') saveExamSession();
}

function quitQuiz() {
  clearInterval(S.timerInterval);
  if (typeof clearAICache === 'function') clearAICache();
  if (_zenMode) exitZenMode();
  if (S.mode === 'exam') {
    if (!confirm('确认退出考试？本次记录不会保存')) {
      const elapsed = Math.floor((Date.now() - S.examStart) / 1000);
      startTimer(Math.max(0, S.examLimit - elapsed));
      return;
    }
    clearExamSession();
  } else if (S.mode === 'practice') {
    // 练习模式：自动保存进度，直接退出
    savePracticeSession();
    toast('练习进度已保存，下次可继续作答');
  }
  showScreen('s-home', 'back');
}

// ── Exam timer ──────────────────────────────
function startTimer(seconds) {
  let rem = seconds;
  let _warned5min = rem <= 300; // 如果进来时已低于5分钟，不重复提醒
  updateTimerDisp(rem);
  S.timerInterval = setInterval(() => {
    rem--;
    updateTimerDisp(rem);
    // 5 分钟倒计时提醒（只触发一次）
    if (rem === 300 && !_warned5min) {
      _warned5min = true;
      toast('⏰ 还剩 5 分钟，注意时间！', false, 4000);
      if (navigator.vibrate) navigator.vibrate([200, 100, 200]);
    }
    // 1 分钟再提醒一次
    if (rem === 60) {
      toast('⚠️ 还剩 1 分钟！', false, 3000);
      if (navigator.vibrate) navigator.vibrate([300, 100, 300, 100, 300]);
    }
    if (rem <= 0) { clearInterval(S.timerInterval); submitExam(); }
  }, 1000);
}
function updateTimerDisp(sec) {
  const m = String(Math.floor(sec / 60)).padStart(2,'0');
  const s = String(sec % 60).padStart(2,'0');
  document.getElementById('timer-display').textContent = `${m}:${s}`;
  if (sec < 300) document.getElementById('quiz-timer').classList.add('urgent');
}

// ── Exam grid ───────────────────────────────
function buildGrid() {
  const inner = document.getElementById('q-grid-inner');
  inner.innerHTML = '';
  const isPractice = S.mode === 'practice';
  let lastGIdx = -1;

  S.questions.forEach((_, i) => {
    const gIdx = getGroupIdxForQ(i);

    // 插入题型分组标签（练习模式和考试模式都支持）
    if (gIdx !== lastGIdx) {
      lastGIdx = gIdx;
      const lbl = document.createElement('div');
      lbl.className = 'q-grid-group-label';
      lbl.textContent = S.modeGroups[gIdx]?.mode || '题目';
      inner.appendChild(lbl);
    }

    const dot = document.createElement('div');
    dot.className = `q-dot${i === 0 ? ' cur' : ''}`;
    dot.dataset.idx = i;
    dot.textContent = i + 1;

    dot.onclick = () => {
      if (!isPractice && !S.examReviewMode) {
        // 考试模式（非回看）：保留原有限制
        const curGIdx    = getGroupIdxForQ(S.cur);
        const targetGIdx = getGroupIdxForQ(i);
        const targetG    = S.modeGroups[targetGIdx];
        if (targetGIdx < S.currentGroupIdx) { toast('不能返回已完成的题型'); return; }
        if (!targetG.allowBack && i < S.cur && targetGIdx === curGIdx) { toast('案例分析题不能回退'); return; }
        if (!targetG.allowBack && i > (S.caseMaxReached[targetGIdx] ?? targetG.startIdx)) { toast('请按顺序作答案例分析题'); return; }
        if (targetGIdx > curGIdx) {
          const captured = i;
          showGroupTransitionDialog(targetG.mode, () => {
            S.currentGroupIdx = targetGIdx;
            if (!targetG.allowBack) S.caseMaxReached[targetGIdx] = S.caseMaxReached[targetGIdx] ?? captured;
            S.cur = captured; renderQ('forward'); updateGridDot();
          });
          return;
        }
      }
      // 回看模式或练习模式：自由跳转
      const dir = i > S.cur ? 'forward' : i < S.cur ? 'back' : 'none';
      S.cur = i;
      renderQ(dir);
      if (!isPractice) updateGridDot();
    };
    inner.appendChild(dot);
  });
}
function updateGridDot() {
  const isPractice = S.mode === 'practice';
  document.querySelectorAll('.q-dot').forEach((dot, i) => {
    const sel     = S.ans[i];
    const hasAns  = sel !== undefined && !(sel instanceof Set && sel.size === 0);
    const revealed= S.revealed.has(i);
    dot.classList.toggle('cur', i === S.cur);

    if (isPractice) {
      // 练习模式：对绿错红，未作答灰
      dot.classList.remove('answered', 'correct', 'wrong', 'revealed', 'flagged');
      if (i !== S.cur) {
        if (revealed && hasAns) {
          const q = S.questions[i];
          const isMulti = isMultiQ(q);
          let ok = false;
          if (isMulti) {
            const correctSet = new Set(q.answer.split(''));
            const selSet = sel instanceof Set ? sel : new Set();
            ok = selSet.size === correctSet.size && [...correctSet].every(l => selSet.has(l));
          } else {
            ok = sel === q?.answer;
          }
          dot.classList.add(ok ? 'correct' : 'wrong');
        } else if (revealed) {
          dot.classList.add('revealed');
        }
        if (S.marked.has(i)) dot.classList.add('flagged');
      }
    } else {
      // 考试模式：原有逻辑
      dot.classList.remove('correct', 'wrong', 'revealed');
      dot.classList.toggle('answered', hasAns && i !== S.cur);
      dot.classList.toggle('flagged',  S.marked.has(i) && i !== S.cur);
    }
  });
}
function toggleGrid() {
  document.getElementById('q-grid-panel').classList.toggle('open');
}

// 点击题目列表区域外自动折叠
document.addEventListener('click', e => {
  const panel = document.getElementById('q-grid-panel');
  if (!panel || !panel.classList.contains('open')) return;
  const toggle = document.getElementById('grid-toggle');
  if (!panel.contains(e.target) && !toggle.contains(e.target)) {
    panel.classList.remove('open');
  }
});

// ── Submit ──────────────────────────────────
async function submitExam() {
  clearInterval(S.timerInterval);
  clearExamSession();   // 正常交卷，清除存档
  S.examSubmitted = true; // 防止任何路径重新保存
  const origMode = S.mode; // 保存原始模式用于 calculateResults
  S.mode = 'exam_done'; // 防止 beforeunload/setInterval 重新保存
  // 在 await reveal 之前记录交卷时间，防止 reveal 异步耗时导致 timeSec 偏大
  const submitAt = Date.now();

  // sealed 模式：从服务端获取答案后再评分
  if (S.examId) {
    try {
      const res = await apiFetch('/api/exam/reveal?id=' + encodeURIComponent(S.examId) + '&' + bankQS());
      if (res.ok) {
        const { answers } = await res.json();
        // 将答案填回题目
        S.questions.forEach(q => {
          const a = answers[q.fingerprint + ':' + q.si];
          if (a) { q.answer = a.answer; q.discuss = a.discuss; }
        });
        S.examId = null;
      } else {
        // 离线或服务端异常：提示稍后获取
        toast('⚠ 无法获取答案，请联网后重新打开应用自动补取', true);
        // 存储 examId 到 localStorage，供后续联网获取
        try {
          const pending = JSON.parse(localStorage.getItem('pending_exam_reveals') || '[]');
          pending.push({
            examId:  S.examId,
            bankID:  S.bankID,
            ts:      Date.now(),
            fps:     S.questions.map(q => q.fingerprint + ':' + q.si),
          });
          localStorage.setItem('pending_exam_reveals', JSON.stringify(pending.slice(-20)));
        } catch(e) {}
      }
    } catch(e) {
      toast('⚠ 网络异常，答案待联网后自动补取', true);
      try {
        const pending = JSON.parse(localStorage.getItem('pending_exam_reveals') || '[]');
        pending.push({
          examId:  S.examId,
          bankID:  S.bankID,
          ts:      Date.now(),
          fps:     S.questions.map(q => q.fingerprint + ':' + q.si),
        });
        localStorage.setItem('pending_exam_reveals', JSON.stringify(pending.slice(-20)));
      } catch(e2) {}
    }
  }

  // 再次确保清除（防止 await 期间被重新保存）
  clearExamSession();
  calculateResults(origMode, submitAt);
  showScreen('s-results');
}
function retryQuiz() {
  // 如果题目还在内存，直接重置状态重新做一遍
  if (S.questions && S.questions.length) {
    // 恢复原始模式（submitExam 将 mode 设为 'exam_done'）
    if (S.mode === 'exam_done' && S.results) S.mode = S.results.mode || 'exam';
    S.examSubmitted = false; // 重置提交标记
    S.examId = null;         // 清除密封 ID，重试不再走 reveal 流程（答案已填回）
    S.cur = 0;
    S.ans = {};
    S.revealed = new Set();
    S.marked = new Set();
    S.examStart = Date.now();
    S.streak = 0;
    S.modeGroups = buildModeGroups(S.questions);
    S.currentGroupIdx = 0;
    S.caseMaxReached = {};
    S.practiceSessionId = S.mode === 'practice' ? String(Date.now()) : null;
    if (S.mode === 'memo') startMemo();
    else {
      startQuiz();
      if (S.mode === 'exam') saveExamSession();
    }
  } else {
    // 题目已被清理，回到配置页重新出题
    openConfig(S.mode || 'practice');
  }
}

/**
 * 处理离线时未能取回的密封答案（pending_exam_reveals）。
 * 页面加载 / 题库切换后调用，遍历待处理队列，逐一向服务端请求答案，
 * 取回后填入对应的复盘缓存条目并重新统计正确率，最后更新本地历史记录。
 */
async function _processPendingReveals() {
  let pending;
  try {
    pending = JSON.parse(localStorage.getItem('pending_exam_reveals') || '[]');
  } catch(e) { return; }
  if (!pending.length) return;

  const now = Date.now();
  // 服务端 examSession 24 小时过期，超时条目直接丢弃
  const alive = pending.filter(p => now - p.ts < 23 * 3600 * 1000);
  const toProcess = alive.filter(p => p.bankID === S.bankID);
  const others    = alive.filter(p => p.bankID !== S.bankID);

  if (!toProcess.length) {
    localStorage.setItem('pending_exam_reveals', JSON.stringify(alive));
    return;
  }

  const remaining = [...others];
  let recovered = 0;

  for (const entry of toProcess) {
    try {
      const res = await apiFetch('/api/exam/reveal?id=' + encodeURIComponent(entry.examId) + '&bank=' + entry.bankID);
      if (!res.ok) continue; // 已过期或服务端无此记录，直接丢弃
      const { answers } = await res.json();
      if (!answers || !Object.keys(answers).length) continue;

      // 找到对应的复盘缓存条目（按 fps 集合匹配）
      const cacheKey = 'quiz-review-cache-b' + entry.bankID;
      let cache;
      try { cache = JSON.parse(localStorage.getItem(cacheKey) || '[]'); } catch(e) { continue; }

      // 用 entry.fps（"fp:si" 数组）匹配缓存里的题目
      const entryFpSet = new Set(entry.fps || []);
      let matched = false;
      cache = cache.map(c => {
        if (matched) return c;
        const cFps = (c.qs || []).map(q => (q.fingerprint || '') + ':' + (q.si ?? 0));
        const overlap = cFps.filter(f => entryFpSet.has(f)).length;
        if (overlap < entryFpSet.size * 0.9) return c; // 不是同一场考试
        matched = true;
        // 将答案填入 qs 并重新统计正确率
        let correct = 0;
        const newQs = (c.qs || []).map((q, idx) => {
          const key = (q.fingerprint || '') + ':' + (q.si ?? 0);
          const a = answers[key];
          if (a && a.answer) {
            const newQ = Object.assign({}, q, { answer: a.answer, discuss: a.discuss || q.discuss });
            // 判断该题是否答对，更新 correct 计数（用 idx 而非 indexOf，避免对象引用比较失效）
            const sel = c.ans ? c.ans[idx] : undefined;
            const selVal = (sel && sel.__set) ? new Set(sel.v) : sel;
            if (selVal) {
              const isMulti = (a.answer.length > 1);
              if (isMulti) {
                const cs = new Set(a.answer.split(''));
                const ss = selVal instanceof Set ? selVal : new Set([selVal]);
                if (ss.size === cs.size && [...cs].every(l => ss.has(l))) correct++;
              } else {
                if (selVal === a.answer) correct++;
              }
            }
            return newQ;
          }
          return q;
        });
        return Object.assign({}, c, { qs: newQs, _answersRecovered: true });
      });

      try { localStorage.setItem(cacheKey, JSON.stringify(cache)); } catch(e) {}
      recovered++;
    } catch(e) {
      // 网络仍不通，保留在队列里下次重试
      remaining.push(entry);
    }
  }

  localStorage.setItem('pending_exam_reveals', JSON.stringify(remaining));
  if (recovered > 0) {
    toast(`✅ 已补取 ${recovered} 份试卷答案，可在历史记录中查看解析`);
  }
}

function finishPractice() {
  if (_autoAdvanceTimer) { clearTimeout(_autoAdvanceTimer); _autoAdvanceTimer = null; }
  clearPracticeSession();  // 完成则删除进度
  calculateResults();
  showScreen('s-results');
}

// ════════════════════════════════════════════
// MEMO mode
// ════════════════════════════════════════════
function startMemo() {
  S.memoQueue   = [...Array(S.questions.length).keys()];
  S.memoKnown   = new Set();
  S.memoAgainSet= new Set();  // 本轮标记"再练"的
  S.memoRatings = {};          // {qi: quality(1/2/5)} 每题最终评分
  S.memoCur     = 0;
  S.memoRevealed= false;
  S.examStart   = Date.now(); // 背题模式也需要计时
  showScreen('s-memo');
  renderMemo('none');
}

function renderMemo(dir = 'forward') {
  const total   = S.questions.length;
  const known   = S.memoKnown.size;
  const again   = S.memoAgainSet.size;
  const left    = S.memoQueue.length - S.memoCur;

  // 进度
  document.getElementById('memo-cur').textContent = S.memoCur + 1;
  document.getElementById('memo-fill').style.width = (known / total * 100) + '%';
  document.getElementById('memo-known-count').textContent = `已掌握 ${known} / ${total}`;
  document.getElementById('memo-known-num').textContent = known;
  document.getElementById('memo-again-num').textContent = again;
  document.getElementById('memo-left-num').textContent  = Math.max(0, left);

  if (S.memoCur >= S.memoQueue.length) { showMemoResults(); return; }

  const qi = S.memoQueue[S.memoCur];
  const q  = S.questions[qi];

  // 重置按钮状态
  S.memoRevealed = false;
  document.getElementById('memo-reveal-btn').classList.remove('hidden');
  document.getElementById('memo-rate-row').classList.add('hidden');

  // 构建新卡片内容
  const answerSet = new Set(q.answer.length > 1 ? q.answer.split('') : [q.answer]);
  const metaTags = `<div class="memo-card-meta">
    ${q.mode ? `<span class="q-tag mode-tag">${esc(q.mode)}</span>` : ''}
    ${q.unit ? `<span class="q-tag unit-tag">${esc(q.unit)}</span>` : ''}
    ${q.answer.length > 1 ? `<span class="multi-badge">⊞ 多选</span>` : ''}
  </div>`;
  const stemHtml = q.stem ? `<div class="memo-stem-block">${renderHTML(q.stem)}</div>` : '';
  // B型题共享选项块
  const sharedOptsHtml = (q.shared_options && q.shared_options.length > 0)
      ? `<div class="memo-shared-opts">
        <div class="memo-shared-opts-title">B型题共享选项</div>
        ${q.shared_options.map((o,i) => {
        const l = String.fromCharCode(65+i);
        const co = o.replace(/^[A-Za-z]\s*[.．、·）)\s]\s*/u, '').trim();
        return `<div class="memo-shared-opt-row"><span class="opt-lbl">${l}</span><span>${esc(co)}</span></div>`;
      }).join('')}
      </div>`
      : '';
  const optsHtml = q.options.map((o, i) => {
    const l = String.fromCharCode(65 + i);
    const co = o.replace(/^[A-Za-z]\s*[.．、·）)\s]\s*/u, '').trim();
    return `<div class="memo-opt" id="mopt-${l}">
      <span class="memo-opt-label">${l}</span><span>${esc(co)}</span>
    </div>`;
  }).join('');
  const explainHtml = q.discuss
      ? `<div class="memo-explain-text">${renderHTML(q.discuss)}</div>
       ${q.point ? `<div class="memo-explain-point">考点：${esc(q.point)}</div>` : ''}`
      : `<div class="memo-explain-text" style="color:var(--muted)">暂无解析</div>`;

  const questionHtml = metaTags + stemHtml + sharedOptsHtml +
      `<div class="memo-q-text">${renderHTML(q.text)}</div>
     <div class="memo-opts">${optsHtml}</div>`;
  const answerHtml = `<div class="memo-answer-inner">
     <div class="memo-answer-label">正确答案</div>
     ${explainHtml}
   </div>`;

  // ── 滑动动画 ──────────────────────────────────
  const wrap   = document.getElementById('memo-card-wrap');
  const oldCard = document.getElementById('memo-card');
  const DUR = 210;

  if (!oldCard || dir === 'none') {
    // 初次渲染，无动画
    if (oldCard) {
      document.getElementById('memo-question-area').innerHTML = questionHtml;
      document.getElementById('memo-answer-area').innerHTML  = answerHtml;
      document.getElementById('memo-answer-area').classList.remove('revealed');
    }
    return;
  }

  // 新卡片
  const newCard = document.createElement('div');
  newCard.className = 'memo-card';
  newCard.id = 'memo-card';
  newCard.innerHTML = `
    <div class="memo-swipe-overlay" id="memo-swipe-overlay"></div>
    <div class="memo-card-question" id="memo-question-area">${questionHtml}</div>
    <div class="memo-card-answer" id="memo-answer-area">${answerHtml}</div>`;

  // 让 wrap 有固定高度容纳两张牌叠放
  const h = oldCard.offsetHeight;
  wrap.style.height = h + 'px';
  wrap.style.overflow = 'hidden';

  // 旧卡片绝对定位撑满 wrap
  oldCard.style.position = 'absolute';
  oldCard.style.inset = '0';
  oldCard.removeAttribute('id'); // 避免 id 冲突

  // 新卡片绝对定位
  newCard.style.position = 'absolute';
  newCard.style.inset = '0';
  wrap.appendChild(newCard);

  // 动画类
  const exitCls  = dir === 'forward' ? 'memo-exit-left'  : 'memo-exit-right';
  const enterCls = dir === 'forward' ? 'memo-enter-right' : 'memo-enter-left';
  oldCard.classList.add(exitCls);
  newCard.classList.add(enterCls);

  setTimeout(() => {
    oldCard.remove();
    newCard.classList.remove(enterCls);
    // 恢复正常流布局
    newCard.style.position = '';
    newCard.style.inset = '';
    wrap.style.height = '';
    wrap.style.overflow = '';
    // 滚动到顶
    document.querySelector('.memo-body').scrollTop = 0;
  }, DUR);
}

function memoReveal() {
  if (S.memoRevealed) return;
  S.memoRevealed = true;

  const qi = S.memoQueue[S.memoCur];
  const q  = S.questions[qi];
  const answerSet = new Set(q.answer.length > 1 ? q.answer.split('') : [q.answer]);

  // 高亮正确选项
  q.options.forEach((_, i) => {
    const l = String.fromCharCode(65 + i);
    const optEl = document.getElementById(`mopt-${l}`);
    if (optEl && answerSet.has(l)) {
      optEl.classList.add('is-answer');
    }
  });

  // 展开答案区
  document.getElementById('memo-answer-area').classList.add('revealed');
  document.getElementById('memo-reveal-btn').classList.add('hidden');
  document.getElementById('memo-rate-row').classList.remove('hidden');

  // 滚动显示答案
  setTimeout(() => {
    document.getElementById('memo-answer-area').scrollIntoView({ behavior:'smooth', block:'nearest' });
  }, 200);
}

/**
 * memoRate(quality) — 三档评分，对应 SM-2 quality：
 *   quality=5  已掌握（轻松答对）← 左滑 / Enter
 *   quality=2  模糊（有印象但不确定）← 中间按钮
 *   quality=1  忘了（完全不会）← 右滑 / R
 */
function memoRate(quality) {
  if (!S.memoRevealed) return;
  const qi = S.memoQueue[S.memoCur];
  S.memoRatings[qi] = quality; // 记录评分，结束时批量上传

  if (quality >= 4) {
    // 已掌握：不再重复
    S.memoKnown.add(qi);
    S.memoAgainSet.delete(qi);
  } else {
    // 模糊或忘了：标记需复习
    S.memoAgainSet.add(qi);
    S.memoKnown.delete(qi);
  }

  S.memoCur++;
  if (S.memoCur >= S.memoQueue.length) {
    const unknown = S.memoQueue.filter(i => !S.memoKnown.has(i));
    if (unknown.length === 0) { showMemoResults(); return; }
    S.memoQueue = unknown;
    S.memoCur   = 0;
    S.memoAgainSet.clear();
    toast(`还有 ${unknown.length} 题，再来一遍 💪`);
  }
  renderMemo('forward');
}

// 兼容旧调用点（HTML onclick / 键盘 / 手势）
function memoKnow()  { memoRate(5); }
function memoHard()  { memoRate(2); }
function memoAgain() { memoRate(1); }

function showMemoResults() {
  const total = S.questions.length;
  const known = S.memoKnown.size;
  S.results = { mode:'memo', total, correct: known, wrong: total - known, skip: 0,
    timeSec: Math.floor((Date.now() - S.examStart) / 1000) };

  // ── 修复 bug：背题模式必须也上传 SM-2 记录 ──
  // 之前 showMemoResults 直接 renderResults()，跳过了 calculateResults()，
  // 导致 SM-2 的下次复习时间永远不会更新。
  _recordMemoSession();

  showScreen('s-results');
  renderResults();
}

/** 把背题评分上传到服务端（携带 quality 字段供 SM-2 精确计算） */
async function _recordMemoSession() {
  if (!S.questions.length) return;
  const items = [];
  S.questions.forEach((q, qi) => {
    const fp = q.fingerprint;
    if (!fp) return;
    const quality = S.memoRatings[qi];   // 1/2/5；未评分（跳过）= undefined
    if (quality === undefined) return;   // 未翻牌的题不记录
    items.push({
      fingerprint: fp,
      result:  quality >= 4 ? 1 : 0,    // 服务端 attempts 表用 0/1
      quality: quality,                  // 精确质量分，progress.py 优先使用
      mode:    q.mode,
      unit:    q.unit,
    });
  });
  if (!items.length) return;

  const today = _localDate();
  const units  = [...new Set(items.map(it => it.unit).filter(Boolean))];
  const payload = {
    id:       'memo-' + String(Date.now()),
    mode:     'memo',
    total:    items.length,
    correct:  items.filter(it => it.result === 1).length,
    wrong:    items.filter(it => it.result === 0).length,
    skip:     0,
    time_sec: S.results.timeSec,
    date:     today,
    units,
    items,
  };
  try {
    await SyncManager.record(payload, S.bankID);
  } catch(e) {
    // 降级直接 POST
    try {
      await apiFetch('/api/record?' + bankQS(), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
      });
    } catch(e2) {
      console.warn('[Quiz] 背题记录上传失败:', e2);
    }
  }
}

// ════════════════════════════════════════════
// Results
// ════════════════════════════════════════════
function calculateResults(origMode, submitAt) {
  // origMode: 传入原始模式（submitExam 中 S.mode 已被改为 'exam_done'）
  const effectiveMode = origMode || S.mode;
  // 深拷贝题目数据，防止后续修改影响结果页面
  const qs = JSON.parse(JSON.stringify(S.questions));
  // 深拷贝答案数据（多选题保持 Set，避免后续 instanceof Set 判断失效导致多选全部判错）
  const ans = {};
  for (const [k, v] of Object.entries(S.ans)) {
    ans[k] = (v instanceof Set) ? new Set(v) : v;
  }

  let correct = 0, wrong = 0, skip = 0;
  const byUnit = {};

  qs.forEach((q, i) => {
    const sel  = ans[i];
    const unit = q.unit || '其他';
    if (!byUnit[unit]) byUnit[unit] = { correct: 0, total: 0 };
    byUnit[unit].total++;

    const isMulti = isMultiQ(q);
    let isCorrect = false;
    if (!sel || (sel instanceof Set && sel.size === 0)) {
      skip++;
      return;
    }
    if (isMulti) {
      const correctSet = new Set(q.answer.split(''));
      const selSet = sel instanceof Set ? sel : new Set([sel]);
      isCorrect = selSet.size === correctSet.size && [...correctSet].every(l => selSet.has(l));
    } else {
      isCorrect = sel === q.answer;
    }
    if (isCorrect) { correct++; byUnit[unit].correct++; }
    else wrong++;
  });

  // ── 记录最后一题耗时 ────────────────────────────────────────────
  if (S._qStartTime != null) {
    const last = S.cur;
    const elapsed = Math.round((Date.now() - S._qStartTime) / 1000);
    S.questionTimes[last] = (S.questionTimes[last] || 0) + elapsed;
    S._qStartTime = null;
  }

  // ── 最慢5题（耗时 > 5s 才计入，排除点错/忽略的情况）──────────────
  const slowest = qs
    .map((q, i) => ({ i, q, sec: S.questionTimes[i] || 0 }))
    .filter(x => x.sec >= 5)
    .sort((a, b) => b.sec - a.sec)
    .slice(0, 5);

  S.results = {
    mode: effectiveMode,
    total: qs.length, correct, wrong, skip,
    timeSec: Math.floor(((submitAt || Date.now()) - S.examStart) / 1000),
    timeLimit: S.examLimit || 0,
    byUnit,
    qs, ans,
    scoring: false,
    questionTimes: { ...S.questionTimes },
    slowest,
  };

  // ── 计分 ──────────────────────────────────────────────────────────
  if (CFG.scoring && effectiveMode === 'exam') {
    S.results.scoring      = true;
    S.results.scorePerMode = { ...CFG.scorePerMode };
    S.results.multiScoreMode = CFG.multiScoreMode;

    let totalScore = 0, earnedScore = 0;
    const scoreByMode = {};  // {mode: {earned, total}}

    qs.forEach((q, i) => {
      const mode      = q.mode || '';
      const perSq     = CFG.scorePerMode[mode] || 1;
      const sel       = S.ans[i];
      const isEmpty   = !sel || (sel instanceof Set && sel.size === 0);
      const isMulti   = isMultiQ(q);
      const correctSet= new Set((q.answer || '').split(''));

      if (!scoreByMode[mode]) scoreByMode[mode] = { earned: 0, total: 0 };
      scoreByMode[mode].total += perSq;
      totalScore += perSq;

      if (isEmpty) return;

      if (!isMulti) {
        // 普通单选：全对得满分
        if (sel === q.answer) {
          earnedScore += perSq;
          scoreByMode[mode].earned += perSq;
        }
      } else {
        const selSet = sel instanceof Set ? sel : new Set([sel]);
        if (CFG.multiScoreMode === 'strict') {
          // 严格：完全正确才得分
          const allCorrect = selSet.size === correctSet.size &&
              [...correctSet].every(l => selSet.has(l));
          if (allCorrect) {
            earnedScore += perSq;
            scoreByMode[mode].earned += perSq;
          }
        } else {
          // 宽松：每个正确选项得 perSq/总正确数，每个错选倒扣同额，最低0
          const optScore = perSq / correctSet.size;
          let got = 0;
          // 正确选中
          selSet.forEach(l => { if (correctSet.has(l))  got += optScore; });
          // 错误选中（多选 / 选了不在正确答案里的）
          selSet.forEach(l => { if (!correctSet.has(l)) got -= optScore; });
          got = Math.max(0, got);
          // 保留一位小数避免浮点噪声
          got = Math.round(got * 10) / 10;
          earnedScore += got;
          scoreByMode[mode].earned += got;
        }
      }
    });

    S.results.totalScore  = Math.round(totalScore  * 10) / 10;
    S.results.earnedScore = Math.round(earnedScore * 10) / 10;
    S.results.scoreByMode = scoreByMode;
  }

  // 预生成 sessionId，同时用于本地历史和服务端记录，确保可以按 id 删除
  const sessionId = String(Date.now());

  // Save to history
  const record = {
    id:      sessionId,
    mode: effectiveMode, total: qs.length, correct,
    pct: Math.round(correct / qs.length * 100),
    skip,
    timeSec: S.results.timeSec,
    time_sec: S.results.timeSec,
    time_limit: S.examLimit || 0,
    date: _localDate(),
    units: [...new Set(qs.map(q => q.unit).filter(Boolean))].slice(0,2).join('、'),
  };
  S.history.unshift(record);
  S.history = S.history.slice(0, 10);
  localStorage.setItem(historyKey(), JSON.stringify(S.history));

  // 复盘缓存：保存题目列表和答案，供历史记录"查看解析"使用
  try {
    const cacheKey = _reviewCacheKey();
    const cache = JSON.parse(localStorage.getItem(cacheKey) || '[]');
    cache.unshift({ id: sessionId, qs: S.questions, ans: _serializeAns(S.ans) });
    localStorage.setItem(cacheKey, JSON.stringify(cache.slice(0, 10)));
  } catch (e) { /* localStorage 满时静默忽略 */ }

  // 持久化到服务端（错题本 + SM-2 + 统计均由此驱动）
  _recordSessionToServer(S.results, S.questions, S.ans, sessionId).then(async () => {
    // 等待数据同步到服务端后再刷新主页徽章
    if (typeof SyncManager !== 'undefined') {
      try { await SyncManager.flush(); } catch(e) { /* 静默 */ }
    }
    _refreshProgressBadges();
  });

  renderResults();
}

function renderResults() {
  const R = S.results;
  // 来自实时做题时 qs 始终有值，重置按钮状态
  const reviewBtn = document.getElementById('res-review-detail-btn');
  if (reviewBtn && R && R.qs) {
    reviewBtn.disabled = false;
    reviewBtn.title = '';
  }
  const pct = Math.round(R.correct / R.total * 100);
  const pass = pct >= 60;

  document.getElementById('score-pct').textContent = pct + '%';
  document.getElementById('score-verdict').textContent = pass ? '🎉 通过！' : '继续努力';
  document.getElementById('score-sub').textContent =
      `答对 ${R.correct} 题，共 ${R.total} 题`;

  // Ring（r=80）
  const circumference = 2 * Math.PI * 80; // 502.65
  const ring = document.getElementById('ring-fill');
  ring.style.strokeDasharray = circumference;
  ring.style.strokeDashoffset = circumference;
  ring.className = `ring-fill ${pct >= 60 ? 'pass' : 'fail'}`;
  setTimeout(() => {
    ring.style.strokeDashoffset = circumference * (1 - pct / 100);
  }, 120);

  document.getElementById('res-correct').textContent = R.correct;
  document.getElementById('res-wrong').textContent   = R.wrong;
  document.getElementById('res-skip').textContent    = R.skip;
  document.getElementById('res-time').textContent    = formatTime(R.timeSec);

  // Unit breakdown
  const bk = document.getElementById('unit-breakdown');
  // 考试模式不显示章节分析（防止泄露分组信息）
  if (R.mode === 'exam') {
    bk.style.display = 'none';
  } else if (R.byUnit && Object.keys(R.byUnit).length > 1) {
    const entries = Object.entries(R.byUnit).sort((a,b) => b[1].total - a[1].total).slice(0, 8);
    bk.innerHTML = '<h3>章节分析</h3>' + entries.map(([unit, d]) => {
      const p = Math.round(d.correct / d.total * 100);
      const color = p >= 60 ? 'var(--success)' : 'var(--danger)';
      return `<div class="unit-row">
        <span class="unit-name">${esc(unit)}</span>
        <div class="unit-bar-wrap"><div class="unit-bar" style="width:${p}%;background:${color}"></div></div>
        <span class="unit-pct">${p}%</span>
      </div>`;
    }).join('');
    bk.style.display = '';
  } else {
    bk.style.display = 'none';
  }

  // ── 计分区块 ──────────────────────────────────────────────────────
  const sb = document.getElementById('score-block');
  if (R.scoring && R.scoreByMode) {
    const pctScore = R.totalScore > 0
        ? Math.round(R.earnedScore / R.totalScore * 100) : 0;
    const modeRows = Object.entries(R.scoreByMode).map(([mode, d]) => {
      const barPct = d.total > 0 ? Math.round(d.earned / d.total * 100) : 0;
      return `<div class="score-mode-row">
        <span class="score-mode-name">${esc(mode)}</span>
        <div class="score-mode-bar-wrap">
          <div class="score-mode-bar" style="width:${barPct}%"></div>
        </div>
        <span class="score-mode-val">${d.earned}/${d.total}</span>
      </div>`;
    }).join('');
    sb.innerHTML = `
      <div class="score-block-header">
        <span class="score-block-title">📊 得分详情
          <span style="font-size:11px;font-weight:400;color:var(--muted);margin-left:6px">
            ${R.multiScoreMode === 'loose' ? '宽松计分' : '严格计分'}
          </span>
        </span>
        <span class="score-block-total">${R.earnedScore}<span>/ ${R.totalScore} 分（${pctScore}%）</span></span>
      </div>
      ${modeRows}`;
    sb.style.display = '';
  } else {
    sb.style.display = 'none';
  }

  // ── 最慢5题 ───────────────────────────────────────────────────────
  let slowBlock = document.getElementById('slowest-block');
  if (!slowBlock) {
    slowBlock = document.createElement('div');
    slowBlock.id = 'slowest-block';
    slowBlock.className = 'slowest-block';
    // 插到计分区块后面
    sb.parentNode.insertBefore(slowBlock, sb.nextSibling);
  }
  if (R.slowest && R.slowest.length > 0) {
    slowBlock.innerHTML = `<h3 class="slowest-title">🐢 耗时最长的题目</h3>` +
      R.slowest.map(({ i, q, sec }) => {
        const preview = (q.text || q.stem || '').replace(/<[^>]+>/g, '').trim().slice(0, 60);
        const min = Math.floor(sec / 60);
        const s   = sec % 60;
        const timeStr = min > 0 ? `${min}分${s}秒` : `${s}秒`;
        const sel = R.ans[i];
        const isCorrect = (() => {
          if (!sel || (sel instanceof Set && sel.size === 0)) return null;
          if (isMultiQ(q)) {
            const cs = new Set(q.answer.split(''));
            const ss = sel instanceof Set ? sel : new Set([sel]);
            return ss.size === cs.size && [...cs].every(l => ss.has(l));
          }
          return sel === q.answer;
        })();
        const badge = isCorrect === null ? '⬜' : isCorrect ? '✅' : '❌';
        return `<div class="slowest-row" onclick="_jumpToReviewQ(${i})">
          <span class="slowest-badge">${badge}</span>
          <span class="slowest-num">第${i+1}题</span>
          <span class="slowest-preview">${esc(preview || '（无预览）')}</span>
          <span class="slowest-time">${timeStr}</span>
        </div>`;
      }).join('');
    slowBlock.style.display = '';
  } else {
    slowBlock.style.display = 'none';
  }

  // ── 本次错题按钮：有答错题目时显示 ─────────────────────────────────
  const swBtn = document.getElementById('res-wrongbook-btn');
  if (swBtn) {
    swBtn.style.display = (R.wrong > 0 && R.qs) ? '' : 'none';
  }
  // ── 分享试卷按钮（仅考试模式有意义）──────────────────────────────
  const shareBtn = document.getElementById('res-share-btn');
  if (shareBtn) {
    shareBtn.style.display = (R.qs && R.qs.length > 0) ? '' : 'none';
  }
  // ── AI 分析报告按钮（考试模式 + AI 已配置时显示）──────────────
  const aiReportBtn = document.getElementById('res-ai-report-btn');
  if (aiReportBtn) {
    const isExamDone = R.mode === 'exam' || R.mode === 'exam_done';
    const aiOk = S.bankInfo && S.bankInfo.ai_enabled;
    aiReportBtn.style.display = (isExamDone && aiOk) ? '' : 'none';
  }
  // 重置报告容器（重做后隐藏旧报告）
  // 重置 AI 报告弹层（重做后清空旧报告）
  const aiModal = document.getElementById('ai-report-modal');
  if (aiModal) aiModal.style.display = 'none';
  const aiMsgs = document.getElementById('ai-report-messages');
  if (aiMsgs) aiMsgs.innerHTML = '';
}

/** 生成并流式显示 AI 考试分析报告 */
function closeAIReport() {
  const modal = document.getElementById('ai-report-modal');
  if (modal) modal.style.display = 'none';
}

async function showAIReport() {
  const R = S.results;
  if (!R) return;

  // 先预加载 quiz_ai.js（含 makeStreamingRenderer / markedRender）
  try { await _loadQuizAI(); } catch(e) {}

  // 打开底部弹层
  const modal = document.getElementById('ai-report-modal');
  const messagesEl = document.getElementById('ai-report-messages');
  if (!modal || !messagesEl) return;
  modal.style.display = 'flex';
  messagesEl.innerHTML = '';

  // Loading 占位
  const loadingEl = document.createElement('div');
  loadingEl.className = 'ai-report-loading';
  loadingEl.innerHTML = '<span class="ai-spinner"></span> AI 正在分析考试报告，请稍候…';
  messagesEl.appendChild(loadingEl);

  // 辅助：反序列化 R.ans 中经 _serializeAns 序列化的 Set
  function _deser(v) {
    if (v && typeof v === 'object' && v.__set) return new Set(v.v || []);
    return v;
  }

  // ── 构建 payload（与原逻辑相同）────────────────────────────
  const byUnit = Object.entries(R.byUnit || {}).map(([unit, v]) => ({
    unit, correct: v.correct, total: v.total,
  })).sort((a, b) => (a.total > 0 ? a.correct/a.total : 1) - (b.total > 0 ? b.correct/b.total : 1));

  const byModeMap = {};
  (R.qs || []).forEach((q, i) => {
    const mode = q.mode || '未知';
    if (!byModeMap[mode]) byModeMap[mode] = { correct: 0, total: 0 };
    byModeMap[mode].total++;
    let sel = _deser(R.ans[i]);
    if (sel && !(sel instanceof Set && sel.size === 0)) {
      const isMulti = typeof isMultiQ === 'function' ? isMultiQ(q) : false;
      const isCorrect = isMulti
        ? (() => { const cs = new Set(q.answer.split('')); const ss = sel instanceof Set ? sel : new Set([sel]); return ss.size === cs.size && [...cs].every(l => ss.has(l)); })()
        : sel === q.answer;
      if (isCorrect) byModeMap[mode].correct++;
    }
  });
  const byMode = Object.entries(byModeMap).map(([mode, v]) => ({
    mode, correct: v.correct, total: v.total,
  })).sort((a, b) => (a.total > 0 ? a.correct/a.total : 1) - (b.total > 0 ? b.correct/b.total : 1));

  const wrongStat = [], wrongSample = [];
  (R.qs || []).forEach((q, i) => {
    let sel = _deser(R.ans[i]);
    if (!sel || (sel instanceof Set && sel.size === 0)) return;
    const isMulti = typeof isMultiQ === 'function' ? isMultiQ(q) : false;
    const isCorrect = isMulti
      ? (() => { const cs = new Set(q.answer.split('')); const ss = sel instanceof Set ? sel : new Set([sel]); return ss.size === cs.size && [...cs].every(l => ss.has(l)); })()
      : sel === q.answer;
    if (!isCorrect) {
      const userAns = sel instanceof Set ? [...sel].join('') : (sel || '');
      wrongStat.push({ unit: q.unit||'', mode: q.mode||'', answer: q.answer||'', user_ans: userAns });
      if (wrongSample.length < 20)
        wrongSample.push({ unit: q.unit||'', mode: q.mode||'', text: (q.text||q.stem||'').slice(0,60), answer: q.answer||'', user_ans: userAns });
    }
  });

  const payload = {
    total: R.total, correct: R.correct, wrong: R.wrong, skip: R.skip,
    time_sec: R.timeSec || 0, score: R.totalEarned || 0, max_score: R.totalScore || 0,
    by_unit: byUnit, by_mode: byMode, wrong_stat: wrongStat, wrong_sample: wrongSample,
  };

  // ── 发起流式请求，用 makeStreamingRenderer 渲染 ─────────────
  try {
    const headers = {
      'Content-Type': 'application/json',
      'X-Session-Token': window.SESSION_TOKEN || '',
    };
    const uid = typeof _getUIDCookie === 'function' ? _getUIDCookie() : '';
    if (uid) headers['X-User-ID'] = uid;

    const res = await fetch('/api/ai/report?' + bankQS(), {
      method: 'POST', headers, body: JSON.stringify(payload),
    });
    if (!res.ok) throw new Error('HTTP ' + res.status);

    // 清除 loading，创建内容容器
    loadingEl.remove();
    const contentEl = document.createElement('div');
    contentEl.className = 'ai-report-msg';
    messagesEl.appendChild(contentEl);

    // 使用 makeStreamingRenderer（quiz_ai.js 提供，已预加载）
    let renderer = null;
    if (typeof makeStreamingRenderer === 'function') {
      renderer = makeStreamingRenderer(contentEl, null, messagesEl);
    }

    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buf = '';

    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      buf += decoder.decode(value, { stream: true });
      const lines = buf.split('\n');
      buf = lines.pop();
      for (const line of lines) {
        if (!line.startsWith('data: ')) continue;
        const data = line.slice(6);
        if (data === '[DONE]') { if (renderer) renderer.end(); break; }
        try {
          const obj = JSON.parse(data);
          if (obj.error) throw new Error(obj.error);
          if (obj.content) {
            if (renderer) {
              renderer.push(obj.content);
            } else {
              // fallback：纯文字追加
              contentEl.textContent += obj.content;
            }
            // 自动滚动到底部
            messagesEl.scrollTop = messagesEl.scrollHeight;
          }
        } catch(e) { /* ignore parse errors */ }
      }
    }
    if (renderer) renderer.end();

  } catch(e) {
    loadingEl.remove();
    const errEl = document.createElement('p');
    errEl.style.cssText = 'color:var(--danger);padding:16px;font-size:13px';
    errEl.textContent = '⚠ 报告生成失败：' + (e.message || '网络错误');
    messagesEl.appendChild(errEl);
  }
}


// ════════════════════════════════════════════
// Utils
// ════════════════════════════════════════════
function esc(s) {
  return (s||'').replace(/&/g,'&').replace(/</g,'<').replace(/>/g,'>')
      .replace(/"/g,'"').replace(/'/g,'&#39;');
}

function highlightInductiveWords(html) {
  // 关键词列表：独立维护，新增/删除在这里改即可
  // 不需要按长度手动排序——算法会自动处理
  const keywords = [
    '不恰当', '不合适', '不正确', '不包括', '不属于', '不常见',
    '不得不', '不适用', '不对的', '错误的', '不符合',
    '不可能', '排除', '除外', '除了', '不是', '无需', '不必',
    '禁止', '不得', '不应', '不可', '不宜', '欠妥', '不妥',
    '不当', '相反', '例外', '无关', '不含', '无效', '不符'
  ];

  // 核心思路：把 HTML 拆成「标签」和「纯文字」交替的片段，
  // 只对纯文字片段做关键词替换，标签原样返回。
  // 这样已经包裹在 <span> 里的文字不会被第二次匹配，
  // 同时"不可能"和"不可"各自出现时都能正常高亮。
  //
  // 在纯文字片段内，用长词优先的 alternation 一次性匹配，
  // 保证同一位置只会被最长的关键词命中一次。
  const sorted  = [...keywords].sort((a, b) => b.length - a.length);
  const escaped = sorted.map(w => w.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'));
  const kwRe    = new RegExp(escaped.join('|'), 'g');

  // /(<[^>]+>)|([^<]+)/g 将整个字符串切分为：
  //   group 1：HTML 标签（< ... >）
  //   group 2：标签之间的纯文字
  return html.replace(/(<[^>]+>)|([^<]+)/g, (match, tag, text) => {
    if (tag)  return tag;                              // 是标签：原样返回
    if (!text) return match;
    return text.replace(kwRe, m =>                    // 是纯文字：高亮关键词
      `<span class="inductive-word">${m}</span>`);
  });
}


/** 全部答完后进入回看模式（可自由导航，但不可修改答案） */
function _enterExamReview() {
  S.examReviewMode = true;
  // 解除组限制，允许自由导航
  S.currentGroupIdx = 0;
  // 更新 btn-next 为"交卷"
  const btn = document.getElementById('btn-next');
  if (btn) { btn.textContent = '交卷 ✓'; btn.disabled = false; }
  // 顶部提示
  toast('✅ 全部题目已作答，可自由回看，满意后点「交卷」', false, 4000);
  // 在 topbar 区域显示回看提示条
  let bar = document.getElementById('exam-review-bar');
  if (!bar) {
    bar = document.createElement('div');
    bar.id = 'exam-review-bar';
    bar.className = 'exam-review-bar';
    bar.innerHTML = '🔍 回看模式——可自由翻页，答案不可修改 &nbsp;<button onclick="submitExam()" style="background:#4f46e5;color:#fff;border:none;border-radius:6px;padding:3px 12px;cursor:pointer;font-size:12px;font-weight:700">交卷 ✓</button>';
    const quizScreen = document.getElementById('s-quiz');
    if (quizScreen) quizScreen.prepend(bar);
  }
  bar.style.display = '';
  // 小地图允许任意跳转：重绘小地图（导航限制在 dot.onclick 判断 examReviewMode）
  updateGridDot();
}

/** 显示交卷确认弹窗（有未答题目时） */
function showExamSubmitConfirm(unanswered, total) {
  const answered = total - unanswered;
  // 复用现有的 group-transition-modal 样式或用 confirm
  const ok = confirm(
    `还有 ${unanswered} 道题目未作答（共 ${total} 题，已答 ${answered} 题）。

` +
    `确定现在交卷吗？未答题目将记为空白。

` +
    `点「取消」继续作答，点「确定」立即交卷。`
  );
  if (ok) submitExam();
}
function showReview() {
  showScreen('s-review');
  // 更新标签页数量角标
  _updateReviewTabCounts();
  // 默认显示全部题目
  setTimeout(() => {
    filterReview('all', document.querySelector('.rtab.active') || document.querySelector('.rtab'));
  }, 250);
}

/** 从最慢题目列表点击跳转：打开解析页并滚动到对应题目 */
function _jumpToReviewQ(idx) {
  showReview();
  setTimeout(() => {
    const cards = document.querySelectorAll('.review-card');
    // review-card 的顺序和 R.qs 一致，直接按 idx 定位
    if (cards[idx]) {
      cards[idx].scrollIntoView({ behavior: 'smooth', block: 'start' });
      cards[idx].classList.add('slowest-highlight');
      setTimeout(() => cards[idx].classList.remove('slowest-highlight'), 2000);
    }
  }, 400);
}

/** 为解析页标签添加数量角标 */
function _updateReviewTabCounts() {
  const R = S.results;
  if (!R || !R.qs) return;
  let okN = 0, wrongN = 0, skipN = 0;
  R.qs.forEach((q, i) => {
    let sel = R.ans ? R.ans[i] : S.ans[i];
    if (sel && sel.__set) sel = new Set(sel.v);
    const isMulti    = isMultiQ(q);
    const correctSet = new Set(isMulti ? q.answer.split('') : [q.answer]);
    const isEmpty    = !sel || (sel instanceof Set && sel.size === 0);
    if (isEmpty) { skipN++; return; }
    let isCorrect;
    if (isMulti) {
      const selSet = sel instanceof Set ? sel : new Set([sel]);
      isCorrect = selSet.size === correctSet.size && [...correctSet].every(l => selSet.has(l));
    } else {
      isCorrect = sel === q.answer;
    }
    if (isCorrect) okN++; else wrongN++;
  });
  const tabs = document.querySelectorAll('.rtab');
  const counts = [R.qs.length, wrongN, okN, skipN];
  tabs.forEach(t => {
    const idx = parseInt(t.dataset.tab || '0', 10);
    let badge = t.querySelector('.rtab-count');
    if (!badge) { badge = document.createElement('span'); badge.className = 'rtab-count'; t.appendChild(badge); }
    badge.textContent = counts[idx];
  });
}

/** 计算单题得分并返回徽章 HTML（仅考试计分模式下有内容）*/
function _buildReviewScoreBadge(q, sel, idx) {
  const R = S.results;
  if (!R || !R.scoring || !R.scorePerMode) return '';

  const mode      = q.mode || '';
  const perSq     = R.scorePerMode[mode] || 1;
  const isEmpty   = !sel || (sel instanceof Set && sel.size === 0);
  const isMulti   = isMultiQ(q);
  const correctSet= new Set((q.answer || '').split(''));

  let earned = 0;
  if (!isEmpty) {
    if (!isMulti) {
      earned = (sel === q.answer) ? perSq : 0;
    } else {
      const selSet = sel instanceof Set ? sel : new Set([sel]);
      if (R.multiScoreMode === 'strict') {
        const allOk = selSet.size === correctSet.size && [...correctSet].every(l => selSet.has(l));
        earned = allOk ? perSq : 0;
      } else {
        const optScore = perSq / correctSet.size;
        let got = 0;
        selSet.forEach(l => { got += correctSet.has(l) ? optScore : -optScore; });
        earned = Math.round(Math.max(0, got) * 10) / 10;
      }
    }
  }

  const cls   = earned >= perSq ? 'full' : earned > 0 ? 'partial' : 'zero';
  const icon  = earned >= perSq ? '✅' : earned > 0 ? '⚡' : '❌';
  const label = isEmpty ? '未作答  0' : `${icon} ${earned}`;
  return `<div class="review-score-badge ${cls}">${label} / ${perSq} 分</div>`;
}

function filterReview(type, tabEl) {
  document.querySelectorAll('.rtab').forEach(t => t.classList.remove('active'));
  if (tabEl) tabEl.classList.add('active');

  const R = S.results;
  if (!R || !R.qs) return;

  const list = document.getElementById('review-list');
  list.innerHTML = '';

  R.qs.forEach((q, i) => {
    // 获取答案（处理序列化的 Set）
    let sel = R.ans ? R.ans[i] : S.ans[i];
    // 反序列化：将 { __set: true, v: [...] } 转回 Set
    if (sel && sel.__set) {
      sel = new Set(sel.v);
    }
    const isMulti   = isMultiQ(q);
    const correctSet= new Set(isMulti ? q.answer.split('') : [q.answer]);

    // 判断空/对/错
    const isEmpty = !sel || (sel instanceof Set && sel.size === 0);
    let isCorrect = false;
    if (!isEmpty) {
      if (isMulti) {
        const selSet = sel instanceof Set ? sel : new Set([sel]);
        isCorrect = selSet.size === correctSet.size && [...correctSet].every(l => selSet.has(l));
      } else {
        isCorrect = sel === q.answer;
      }
    }
    const isSkip = isEmpty;

    if (type === 'wrong' && (isCorrect || isSkip)) return;
    if (type === 'ok'    && (!isCorrect || isSkip)) return;
    if (type === 'skip'  && !isSkip) return;

    const dotClass = isSkip ? 'skip' : isCorrect ? 'ok' : 'err';
    const ansClass  = isSkip ? '' : isCorrect ? 'ok' : 'err';
    // 展示答案：单选显示字母，多选显示"A B C"，未答显示"—"
    let ansDisplay;
    if (isSkip) {
      ansDisplay = '—';
    } else if (isMulti) {
      const selSet = sel instanceof Set ? sel : new Set([sel]);
      ansDisplay = [...selSet].sort().join('');
    } else {
      ansDisplay = sel;
    }

    const item = document.createElement('div');
    item.className = 'review-item';

    // B型题：共享选项回显（高亮正确答案行）
    const sharedOptsReviewHtml = (q.shared_options && q.shared_options.length > 0)
        ? `<div class="review-shared-opts">
          <div class="review-shared-opts-title">B型题共享选项</div>
          ${q.shared_options.map((o,oi) => {
          const l = String.fromCharCode(65+oi);
          const isCor = correctSet.has(l);
          const clean = o.replace(/^[A-Za-z]\s*[.．、·）)\s]\s*/u, '').trim();
          return `<div class="review-shared-opt-row${isCor ? ' is-ans' : ''}">
              <span class="opt-lbl">${l}</span><span>${esc(clean)}</span>
            </div>`;
        }).join('')}
        </div>`
        : '';
    const optsHtml = q.options.map((o, oi) => {
      const l = String.fromCharCode(65 + oi);
      const isCor = correctSet.has(l);
      const selSet = isMulti ? (sel instanceof Set ? sel : new Set()) : null;
      const isSel = isMulti ? selSet.has(l) : sel === l;
      const wrongSel = isSel && !isCor;
      return `<div class="review-opt${isCor ? ' is-correct' : ''}${wrongSel ? ' is-selected wrong' : ''}">
        <span class="review-opt-lbl">${l}</span><span>${esc(o)}</span>
      </div>`;
    }).join('');

    const multiTag = isMulti ? `<span style="font-size:10px;color:var(--warning);margin-left:4px">[多选]</span>` : '';

    item.innerHTML = `
      <div class="review-item-head">
        <span class="review-dot ${dotClass}"></span>
        <span class="review-item-q"><b>${i+1}.</b>${multiTag} ${esc(q.text)}</span>
        <span class="review-ans ${ansClass}" style="min-width:${isMulti?'auto':'20px'};font-size:${isMulti?'11px':'12px'}">${ansDisplay}</span>
      </div>
      <div class="review-expand" id="rexp-${i}">
        <div class="review-expand-inner">
          ${_buildReviewScoreBadge(q, sel, i)}
          ${q.stem ? `<div class="review-stem">${renderHTML(q.stem)}</div>` : ''}
          ${sharedOptsReviewHtml}
          ${isMulti ? `<div style="font-size:12px;color:var(--muted);margin-bottom:8px">正确答案：<span style="color:var(--success);font-weight:700">${q.answer.split('').join(' ')}</span></div>` : ''}
          <div class="review-opts-list">${optsHtml}</div>
          <div class="review-discuss">${q.discuss ? `<strong>解析：</strong>${renderHTML(q.discuss)}` : '暂无解析'}
            ${q.point ? `<br><span style="color:var(--accent);font-size:12px">考点：${esc(q.point)}</span>` : ''}
          </div>
        </div>
      </div>`;

    // AI Q&A panel — placed below the review content
    const aiSlot = item.querySelector('.review-expand');
    if (aiSlot) _loadQuizAI().then(() => initAIPanel(aiSlot, q, q.si || 0, ansDisplay)).catch(() => {});

    item.querySelector('.review-item-head').onclick = () => {
      const el = document.getElementById(`rexp-${i}`);
      const qEl = item.querySelector('.review-item-q');
      const isOpen = el.classList.toggle('open');
      if (isOpen) {
        // 展开：移除行数限制，让题目完整显示
        qEl.style.webkitLineClamp = 'unset';
        qEl.style.overflow = 'visible';
        el.style.maxHeight = el.scrollHeight + 'px';
        // 动画结束后设为 none，让内部 AI 面板等动态内容可以自由撑高
        setTimeout(() => { if (el.classList.contains('open')) el.style.maxHeight = 'none'; }, 400);
      } else {
        // 收起：先捕获当前实际高度，再动画到 0
        qEl.style.webkitLineClamp = '';
        qEl.style.overflow = '';
        el.style.maxHeight = el.scrollHeight + 'px';
        requestAnimationFrame(() => { el.style.maxHeight = '0'; });
      }
    };
    list.appendChild(item);
  });

  if (!list.children.length) {
    list.innerHTML = '<div style="text-align:center;color:var(--muted);padding:40px;font-size:14px">没有符合条件的题目</div>';
  }
}

// ════════════════════════════════════════════
// 进度持久化：错题本 + SM-2 + 统计
// ════════════════════════════════════════════

/** 把本次答题结果交给 SyncManager（离线优先：先写 IndexedDB，再异步上传服务端） */
async function _recordSessionToServer(results, questions, ans, sessionId) {
  if (!results || !questions) return;
  const items = [];
  questions.forEach((q, i) => {
    const fp = q.fingerprint;
    if (!fp) return;
    let sel = ans[i];
    if (sel && sel.__set) sel = new Set(sel.v);
    const isEmpty  = !sel || (sel instanceof Set && sel.size === 0);
    let   result   = -1;  // -1=skip
    if (!isEmpty) {
      const isMulti   = isMultiQ(q);
      const correctSet= new Set(isMulti ? q.answer.split('') : [q.answer]);
      if (isMulti) {
        const selSet = sel instanceof Set ? sel : new Set([sel]);
        result = (selSet.size === correctSet.size && [...correctSet].every(l => selSet.has(l))) ? 1 : 0;
      } else {
        result = (sel === q.answer) ? 1 : 0;
      }
    }
    items.push({ fingerprint: fp, result, mode: q.mode, unit: q.unit, quality: result === 1 ? 4 : result === 0 ? 1 : undefined });
  });

  const today = _localDate();
  const units  = [...new Set(questions.map(q => q.unit).filter(Boolean))];
  const payload = {
    id:       sessionId || String(Date.now()),
    bank_id:  S.bankID,   // for PG multi-bank isolation
    mode:     results.mode,
    total:    results.total,
    correct:  results.correct,
    wrong:    results.wrong,
    skip:     results.skip,
    time_sec: results.timeSec,
    date:     today,
    units,
    items,
  };

  // 离线优先：交给 SyncManager，先写 IndexedDB，有网时自动上传
  // 即使服务端宕机或无网络，记录也不会丢失
  if (typeof SyncManager !== 'undefined') {
    try {
      await SyncManager.record(payload, S.bankID);
      return;
    } catch (e) {
      // IndexedDB 不可用（如隐私模式被禁）→ 降级直接 POST
      console.warn('[Quiz] SyncManager 不可用，降级直接上传:', e);
    }
  }
  // 降级路径：直接 POST（兼容 SyncManager 加载失败的极端情况）
  try {
    await apiFetch('/api/record?' + bankQS(), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });
  } catch (e) {
    console.warn('[Quiz] 记录上传失败:', e);
  }
}

/** 刷新主页徽章（今日复习数 + 错题数） */
async function _refreshProgressBadges() {
  try {
    const [dueRes, wbRes] = await Promise.all([
      apiFetch('/api/review/due?' + bankQS() + '&date=' + _localDate()).then(r => r.json()),
      apiFetch('/api/wrongbook?' + bankQS()).then(r => r.json()),
    ]);
    const dueCount   = (dueRes.fingerprints || []).length;
    const wrongCount = (wbRes.items || []).length;
    const dueBadge   = document.getElementById('due-badge');
    const wrongBadge = document.getElementById('wrong-badge');
    if (dueBadge) {
      dueBadge.textContent = dueCount;
      dueBadge.style.display = dueCount > 0 ? '' : 'none';
    }
    if (wrongBadge) {
      wrongBadge.textContent = wrongCount > 99 ? '99+' : wrongCount;
      wrongBadge.style.display = wrongCount > 0 ? '' : 'none';
    }
    // 成绩页按钮
    const resRev = document.getElementById('res-review-btn');
    if (resRev) resRev.style.display = dueCount   > 0 ? '' : 'none';
  } catch (e) { /* 静默失败，不影响主流程 */ }
}

/** 开始今日 SM-2 复习（练习模式，仅加载到期题目） */
async function startReview() {
  try {
    const res  = await apiFetch('/api/review/due?' + bankQS() + '&date=' + _localDate()).then(r => r.json());
    const fps  = res.fingerprints || [];
    if (!fps.length) { toast('🎉 今日没有待复习题目！'); return; }
    const data = await apiFetch(
        '/api/questions?shuffle=1&fingerprints=' + fps.join(',') + '&' + bankQS()
    ).then(r => r.json());
    if (!data.items || !data.items.length) { toast('找不到对应题目，题库可能已更新'); return; }
    S.mode      = 'practice';
    S.questions = data.items;
    S.cur = 0; S.ans = {}; S.revealed = new Set(); S.marked = new Set();
    S.examStart = Date.now();
    S.modeGroups = buildModeGroups(data.items);
    S.currentGroupIdx = 0; S.caseMaxReached = {};
    S.practiceSessionId = String(Date.now());
    S.streak = 0;
    toast(`🔄 复习模式：共 ${data.items.length} 题`);
    startQuiz();
  } catch(e) { toast('加载复习题目失败'); }
}

/** 开始错题练习（练习模式）。
 *  filterUnit: undefined=自动弹章节选择 | '__all__'=全部 | 字符串=单章节 | 数组=多章节
 *  cachedItems: 已从 /api/wrongbook 取回的 items，有则直接用（避免重复请求）
 */
async function startWrongBookReview(filterUnit, cachedItems) {
  try {
    let allItems = cachedItems;
    if (!allItems) {
      const res = await apiFetch('/api/wrongbook?' + bankQS()).then(r => r.json());
      allItems = res.items || [];
    }
    if (!allItems.length) { toast('暂时没有错题记录 👍'); return; }

    // 如果没有指定 unit，且错题跨多个章节，弹出选择面板（把 allItems 缓存传入）
    if (filterUnit === undefined) {
      const units = [...new Set(allItems.map(it => it.unit).filter(Boolean))];
      if (units.length > 1) {
        _showWbUnitPicker(units, allItems);
        return;
      }
    }

    // 按 unit 过滤
    let items;
    if (Array.isArray(filterUnit)) {
      // 多章节数组
      const unitSet = new Set(filterUnit);
      items = allItems.filter(it => unitSet.has(it.unit));
    } else if (filterUnit && filterUnit !== '__all__') {
      // 单章节字符串
      items = allItems.filter(it => it.unit === filterUnit);
    } else {
      items = allItems;
    }

    const fps = items.map(it => it.fingerprint).slice(0, 100);
    if (!fps.length) { toast('所选章节暂无错题'); return; }

    const data = await apiFetch(
        '/api/questions?shuffle=1&fingerprints=' + fps.join(',') + '&' + bankQS()
    ).then(r => r.json());
    if (!data.items || !data.items.length) { toast('找不到对应题目'); return; }
    S.mode      = 'practice';
    S.questions = data.items;
    S.cur = 0; S.ans = {}; S.revealed = new Set(); S.marked = new Set();
    S.examStart = Date.now();
    S.modeGroups = buildModeGroups(data.items);
    S.currentGroupIdx = 0; S.caseMaxReached = {};
    S.practiceSessionId = String(Date.now());
    S.streak = 0;
    let label = '';
    if (Array.isArray(filterUnit))                        label = `【${filterUnit.length}章】`;
    else if (filterUnit && filterUnit !== '__all__')       label = `【${filterUnit}】`;
    toast(`📕 错题模式${label}：共 ${data.items.length} 题`);
    startQuiz();
  } catch(e) { toast('加载错题失败'); }
}

/** 弹出章节选择面板，让用户选择要重做的错题章节 */
function _showWbUnitPicker(units, allItems) {
  const old = document.getElementById('wb-unit-picker');
  if (old) old.remove();

  // 已选中的章节集合（空 = 未选）
  const selected = new Set();

  const panel = document.createElement('div');
  panel.id = 'wb-unit-picker';
  panel.className = 'wb-unit-picker-overlay';

  const box = document.createElement('div');
  box.className = 'wb-unit-picker-box';

  // ── 标题 ──
  const title = document.createElement('div');
  title.className = 'wb-unit-picker-title';
  title.textContent = '选择章节重做错题';

  // ── 副标题提示 ──
  const hint = document.createElement('div');
  hint.className = 'wb-unit-picker-hint';
  hint.textContent = '可多选，不选则练习全部章节';

  // ── 章节列表（可滚动）──
  const list = document.createElement('div');
  list.className = 'wb-unit-picker-list';

  // 全部章节按钮（单独一行，选中时高亮）
  const allBtn = document.createElement('button');
  allBtn.className = 'wb-unit-chip wb-unit-chip-all';
  allBtn.innerHTML = `全部章节 <span class="wb-unit-chip-cnt">${allItems.length}</span>`;
  allBtn.addEventListener('click', () => {
    panel.remove();
    startWrongBookReview('__all__', allItems); // 传缓存，不重新请求
  });
  list.appendChild(allBtn);

  // 各章节按钮
  units.forEach(u => {
    const cnt = allItems.filter(it => it.unit === u).length;
    const btn = document.createElement('button');
    btn.className = 'wb-unit-chip';
    btn.innerHTML = `${esc(u)} <span class="wb-unit-chip-cnt">${cnt}</span>`;
    btn.addEventListener('click', () => {
      if (selected.has(u)) {
        selected.delete(u);
        btn.classList.remove('selected');
      } else {
        selected.add(u);
        btn.classList.add('selected');
      }
      // 更新确认按钮文字
      confirmBtn.textContent = selected.size > 0
        ? `开始练习（已选 ${selected.size} 章）`
        : '开始练习（全部章节）';
    });
    list.appendChild(btn);
  });

  // ── 底部操作按钮 ──
  const footer = document.createElement('div');
  footer.className = 'wb-unit-picker-footer';

  const cancelBtn = document.createElement('button');
  cancelBtn.className = 'wb-unit-picker-cancel';
  cancelBtn.textContent = '取消';
  cancelBtn.addEventListener('click', () => panel.remove());

  const confirmBtn = document.createElement('button');
  confirmBtn.className = 'wb-unit-picker-confirm';
  confirmBtn.textContent = '开始练习（全部章节）';
  confirmBtn.addEventListener('click', () => {
    panel.remove();
    if (selected.size === 0) {
      startWrongBookReview('__all__', allItems);        // 全部，传缓存
    } else if (selected.size === 1) {
      startWrongBookReview([...selected][0], allItems); // 单章节，传缓存
    } else {
      startWrongBookReview([...selected], allItems);    // 多章节数组，传缓存
    }
  });

  footer.appendChild(cancelBtn);
  footer.appendChild(confirmBtn);
  box.appendChild(title);
  box.appendChild(hint);
  box.appendChild(list);
  box.appendChild(footer);
  panel.appendChild(box);

  // 点背景关闭
  panel.addEventListener('click', e => { if (e.target === panel) panel.remove(); });
  document.body.appendChild(panel);
}


/** 只练习本次答错的题目 */
function practiceSessionWrong() {
  const R = S.results;
  if (!R || !R.qs || !R.ans) { toast('没有可用的错题数据'); return; }
  const wrongQs = [];
  R.qs.forEach((q, i) => {
    let sel = R.ans[i];
    if (sel && sel.__set) sel = new Set(sel.v);
    const isEmpty = !sel || (sel instanceof Set && sel.size === 0);
    if (isEmpty) return; // 未答不算错
    const isMulti = isMultiQ(q);
    const correctSet = new Set(isMulti ? q.answer.split('') : [q.answer]);
    let isCorrect;
    if (isMulti) {
      const selSet = sel instanceof Set ? sel : new Set([sel]);
      isCorrect = selSet.size === correctSet.size && [...correctSet].every(l => selSet.has(l));
    } else {
      isCorrect = sel === q.answer;
    }
    if (!isCorrect) wrongQs.push(q);
  });
  if (!wrongQs.length) { toast('本次没有答错的题目 👍'); return; }
  S.mode      = 'practice';
  S.questions = wrongQs;
  S.cur = 0; S.ans = {}; S.revealed = new Set(); S.marked = new Set();
  S.examStart = Date.now();
  S.modeGroups = buildModeGroups(wrongQs);
  S.currentGroupIdx = 0; S.caseMaxReached = {};
  S.practiceSessionId = String(Date.now());
  S.streak = 0;
  toast(`📕 本次错题：共 ${wrongQs.length} 题`);
  startQuiz();
}

/** 分享试卷（生成分享链接） */
async function shareExam() {
  // 结果页入口：从 S.results 读取（历史记录打开也走这里）
  const R = S.results;
  if (!R || !R.qs || !R.qs.length) { toast('没有可分享的题目'); return; }
  return _doShareExam(R.qs, {
    mode:           R.mode,
    timeLimit:      R.timeLimit || S.examLimit || CFG.examTime * 60,
    scoring:        (R.scoring != null) ? R.scoring : CFG.scoring,
    scorePerMode:   R.scorePerMode   || CFG.scorePerMode   || {},
    multiScoreMode: R.multiScoreMode || CFG.multiScoreMode || 'strict',
  });
}

/** 从考试/练习进行中分享当前试卷（顶部按钮） */
async function shareCurrentQuiz() {
  // 进行中入口：只读取实时状态，绝不回退到 S.results（可能是上一场考试的残留）
  if (!S.questions || !S.questions.length) { toast('没有可分享的题目'); return; }
  return _doShareExam(S.questions, {
    mode:           S.mode,
    timeLimit:      S.examLimit || CFG.examTime * 60,
    scoring:        !!CFG.scoring,
    scorePerMode:   CFG.scorePerMode   || {},
    multiScoreMode: CFG.multiScoreMode || 'strict',
  });
}

async function _doShareExam(qs, opts) {
  if (!qs || !qs.length) { toast('没有可分享的题目'); return; }
  const fps = qs.map(q => q.fingerprint).filter(Boolean);
  if (!fps.length) { toast('题目缺少标识，无法分享'); return; }
  // 精确到小题级别：fingerprint:si 组合，避免服务端把 A3/案例分析的同一题干下
  // 所有小题全部还原（导致 220 → 221 多出一题）
  const subIds = qs
    .filter(q => q.fingerprint != null)
    .map(q => q.fingerprint + ':' + (q.si != null ? q.si : 0));
  try {
    const curMode = opts.mode || 'exam';
    const shareMode = (curMode === 'exam' || curMode === 'exam_done') ? 'exam' : (curMode || 'exam');
    const res = await apiFetch('/api/exam/share?' + bankQS(), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        fingerprints:     fps,
        sub_ids:          subIds,
        mode:             shareMode,
        time_limit:       opts.timeLimit,
        scoring:          opts.scoring,
        score_per_mode:   opts.scorePerMode,
        multi_score_mode: opts.multiScoreMode,
      }),
    });
    const d = await res.json();
    if (!d.token) { toast('分享失败', true); return; }
    const url = location.origin + location.pathname + '#share=' + d.token;
    if (navigator.share) {
      navigator.share({ title: '医考练习 - 试卷分享', text: `共 ${qs.length} 题`, url }).catch(()=>{});
    } else if (navigator.clipboard) {
      await navigator.clipboard.writeText(url);
      toast('✅ 分享链接已复制到剪贴板');
    } else {
      prompt('分享链接：', url);
    }
  } catch(e) { toast('分享失败: ' + e.message, true); }
}

/** 检查 URL hash 是否包含分享令牌，自动加入 */
async function _checkShareToken() {
  const m = location.hash.match(/share=([a-f0-9]+)/);
  if (!m) return;
  const token = m[1];
  location.hash = ''; // 清除 hash 防止重复触发
  try {
    // 注意：join 接口从 token 内部读取 bankIdx，客户端不需要传 ?bank=N
    const res = await apiFetch('/api/exam/join?token=' + token);
    const d   = await res.json();
    if (d.error) { toast('分享链接已过期或无效（' + d.error + '）', true); return; }
    if (!d.items || !d.items.length) { toast('分享试卷为空或已过期', true); return; }

    // 若服务端返回了 bank_idx（与当前题库不同），先静默切换
    if (typeof d.bank_idx === 'number' && d.bank_idx !== S.bankID && d.bank_idx < S.banksInfo.length) {
      S.bankID = d.bank_idx;
      localStorage.setItem(SELECTED_BANK_KEY, String(d.bank_idx));
      try { S.bankInfo = await apiFetch('/api/info?bank=' + d.bank_idx).then(r => r.json()); } catch(_) {}
    }

    S.mode      = d.mode || 'exam';
    S.questions = d.items;
    S.examId    = d.exam_id || null;
    S.cur = 0; S.ans = {}; S.revealed = new Set(); S.marked = new Set();
    S.examStart = Date.now();
    S.modeGroups = buildModeGroups(d.items);
    S.currentGroupIdx = 0; S.caseMaxReached = {};
    S.practiceSessionId = null;
    S.streak = 0;
    toast(`📋 已加载分享试卷：${d.items.length} 题`);
    if (S.mode === 'exam' || S.mode === 'exam_done') {
      S.mode = 'exam';
      S.examLimit = d.time_limit || 90 * 60;
      CFG.examTime       = Math.round(S.examLimit / 60);
      // 恢复分享者的计分配置，让接收方体验完全一致
      CFG.scoring        = !!d.scoring;
      CFG.scorePerMode   = d.score_per_mode   || {};
      CFG.multiScoreMode = d.multi_score_mode || 'strict';
      startQuiz(S.examLimit);
      saveExamSession();
    } else {
      // 练习模式保持默认逻辑，不复制计分配置
      CFG.scoring        = false;
      CFG.scorePerMode   = {};
      CFG.multiScoreMode = 'strict';
      startQuiz();
    }
    // 锁定模式：隐藏返回/退出相关控件，一次性 token
    if (S.sharedLocked) _applySharedLockUI();
  } catch(e) { toast('加载分享试卷失败', true); }
}

/** 未验证的分享接收者：锁定在考试页面，屏蔽所有退出入口 */
function _applySharedLockUI() {
  // 用一个全局 style 标签统一控制，刷新 DOM 不会丢失
  let st = document.getElementById('shared-lock-style');
  if (!st) {
    st = document.createElement('style');
    st.id = 'shared-lock-style';
    st.textContent = `
      body.shared-locked #brand,
      body.shared-locked #share-quiz-btn,
      body.shared-locked #s-home,
      body.shared-locked #s-stats,
      body.shared-locked #s-wb,
      body.shared-locked .bottom-nav { display: none !important; }
    `;
    document.head.appendChild(st);
  }
  document.body.classList.add('shared-locked');
  // 拦截 history 返回（避免用户按返回键回到主页）
  try {
    history.pushState({sharedLock: true}, '', location.pathname);
    window.addEventListener('popstate', function _blockBack() {
      if (document.body.classList.contains('shared-locked')) {
        history.pushState({sharedLock: true}, '', location.pathname);
        toast('分享试卷不可退出，请完成后关闭页面');
      }
    });
  } catch(_) {}
}

/** 提交分享试卷后的终端页面：禁止回退、禁止再次进入 */
function _showSharedLockEndScreen() {
  // 交卷后即标记 token 为"已使用"：用一个 localStorage 键阻止刷新后再进
  try { localStorage.setItem('med_exam_share_done_' + Date.now(), '1'); } catch(_) {}
  // 构造一个遮盖层
  let el = document.getElementById('shared-lock-end');
  if (!el) {
    el = document.createElement('div');
    el.id = 'shared-lock-end';
    el.style.cssText = 'position:fixed;inset:0;z-index:99999;background:var(--bg,#fff);display:flex;flex-direction:column;align-items:center;justify-content:center;padding:24px;text-align:center;gap:16px';
    el.innerHTML = `
      <div style="font-size:48px">✅</div>
      <div style="font-size:20px;font-weight:600">试卷已提交</div>
      <div style="color:var(--muted,#888);max-width:420px;line-height:1.6">
        分享试卷为一次性有效，已答完后无法重新进入。<br>
        如需查看完整解析或继续练习，请联系分享者或使用您自己的账户访问。
      </div>
      <button onclick="window.close()" style="margin-top:12px;padding:10px 24px;border:none;border-radius:8px;background:var(--accent,#3b82f6);color:#fff;font-size:15px;cursor:pointer">关闭页面</button>
    `;
    document.body.appendChild(el);
  }
}

// ── 收藏功能 ────────────────────────────────────────────────────

// 收藏元数据 key（存收藏时间戳，fp→timestamp）
const FAV_TS_KEY = 'med_exam_fav_ts';

function _loadFavTs() {
  try { return JSON.parse(localStorage.getItem(FAV_TS_KEY) || '{}'); }
  catch(_) { return {}; }
}
function _saveFavTs(map) {
  try { localStorage.setItem(FAV_TS_KEY, JSON.stringify(map)); } catch(_) {}
}

/** 在 toggleFavorite 保存时同时记录时间戳 */
function _recordFavTimestamp(fp, adding) {
  const ts = _loadFavTs();
  if (adding) ts[fp] = Date.now();
  else delete ts[fp];
  _saveFavTs(ts);
}

/** 更新主页收藏徽章 */
function refreshFavBadge() {
  const cnt = _loadFavorites().size;
  const badge = document.getElementById('fav-badge');
  if (!badge) return;
  if (cnt > 0) { badge.textContent = cnt > 99 ? '99+' : cnt; badge.style.display = ''; }
  else badge.style.display = 'none';
}

/** 打开收藏页 */
function openFavorites() {
  showScreen('s-favorites');
  _renderFavoritesScreen();
}

function _renderFavoritesScreen() {
  const favs    = _loadFavorites();
  const favTs   = _loadFavTs();
  const favData = _loadFavData();

  const bankNameEl = document.getElementById('fav-bank-name');
  if (bankNameEl) bankNameEl.textContent = (S.bankInfo && S.bankInfo.name) || '';

  const clearBtn = document.getElementById('fav-clear-btn');
  if (clearBtn) clearBtn.style.display = favs.size > 0 ? '' : 'none';

  const validFavs = [...favs].filter(fp => favData[fp]);

  const todayStart = new Date(); todayStart.setHours(0,0,0,0);
  const yestStart  = new Date(todayStart.getTime() - 86400000);

  let cntToday = 0, cntYest = 0;
  validFavs.forEach(fp => {
    const ts = favTs[fp] || 0;
    if (ts >= todayStart.getTime()) cntToday++;
    else if (ts >= yestStart.getTime()) cntYest++;
  });

  document.getElementById('fav-cnt-today').textContent     = cntToday;
  document.getElementById('fav-cnt-yesterday').textContent = cntYest;
  document.getElementById('fav-cnt-all').textContent       = Math.max(favs.size, validFavs.length);

  const listEl  = document.getElementById('fav-unit-list');
  const emptyEl = document.getElementById('fav-empty');

  if (favs.size === 0) { listEl.innerHTML = ''; emptyEl.style.display = ''; return; }
  emptyEl.style.display = 'none';

  const unitMap = {};
  validFavs.forEach(fp => {
    const unit = (favData[fp] && favData[fp].unit) || '未分类';
    if (!unitMap[unit]) unitMap[unit] = [];
    unitMap[unit].push(fp);
  });

  const uncached = favs.size - validFavs.length;
  if (uncached > 0) unitMap['（需重新收藏以恢复）'] = [];

  listEl.innerHTML = Object.entries(unitMap).map(([unit, fps]) => {
    const disabled = fps.length === 0;
    const clickAttr = disabled ? '' : `onclick="startFavoritesQuiz('unit', ${JSON.stringify(unit)})"`;
    return `<div class="fav-unit-row${disabled ? ' fav-unit-row-disabled' : ''}" ${clickAttr}>
      <div class="fav-unit-left">
        <div class="fav-unit-name">${esc(unit)}</div>
        <div class="fav-unit-count">${fps.length} 题${disabled ? '　需重新收藏' : ''}</div>
      </div>
      ${disabled ? '' : '<span class="fav-unit-chevron">›</span>'}
    </div>`;
  }).join('');
}

/** 开始做收藏题目（使用本地缓存数据，不依赖 S.questions） */
function startFavoritesQuiz(filter, unitName) {
  const favs    = _loadFavorites();
  const favTs   = _loadFavTs();
  const favData = _loadFavData();

  let fps = [...favs].filter(fp => favData[fp]);

  const todayStart = new Date(); todayStart.setHours(0,0,0,0);
  const yestStart  = new Date(todayStart.getTime() - 86400000);

  if (filter === 'today') {
    fps = fps.filter(fp => (favTs[fp] || 0) >= todayStart.getTime());
  } else if (filter === 'yesterday') {
    fps = fps.filter(fp => { const ts = favTs[fp]||0; return ts >= yestStart.getTime() && ts < todayStart.getTime(); });
  } else if (filter === 'unit' && unitName) {
    fps = fps.filter(fp => favData[fp] && favData[fp].unit === unitName);
  }

  if (fps.length === 0) { toast('该范围暂无可做的收藏题目'); return; }

  const items = fps.map(fp => {
    const d = favData[fp];
    return {
      fingerprint: d.fingerprint, si: d.si ?? 0,
      mode: d.mode||'', unit: d.unit||'', text: d.text||'',
      stem: d.stem||'', options: d.options||[],
      shared_options: d.shared_options||[], sharedOptions: d.shared_options||[],
      answer: d.answer||'', discuss: d.discuss||'', rate: d.rate||'',
    };
  });

  S.mode = 'practice'; S.questions = items;
  S.cur = 0; S.ans = {}; S.revealed = new Set(); S.marked = new Set();
  S.examStart = Date.now(); S.streak = 0;
  S.modeGroups = buildModeGroups(items);
  S.currentGroupIdx = 0; S.caseMaxReached = {};
  S.practiceSessionId = String(Date.now());
  showScreen('s-quiz');
  startQuiz();
  toast(`⭐ 收藏题目：${items.length} 题`);
}

/** 清空所有收藏 */
function confirmClearFavorites() {
  if (!confirm(`确认清空全部 ${_loadFavorites().size} 道收藏题目？`)) return;
  try {
    localStorage.removeItem(FAV_KEY);
    localStorage.removeItem(FAV_TS_KEY);
    localStorage.removeItem(FAV_DATA_KEY);
  } catch(_) {}
  refreshFavBadge();
  _renderFavoritesScreen();
  toast('已清空收藏');
}


/** 打开统计页面 */
async function openStats() {
  showScreen('s-stats');
  // 先确保所有待同步数据已上传到服务端，再读取统计
  if (typeof SyncManager !== 'undefined') {
    try { await SyncManager.flush(); } catch(e) { /* 静默，不影响主流程 */ }
  }
  try {
    const [d, wb, rs] = await Promise.all([
      apiFetch('/api/stats?' + bankQS()).then(r => r.json()),
      apiFetch('/api/wrongbook?' + bankQS()).then(r => r.json()),
      apiFetch('/api/record/status?' + bankQS()).then(r => r.json()),
    ]);
    _renderStatsOverall(d.overall || {});
    _renderTrendChart(d.history || []);
    _renderUnitStats(d.units || []);
    _renderWrongbookPreview(wb.items || []);
    _renderRecordStatus(rs);
  } catch(e) {
    document.getElementById('unit-stats-list').innerHTML =
        '<div style="color:var(--danger);font-size:13px">加载失败，请稍后重试</div>';
  }
}

/** 渲染学习记录设置栏 */
function _renderRecordStatus(rs) {
  const el = document.getElementById('record-status-body');
  if (!el) return;

  if (!rs.enabled) {
    el.innerHTML = `
      <div style="display:flex;align-items:center;gap:8px;color:var(--muted);font-size:13px;padding:4px 0">
        <span style="font-size:16px">🔕</span>
        <span>学习记录已关闭（启动时传入 <code style="background:var(--card);padding:2px 5px;border-radius:4px">--no-record</code> 参数）</span>
      </div>`;
    return;
  }

  const isLegacy = rs.user_id === '_legacy';
  const shortId = isLegacy ? '（未识别）' : rs.user_id.slice(0, 8) + '…';
  const idTip   = isLegacy
      ? '刷新页面后将自动分配新 ID'
      : '此 ID 存储在浏览器 Cookie 中，清除 Cookie 将获得新 ID';

  el.innerHTML = `
    <div style="display:flex;flex-direction:column;gap:10px">
      <div style="display:flex;align-items:center;gap:8px;font-size:13px">
        <span style="font-size:15px">🟢</span>
        <span style="color:var(--text)">记录功能已启用</span>
      </div>
      <div style="font-size:12px;color:var(--muted);background:var(--card);border-radius:8px;padding:8px 10px;line-height:1.7">
        <div>当前用户 ID：<code
            data-uid="${rs.user_id}"
            onclick="_copyUID(this)"
            title="${isLegacy ? '' : '点击复制完整 ID'}"
            style="color:var(--accent);${isLegacy ? '' : 'cursor:pointer;'}user-select:text;-webkit-user-select:text"
          >${shortId}</code>
          <span id="uid-copy-tip"
            style="font-size:10px;color:var(--accent);opacity:0;margin-left:4px;transition:opacity .3s;pointer-events:none">已复制</span>
          <span onclick="_toggleMigrateForm()"
            title="从其他来源迁移数据"
            style="margin-left:6px;font-size:11px;color:var(--muted);opacity:0.5;cursor:pointer;user-select:none">↹</span>
        </div>
        <div style="margin-top:2px">${idTip}</div>
      </div>
      <!-- 数据迁移面板：默认隐藏，点击 ↹ 展开 -->
      <div id="migrate-form" style="display:none;background:var(--card);border-radius:8px;padding:10px;font-size:12px">
        <div style="color:var(--muted);margin-bottom:6px">将旧 ID 的学习记录合并到当前 ID（原数据将被删除）</div>
        <div style="display:flex;gap:6px;align-items:center">
          <input id="migrate-from-input" placeholder="粘贴旧用户 ID"
            style="flex:1;padding:5px 8px;border-radius:6px;border:1px solid var(--border);
                   background:var(--bg);color:var(--text);font-size:12px;font-family:monospace"/>
          <button onclick="_doMigrate()"
            style="padding:5px 10px;border-radius:6px;font-size:12px;font-weight:600;
                   background:var(--accent-bg,#1e3a5f);color:var(--accent);border:1px solid var(--accent);cursor:pointer">
            迁移
          </button>
        </div>
      </div>
      <button onclick="_confirmClearRecord()"
        style="align-self:flex-start;padding:7px 14px;border-radius:8px;font-size:13px;font-weight:600;
               background:var(--danger-bg);color:var(--danger);border:1.5px solid var(--danger);cursor:pointer">
        🗑 清空我的学习记录
      </button>
    </div>`;
}

/** 复制用户 ID 到剪贴板（用于切换域名/来源后迁移数据时使用） */
function _copyUID(el) {
  const uid = el.dataset.uid;
  if (!uid || uid === '_legacy') return;
  const tip = document.getElementById('uid-copy-tip');
  const show = () => {
    if (!tip) return;
    tip.style.opacity = '1';
    clearTimeout(tip._t);
    tip._t = setTimeout(() => { tip.style.opacity = '0'; }, 2000);
  };
  if (navigator.clipboard && navigator.clipboard.writeText) {
    navigator.clipboard.writeText(uid).then(show).catch(() => {
      // HTTPS 不可用或权限拒绝时降级：弹出完整 ID 供手动复制
      prompt('完整用户 ID（Ctrl+C / ⌘C 复制）:', uid);
    });
  } else {
    // 旧浏览器降级方案
    const ta = document.createElement('textarea');
    ta.value = uid;
    ta.style.cssText = 'position:fixed;opacity:0;top:0;left:0';
    document.body.appendChild(ta);
    ta.select();
    try { document.execCommand('copy'); show(); } catch (_) { prompt('完整用户 ID:', uid); }
    document.body.removeChild(ta);
  }
}

/** 切换迁移面板显示 */
function _toggleMigrateForm() {
  const f = document.getElementById('migrate-form');
  if (!f) return;
  f.style.display = f.style.display === 'none' ? '' : 'none';
  if (f.style.display !== 'none') {
    const inp = document.getElementById('migrate-from-input');
    if (inp) inp.focus();
  }
}

/** 执行数据迁移 */
async function _doMigrate() {
  const inp = document.getElementById('migrate-from-input');
  const fromUID = (inp && inp.value || '').trim();
  if (!fromUID) { toast('请输入旧用户 ID', true); return; }
  const yes = confirm(
    '⚠️ 确认迁移？\n\n' +
    '旧 ID：' + fromUID + '\n' +
    '将合并到当前 ID，迁移后旧 ID 数据将被删除。\n\n此操作不可撤销。'
  );
  if (!yes) return;
  try {
    const res = await apiFetch('/api/record/migrate?' + bankQS(), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ from_uid: fromUID }),
    }).then(r => r.json());
    if (res.ok) {
      const m = res.migrated;
      toast(`迁移完成：${m.sessions||0} 会话、${m.attempts||0} 答题记录、${m.sm2_cards||0} 复习卡`);
      const f = document.getElementById('migrate-form');
      if (f) f.style.display = 'none';
      openStats();
    } else {
      toast('迁移失败：' + (res.error || '未知错误'), true);
    }
  } catch(e) { toast('请求失败', true); }
}

/** 清空记录确认 */
async function _confirmClearRecord() {
  const yes = confirm('⚠️ 确定要清空你的全部学习记录吗？\n\n这将删除所有答题历史、错题记录和 SM-2 复习进度，无法恢复。');
  if (!yes) return;
  try {
    const res = await apiFetch('/api/record/clear?' + bankQS(), { method: 'POST' }).then(r => r.json());
    if (res.ok) {
      const d = res.deleted;
      toast(`已清空：${d.attempts} 条答题记录、${d.sessions} 个会话、${d.sm2_cards} 条复习卡`);

      // 同步清空本地缓存（服务端已清空，本地也要一致）
      S.history = [];
      localStorage.removeItem(historyKey());
      localStorage.removeItem(practiceSessionsKey());
      localStorage.removeItem(examSessionKey());

      // 刷新主页最近记录区域 + 统计页
      renderHistorySection();
      openStats();
    } else {
      toast('清空失败：' + (res.error || '未知错误'));
    }
  } catch(e) { toast('请求失败'); }
}

function _renderStatsOverall(o) {
  document.getElementById('st-total').textContent       = o.total_attempts   ?? '—';
  document.getElementById('st-accuracy').textContent    = o.accuracy != null  ? o.accuracy + '%' : '—';
  document.getElementById('st-wrong-topics').textContent= o.wrong_topics      ?? '—';
  document.getElementById('st-due').textContent         = o.due_today         ?? '—';
}

function _renderTrendChart(history) {
  const svg   = document.getElementById('trend-chart');
  const empty = document.getElementById('trend-empty');
  svg.innerHTML = '';
  const sessions = history.filter(h => h.mode !== 'memo' && h.date);
  if (!sessions.length) {
    svg.style.display = 'none';
    if (empty) empty.style.display = '';
    return;
  }
  // 按天聚合：同一天的所有 session 合并计算正确率
  const byDay = {};
  sessions.forEach(h => {
    if (!byDay[h.date]) byDay[h.date] = { total: 0, correct: 0, date: h.date };
    byDay[h.date].total   += h.total || 0;
    byDay[h.date].correct += h.correct || 0;
  });
  const recent = Object.values(byDay)
    .sort((a, b) => a.date.localeCompare(b.date))
    .slice(-15)
    .map(d => ({ ...d, pct: d.total > 0 ? Math.round(d.correct / d.total * 100) : 0 }));
  if (!recent.length) {
    svg.style.display = 'none';
    if (empty) empty.style.display = '';
    return;
  }
  svg.style.display = '';
  if (empty) empty.style.display = 'none';

  const W = 320, H = 72, PAD = 20, BAR_GAP = 4;
  const n   = recent.length;
  const bw  = Math.max(6, (W - PAD * 2 - BAR_GAP * (n - 1)) / n);
  const pts = recent.map((h, i) => ({
    x: PAD + i * (bw + BAR_GAP) + bw / 2,
    y: H - PAD - (h.pct / 100) * (H - PAD * 2),
    pct: h.pct,
    date: h.date || '',
  }));

  // 渐变背景填充
  const gradId = 'tg' + Math.random().toString(36).slice(2, 7);
  svg.innerHTML += `<defs>
    <linearGradient id="${gradId}" x1="0" y1="0" x2="0" y2="1">
      <stop offset="0%" stop-color="var(--accent)" stop-opacity=".25"/>
      <stop offset="100%" stop-color="var(--accent)" stop-opacity="0"/>
    </linearGradient>
  </defs>`;

  // 填充区域
  if (pts.length > 1) {
    const areaD = `M ${pts[0].x} ${H - PAD} ` +
        pts.map(p => `L ${p.x} ${p.y}`).join(' ') +
        ` L ${pts[pts.length-1].x} ${H - PAD} Z`;
    svg.innerHTML += `<path d="${areaD}" fill="url(#${gradId})"/>`;
    const lineD = `M ` + pts.map(p => `${p.x} ${p.y}`).join(' L ');
    svg.innerHTML += `<path d="${lineD}" class="trend-line"/>`;
  }

  pts.forEach((p, i) => {
    // 圆点
    svg.innerHTML += `<circle cx="${p.x}" cy="${p.y}" r="3.5" class="trend-dot"/>`;
    // 正确率标签（每隔一个显示，防止拥挤）
    if (n <= 8 || i % 2 === 0) {
      svg.innerHTML += `<text x="${p.x}" y="${p.y - 7}" text-anchor="middle"
        class="trend-pct-label">${p.pct}%</text>`;
    }
    // 日期标签（首尾）
    if (i === 0 || i === n - 1) {
      svg.innerHTML += `<text x="${p.x}" y="${H - 4}" text-anchor="middle"
        class="trend-label">${p.date.replace(/\d{4}\//, '')}</text>`;
    }
  });
}

function _renderUnitStats(units) {
  const el = document.getElementById('unit-stats-list');
  if (!units.length) {
    el.innerHTML = '<div style="color:var(--muted);font-size:13px;padding:8px 0">暂无数据</div>';
    return;
  }
  // 按正确率从低到高排（弱项优先）
  const sorted = [...units].sort((a, b) => a.accuracy - b.accuracy);
  el.innerHTML = sorted.map(u => {
    const color = u.accuracy >= 80 ? 'var(--success)'
        : u.accuracy >= 60 ? 'var(--warning)'
            : 'var(--danger)';
    return `<div class="unit-stat-row">
      <span class="unit-stat-name" title="${esc(u.unit)}">${esc(u.unit)}</span>
      <div class="unit-stat-bar-wrap">
        <div class="unit-stat-bar" style="width:${u.accuracy}%;background:${color}"></div>
      </div>
      <span class="unit-stat-pct" style="color:${color}">${u.accuracy}%</span>
      <span class="unit-stat-cnt">${u.total}题</span>
    </div>`;
  }).join('');
}

function _renderWrongbookPreview(items) {
  const el  = document.getElementById('wrongbook-preview');
  const btn = document.getElementById('wrongbook-all-btn');
  if (!items.length) {
    el.innerHTML = '<div style="color:var(--muted);font-size:13px;padding:8px 0">暂无错题记录 👍</div>';
    if (btn) btn.style.display = 'none';
    return;
  }
  if (btn) btn.style.display = '';
  el.innerHTML = '';
  items.slice(0, 5).forEach((it, idx) => {
    const hasText = it.text && it.text.trim();
    // 去除 HTML 标签后的纯文字
    const plainText = hasText
      ? it.text.replace(/<[^>]+>/g, '').replace(/\s+/g, ' ').trim()
      : '';

    const card = document.createElement('div');
    card.className = 'wb-card';

    // ── 展开体（完整内容）────────────────────────────────
    const bodyHTML = `
      <div class="wb-card-body" style="display:none">
        ${it.stem ? `<div class="wb-stem">${renderHTML(it.stem)}</div>` : ''}
        ${hasText
          ? `<div class="wb-text">${renderHTML(it.text)}</div>`
          : `<div class="wb-no-text">题目与当前题库版本不匹配，无法显示内容<br>
             <span style="font-size:11px;opacity:.6">ID: ${esc(it.fingerprint)}</span></div>`}
        ${(() => {
          const opts = it.options || it.shared_options || [];
          if (!opts.length) return '';
          // 答案可能是单字母(A)或多字母(AB)
          const answerSet = new Set(
            it.answer ? it.answer.toUpperCase().split('').filter(ch => /[A-Z]/.test(ch)) : []
          );
          const letters = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ';
          return '<div class="wb-options">' +
            opts.map((opt, i) => {
              const letter = letters[i] || String(i + 1);
              // 剥离选项文本里可能自带的字母前缀（"A." "A、" "A）" 等）
              const cleanOpt = opt.replace(/^[A-Za-z]\s*[.．、·）)·\s]\s*/u, '').trim();
              const isCor = answerSet.has(letter);
              return `<div class="wb-option${isCor ? ' wb-option-correct' : ''}">` +
                `<span class="wb-option-key">${letter}</span>${esc(cleanOpt)}</div>`;
            }).join('') + '</div>';
        })()}
        ${it.answer ? `
          <div class="wb-answer">
            <span class="wb-label">正确答案：</span><strong>${esc(it.answer)}</strong>
          </div>` : ''}
        ${it.discuss ? `
          <div class="wb-discuss">
            <div class="wb-label">解析</div>
            <div class="wb-discuss-body">${renderHTML(it.discuss)}</div>
          </div>` : ''}
        <div class="wb-meta">
          ${it.unit ? `<span class="wb-unit">${esc(it.unit)}</span>` : ''}
          <span class="wb-stat">共答 ${it.total} 次，答对 ${it.correct} 次，正确率 ${it.accuracy}%</span>
        </div>
      </div>`;

    // ── 标题行（始终可见）────────────────────────────────
    card.innerHTML = `
      <div class="wb-card-header" onclick="_toggleWbCard(this)">
        <div class="wb-wrong-badge">✗${it.wrong}</div>
        <div class="wb-card-main">
          <div class="wb-preview-text">${esc(plainText || '（题目内容不可用）')}</div>
          <div class="wb-preview-meta">
            <span>正确率 <strong style="color:${it.accuracy>=60?'var(--success)':'var(--danger)'}">${it.accuracy}%</strong></span>
            <span>共答 ${it.total} 次</span>
          </div>
        </div>
        <div class="wb-chevron">▾</div>
      </div>
      ${bodyHTML}`;

    el.appendChild(card);
  });
}

function _toggleWbCard(header) {
  const card    = header.parentElement;
  const body    = header.nextElementSibling;
  const chevron = header.querySelector('.wb-chevron');
  const open    = body.style.display === 'none';
  body.style.display = open ? '' : 'none';
  if (chevron) chevron.textContent = open ? '▴' : '▾';
  card.classList.toggle('wb-card-open', open);
}

// ════════════════════════════════════════════
// History
// ════════════════════════════════════════════
function loadHistory() {
  try { S.history = JSON.parse(localStorage.getItem(historyKey()) || '[]'); } catch { S.history = []; }
  try {
    const arr = JSON.parse(localStorage.getItem(deletedIdsKey()) || '[]');
    S.deletedIds = new Set(arr);
  } catch { S.deletedIds = new Set(); }
  // 同时从服务端拉取完整历史，成功后合并刷新（不阻塞本地渲染）
  _fetchServerHistory();
}

async function _fetchServerHistory() {
  try {
    const res = await apiFetch('/api/history?' + bankQS() + '&limit=50');
    if (!res.ok) return;
    const data = await res.json();
    const items = data.items || [];
    // 过滤掉本地已删除的 id（防止并发时已删项被服务端响应覆盖写回）
    const filtered = items.filter(h => !S.deletedIds?.has(String(h.id)));
    // 合并：服务端历史作为主数据，用 id 去重本地记录
    const serverIds = new Set(filtered.map(h => h.id));
    const localOnly = S.history.filter(h => h.id && !serverIds.has(h.id));
    S.serverHistory = filtered;
    S.localOnlyHistory = localOnly;
    renderHistorySection();
  } catch (e) { /* 离线时静默失败，保留本地数据 */ }
}

// ── 置顶记录持久化 ──────────────────────────────────
function _pinnedKey() { return 'quiz-pinned' + _bankSuffix(); }
function _getPinned() {
  try { return new Set(JSON.parse(localStorage.getItem(_pinnedKey()) || '[]')); }
  catch { return new Set(); }
}
function _savePinned(set) {
  localStorage.setItem(_pinnedKey(), JSON.stringify([...set]));
}

function renderHistorySection() {
  const sec  = document.getElementById('recent-section');
  const list = document.getElementById('recent-list');

  const practiceSessions = _getPracticeSessions();
  const serverH    = S.serverHistory   || [];
  const localOnlyH = S.localOnlyHistory || [];
  const baseHistory = serverH.length
    ? [...localOnlyH, ...serverH]
    : (S.history || []);

  if (!practiceSessions.length && !baseHistory.length) {
    sec.style.display = 'none';
    return;
  }
  sec.style.display = '';
  list.innerHTML = '';

  const modeIcon  = { practice:'✏️', exam:'⏱', memo:'🧠' };
  const pinned    = _getPinned();
  const unitsText = h => {
    const arr = Array.isArray(h.units) ? h.units : (h.units ? [h.units] : []);
    if (!arr.length) return '全部章节';
    return arr.length <= 2 ? arr.join('、') : arr.slice(0,2).join('、') + ' 等';
  };

  // 置顶排前、其余按时间倒序
  const sorted = [...baseHistory].sort((a, b) => {
    const pa = pinned.has(a.id) ? 1 : 0;
    const pb = pinned.has(b.id) ? 1 : 0;
    return pb - pa;
  });

  // ── 进行中的练习 ────────────────────────────────
  practiceSessions.forEach((s, psIdx) => {
    const ago = _fmtAgo(s.savedAt);
    const el  = _mkRecentItem('ps:' + s.id);

    const delMask = document.createElement('div');
    delMask.className = 'rh-del-mask';
    delMask.innerHTML = '🗑 删除';

    const content = document.createElement('div');
    content.className = 'rh-content';
    content.innerHTML = `
      <div class="recent-info" onclick="resumePracticeSession('${s.id}')">
        <div class="recent-name">✏️ ${esc(s.title)}<span class="recent-badge ongoing">进行中</span></div>
        <div class="recent-meta">已答 ${s.answered}/${s.total} 题 · ${ago}</div>
      </div>
      <div style="display:flex;align-items:center;gap:6px;flex-shrink:0">
        <button class="recent-resume-btn" onclick="resumePracticeSession('${s.id}')">继续 →</button>
        <button class="recent-del-btn pc-only" title="删除"
          onclick="event.stopPropagation();_deletePracticeSession('${s.id}',this)">✕</button>
      </div>`;

    el.appendChild(delMask);
    el.appendChild(content);
    list.appendChild(el);

    // 左滑删除（进行中只支持删除，不支持置顶）
    _bindSwipePractice(el, s.id);
  });

  // ── 已完成历史 ───────────────────────────────────
  sorted.slice(0, 20).forEach((h, idx) => {
    const id      = h.id || ('idx:' + idx);
    const isLocal = !h.id || localOnlyH.some(l => l.id === h.id);
    const isPinned = pinned.has(h.id);
    const syncBadge = isLocal && serverH.length
      ? '<span class="recent-badge pending">待同步</span>' : '';
    const pinBadge  = isPinned
      ? '<span class="recent-badge pinned">📌 置顶</span>' : '';

    const el = _mkRecentItem(id);

    // 左滑遮罩（删除）
    const delMask = document.createElement('div');
    delMask.className = 'rh-del-mask';
    delMask.innerHTML = '🗑 删除';

    // 右滑遮罩（置顶/取消置顶）
    const pinMask = document.createElement('div');
    pinMask.className = 'rh-pin-mask';
    pinMask.innerHTML = isPinned ? '📌 取消置顶' : '📌 置顶';

    // 内容层
    const content = document.createElement('div');
    content.className = 'rh-content';
    content.innerHTML = `
      <div class="recent-info" onclick="openHistoryResult('${id}',${idx})">
        <div class="recent-name">
          ${modeIcon[h.mode]||''} ${esc(h.date)} · ${esc(unitsText(h))}
          <span class="recent-badge done">已完成</span>${syncBadge}${pinBadge}
        </div>
        <div class="recent-meta">${h.total} 题 · 用时 ${formatTime(h.time_sec || h.timeSec || 0)}</div>
      </div>
      <div style="display:flex;align-items:center;gap:6px;flex-shrink:0">
        <div class="recent-score">${h.pct}%</div>
        <button class="recent-del-btn pc-only" title="删除"
          onclick="event.stopPropagation();_confirmDelete('${id}',${idx})">✕</button>
      </div>`;

    el.appendChild(delMask);
    el.appendChild(pinMask);
    el.appendChild(content);
    list.appendChild(el);

    _bindSwipe(el, id, idx, isPinned);
  });
}

function _mkRecentItem(id) {
  const el = document.createElement('div');
  el.className = 'recent-item rh-swipe-wrap';
  if (id) el.dataset.hid = id;
  return el;
}

// ── 手势绑定 ───────────────────────────────────────
function _bindSwipe(wrap, id, idx, isPinned) {
  const content  = wrap.querySelector('.rh-content');
  const delMask  = wrap.querySelector('.rh-del-mask');
  const pinMask  = wrap.querySelector('.rh-pin-mask');
  const THRESHOLD = 72;   // 触发动作的最小滑动距离
  const MAX_DRAG  = 110;

  let startX = 0, startY = 0, dx = 0, dragging = false, axis = null;

  function onStart(x, y) {
    startX = x; startY = y; dx = 0; axis = null; dragging = true;
    content.style.transition = 'none';
  }
  function onMove(x, y) {
    if (!dragging) return;
    const rawDx = x - startX;
    const rawDy = y - startY;
    if (!axis) {
      if (Math.abs(rawDx) < 6 && Math.abs(rawDy) < 6) return;
      axis = Math.abs(rawDx) > Math.abs(rawDy) ? 'x' : 'y';
    }
    if (axis === 'y') return;  // 纵向滚动不拦截
    dx = Math.max(-MAX_DRAG, Math.min(MAX_DRAG, rawDx));
    content.style.transform = `translateX(${dx}px)`;
    // 遮罩随拖动显示
    delMask.style.opacity  = dx < 0 ? Math.min(1, -dx / THRESHOLD) : 0;
    pinMask.style.opacity  = dx > 0 ? Math.min(1, dx / THRESHOLD) : 0;
  }
  function onEnd() {
    if (!dragging || axis !== 'x') { dragging = false; return; }
    dragging = false;
    content.style.transition = 'transform .25s ease';

    if (dx < -THRESHOLD) {
      // 左滑超阈值 → 滑出后显示撤销
      content.style.transform = `translateX(-100%)`;
      delMask.style.opacity = 1;
      _showDeleteWithUndo(wrap, id, idx);
    } else if (dx > THRESHOLD) {
      // 右滑超阈值 → 置顶/取消置顶
      content.style.transform = 'translateX(0)';
      pinMask.style.opacity = 0;
      _togglePin(id, isPinned);
    } else {
      // 未超阈值 → 弹回
      content.style.transform = 'translateX(0)';
      delMask.style.opacity = 0;
      pinMask.style.opacity = 0;
    }
  }

  // Touch
  wrap.addEventListener('touchstart', e => {
    onStart(e.touches[0].clientX, e.touches[0].clientY);
  }, { passive: true });
  wrap.addEventListener('touchmove', e => {
    if (axis === 'x') e.preventDefault();
    onMove(e.touches[0].clientX, e.touches[0].clientY);
  }, { passive: false });
  wrap.addEventListener('touchend', onEnd, { passive: true });

  // Mouse (PC 鼠标拖拽也支持，但 PC 主要靠按钮)
  wrap.addEventListener('mousedown', e => {
    if (e.button !== 0) return;
    onStart(e.clientX, e.clientY);
    const up  = () => { onEnd(); cleanup(); };
    const mv  = e2 => onMove(e2.clientX, e2.clientY);
    const cleanup = () => { window.removeEventListener('mousemove', mv); window.removeEventListener('mouseup', up); };
    window.addEventListener('mousemove', mv);
    window.addEventListener('mouseup', up);
  });
}

// ── 置顶逻辑 ──────────────────────────────────────
function _togglePin(id, wasPinned) {
  if (!id) return;
  const pinned = _getPinned();
  if (wasPinned) pinned.delete(id);
  else           pinned.add(id);
  _savePinned(pinned);
  renderHistorySection();
  toast(wasPinned ? '已取消置顶' : '已置顶');
}

// ── 滑动删除 + 撤销 ──────────────────────────────
function _showDeleteWithUndo(wrap, id, idx) {
  // 立即标记为已删，防止 _fetchServerHistory 在等待期间把它加回来
  if (id) {
    if (!S.deletedIds) S.deletedIds = new Set();
    S.deletedIds.add(id);
    localStorage.setItem(deletedIdsKey(), JSON.stringify([...S.deletedIds]));
  }
  const undo = document.createElement('div');
  undo.className = 'rh-undo-bar';
  undo.innerHTML = `<span>已删除</span><button onclick="_undoDelete(this,'${id}',${idx})">撤销</button>`;
  wrap.replaceWith(undo);
  requestAnimationFrame(() => undo.classList.add('rh-undo-in'));

  // 2.7s 后开始淡出，3s 后真正删除
  const fadeTimer = setTimeout(() => undo.classList.add('rh-undo-out'), 2700);
  const delTimer  = setTimeout(() => {
    undo.remove();
    deleteHistoryItem(id, idx);
  }, 3000);
  undo._fadeTimer = fadeTimer;
  undo._timer     = delTimer;
}

function _undoDelete(btn, id, idx) {
  const bar = btn.parentElement;
  clearTimeout(bar._fadeTimer);
  clearTimeout(bar._timer);
  // 撤销：从 deletedIds 移除
  if (id && S.deletedIds) {
    S.deletedIds.delete(id);
    localStorage.setItem(deletedIdsKey(), JSON.stringify([...S.deletedIds]));
  }
  bar.classList.remove('rh-undo-out');
  bar.classList.add('rh-undo-in');
  // 淡出后移除
  bar.classList.add('rh-undo-out');
  setTimeout(() => { bar.remove(); renderHistorySection(); }, 300);
}

// ── 进行中练习：左滑删除 ──────────────────────────
function _bindSwipePractice(wrap, sessionId) {
  const content = wrap.querySelector('.rh-content');
  const delMask = wrap.querySelector('.rh-del-mask');
  const THRESHOLD = 72, MAX_DRAG = 110;
  let startX = 0, startY = 0, dx = 0, dragging = false, axis = null;

  function onStart(x, y) { startX=x; startY=y; dx=0; axis=null; dragging=true; content.style.transition='none'; }
  function onMove(x, y) {
    if (!dragging) return;
    const rawDx = x-startX, rawDy = y-startY;
    if (!axis) { if (Math.abs(rawDx)<6 && Math.abs(rawDy)<6) return; axis=Math.abs(rawDx)>Math.abs(rawDy)?'x':'y'; }
    if (axis==='y') return;
    dx = Math.max(-MAX_DRAG, Math.min(0, rawDx));  // 进行中只能左滑
    content.style.transform = `translateX(${dx}px)`;
    delMask.style.opacity = Math.min(1, -dx/THRESHOLD);
  }
  function onEnd() {
    if (!dragging || axis!=='x') { dragging=false; return; }
    dragging=false;
    content.style.transition = 'transform .25s ease';
    if (dx < -THRESHOLD) {
      content.style.transform = 'translateX(-100%)';
      delMask.style.opacity = 1;
      _showPracticeDeleteUndo(wrap, sessionId);
    } else {
      content.style.transform = 'translateX(0)';
      delMask.style.opacity = 0;
    }
  }
  wrap.addEventListener('touchstart', e=>onStart(e.touches[0].clientX, e.touches[0].clientY), {passive:true});
  wrap.addEventListener('touchmove', e=>{ if(axis==='x') e.preventDefault(); onMove(e.touches[0].clientX, e.touches[0].clientY); }, {passive:false});
  wrap.addEventListener('touchend', onEnd, {passive:true});
  wrap.addEventListener('mousedown', e=>{ if(e.button!==0) return; onStart(e.clientX,e.clientY);
    const up=()=>{onEnd();cleanup();}; const mv=e2=>onMove(e2.clientX,e2.clientY);
    const cleanup=()=>{window.removeEventListener('mousemove',mv);window.removeEventListener('mouseup',up);};
    window.addEventListener('mousemove',mv); window.addEventListener('mouseup',up); });
}

function _showPracticeDeleteUndo(wrap, sessionId) {
  const undo = document.createElement('div');
  undo.className = 'rh-undo-bar';
  undo.innerHTML = `<span>已删除</span><button onclick="_undoPracticeDelete(this,'${sessionId}')">撤销</button>`;
  wrap.replaceWith(undo);
  requestAnimationFrame(() => undo.classList.add('rh-undo-in'));
  const fadeTimer = setTimeout(() => undo.classList.add('rh-undo-out'), 2700);
  const delTimer  = setTimeout(() => { undo.remove(); _deletePracticeSession(sessionId); }, 3000);
  undo._fadeTimer = fadeTimer;
  undo._timer = delTimer;
}

function _undoPracticeDelete(btn, sessionId) {
  const bar = btn.parentElement;
  clearTimeout(bar._fadeTimer); clearTimeout(bar._timer);
  bar.classList.add('rh-undo-out');
  setTimeout(() => { bar.remove(); renderHistorySection(); }, 300);
}

function _deletePracticeSession(sessionId, btn) {
  if (btn) { // PC 按钮点击：confirm
    if (!confirm('确定放弃这条进行中的记录？')) return;
  }
  clearPracticeSession(sessionId);
  renderHistorySection();
}

// ── PC 按钮删除（confirm） ──────────────────────────
function _confirmDelete(id, idx) {
  if (!confirm('确定删除这条练习记录？')) return;
  deleteHistoryItem(id, idx);
}

function _fmtAgo(ts) {
  const sec = Math.floor((Date.now() - ts) / 1000);
  if (sec < 60)  return '刚刚';
  if (sec < 3600) return Math.floor(sec/60) + ' 分钟前';
  if (sec < 86400) return Math.floor(sec/3600) + ' 小时前';
  return Math.floor(sec/86400) + ' 天前';
}

/** 点击已完成记录 → 查看结果摘要页 */
function openHistoryResult(id, idx) {
  // 用与 renderHistorySection 完全一致的数据源和排序，避免 idx 错位
  const serverH    = S.serverHistory    || [];
  const localOnlyH = S.localOnlyHistory || [];
  const baseHistory = serverH.length ? [...localOnlyH, ...serverH] : (S.history || []);
  const pinned = (typeof _getPinned === 'function') ? _getPinned() : new Set();
  const sorted = [...baseHistory].sort((a, b) => {
    const pa = pinned.has(a.id) ? 1 : 0;
    const pb = pinned.has(b.id) ? 1 : 0;
    return pb - pa;
  });
  let h = null;
  // 优先用 id 精确匹配
  if (id && !String(id).startsWith('idx:')) {
    h = sorted.find(x => String(x.id) === String(id))
     || baseHistory.find(x => String(x.id) === String(id));
  }
  // 再用 idx 兜底
  if (!h) h = sorted[idx] || baseHistory[idx] || (S.history && S.history[idx]);
  if (!h) { toast('记录不存在或已删除', true); return; }

  // 尝试从复盘缓存恢复题目和答案（按真实 id 查找）
  let cachedQs = null, cachedAns = null;
  try {
    const cache = JSON.parse(localStorage.getItem(_reviewCacheKey()) || '[]');
    const entry = cache.find(e => String(e.id) === String(h.id));
    if (entry) { cachedQs = entry.qs; cachedAns = entry.ans; }
  } catch (e) { /* 缓存读取失败静默忽略 */ }

  // 恢复 S.questions 和 S.mode，让"再练一次"能复用同一批题目
  if (cachedQs) {
    S.questions = cachedQs;
    S.mode      = h.mode || S.mode;
  }

  // 恢复时间限制（供"分享试卷"使用原始时限）
  const restoredLimit = h.time_limit || h.timeLimit || 0;
  if (restoredLimit > 0) S.examLimit = restoredLimit;

  S.results = {
    mode:    h.mode,
    total:   h.total,
    correct: h.correct,
    wrong:   h.wrong ?? (h.total - h.correct - (h.skip || 0)),
    skip:    h.skip || 0,
    timeSec: h.time_sec || h.timeSec || 0,
    timeLimit: restoredLimit || (S.examLimit || 0),
    byUnit:  null,
    qs:      cachedQs,
    ans:     cachedAns,
  };

  // 查看解析按钮：有缓存才可用，否则置灰提示
  const reviewBtn = document.getElementById('res-review-detail-btn');
  if (reviewBtn) {
    if (cachedQs) {
      reviewBtn.disabled = false;
      reviewBtn.title = '';
    } else {
      reviewBtn.disabled = true;
      reviewBtn.title = '当前记录不包含题目详情（仅最近10次有效）';
    }
  }

  renderResults();
  showScreen('s-results');
}

/** 删除单条历史记录（由滑动确认或 PC 按钮 confirm 后调用，不再二次确认） */
async function deleteHistoryItem(id, idx) {
  // 0. 持久标记为已删（永不自动移除，防止任何途径复活）
  if (id) {
    if (!S.deletedIds) S.deletedIds = new Set();
    S.deletedIds.add(String(id));
    localStorage.setItem(deletedIdsKey(), JSON.stringify([...S.deletedIds]));
  }
  // 1. 从 SyncManager IndexedDB 队列移除（防止未同步的会话被重新上传）
  if (id && typeof SyncManager !== 'undefined' && SyncManager.purgeSession) {
    try { await SyncManager.purgeSession(String(id)); } catch(e) {}
  }
  // 2. 从服务端删除（有真实 id，排除 'idx:N' 占位符）
  if (id && !id.startsWith('idx:')) {
    try {
      await apiFetch('/api/session/' + encodeURIComponent(id) + '?' + bankQS(), {
        method: 'DELETE',
      });
      // 注意：不从 deletedIds 移除！保持作为永久墓碑
    } catch (e) { /* 离线时仍删本地，deletedIds 持久过滤 */ }
    if (S.serverHistory) {
      S.serverHistory = S.serverHistory.filter(h => h.id !== id);
    }
  }

  // 2. 从本地历史删除
  //    有真实 id → 按 id 匹配；仅有占位 idx:N → 按数组位置匹配
  if (id && !id.startsWith('idx:')) {
    S.history = S.history.filter(h => h.id !== id);
  } else {
    const fakeIdx = parseInt(id.replace('idx:', ''), 10);
    S.history = S.history.filter((_, i) => i !== fakeIdx);
  }
  localStorage.setItem(historyKey(), JSON.stringify(S.history));
  if (S.localOnlyHistory) {
    S.localOnlyHistory = S.localOnlyHistory.filter(h => h.id !== id);
  }
  // 同时从置顶移除
  const pinned = _getPinned();
  pinned.delete(id);
  _savePinned(pinned);

  renderHistorySection();
}

// ════════════════════════════════════════════
// Theme
// ════════════════════════════════════════════
function toggleTheme() {
  const cur = document.documentElement.getAttribute('data-theme');
  const next = cur === 'dark' ? 'light' : 'dark';
  document.documentElement.setAttribute('data-theme', next);
  localStorage.setItem('quiz-theme', next);
}
function loadTheme() {
  const t = localStorage.getItem('quiz-theme') || 'dark';
  document.documentElement.setAttribute('data-theme', t);
}

// ════════════════════════════════════════════
// Keyboard shortcuts
// ════════════════════════════════════════════
document.addEventListener('keydown', e => {
  // 输入框/文本域内不触发快捷键
  const tag = document.activeElement && document.activeElement.tagName;
  if (tag === 'INPUT' || tag === 'TEXTAREA') return;

  const active = document.querySelector('.screen.active')?.id;
  if (active === 's-quiz') {
    if ('1234'.includes(e.key)) {
      const i = Number(e.key) - 1;
      const opts = document.querySelectorAll('.opt:not(:disabled)');
      if (opts[i]) opts[i].click();
    }
    if ('abcdABCD'.includes(e.key)) {
      const i = e.key.toUpperCase().charCodeAt(0) - 65;
      const opts = document.querySelectorAll('.opt:not(:disabled)');
      if (opts[i]) opts[i].click();
    }
    if (e.key === 'ArrowRight' || e.key === 'Enter') {
      const btn = document.getElementById('btn-next');
      if (!btn.disabled) btn.click();
    }
    if (e.key === 'ArrowLeft') {
      const btn = document.getElementById('btn-prev');
      if (!btn.disabled) btn.click();
    }
    // M 或 Space → 标记/取消标记
    if (e.key === 'm' || e.key === 'M' || e.key === ' ') {
      e.preventDefault();
      toggleFlag();
    }
    // F → 同 M（保持向后兼容）
    if (e.key === 'f' || e.key === 'F') toggleFlag();
    // E → 展开/滚动到解析区
    if (e.key === 'e' || e.key === 'E') {
      const explain = document.getElementById('explain-panel');
      if (explain) {
        explain.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
        // 若解析是折叠的（有 hidden 类），展开它
        explain.classList.remove('hidden');
      }
    }
  }
  if (active === 's-memo') {
    if (e.key === ' ' || e.key === 'Enter') {
      e.preventDefault();
      if (!S.memoRevealed) memoReveal();
      else memoKnow();    // Enter = 记住了 (quality=5)
    }
    if (e.key === 'ArrowLeft' || e.key === 'r' || e.key === 'R') {
      if (S.memoRevealed) memoAgain(); // R / ← = 忘了 (quality=1)
    }
    if (e.key === 'h' || e.key === 'H') {
      if (S.memoRevealed) memoHard();  // H = 模糊 (quality=2)
    }
  }
});

// ════════════════════════════════════════════
// Utils
// ════════════════════════════════════════════
function esc(s) {
  return (s||'').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;')
      .replace(/"/g,'&quot;').replace(/'/g,'&#39;');
}
function formatTime(sec) {
  if (sec < 60) return sec + '秒';
  const m = Math.floor(sec / 60), s = sec % 60;
  return s > 0 ? `${m}分${s}秒` : `${m}分钟`;
}


// 背题：点击卡片主体也可以揭示答案
document.getElementById('memo-card')?.addEventListener('click', (e) => {
  if (!S.memoRevealed && document.querySelector('.screen.active')?.id === 's-memo') {
    // 只有点击题目区触发揭示（避免点按钮区时重复）
    if (!e.target.closest('.memo-rate-row') && !e.target.closest('.memo-reveal-btn')) {
      memoReveal();
    }
  }
});


// ════════════════════════════════════════════
// 触控手势系统
// ════════════════════════════════════════════
function vibrate(p){ try{navigator.vibrate?.(p)}catch(e){} }

function getSwipeDir(dx, dy){
  const adx=Math.abs(dx), ady=Math.abs(dy);
  if(adx<6 && ady<6) return null;
  if(ady > adx*1.3) return 'vertical';
  return dx>0?'right':'left';
}

// ── 检查触摸起点是否在可横向滚动的容器内 ─────────────────────
// 用于避免代码块等横向滚动区域被误判为切题手势
function _isTouchInHScrollable(el) {
  while (el && el !== document.body) {
    const tag = el.tagName;
    // pre / code blocks are always considered h-scrollable
    if (tag === 'PRE' || tag === 'CODE') return true;
    const style = window.getComputedStyle(el);
    const ox = style.overflowX;
    if ((ox === 'auto' || ox === 'scroll') && el.scrollWidth > el.clientWidth + 2) return true;
    el = el.parentElement;
  }
  return false;
}

// ── 做题区：左右滑动切题 ─────────────────────
(function setupQuizSwipe(){
  const THRESHOLD=70, VELOCITY=0.32, RESIST=0.35;
  let tx=0,ty=0,t0=0,locked=null,dragging=false,touchTarget=null;

  const quizScreen = document.getElementById('s-quiz');
  if(!quizScreen) return;
  const quizBody = quizScreen.querySelector('.quiz-body');
  if(!quizBody) return;
  const hintL = document.getElementById('swipe-hint-left');
  const hintR = document.getElementById('swipe-hint-right');

  function stage(){ return quizBody.querySelector('.q-stage'); }
  function canPrev(){
    if(S.cur<=0) return false;
    if(S.mode==='exam') return examCanGoBack(S.cur);
    return true;
  }
  function canNext(){
    if(S.mode==='exam'){
      if(S.cur>=S.questions.length-1) return false;
      // 跨题型组时不允许滑动（需按钮弹窗确认）
      const nextGIdx=getGroupIdxForQ(S.cur+1);
      const curGIdx =getGroupIdxForQ(S.cur);
      return nextGIdx===curGIdx;
    }
    // 练习模式：自由滑动，只要不是最后一题
    return S.cur < S.questions.length - 1;
  }
  function updateHints(dx){
    hintL?.classList.toggle('visible', dx>10 && canPrev());
    hintR?.classList.toggle('visible', dx<-10 && canNext());
    hintL?.classList.toggle('active', Math.abs(dx)>=THRESHOLD && dx>0 && canPrev());
    hintR?.classList.toggle('active', Math.abs(dx)>=THRESHOLD && dx<0 && canNext());
  }
  function resetHints(){
    hintL?.classList.remove('visible','active');
    hintR?.classList.remove('visible','active');
  }

  quizBody.addEventListener('touchstart', e=>{
    if(document.querySelector('.screen.active')?.id!=='s-quiz') return;
    const t=e.touches[0]; tx=t.clientX; ty=t.clientY; t0=Date.now();
    locked=null; dragging=false; touchTarget=e.target;
  },{passive:true});

  quizBody.addEventListener('touchmove', e=>{
    if(document.querySelector('.screen.active')?.id!=='s-quiz') return;
    const t=e.touches[0], dx=t.clientX-tx, dy=t.clientY-ty;
    if(!locked){ const d=getSwipeDir(dx,dy); if(!d) return;
      if(d!=='vertical'&&_isTouchInHScrollable(touchTarget)){locked='vertical';return;}
      locked=d;
    }
    if(locked==='vertical') return;
    e.preventDefault();
    dragging=true;
    const s=stage(); if(!s) return;
    s.classList.add('is-dragging');
    let move=dx;
    if((dx>0&&!canPrev())||(dx<0&&!canNext())) move=dx*RESIST;
    s.style.transform=`translateX(${move}px)`;
    updateHints(dx);
  },{passive:false});

  quizBody.addEventListener('touchend', e=>{
    if(!dragging){dragging=false;return;}
    dragging=false; resetHints();
    const s=stage(); if(!s) return;
    s.classList.remove('is-dragging');
    s.style.transform='';
    const dx=e.changedTouches[0].clientX-tx;
    const dt=Date.now()-t0;
    const vel=Math.abs(dx)/dt;
    const ok=Math.abs(dx)>=THRESHOLD||vel>=VELOCITY;
    if(ok){
      if(dx>0&&canPrev()){
        vibrate(10); S.cur--; renderQ('back');
        if(S.mode==='exam') updateGridDot();
      } else if(dx<0&&canNext()){
        vibrate(10); S.cur++; renderQ('forward');
        if(S.mode==='exam') updateGridDot();
      } else {
        s.style.transition='transform .22s cubic-bezier(.25,.46,.45,.94)';
        s.style.transform=`translateX(${dx>0?5:-5}px)`;
        setTimeout(()=>{s.style.transition='';s.style.transform='';},230);
        vibrate([6,30,6]);
      }
    } else if(Math.abs(dx)>8){
      s.style.transition='transform .2s cubic-bezier(.25,.46,.45,.94)';
      setTimeout(()=>{s.style.transition='';},220);
    }
  },{passive:true});
})();

// ── 背题卡片：左划=已掌握，右划=再练 ──────────
(function setupMemoSwipe(){
  const THRESHOLD=80, VELOCITY=0.38, RESIST=0.38;
  let tx=0,ty=0,t0=0,locked=null,dragging=false,touchTarget=null;

  function card(){ return document.getElementById('memo-card'); }
  function overlay(){ return document.getElementById('memo-swipe-overlay'); }

  function setOverlay(dx){
    const o=overlay(); if(!o||!S.memoRevealed) return;
    const pct=Math.min(Math.abs(dx)/THRESHOLD,1);
    const trig=Math.abs(dx)>=THRESHOLD;
    if(dx<-20){
      o.className='memo-swipe-overlay'+(trig?' show-know':'');
      o.innerHTML=trig?'✓ 已掌握':'';
      o.style.opacity=pct*0.85;
    } else if(dx>20){
      o.className='memo-swipe-overlay'+(trig?' show-again':'');
      o.innerHTML=trig?'↺ 再练':'';
      o.style.opacity=pct*0.85;
    } else {
      o.className='memo-swipe-overlay'; o.innerHTML=''; o.style.opacity=0;
    }
  }
  function resetOverlay(){
    const o=overlay(); if(o){o.className='memo-swipe-overlay';o.innerHTML='';o.style.opacity=0;}
  }

  const memoBody = document.querySelector('#s-memo .memo-body');
  if(!memoBody) return;

  memoBody.addEventListener('touchstart', e=>{
    if(document.querySelector('.screen.active')?.id!=='s-memo') return;
    if(e.target.closest('.memo-reveal-btn')||e.target.closest('.memo-rate-row')) return;
    const t=e.touches[0]; tx=t.clientX; ty=t.clientY; t0=Date.now();
    locked=null; dragging=false; touchTarget=e.target;
  },{passive:true});

  memoBody.addEventListener('touchmove', e=>{
    if(document.querySelector('.screen.active')?.id!=='s-memo') return;
    const t=e.touches[0], dx=t.clientX-tx, dy=t.clientY-ty;
    if(!locked){const d=getSwipeDir(dx,dy);if(!d) return;
      if(d!=='vertical'&&_isTouchInHScrollable(touchTarget)){locked='vertical';return;}
      locked=d;}
    if(locked==='vertical') return;
    if(!S.memoRevealed) return;
    e.preventDefault(); dragging=true;
    const c=card(); if(!c) return;
    c.classList.add('is-dragging');
    c.style.transform=`translateX(${dx}px) rotate(${dx*0.018}deg)`;
    setOverlay(dx);
  },{passive:false});

  memoBody.addEventListener('touchend', e=>{
    resetOverlay();
    const dx=e.changedTouches[0].clientX-tx;
    const dy=e.changedTouches[0].clientY-ty;
    const dt=Date.now()-t0;
    const vel=Math.abs(dx)/dt;
    const c=card();
    if(c){c.classList.remove('is-dragging');c.style.transform='';}

    if(!S.memoRevealed){
      if(Math.abs(dx)<25&&Math.abs(dy)<25){
        if(!e.target.closest('.memo-rate-row')&&!e.target.closest('.memo-reveal-btn'))
          memoReveal();
      }
      dragging=false; return;
    }
    if(!dragging){dragging=false;return;}
    dragging=false;
    const ok=Math.abs(dx)>=THRESHOLD||vel>=VELOCITY;
    if(ok){
      if(dx<0){vibrate(10);memoKnow();}   // 左划=记住了
      else{vibrate([6,30,6]);memoAgain();} // 右划=忘了
    } else if(Math.abs(dx)>8){
      if(c){c.style.transition='transform .2s cubic-bezier(.25,.46,.45,.94)';setTimeout(()=>{c.style.transition='';},220);}
    }
  },{passive:true});
})();

// 背题点击卡片揭示（保留原有逻辑）
document.addEventListener('click', e=>{
  if(document.querySelector('.screen.active')?.id!=='s-memo') return;
  if(S.memoRevealed) return;
  if(e.target.closest('.memo-rate-row')||e.target.closest('.memo-reveal-btn')) return;
  if(e.target.closest('.memo-card')) memoReveal();
});

// ════════════════════════════════════════════
// 计算器（表达式输入模式）
// ════════════════════════════════════════════
var _calc = { input: '', done: false, is2nd: false, isDeg: true, history: [], mem: null };

function toggleCalc() {
  var m = document.getElementById('calc-modal');
  var opening = m.style.display !== 'flex';
  m.style.display = opening ? 'flex' : 'none';
  if (opening) {
    // 简易/科学模式：输入框设为 readonly，阻止移动端弹出键盘
    // 高级模式需要手动输入，不设 readonly
    var inp = document.getElementById('calc-result');
    if (inp) {
      var adv = document.getElementById('calc-keys-adv');
      var isAdv = adv && adv.style.display !== 'none';
      if (isAdv) {
        inp.removeAttribute('readonly');
        setTimeout(function() { inp.focus(); inp.selectionStart = inp.selectionEnd = inp.value.length; }, 50);
      } else {
        inp.setAttribute('readonly', 'readonly');
        inp.blur();
      }
    }
  }
}

function setCalcMode(mode) {
  document.getElementById('calc-keys-simple').style.display = mode === 'simple' ? '' : 'none';
  document.getElementById('calc-keys-sci').style.display    = mode === 'sci'    ? '' : 'none';
  document.getElementById('calc-keys-adv').style.display    = mode === 'adv'    ? '' : 'none';
  var sharedDisp = document.querySelector('.calc-display');
  if (sharedDisp) sharedDisp.style.display = mode === 'adv' ? 'none' : '';
  document.getElementById('calc-mode-simple').classList.toggle('active', mode === 'simple');
  document.getElementById('calc-mode-sci').classList.toggle('active', mode === 'sci');
  document.getElementById('calc-mode-adv').classList.toggle('active', mode === 'adv');
  if (mode === 'adv') _loadAdvAssets();
  var inp = document.getElementById('calc-result');
  if (inp) {
    if (mode !== 'adv') {
      // 简易/科学：readonly，阻止移动端键盘弹出
      inp.setAttribute('readonly', 'readonly');
      inp.blur();
    } else {
      inp.removeAttribute('readonly');
      setTimeout(function() { inp.focus(); inp.selectionStart = inp.selectionEnd = inp.value.length; }, 30);
    }
  }
}

// ── 光标感知的核心工具函数 ──────────────────────────────────────────

/** 在光标处插入文本，光标移到插入内容末尾 */
function _calcInsert(text) {
  var inp = document.getElementById('calc-result');
  if (!inp) { _calc.input += text; return; }

  // 当前显示为 "0"（初始状态）且输入数字/字母 → 直接替换
  if (inp.value === '0' && /^[\d.(]/.test(text)) {
    inp.value = ''; _calc.input = '';
    inp.selectionStart = inp.selectionEnd = 0;
  }

  // 当前显示为 "Error" → 任何按键都先清空
  if (inp.value === 'Error') {
    inp.value = ''; _calc.input = ''; _calc.done = false;
    inp.selectionStart = inp.selectionEnd = 0;
  }

  if (_calc.done) {
    // 计算完成后按数字/函数键：清空重新开始
    if (/^[\d.(πe]/.test(text)) {
      inp.value = ''; _calc.input = ''; _calc.done = false;
    } else {
      // 按运算符：延续结果继续运算
      _calc.done = false;
    }
  }
  var s = inp.selectionStart, e = inp.selectionEnd;
  var v = inp.value;
  inp.value = v.slice(0, s) + text + v.slice(e);
  inp.selectionStart = inp.selectionEnd = s + text.length;
  _calc.input = inp.value;
  inp.focus();
}

/** 在光标处向前删除一个"词元"（匹配函数名括号作为整体删除） */
function _calcDelAt() {
  var inp = document.getElementById('calc-result');
  if (!inp || _calc.done) return;
  var s = inp.selectionStart, e = inp.selectionEnd, v = inp.value;
  if (s !== e) {
    // 有选区：删除选区
    inp.value = v.slice(0, s) + v.slice(e);
    inp.selectionStart = inp.selectionEnd = s;
  } else if (s > 0) {
    var before = v.slice(0, s);
    var fm = before.match(/(asin|acos|atan|sin|cos|tan|sqrt|lg|ln|10\^|e\^)\($/);
    var del = fm ? fm[0].length : 1;
    inp.value = v.slice(0, s - del) + v.slice(s);
    inp.selectionStart = inp.selectionEnd = s - del;
  }
  _calc.input = inp.value;
  inp.focus();
}

/** 直接在 input 里打字时同步到 _calc.input */
function _calcSyncFromInput() {
  var inp = document.getElementById('calc-result');
  if (inp) _calc.input = inp.value;
  // 如果用户手动清空，重置 done 状态
  if (!_calc.input) _calc.done = false;
}

/** input 键盘事件：Enter = 计算，Escape = AC */
function _calcInputKeydown(e) {
  if (e.key === 'Enter') { e.preventDefault(); calcEval(); }
  if (e.key === 'Escape') { e.preventDefault(); calcAC(); }
}

function _calcRefresh() {
  // 仅在外部（非用户输入路径）强制同步 DOM
  var inp = document.getElementById('calc-result');
  if (inp) inp.value = _calc.input || '0';
  document.getElementById('calc-expr').textContent = '';
}

// ── 按键函数（全部改为 _calcInsert 插入到光标处）─────────────────────

function calcDigit(d) {
  if (d === '.') {
    // 防止同一数字段重复小数点
    var inp = document.getElementById('calc-result');
    var pos = inp ? inp.selectionStart : _calc.input.length;
    var before = (inp ? inp.value : _calc.input).slice(0, pos);
    var seg = (before.match(/[\d.]*$/) || [''])[0];
    if (seg.includes('.')) return;
  }
  _calcInsert(d);
}

function calcOp(op) {
  if (op === '(' || op === ')') { _calcInsert(op); return; }
  if (_calc.done) _calc.done = false;
  // 替换末尾已有的运算符（避免连续输入运算符产生乱码）
  var inp = document.getElementById('calc-result');
  if (inp) {
    inp.value = inp.value.replace(/\s*[+\-×÷]\s*$/, '');
    _calc.input = inp.value;
    inp.selectionStart = inp.selectionEnd = inp.value.length;
  }
  _calcInsert(' ' + op + ' ');
}

function calcAC() {
  _calc.input = ''; _calc.done = false;
  var inp = document.getElementById('calc-result');
  if (inp) { inp.value = '0'; inp.focus(); inp.select(); }
  document.getElementById('calc-expr').textContent = '';
  _calc.history = [];
  var hEl = document.getElementById('calc-history');
  if (hEl) hEl.innerHTML = '';
}

function calcDel() { _calcDelAt(); }

function calcFunc(fn) {
  if (_calc.done) { _calc.done = false; }
  var map2 = { sin:'asin(', cos:'acos(', tan:'atan(', lg:'10^(', ln:'e^(', '√':'(' };
  var map1 = { sin:'sin(', cos:'cos(', tan:'tan(', lg:'lg(', ln:'ln(', '√':'sqrt(' };
  if (fn === '!') { _calcInsert('!'); }
  else if (fn === '1/x') {
    var inp = document.getElementById('calc-result');
    if (inp) { inp.value = '1 ÷ (' + inp.value + ')'; inp.selectionStart = inp.selectionEnd = inp.value.length; _calc.input = inp.value; inp.focus(); }
  }
  else { _calcInsert(_calc.is2nd ? (map2[fn] || fn + '(') : (map1[fn] || fn + '(')); }
}

function calcPow() { if (_calc.done) _calc.done = false; _calcInsert('^'); }

function calcConst(c) { _calcInsert(c); }

async function calcEval() {
  var inp = document.getElementById('calc-result');
  var expr = inp ? inp.value.trim() : _calc.input.trim();
  if (!expr || expr === '0') return;
  document.getElementById('calc-expr').textContent = expr + ' =';
  if (inp) inp.value = '…';
  try {
    await _ensureMathJS();
    var val = _calcLocalEval(expr, _calc.isDeg);
    if (inp) { inp.value = val; inp.select(); }
    _calc.input = val;
    _calc.history.push({ expr: expr, val: val });
    _calcRenderHistory();
  } catch (e) {
    if (inp) inp.value = 'Error';
    _calc.input = '';
  }
  _calc.done = true;
  if (inp) inp.focus();
}

// 本地计算（math.js）— 简易/科学模式共用
function _calcLocalEval(raw, deg) {
  var s = raw;
  s = s.replace(/×/g, '*').replace(/÷/g, '/').replace(/−/g, '-');
  s = s.replace(/π/g, 'pi');
  // lg(x) → log10(x)
  s = s.replace(/\blg\(/g, 'log10(');
  // 10^(...) → 10^(...)  — math.js handles ^ natively
  s = s.replace(/10\^/g, '10^');
  // e^(...) → e^(...)
  s = s.replace(/\be\^/g, 'e^');
  // % → /100
  s = s.replace(/%/g, '/100');

  var scope = {};
  if (deg) {
    var toRad = Math.PI / 180, fromRad = 180 / Math.PI;
    scope = {
      sin: function(x) { return Math.sin(x * toRad); },
      cos: function(x) { return Math.cos(x * toRad); },
      tan: function(x) { return Math.tan(x * toRad); },
      asin: function(x) { return Math.asin(x) * fromRad; },
      acos: function(x) { return Math.acos(x) * fromRad; },
      atan: function(x) { return Math.atan(x) * fromRad; },
    };
  }

  var result = math.evaluate(s, scope);
  if (typeof result !== 'number') return String(result);
  if (!isFinite(result)) return result > 0 ? '∞' : result < 0 ? '-∞' : 'Error';
  if (Number.isNaN(result)) return 'Error';
  return parseFloat(result.toPrecision(12)).toString();
}

function _calcRenderHistory() {
  var el = document.getElementById('calc-history');
  if (!el) return;
  // 渲染时剔除最后一条（最后一条是"当前计算"，已在 calc-expr/calc-result 显示）
  var items = _calc.history.slice(0, -1);
  el.innerHTML = items.map(function(h) {
    return '<div class="calc-history-item"><span class="ch-expr">' +
      _escHtml(h.expr) + ' =</span><span class="ch-val">' + _escHtml(h.val) + '</span></div>';
  }).join('');
  // 保持滚动到底部（紧贴当前计算）
  el.scrollTop = el.scrollHeight;
  var display = el.closest('.calc-display');
  if (display) display.scrollTop = display.scrollHeight;
}

function _escHtml(s) {
  return s.replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;');
}

function calc2nd() {
  _calc.is2nd = !_calc.is2nd;
  document.getElementById('calc-2nd').classList.toggle('is-2nd', _calc.is2nd);
  var lbl = _calc.is2nd
    ? { 'calc-sin':'asin', 'calc-cos':'acos', 'calc-tan':'atan', 'calc-lg':'10^x', 'calc-ln':'e^x' }
    : { 'calc-sin':'sin',  'calc-cos':'cos',  'calc-tan':'tan',  'calc-lg':'lg',   'calc-ln':'ln' };
  for (var id in lbl) { var el = document.getElementById(id); if (el) el.textContent = lbl[id]; }
}

function calcToggleDeg() {
  _calc.isDeg = !_calc.isDeg;
  document.getElementById('calc-deg').textContent = _calc.isDeg ? 'deg' : 'rad';
}

function calcMemory() {
  if (_calcMemLongFired) { _calcMemLongFired = false; return; }
  var inp = document.getElementById('calc-result');
  var val = inp ? inp.value : _calc.input;
  if (_calc.mem !== null) {
    // Recall: insert memory value at cursor
    _calcInsert(_calc.mem);
  } else {
    // Store current result
    if (val && val !== '0' && val !== 'Error' && val !== '…') {
      _calc.mem = val;
    }
  }
  _calcUpdateMemBtns();
}
function _calcUpdateMemBtns() {
  var hasMem = _calc.mem !== null;
  ['calc-mem-s', 'calc-mem-sc'].forEach(function(id) {
    var el = document.getElementById(id);
    if (!el) return;
    el.textContent = hasMem ? 'MR' : 'M';
    el.classList.toggle('has-mem', hasMem);
  });
}
// Long press to clear memory
var _calcMemLongFired = false;
(function() {
  ['calc-mem-s', 'calc-mem-sc'].forEach(function(id) {
    var el = document.getElementById(id);
    if (!el) return;
    var timer = null;
    function startPress() {
      _calcMemLongFired = false;
      timer = setTimeout(function() {
        _calcMemLongFired = true;
        _calc.mem = null; _calcUpdateMemBtns();
        toast('记忆已清除'); timer = null;
      }, 600);
    }
    function endPress() { if (timer) { clearTimeout(timer); timer = null; } }
    el.addEventListener('touchstart', startPress, { passive: true });
    el.addEventListener('touchend', endPress);
    el.addEventListener('mousedown', startPress);
    el.addEventListener('mouseup', endPress);
  });
})();

// ════════════════════════════════════════════
// 高级计算器 (math.js + KaTeX LaTeX 渲染)
// ════════════════════════════════════════════

var _mathJSLoaded = false;
var _advDeg = true;

function _loadScript(url) {
  return new Promise(function(resolve, reject) {
    var s = document.createElement('script');
    s.src = url; s.onload = resolve; s.onerror = reject;
    document.head.appendChild(s);
  });
}

// 仅加载 math.js（简易/科学模式按 = 时调用）
function _ensureMathJS() {
  if (_mathJSLoaded || typeof math !== 'undefined') { _mathJSLoaded = true; return Promise.resolve(); }
  return _loadScript('/static/math.min.js')
    .then(function() { _mathJSLoaded = true; });
}

// 加载 math.js + KaTeX（高级模式用）
// ════════════════════════════════════════════════════════════════
// 高级计算器 — 5分页表达式引擎 (math.js + KaTeX)
// ════════════════════════════════════════════════════════════════

function _loadAdvAssets() {
  var tasks = [];
  if (typeof math === 'undefined') tasks.push(_ensureMathJS());
  if (typeof katex === 'undefined') {
    tasks.push(_loadScript('/static/katex.min.js'));
    if (!document.querySelector('link[href*="katex"]')) {
      var link = document.createElement('link');
      link.rel = 'stylesheet'; link.href = '/static/katex.min.css';
      document.head.appendChild(link);
    }
  }
  return tasks.length ? Promise.all(tasks) : Promise.resolve();
}

// ── 状态 ────────────────────────────────────────────────────────
var _cadv = {
  deg:     true,     // DEG=true / RAD=false
  lastAns: 0,        // Ans
  scope:   {},       // 持久变量 {x:5, ...}
  tab:     'basic',  // 当前分页
};
var _cadvPreviewTimer = null;

// ── 分页切换 ────────────────────────────────────────────────────
function cadvTab(name) {
  var tabs = ['basic','trig','algebra','stat','matrix'];
  tabs.forEach(function(t) {
    var btn = document.getElementById('cadv-tab-' + t);
    var pane = document.getElementById('cadv-keys-' + t);
    if (btn) btn.classList.toggle('active', t === name);
    if (pane) pane.style.display = t === name ? '' : 'none';
  });
  _cadv.tab = name;
}

// ── 输入框光标感知插入 ─────────────────────────────────────────
function _cadvGetInput() { return document.getElementById('cadv-input'); }

function cadvIns(text) {
  var inp = _cadvGetInput();
  if (!inp) return;
  // 若输入框未获焦点（移动端点按钮时），追加到末尾
  // 若已获焦点（用户主动点击后），在光标处插入
  var isFocused = (document.activeElement === inp);
  var s, e;
  if (isFocused) {
    s = inp.selectionStart;
    e = inp.selectionEnd;
  } else {
    s = e = inp.value.length;  // 追加到末尾
  }
  var v = inp.value;
  inp.value = v.slice(0, s) + text + v.slice(e);
  var pos = s + text.length;
  inp.selectionStart = inp.selectionEnd = pos;
  _cadv._expr = inp.value;
  _cadvSchedulePreview();
}

function cadvDigit(d) { cadvIns(d); }
function cadvOp(op)   { cadvIns(op); }
function cadvAns()    { cadvIns(String(_cadv.lastAns)); }

function cadvDel() {
  var inp = _cadvGetInput(); if (!inp) return;
  var s = inp.selectionStart, e = inp.selectionEnd;
  if (s !== e) { cadvIns(''); return; }
  if (s === 0) return;
  // 智能删除：整体删除多字符函数名如 'sqrt('、'mean(' 等
  var before = inp.value.slice(0, s);
  var fm = before.match(/(asinh|acosh|atanh|sinh|cosh|tanh|asin|acos|atan|sin|cos|tan|sqrt|cbrt|log10|log2|log|abs|simplify|derivative|rationalize|polynomialRoot|gcd|lcm|xgcd|invmod|floor|ceil|round|mean|median|variance|std|mad|sum|prod|min|max|gamma|lgamma|erf|cumsum|isPrime|permutations|combinations|quantileSeq|det|inv|transpose|trace|norm|eigs|pinv|cross|dot|kron|identity|zeros|ones|diag|hypot|sign|nthRoot|bitAnd|bitOr|bitXor|bitNot|leftShift|re|im|conj|arg|diff)\($/);
  if (fm) {
    inp.value = before.slice(0, before.length - fm[0].length) + inp.value.slice(s);
    inp.selectionStart = inp.selectionEnd = s - fm[0].length;
  } else {
    inp.value = before.slice(0, -1) + inp.value.slice(s);
    inp.selectionStart = inp.selectionEnd = s - 1;
  }
  _cadv._expr = inp.value;
  _cadvSchedulePreview();
}

function cadvAC() {
  var inp = _cadvGetInput(); if (!inp) return;
  inp.value = '';
  _cadv._expr = '';
  var exprEl = document.getElementById('cadv-expr');
  var resEl  = document.getElementById('cadv-result');
  if (exprEl) exprEl.innerHTML = '';
  if (resEl)  resEl.textContent = '0';
}

function cadvToggleDeg() {
  _cadv.deg = !_cadv.deg;
  var btn = document.getElementById('cadv-deg-btn');
  var ind = document.getElementById('cadv-deg-ind');
  var lbl = _cadv.deg ? 'DEG' : 'RAD';
  if (btn) { btn.textContent = lbl; btn.classList.toggle('is-rad', !_cadv.deg); }
  if (ind) ind.textContent = lbl;
}

function cadvShowHelp() {
  var m = document.getElementById('cadv-help-modal');
  if (m) m.style.display = 'flex';
}
function cadvHideHelp() {
  var m = document.getElementById('cadv-help-modal');
  if (m) m.style.display = 'none';
}

// ── 键盘事件 ────────────────────────────────────────────────────
function _cadvInputChange() {
  var inp = _cadvGetInput(); if (!inp) return;
  _cadv._expr = inp.value;
  _cadvSchedulePreview();
}

function _cadvInputKeyDown(e) {
  if (e.key === 'Enter') { e.preventDefault(); cadvEval(); }
}

// ── 实时 LaTeX 预览（防抖 80ms）───────────────────────────────
function _cadvSchedulePreview() {
  clearTimeout(_cadvPreviewTimer);
  _cadvPreviewTimer = setTimeout(_cadvRenderPreview, 80);
}

function _cadvRenderPreview() {
  var exprEl = document.getElementById('cadv-expr');
  if (!exprEl) return;
  var inp = _cadvGetInput();
  var raw = (inp ? inp.value : (_cadv._expr || '')).trim();
  if (!raw) { exprEl.innerHTML = ''; return; }

  if (typeof math === 'undefined' || typeof katex === 'undefined') {
    exprEl.textContent = raw;
    _loadAdvAssets().then(_cadvRenderPreview);
    return;
  }

  // 跳过赋值语句的 LaTeX 预览（直接显示原文）
  if (/^[a-zA-Z_]\w*\s*=/.test(raw)) {
    exprEl.textContent = raw;
    exprEl.style.opacity = '0.7';
    return;
  }

  var tryList = [raw, raw+')', raw+'))', raw+')))', raw+',x)', raw+',x))'];
  for (var i = 0; i < tryList.length; i++) {
    try {
      var node = math.parse(_cadvPreProcess(tryList[i]));
      var tex = node.toTex({ parenthesis: 'auto' });
      katex.render(tex, exprEl, { throwOnError: false, displayMode: false });
      exprEl.style.opacity = i === 0 ? '1' : String(0.65 - i * 0.05);
      return;
    } catch(_) {}
  }
  exprEl.textContent = raw;
  exprEl.style.opacity = '0.45';
}

// ── 预处理：将用户习惯写法转为 math.js 可识别形式 ────────────
function _cadvPreProcess(expr) {
  var s = expr;
  // DEG 模式：用 scope 注入三角函数覆盖
  // mod 关键字
  s = s.replace(/\bmod\b/g, ' mod ');
  // lgamma → 用 log(abs(gamma(x))) 近似（math.js 无内置）
  // phi → golden ratio
  s = s.replace(/\bphi\b/g, '(1+sqrt(5))/2');
  return s;
}

// ── 构建 DEG 模式 scope ────────────────────────────────────────
function _cadvScope() {
  var base = Object.assign({}, _cadv.scope);
  base.Ans = _cadv.lastAns;
  if (_cadv.deg) {
    var toRad = Math.PI / 180, fromRad = 180 / Math.PI;
    var wrap = function(fn) { return function(x) { return Math[fn](x * toRad); }; };
    var iwrap = function(fn) { return function(x) { return Math[fn](x) * fromRad; }; };
    base.sin = wrap('sin'); base.cos = wrap('cos'); base.tan = wrap('tan');
    base.cot = function(x) { return 1 / Math.tan(x * toRad); };
    base.sec = function(x) { return 1 / Math.cos(x * toRad); };
    base.csc = function(x) { return 1 / Math.sin(x * toRad); };
    base.asin = iwrap('asin'); base.acos = iwrap('acos'); base.atan = iwrap('atan');
    base.atan2 = function(y,x) { return Math.atan2(y,x) * fromRad; };
    base.acot = function(x) { return fromRad * (Math.PI/2 - Math.atan(x)); };
    base.asec = function(x) { return iwrap('acos')(1/x); };
    base.acsc = function(x) { return iwrap('asin')(1/x); };
  }
  // lgamma via log(gamma)
  base.lgamma = function(x) { return Math.log(Math.abs(math.gamma(x))); };
  // acoth
  base.acoth = function(x) { return 0.5 * Math.log((x+1)/(x-1)); };
  return base;
}

// ── 求值 ────────────────────────────────────────────────────────
function cadvEval() {
  var inp = _cadvGetInput(); if (!inp) return;
  var raw = inp.value.trim(); if (!raw) return;
  var exprEl  = document.getElementById('cadv-expr');
  var resEl   = document.getElementById('cadv-result');
  var ansDisp = document.getElementById('cadv-ans-disp');

  _loadAdvAssets().then(function() {
    var processed = _cadvPreProcess(raw);
    var resultTex = '', exprTex = '';

    // 赋值语句：x = expr
    var assignM = raw.match(/^([a-zA-Z_]\w*)\s*=\s*(.+)$/);
    if (assignM) {
      var varName = assignM[1];
      var valExpr = _cadvPreProcess(assignM[2]);
      try {
        var val = math.evaluate(valExpr, _cadvScope());
        _cadv.scope[varName] = val;
        _updateVarHint();
        resultTex = varName + ' = ' + _formatResult(val);
        exprTex = varName + ' \\leftarrow ' + _toTex(valExpr);
        _renderKatex(exprEl, exprTex);
        _renderKatex(resEl, resultTex);
        inp.value = '';
      } catch(e) {
        _showError(resEl, e);
      }
      return;
    }

    // 求导
    if (/^d(?:erivative)?\s*\(/.test(processed) || processed.startsWith('derivative(')) {
      try {
        var m = processed.match(/derivative\((.+),\s*"?([a-z])"?\)$/i);
        var inner = m ? m[1] : processed.slice(processed.indexOf('(')+1, -1);
        var vari  = m ? m[2] : 'x';
        var node  = math.derivative(inner, vari);
        resultTex = node.toTex({ parenthesis:'auto' });
        exprTex   = '\\frac{d}{d' + vari + '}(' + _toTex(inner) + ')';
        _renderKatex(exprEl, exprTex + ' =');
        _renderKatex(resEl, resultTex);
        inp.value = '';
      } catch(e) { _showError(resEl, e); }
      return;
    }

    // 化简
    if (processed.startsWith('simplify(')) {
      try {
        var inner = processed.slice(9, -1);
        var node = math.simplify(inner);
        resultTex = node.toTex({ parenthesis:'auto' });
        exprTex   = '\\text{simplify}(' + _toTex(inner) + ')';
        _renderKatex(exprEl, exprTex + ' =');
        _renderKatex(resEl, resultTex);
        inp.value = '';
      } catch(e) { _showError(resEl, e); }
      return;
    }

    // 通用求值
    try {
      var scope = _cadvScope();
      var result = math.evaluate(processed, scope);
      resultTex = _formatResult(result);
      exprTex   = _toTex(processed);
      _cadv.lastAns = (typeof result === 'number') ? result : _cadv.lastAns;
      if (ansDisp) ansDisp.textContent = _shortNum(_cadv.lastAns);
      _renderKatex(exprEl, exprTex + ' =');
      _renderKatex(resEl, resultTex);
      // 推入历史
      _cadvPushHistory(raw, _latexToText(resultTex));
      inp.value = '';
    } catch(e) { _showError(resEl, e); }
  }).catch(function(e) { if (resEl) resEl.textContent = 'Load error'; });
}

// ── 辅助 ────────────────────────────────────────────────────────
function _toTex(expr) {
  try { return math.parse(expr).toTex({ parenthesis:'auto' }); }
  catch(_) { return expr; }
}
function _formatResult(val) {
  if (typeof val === 'number') {
    if (!isFinite(val)) return val > 0 ? '\\infty' : '-\\infty';
    if (isNaN(val)) return '\\text{NaN}';
    if (Number.isInteger(val) && Math.abs(val) < 1e15) return String(val);
    return String(parseFloat(val.toPrecision(12)));
  }
  if (val && typeof val.toTex === 'function') return val.toTex({ parenthesis:'auto' });
  if (Array.isArray(val) || (val && val.isMatrix)) {
    try { return math.parse(math.format(val, {precision:6})).toTex({parenthesis:'auto'}); }
    catch(_) { return '\\text{' + math.format(val,{precision:6}).replace(/[{}\\_^]/g,'') + '}'; }
  }
  return '\\text{' + String(val).replace(/[{}\\_^]/g, '').slice(0,60) + '}';
}
function _shortNum(v) {
  if (typeof v !== 'number') return String(v);
  if (!isFinite(v)) return v > 0 ? '∞' : '-∞';
  if (Number.isInteger(v) && Math.abs(v) < 1e9) return String(v);
  return parseFloat(v.toPrecision(6)).toString();
}
function _renderKatex(el, tex) {
  if (!el) return;
  try { katex.render(tex, el, { throwOnError:false, displayMode:false }); }
  catch(_) { el.textContent = tex; }
}
function _showError(el, e) {
  if (!el) return;
  var msg = (e && e.message) ? e.message.replace(/[{}\\_]/g,'').slice(0,50) : '错误';
  el.innerHTML = '<span style="color:var(--danger);font-size:13px">⚠ ' + msg + '</span>';
}
function _latexToText(tex) { return tex.replace(/\\[a-zA-Z]+\{([^}]+)\}/g,'$1').replace(/\\/g,'').slice(0,30); }

function _updateVarHint() {
  var el = document.getElementById('cadv-var-hint');
  if (!el) return;
  var keys = Object.keys(_cadv.scope).filter(function(k){ return k !== 'Ans'; });
  el.textContent = keys.length ? keys.slice(0,4).map(function(k){ return k+'='+_shortNum(_cadv.scope[k]); }).join('  ') : '';
}

// 简易历史显示（复用 calc-history 区域但仅在高级模式下，
// 改为插入到 cadv-lcd 上方的一个历史行）
function _cadvPushHistory(expr, result) {
  // 用已有的 calc-history 元素
  var hist = document.getElementById('calc-history');
  if (!hist) return;
  var item = document.createElement('div');
  item.className = 'calc-history-item';
  item.innerHTML = '<div class="ch-expr">' + expr.slice(0,40) + '</div><div class="ch-val">' + result + '</div>';
  hist.appendChild(item);
  // 只保留最近5条
  while (hist.children.length > 5) hist.removeChild(hist.firstChild);
  hist.scrollTop = hist.scrollHeight;
}


function _showCalcBtn(show) {
  var btn = document.getElementById('calc-toggle-btn');
  if (btn) btn.style.display = show ? '' : 'none';
}

// 页面关闭/刷新时兜底保存
window.addEventListener('beforeunload', () => {
  saveExamSession();
});

// 每 20 秒自动保存一次（防止仅修改标记/位置未及时保存）
setInterval(() => {
  if (S.mode === 'exam' && S.questions.length) saveExamSession();
}, 20000);

// ════════════════════════════════════════════
// 离线同步角标 & 手动同步
// ════════════════════════════════════════════

/**
 * 根据 SyncManager 状态更新同步呼吸点。
 *   pending > 0 且 offline → 橙色呼吸点
 *   pending > 0 且 online  → 蓝色呼吸点
 *   pending = 0            → 隐藏
 */
function _updateSyncBadge(syncState) {
  const dot = document.getElementById('sync-dot');
  const tip = document.getElementById('sync-dot-tip');
  if (!dot) return;

  const { pending, online, syncing } = syncState;
  if (pending === 0) {
    dot.style.display = 'none';
    return;
  }

  dot.style.display = '';
  if (!online) {
    dot.dataset.status = 'offline';
    if (tip) tip.textContent = `离线 · ${pending} 条待同步`;
  } else if (syncing) {
    dot.dataset.status = 'syncing';
    if (tip) tip.textContent = '同步中…';
  } else {
    dot.dataset.status = 'pending';
    if (tip) tip.textContent = `${pending} 条待同步 · 点击同步`;
  }
}

/** 手动触发同步（绑定到呼吸点） */
async function syncNow() {
  if (typeof SyncManager === 'undefined') return;
  try {
    await SyncManager.flush();
    _refreshProgressBadges();
  } catch(e) { /* 静默 */ }
}

// ════════════════════════════════════════════
// Streak celebration (practice mode)
// ════════════════════════════════════════════

function _trackStreak(correct) {
  if (S.mode !== 'practice') return;
  if (correct) {
    S.streak++;
    // Trigger at 5, 10, 15, 20, ...
    if (S.streak >= 5 && S.streak % 5 === 0) {
      _showStreakCelebration(S.streak);
    }
  } else {
    S.streak = 0;
  }
}

function _showStreakCelebration(count) {
  // Remove existing overlay if any
  const old = document.getElementById('streak-overlay');
  if (old) old.remove();

  // 计算本次练习实时正确率
  const answered = Object.keys(S.ans).length;
  const correctSoFar = Object.entries(S.ans).filter(([i, sel]) => {
    const q = S.questions[parseInt(i)];
    if (!q || !sel) return false;
    if (sel instanceof Set && sel.size === 0) return false;
    if (isMultiQ(q)) {
      const cs = new Set(q.answer.split(''));
      const ss = sel instanceof Set ? sel : new Set([sel]);
      return ss.size === cs.size && [...cs].every(l => ss.has(l));
    }
    return sel === q.answer;
  }).length;
  const accPct = answered > 0 ? Math.round(correctSoFar / answered * 100) : 0;
  const accText = answered > 0 ? `本次正确率 ${accPct}%（${correctSoFar}/${answered}题）` : '';

  const emojis = ['🔥','⚡','🎯','💪','🏆','✨','🌟','💫'];
  const emoji = count >= 20 ? '🏆' : count >= 15 ? '💪' : count >= 10 ? '⚡' : '🔥';
  const texts = count >= 20 ? '太强了！' : count >= 15 ? '势不可挡！' : count >= 10 ? '超级棒！' : '继续保持！';

  const overlay = document.createElement('div');
  overlay.id = 'streak-overlay';
  overlay.innerHTML = `
    <div class="streak-particles"></div>
    <div class="streak-card">
      <div class="streak-emoji">${emoji}</div>
      <div class="streak-count">连对 ${count} 题</div>
      <div class="streak-text">${texts}</div>
      ${accText ? `<div class="streak-acc">${accText}</div>` : ''}
    </div>`;
  document.body.appendChild(overlay);

  // Spawn particles
  const particleBox = overlay.querySelector('.streak-particles');
  for (let i = 0; i < 24; i++) {
    const p = document.createElement('span');
    p.className = 'streak-p';
    p.textContent = emojis[Math.floor(Math.random() * emojis.length)];
    p.style.setProperty('--x', (Math.random() * 200 - 100) + 'px');
    p.style.setProperty('--y', (Math.random() * -200 - 60) + 'px');
    p.style.setProperty('--r', (Math.random() * 360) + 'deg');
    p.style.setProperty('--d', (Math.random() * 0.5 + 0.3) + 's');
    p.style.left = (40 + Math.random() * 20) + '%';
    p.style.top = (40 + Math.random() * 20) + '%';
    particleBox.appendChild(p);
  }

  // Auto dismiss after 2s
  setTimeout(() => { overlay.classList.add('streak-fade-out'); }, 1800);
  setTimeout(() => { overlay.remove(); }, 2500);
}

// ── PWA 切回前台自动刷新主页数据 ─────────────────────────────────────
document.addEventListener('visibilitychange', function() {
  if (document.visibilityState !== 'visible') return;
  const home = document.getElementById('s-home');
  if (home && home.classList.contains('active')) {
    refreshHomeData();
  }
});

init();
// ════════════════════════════════════════════
// 服务端重启 / Session 失效处理
// ════════════════════════════════════════════
(function () {
  let _handled = false;

  window._onAuthExpired = function () {
    if (_handled) return;
    _handled = true;

    // 1. 立即保存当前做题状态到 localStorage
    let saveMsg = '';
    const screen = document.querySelector('.screen.active')?.id;

    if (screen === 's-quiz' || screen === 's-results') {
      if (S.mode === 'practice' && S.questions.length) {
        // 练习模式：强制保存进度存档，刷新后可从「进行中」继续
        savePracticeSession();
        saveMsg = '练习进度已自动保存，';
      } else if (S.mode === 'exam' && S.questions.length && !S.examSubmitted) {
        // 考试模式：把当前状态写入 examSession key，刷新后 checkResumeSession 会弹恢复提示
        try {
          const key = examSessionKey();
          const existing = JSON.parse(localStorage.getItem(key) || 'null');
          if (!existing) {
            // 仅在尚未有存档时写入，避免覆盖更完整的存档
            localStorage.setItem(key, JSON.stringify({
              v: 1, savedAt: Date.now(),
              cur: S.cur, questions: S.questions,
              ans: _serializeAns(S.ans),
              revealed: [...S.revealed], marked: [...S.marked],
              examStart: S.examStart, examLimit: S.examLimit,
            }));
          }
          saveMsg = '考试进度已自动保存，';
        } catch (e) { /* 存储失败时静默 */ }
      } else if (S.mode === 'memo' && S.questions.length) {
        saveMsg = '';
      }
    }

    // 2. 显示持久横幅
    _showAuthExpiredBanner(saveMsg);
  };
})();

// Review 页面：左右滑动切换 Tab（跟手拖拽 + 平滑过渡）
(function setupReviewTabSwipe(){
  const THRESHOLD = 40, VELOCITY = 0.25;
  let tx=0, ty=0, t0=0, locked=null, dragging=false, animating=false, touchTarget=null;

  const reviewScreen = document.getElementById('s-review');
  if (!reviewScreen) return;
  const reviewBody = reviewScreen.querySelector('.review-list');
  if (!reviewBody) return;

  function getCurrentTab(){
    const active = reviewScreen.querySelector('.rtab.active');
    return active ? parseInt(active.dataset.tab || '0', 10) : 0;
  }

  function switchTab(direction){
    if (animating) return;
    const current = getCurrentTab();
    const next = direction === 'left'
      ? Math.min(current + 1, 3)
      : Math.max(current - 1, 0);
    if (next === current) return;
    animating = true;

    // 滑出方向
    const offset = direction === 'left' ? '-100%' : '100%';
    const enterFrom = direction === 'left' ? '60%' : '-60%';

    reviewBody.style.transition = 'transform .15s ease-out, opacity .15s ease-out';
    reviewBody.style.transform  = `translateX(${offset})`;
    reviewBody.style.opacity    = '0';

    setTimeout(() => {
      // 切换内容（无过渡）
      reviewBody.style.transition = 'none';
      reviewBody.style.transform  = `translateX(${enterFrom})`;
      const btn = reviewScreen.querySelector(`.rtab[data-tab="${next}"]`);
      if (btn) btn.click();

      // 强制 reflow
      void reviewBody.offsetHeight;

      // 滑入
      reviewBody.style.transition = 'transform .15s ease-out, opacity .15s ease-out';
      reviewBody.style.transform  = '';
      reviewBody.style.opacity    = '';

      setTimeout(() => { animating = false; }, 160);
    }, 150);
    vibrate(10);
  }

  reviewBody.addEventListener('touchstart', e => {
    if (animating) return;
    if (document.querySelector('.screen.active')?.id !== 's-review') return;
    const t = e.touches[0]; tx = t.clientX; ty = t.clientY; t0 = Date.now();
    locked = null; dragging = false; touchTarget = e.target;
    reviewBody.style.transition = 'none';
  }, { passive: true });

  reviewBody.addEventListener('touchmove', e => {
    if (animating) return;
    if (document.querySelector('.screen.active')?.id !== 's-review') return;
    const t = e.touches[0];
    const dx = t.clientX - tx, dy = t.clientY - ty;
    if (!locked){ const d = getSwipeDir(dx, dy); if (!d) return;
      if(d!=='vertical'&&_isTouchInHScrollable(touchTarget)){locked='vertical';return;}
      locked = d;
    }
    if (locked === 'vertical') return;
    e.preventDefault();
    dragging = true;
    const clamp = Math.max(-80, Math.min(80, dx));
    reviewBody.style.transform = `translateX(${clamp}px)`;
    reviewBody.style.opacity   = String(1 - Math.abs(clamp) / 200);
  }, { passive: false });

  reviewBody.addEventListener('touchend', e => {
    if (!dragging){ dragging = false; return; }
    dragging = false;
    const dx = e.changedTouches[0].clientX - tx;
    const dt = Date.now() - t0;
    const vel = Math.abs(dx) / dt;

    if ((Math.abs(dx) >= THRESHOLD || vel >= VELOCITY) && !animating){
      // 触发切换（switchTab 内部会处理动画）
      reviewBody.style.transition = 'none';
      reviewBody.style.transform  = '';
      reviewBody.style.opacity    = '';
      if (dx < 0) switchTab('left');
      else        switchTab('right');
    } else {
      // 弹回
      reviewBody.style.transition = 'transform .15s, opacity .15s';
      reviewBody.style.transform  = '';
      reviewBody.style.opacity    = '';
    }
  }, { passive: true });
})();
