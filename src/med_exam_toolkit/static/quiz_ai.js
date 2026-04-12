/* ================================================================
   quiz_ai.js  — AI 答疑面板模块
   依赖：common.js (apiFetch), quiz.js (S, esc, isMultiQ), marked.min.js, katex
   ================================================================ */

// ── 按需懒加载 AI 静态资源 ────────────────────────────────────────
// katex(264KB) + smd(12KB) + auto-render(3.5KB) 只在首次打开 AI 面板时加载
const AI_STATIC_BASE = '/static';
const _aiAssetsLoaded = { css: false, js: false };
let _aiAssetsPromise = null;

function loadAIAssets() {
  if (_aiAssetsLoaded.js) return Promise.resolve();
  if (_aiAssetsPromise) return _aiAssetsPromise;
  _aiAssetsPromise = new Promise((resolve, reject) => {
    if (!_aiAssetsLoaded.css) {
      const link = document.createElement('link');
      link.rel = 'stylesheet';
      link.href = AI_STATIC_BASE + '/katex.min.css';
      document.head.appendChild(link);
      _aiAssetsLoaded.css = true;
    }
    const scripts = [
      AI_STATIC_BASE + '/marked.min.js',
      AI_STATIC_BASE + '/smd.min.js',
      AI_STATIC_BASE + '/katex.min.js',
      AI_STATIC_BASE + '/auto-render.min.js',
    ];
    function loadNext(i) {
      if (i >= scripts.length) { _aiAssetsLoaded.js = true; _configureMarked(); resolve(); return; }
      const sc = document.createElement('script');
      sc.src = scripts[i];
      sc.onload = () => loadNext(i + 1);
      sc.onerror = () => reject(new Error('Failed to load ' + scripts[i]));
      document.body.appendChild(sc);
    }
    loadNext(0);
  });
  return _aiAssetsPromise;
}

const AI_MAX_ROUNDS = 3;
const _AI_SEND_ICON = '<svg width="16" height="16" viewBox="0 0 16 16" fill="none"><path d="M8 14V2M8 2L3 7M8 2l5 5" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/></svg>';
const _AI_MIC_ICON = '<svg width="16" height="16" viewBox="0 0 16 16" fill="none"><path d="M8 1a2.5 2.5 0 0 0-2.5 2.5v4a2.5 2.5 0 0 0 5 0v-4A2.5 2.5 0 0 0 8 1z" stroke="currentColor" stroke-width="1.5"/><path d="M4 7v.5a4 4 0 0 0 8 0V7M8 12.5V15M5.5 15h5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>';
const _AI_MIC_STOP = '<svg width="16" height="16" viewBox="0 0 16 16" fill="none"><rect x="3.5" y="3.5" width="9" height="9" rx="1.5" stroke="currentColor" stroke-width="1.5"/></svg>';

// key: "fingerprint-idx" → { round, history, streaming, els }
const aiPanels = new Map();

// ── Marked extensions for LaTeX math ──────────────────────────────

// Block math: $$...$$ on its own line
const blockMathExt = {
  name: 'blockMath',
  level: 'block',
  start(src) { return src.indexOf('$$'); },
  tokenizer(src) {
    const match = src.match(/^\$\$([\s\S]+?)\$\$/);
    if (match) {
      return { type: 'blockMath', raw: match[0], formula: match[1].trim() };
    }
  },
  renderer(token) {
    if (typeof katex !== 'undefined') {
      try {
        return '<p class="ai-math-display">' + katex.renderToString(token.formula, { displayMode: true, throwOnError: false }) + '</p>';
      } catch (e) { /* fall through */ }
    }
    return '<p class="ai-math-display">' + esc(token.raw) + '</p>';
  }
};

// Inline math: $...$ (no newlines inside)
const inlineMathExt = {
  name: 'inlineMath',
  level: 'inline',
  start(src) { return src.indexOf('$'); },
  tokenizer(src) {
    // Display math $$...$$ in inline context
    const displayMatch = src.match(/^\$\$([\s\S]+?)\$\$/);
    if (displayMatch) {
      return { type: 'inlineMath', raw: displayMatch[0], formula: displayMatch[1].trim(), display: true };
    }
    // Inline math $...$
    const inlineMatch = src.match(/^\$([^\$\n]+?)\$/);
    if (inlineMatch) {
      return { type: 'inlineMath', raw: inlineMatch[0], formula: inlineMatch[1], display: false };
    }
  },
  renderer(token) {
    if (typeof katex !== 'undefined') {
      try {
        return katex.renderToString(token.formula, { displayMode: !!token.display, throwOnError: false });
      } catch (e) { /* fall through */ }
    }
    return esc(token.raw);
  }
};

// Configure marked for medical content + LaTeX math (called after lazy-load)
function _configureMarked() {
  if (typeof marked !== 'undefined' && !_configureMarked._done) {
    marked.use({
      breaks: true,
      gfm: true,
      extensions: [blockMathExt, inlineMathExt],
    });
    _configureMarked._done = true;
  }
}

// ── Table separator fix ──────────────────────────────────────────
// AI models sometimes output malformed separators like | : | (no dashes).
// marked requires ≥3 dashes per cell. Fix before parsing.
function fixTableSeparators(text) {
  return text.replace(
    /^(\|[\s:\-|]*\|)$/gm,
    function(row) {
      return row.replace(/(?<=\|)\s*:?-{0,2}\s*(?=\|)/g, ' :--- ');
    }
  );
}

// ── Join broken table rows ───────────────────────────────────────
// AI may output table headers that wrap across multiple lines.
// marked requires each row on a single line. Join them.
function joinBrokenTableRows(text) {
  var lines = text.split('\n');
  var result = [];
  var i = 0;
  while (i < lines.length) {
    var line = lines[i];
    // Check if this line looks like start of a table row (starts with |)
    // but does NOT end with | — it's broken
    if (/^\|/.test(line.trim()) && !/\|\s*$/.test(line.trim())) {
      // Look ahead: keep joining until we find a line ending with |
      // or hit a separator row or non-table line
      var joined = line;
      while (++i < lines.length) {
        var next = lines[i].trim();
        // separator row → stop, don't join
        if (/^\|[\s:\-|]*\|$/.test(next)) break;
        // empty line → stop
        if (!next) break;
        joined += next;
        if (/\|\s*$/.test(next)) { i++; break; }
      }
      result.push(joined);
    } else {
      result.push(line);
      i++;
    }
  }
  return result.join('\n');
}

// ── Fix CJK bold/italic rendering ─────────────────────────────────
// CommonMark's "flanking delimiter" rules reject ** when:
//   - opening ** is preceded by CJK char + followed by CJK punctuation
//     e.g. 领域**（外科）这些**  ← opening ** not left-flanking
//   - closing ** is preceded by CJK punctuation + followed by CJK char
//     e.g. **错误（A）**类型     ← closing ** not right-flanking
// Fix: convert **text** → <strong>text</strong> before marked parses,
// bypassing the flanking check entirely. Code blocks are protected.
function fixBrokenInlineFormatting(text) {
  var parts = text.split(/(```[\s\S]*?```)/g);
  for (var i = 0; i < parts.length; i++) {
    if (parts[i].indexOf('```') === 0) continue; // skip fenced code blocks
    parts[i] = parts[i].replace(/\*\*([^*\n]+?)\*\*/g, '<strong>$1</strong>');
  }
  return parts.join('');
}

// ── Shared marked render (table fix + LaTeX) ─────────────────────
function markedRender(text) {
  if (typeof marked !== 'undefined' && marked.parse) {
    return marked.parse(fixBrokenInlineFormatting(joinBrokenTableRows(fixTableSeparators(text))), { async: false });
  }
  return '<pre>' + esc(text) + '</pre>';
}


// ── Textarea 自适应高度（最多 4 行）────────────────────────────────
function autoResizeTextarea(el) {
  el.style.height = 'auto';
  // lineHeight 约 20px；4 行上限
  const lineH = parseInt(window.getComputedStyle(el).lineHeight) || 20;
  const maxH  = lineH * 4 + 16; // 16 = padding top+bottom
  el.style.height = Math.min(el.scrollHeight, maxH) + 'px';
  el.style.overflowY = el.scrollHeight > maxH ? 'auto' : 'hidden';
}

// ── Streaming renderer ─────────────────────────────────────────────
// Uses streaming-markdown (smd) for incremental DOM rendering.
// smd only appends to the DOM — never rewrites — so text always
// appears at its final position with zero layout jumps.
// Characters are drip-fed via requestAnimationFrame for smooth flow.

/**
 * Create a streaming renderer attached to a container + cursor.
 * - push(chunk): accumulate text, schedule drip animation
 * - flush():    finalize stream, do a final marked render for polish
 */
function makeStreamingRenderer(container, cursor, scrollTarget) {
  let pending = '';
  let fullText = '';
  let rafId = null;
  let smdParser = null;

  // Initialize smd parser
  if (typeof smd !== 'undefined' && smd.default_renderer && smd.parser) {
    const renderer = smd.default_renderer(container);
    smdParser = smd.parser(renderer);
  }

  // Observe new block elements and animate them in
  let observer = null;
  try {
    observer = new MutationObserver(function(mutations) {
      for (const m of mutations) {
        for (const node of m.addedNodes) {
          if (node.nodeType === 1 && /^(P|H[1-6]|UL|OL|LI|TABLE|BLOCKQUOTE|PRE)$/i.test(node.tagName)) {
            node.classList.add('ai-block-in');
          }
        }
      }
    });
    observer.observe(container, { childList: true, subtree: true });
  } catch(e) {}

  function placeCursor() {
    if (cursor) container.appendChild(cursor);
  }

  // 目标速率：约 40ms 写完一个 SSE chunk（raf ≈ 16ms/帧）
  // 每帧释放字符数 = pending.length / FRAMES_PER_CHUNK，下限 1，上限 32
  const FRAMES_PER_CHUNK = 3;  // 每个 chunk 用 3 帧写完
  const MAX_PER_FRAME = 32;    // 单帧最多写 32 字符（避免过大 chunk 卡顿）
  const MIN_PER_FRAME = 1;

  function drip() {
    if (pending.length === 0) { rafId = null; return; }

    // 动态决定本帧写多少字符
    const want = Math.ceil(pending.length / FRAMES_PER_CHUNK);
    const n = Math.max(MIN_PER_FRAME, Math.min(MAX_PER_FRAME, want));

    // 不在词中间断开（中文逐字，英文到词边界）
    let end = n;
    if (end < pending.length && !/[\s\u4e00-\u9fff\u3000-\u303f]/.test(pending[end])) {
      // 在英文词中间：往后找词边界（最多再多 8 字符）
      let look = end;
      while (look < pending.length && look < end + 8 && !/\s/.test(pending[look])) look++;
      end = look;
    }
    end = Math.min(end, pending.length);

    const slice = pending.slice(0, end);
    pending = pending.slice(end);
    fullText += slice;

    if (smdParser) {
      smd.parser_write(smdParser, slice);
    } else {
      container.appendChild(document.createTextNode(slice));
    }

    placeCursor();
    if (scrollTarget) scrollMessages(scrollTarget);
    rafId = requestAnimationFrame(drip);
  }

  function push(text) {
    pending += text;
    if (!rafId) rafId = requestAnimationFrame(drip);
  }

  function flush() {
    if (rafId) { cancelAnimationFrame(rafId); rafId = null; }
    if (pending.length > 0) {
      fullText += pending;
      if (smdParser) smd.parser_write(smdParser, pending);
      pending = '';
    }
    if (smdParser) {
      smd.parser_end(smdParser);
      smdParser = null;
    }
    if (observer) { observer.disconnect(); observer = null; }
    // Re-render with marked for LaTeX/GFM polish + table fix
    if (fullText) {
      try {
        container.innerHTML = markedRender(fullText);
      } catch (e) { /* keep smd output */ }
    }
  }

  return { push, flush };
}


/**
 * Create and insert the AI Q&A section into a container.
 * @param {HTMLElement} container - parent element to append into
 * @param {object} q - question object (must have .fingerprint)
 * @param {number} sqIdx - sub-question index (0 for simple questions)
 * @param {string} userAnswer - user's selected answer letter(s)
 */
function initAIPanel(container, q, sqIdx, userAnswer) {
  // Hide AI entry when not configured
  if (typeof S === 'undefined' || !S.bankInfo || !S.bankInfo.ai_enabled) return;
  loadAIAssets().catch(() => {}); // 预热：静默触发加载

  const key = q.fingerprint + '-' + sqIdx;

  // Extract cached data from previous visit (DOM refs are stale after navigation)
  let cachedRound = 0, cachedHistory = [];
  if (aiPanels.has(key)) {
    const old = aiPanels.get(key);
    cachedRound = old.round;
    cachedHistory = old.history;
    aiPanels.delete(key);
  }

  // ── Entry button ──
  const entryBtn = document.createElement('button');
  entryBtn.className = 'ai-entry-btn';
  if (cachedHistory.length > 0) {
    entryBtn.innerHTML = '<span class="ai-sparkle">&#10024;</span> AI 解析 (已有对话)';
  } else {
    entryBtn.innerHTML = '<span class="ai-sparkle">&#10024;</span> AI 帮你解析';
  }
  entryBtn.onclick = () => toggleAIPanel(key);

  // ── Panel ──
  const panel = document.createElement('div');
  panel.className = 'ai-panel';
  panel.style.display = 'none';

  // Header with round indicator
  const header = document.createElement('div');
  header.className = 'ai-panel-header';
  header.innerHTML = '<span class="ai-panel-title">AI 答疑</span><span class="ai-round-badge">1/' + AI_MAX_ROUNDS + '</span>';

  // Messages area
  const messages = document.createElement('div');
  messages.className = 'ai-messages';
  _bindScrollPause(messages);  // 用户上滑时暂停自动滚动

  // Input area — Claude/ChatGPT style floating capsule
  const inputArea = document.createElement('div');
  inputArea.className = 'ai-input-area';
  const inputBox = document.createElement('div');
  inputBox.className = 'ai-input-box';
  const input = document.createElement('textarea');
  input.className = 'ai-input';
  input.placeholder = '追问…';
  input.maxLength = 500;
  input.rows = 1;

  const sendBtn = document.createElement('button');
  sendBtn.className = 'ai-send-btn';
  sendBtn.innerHTML = _AI_SEND_ICON;

  // 长按发送按钮 → 语音输入（仅在 ASR 已配置时生效）
  const _asrEnabled = typeof S !== 'undefined' && S.bankInfo && S.bankInfo.asr_enabled;
  let _longPressTimer = null;
  let _longPressTriggered = false;

  function _onPressStart(e) {
    if (!_asrEnabled || sendBtn.disabled) return;
    _longPressTriggered = false;
    _longPressTimer = setTimeout(() => {
      _longPressTriggered = true;
      e.preventDefault();
      _startASR(key);
    }, 500);
  }
  function _onPressEnd(e) {
    if (_longPressTimer) { clearTimeout(_longPressTimer); _longPressTimer = null; }
    if (_longPressTriggered) {
      e.preventDefault();
      _stopASR();
      _longPressTriggered = false;
      return;
    }
  }
  function _onPressCancel() {
    if (_longPressTimer) { clearTimeout(_longPressTimer); _longPressTimer = null; }
  }

  sendBtn.addEventListener('touchstart', _onPressStart, { passive: false });
  sendBtn.addEventListener('touchend', _onPressEnd);
  sendBtn.addEventListener('touchcancel', _onPressCancel);
  sendBtn.addEventListener('mousedown', _onPressStart);
  sendBtn.addEventListener('mouseup', _onPressEnd);
  sendBtn.addEventListener('mouseleave', _onPressCancel);
  sendBtn.addEventListener('contextmenu', (e) => { if (_asrEnabled) e.preventDefault(); });

  sendBtn.onclick = (e) => {
    if (_longPressTriggered) { e.preventDefault(); return; }
    sendAIMessage(key);
  };
  input.onkeydown = (e) => {
    if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); sendAIMessage(key); }
  };
  input.oninput = () => autoResizeTextarea(input);
  inputBox.appendChild(input);
  inputBox.appendChild(sendBtn);
  inputArea.appendChild(inputBox);

  panel.appendChild(header);
  panel.appendChild(messages);
  panel.appendChild(inputArea);

  container.appendChild(entryBtn);
  container.appendChild(panel);

  aiPanels.set(key, {
    round: cachedRound,
    history: cachedHistory,
    streaming: false,
    q,
    sqIdx,
    userAnswer,
    els: { entryBtn, panel, header, messages, input, sendBtn },
  });

  // Restore cached conversation into new DOM
  if (cachedHistory.length > 0) {
    restoreMessages(key);
    // Auto-expand panel to show cached conversation
    panel.style.display = '';
  }
}

/**
 * Restore cached conversation messages into a freshly created panel DOM.
 */
function restoreMessages(key) {
  const state = aiPanels.get(key);
  if (!state || state.history.length === 0) return;

  const { history, els } = state;
  const { messages, header, input, sendBtn } = els;

  updateRoundBadge(header, state.round);

  for (const msg of history) {
    if (msg.role === 'user') {
      appendMsg(messages, 'user', msg.content);
    } else {
      const msgEl = appendMsg(messages, 'assistant', '');

      // Restore thinking section if reasoning exists
      if (msg.reasoning) {
        const thinkingWrap = document.createElement('div');
        thinkingWrap.className = 'ai-thinking-wrap';
        const thinkingHeader = document.createElement('div');
        thinkingHeader.className = 'ai-thinking-header ai-thinking-collapsed';
        thinkingHeader.innerHTML = '<span class="ai-thinking-icon">&#128161;</span> <span class="ai-thinking-label">思考过程</span>';
        const thinkingBody = document.createElement('div');
        thinkingBody.className = 'ai-thinking-body';
        thinkingBody.style.display = 'none';
        renderContent(thinkingBody, msg.reasoning, null);
        thinkingWrap.appendChild(thinkingHeader);
        thinkingWrap.appendChild(thinkingBody);
        msgEl.appendChild(thinkingWrap);
        thinkingHeader.onclick = () => {
          const hidden = thinkingBody.style.display === 'none';
          thinkingBody.style.display = hidden ? '' : 'none';
          thinkingHeader.classList.toggle('ai-thinking-collapsed', !hidden);
          thinkingHeader.classList.toggle('ai-thinking-expanded', hidden);
        };
      }

      // Render main content
      const contentWrap = document.createElement('div');
      contentWrap.className = 'ai-content-wrap';
      renderContent(contentWrap, msg.content, null);
      msgEl.appendChild(contentWrap);
    }
  }

  // Update input state
  if (state.round >= AI_MAX_ROUNDS) {
    input.disabled = true;
    sendBtn.disabled = true;
    input.placeholder = '答疑次数已用完';
    const notice = document.createElement('div');
    notice.className = 'ai-closed';
    notice.textContent = '答疑次数已用完';
    messages.appendChild(notice);
  }

  scrollMessages(messages);
}

function clearAICache() {
  aiPanels.clear();
}

function toggleAIPanel(key) {
  const state = aiPanels.get(key);
  if (!state) return;
  const { panel } = state.els;
  const isOpen = panel.style.display !== 'none';
  if (isOpen) {
    panel.style.display = 'none';
    return;
  }
  loadAIAssets().then(() => {
    panel.style.display = '';
    // 滚动到 AI 面板位置，让用户看到面板已展开
    setTimeout(() => panel.scrollIntoView({ behavior: 'smooth', block: 'nearest' }), 50);
    if (state.round === 0 && !state.streaming) sendAIMessage(key);
  }).catch(err => console.error('[AI] 资源加载失败', err));
}

function sendAIMessage(key) {
  const state = aiPanels.get(key);
  if (!state || state.streaming) return;
  if (state.round >= AI_MAX_ROUNDS) return;

  const { q, sqIdx, userAnswer, els } = state;
  const { messages, input, sendBtn, header } = els;

  // If this is a follow-up (round > 0), capture user input
  let userText = '';
  if (state.round > 0) {
    userText = input.value.trim();
    if (!userText) return;
    // Show user message bubble
    appendMsg(messages, 'user', userText);
    state.history.push({ role: 'user', content: userText });
    input.value = '';
  }

  state.streaming = true;
  state.round++;
  updateRoundBadge(header, state.round);
  // 输入框保持可输入；仅按钮变为加载动画
  sendBtn.disabled = true;
  sendBtn.dataset.origHTML = sendBtn.innerHTML;
  sendBtn.innerHTML = '<span class="ai-spinner"></span>';
  sendBtn.classList.add('ai-sending');

  // Create assistant message container
  _resetScrollPause();  // 新消息开始，重置滚动暂停状态
  const msgEl = appendMsg(messages, 'assistant', '');
  msgEl.classList.add('ai-typing');

  // Thinking section (hidden initially)
  const thinkingWrap = document.createElement('div');
  thinkingWrap.className = 'ai-thinking-wrap';
  thinkingWrap.style.display = 'none';

  const thinkingHeader = document.createElement('div');
  thinkingHeader.className = 'ai-thinking-header';
  thinkingHeader.innerHTML = '<span class="ai-thinking-icon">&#128161;</span> <span class="ai-thinking-label">思考中…</span>';

  const thinkingBody = document.createElement('div');
  thinkingBody.className = 'ai-thinking-body';

  thinkingWrap.appendChild(thinkingHeader);
  thinkingWrap.appendChild(thinkingBody);
  msgEl.appendChild(thinkingWrap);

  // Content section
  const contentWrap = document.createElement('div');
  contentWrap.className = 'ai-content-wrap';
  msgEl.appendChild(contentWrap);

  // Cursor
  // 不使用文字光标，改用按钮加载动画指示流式状态

  // Build request body
  const reqBody = {
    fingerprint: q.fingerprint,
    sq_index: sqIdx,
    user_answer: userAnswer,
    bank: typeof S !== 'undefined' && S.bankID != null ? S.bankID : 0,
    history: state.history.slice(),
  };

  // Use raw fetch (not apiFetch) because we need streaming
  const uid = typeof _getUIDCookie === 'function' ? _getUIDCookie() : '';
  const headers = {
    'Content-Type': 'application/json',
    'Accept': 'text/event-stream',
    'X-Session-Token': typeof window !== 'undefined' && window.SESSION_TOKEN ? window.SESSION_TOKEN : '',
  };
  if (uid) headers['X-User-ID'] = uid;

  let fullReasoning = '';
  let hasReasoning = false;
  let thinkingCollapsed = false;
  let aborted = false;
  let contentRenderer = null; // streaming renderer for content
  let reasoningRenderer = null; // streaming renderer for thinking
  let fullRawText = ''; // raw text for history saving

  fetch('/api/ai/chat', {
    method: 'POST',
    headers,
    body: JSON.stringify(reqBody),
  }).then(res => {
    if (!res.ok) {
      return res.text().then(t => { throw new Error(t); });
    }
    if (!res.body) {
      throw new Error('ReadableStream not available');
    }
    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';
    let chunkCount = 0;

    function read() {
      return reader.read().then(({ done, value }) => {
        if (aborted) return;
        if (done) {
          // 流结束时 buffer 中可能还有最后一行完整数据，必须处理完再收尾
          if (buffer.trim()) {
            buffer += '\n'; // 补换行让 split 能提取最后一行
            const tail = buffer.split('\n');
            buffer = '';
            for (const line of tail) {
              if (!line.startsWith('data: ')) continue;
              const data = line.slice(6);
              if (data === '[DONE]') break;
              try {
                const obj = JSON.parse(data);
                if (obj.content) { if (!contentRenderer) contentRenderer = makeStreamingRenderer(contentWrap, null, messages); contentRenderer.push(obj.content); fullRawText += obj.content; }
                if (obj.reasoning) { if (!reasoningRenderer) reasoningRenderer = makeStreamingRenderer(thinkingBody, null, messages); reasoningRenderer.push(obj.reasoning); fullReasoning += obj.reasoning; }
              } catch(e) {}
            }
          }
          finishStream();
          return;
        }
        const text = decoder.decode(value, { stream: true });
        buffer += text;
        const lines = buffer.split('\n');
        buffer = lines.pop(); // keep incomplete line
        for (const line of lines) {
          if (!line.startsWith('data: ')) continue;
          const data = line.slice(6);
          if (data === '[DONE]') {
            finishStream();
            return;
          }
          try {
            const obj = JSON.parse(data);
            chunkCount++;
            if (obj.error) {
              contentWrap.textContent = '[错误] ' + obj.error;
              fullRawText = '[错误] ' + obj.error;
              scrollMessages(messages);
              finishStream();
              return;
            }

            // Handle reasoning/thinking content — stream it incrementally
            if (obj.reasoning) {
              if (!hasReasoning) {
                hasReasoning = true;
                thinkingWrap.style.display = '';
                thinkingWrap.classList.add('ai-thinking-fadein');
                reasoningRenderer = makeStreamingRenderer(thinkingBody, null, messages);
              }
              reasoningRenderer.push(obj.reasoning);
              fullReasoning += obj.reasoning;
              scrollMessages(messages);
            }

            // Handle main content — stream it paragraph by paragraph
            if (obj.content) {
              // If we had reasoning and now content starts, collapse thinking
              if (hasReasoning && !thinkingCollapsed) {
                thinkingCollapsed = true;
                collapseThinking(thinkingHeader, thinkingBody);
              }
              // Lazily create streaming renderer on first content chunk
              if (!contentRenderer) {
                contentRenderer = makeStreamingRenderer(contentWrap, null, messages);
              }
              contentRenderer.push(obj.content);
              fullRawText += obj.content;
            }
          } catch (e) { console.warn('[AI] parse error:', e, 'line:', line); }
        }
        return read();
      });
    }
    return read();
  }).catch(err => {
    if (!aborted) {
      if (!fullRawText && !fullReasoning) {
        contentWrap.textContent = '[请求失败] ' + (err.message || '网络错误');
        msgEl.classList.add('ai-error');
      }
      finishStream();
    }
  });

  function finishStream() {
    if (aborted) return;
    aborted = true;
    state.streaming = false;
    msgEl.classList.remove('ai-typing');

    // If reasoning was never collapsed (no content came after), collapse it now
    if (hasReasoning && !thinkingCollapsed) {
      thinkingCollapsed = true;
      collapseThinking(thinkingHeader, thinkingBody);
    }

    // Final flush of streaming renderer
    if (contentRenderer) {
      contentRenderer.flush();
    }

    // Save assistant response to history
    if (fullRawText) {
      state.history.push({ role: 'assistant', content: fullRawText, reasoning: fullReasoning || '' });
    }

    if (state.round >= AI_MAX_ROUNDS) {
      input.disabled = true;
      sendBtn.disabled = true;
      sendBtn.classList.remove('ai-sending');
      sendBtn.innerHTML = sendBtn.dataset.origHTML || _AI_SEND_ICON;
      input.placeholder = '答疑次数已用完';
      const notice = document.createElement('div');
      notice.className = 'ai-closed';
      notice.textContent = '答疑次数已用完';
      messages.appendChild(notice);
    } else {
      input.disabled = false;
      sendBtn.disabled = false;
      sendBtn.classList.remove('ai-sending');
      sendBtn.innerHTML = sendBtn.dataset.origHTML || _AI_SEND_ICON;
      // 不自动 focus，避免移动端弹出输入法
    }
    scrollMessages(messages);
  }
}

/**
 * Render markdown content into a container, optionally appending a cursor element.
 */
function renderContent(container, text, cursor) {
  container.innerHTML = markedRender(text);
  if (cursor) container.appendChild(cursor);
}

/**
 * Collapse the thinking section — make it toggleable.
 */
function collapseThinking(headerEl, bodyEl) {
  headerEl.querySelector('.ai-thinking-label').textContent = '思考过程';
  headerEl.classList.add('ai-thinking-collapsed');
  bodyEl.style.display = 'none';
  headerEl.onclick = () => {
    const hidden = bodyEl.style.display === 'none';
    bodyEl.style.display = hidden ? '' : 'none';
    headerEl.classList.toggle('ai-thinking-collapsed', !hidden);
    headerEl.classList.toggle('ai-thinking-expanded', hidden);
  };
}

function appendMsg(container, role, text) {
  const el = document.createElement('div');
  el.className = 'ai-msg ' + (role === 'user' ? 'ai-msg-user' : 'ai-msg-assistant');
  if (text) el.textContent = text;
  container.appendChild(el);
  scrollMessages(container);
  return el;
}

let _scrollRafId = null;
let _scrollPaused = false;   // 用户手动上滑时暂停自动滚动
let _scrollTarget = null;    // 当前滚动容器引用

function scrollMessages(container) {
  if (_scrollPaused) return;
  if (_scrollRafId) return;
  function step() {
    if (_scrollPaused) { _scrollRafId = null; return; }
    const target = container.scrollHeight - container.clientHeight;
    const diff = target - container.scrollTop;
    if (diff <= 1) {
      container.scrollTop = target;
      _scrollRafId = null;
      return;
    }
    container.scrollTop += diff * 0.18;
    _scrollRafId = requestAnimationFrame(step);
  }
  _scrollRafId = requestAnimationFrame(step);
}

function _bindScrollPause(messagesEl) {
  _scrollTarget = messagesEl;
  function onPress() {
    _scrollPaused = true;
    if (_scrollRafId) { cancelAnimationFrame(_scrollRafId); _scrollRafId = null; }
  }
  messagesEl.addEventListener('touchstart', onPress, { passive: true });
  messagesEl.addEventListener('mousedown',  onPress);
  function onRelease() {
    setTimeout(() => {
      var atBottom = messagesEl.scrollHeight - messagesEl.scrollTop - messagesEl.clientHeight < 40;
      if (atBottom) _scrollPaused = false;
    }, 100);
  }
  messagesEl.addEventListener('touchend',   onRelease, { passive: true });
  messagesEl.addEventListener('mouseup',    onRelease);
  messagesEl.addEventListener('wheel', () => {
    _scrollPaused = true;
    if (_scrollRafId) { cancelAnimationFrame(_scrollRafId); _scrollRafId = null; }
    setTimeout(() => {
      var atBottom = messagesEl.scrollHeight - messagesEl.scrollTop - messagesEl.clientHeight < 40;
      if (atBottom) _scrollPaused = false;
    }, 50);
  }, { passive: true });
}

function _resetScrollPause() {
  _scrollPaused = false;
  if (_scrollRafId) { cancelAnimationFrame(_scrollRafId); _scrollRafId = null; }
}

function updateRoundBadge(header, round) {
  const badge = header.querySelector('.ai-round-badge');
  if (badge) badge.textContent = round + '/' + AI_MAX_ROUNDS;
}

// ══════════════════════════════════════════
// ASR 语音识别
// ══════════════════════════════════════════
let _asrState = null;

async function _startASR(key) {
  if (_asrState) return;
  const state = aiPanels.get(key);
  if (!state) return;
  const { input, sendBtn } = state.els;
  let stream;
  try {
    stream = await navigator.mediaDevices.getUserMedia({ audio: { sampleRate: 16000, channelCount: 1, echoCancellation: true, noiseSuppression: true } });
  } catch (e) { toast('无法访问麦克风，请检查权限设置'); return; }

  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const ws = new WebSocket(`${proto}//${location.host}/api/asr/ws?token=${encodeURIComponent(window.SESSION_TOKEN||'')}`);
  ws.binaryType = 'arraybuffer';
  let ready = false;
  const pendingChunks = [];

  ws.onmessage = (evt) => {
    try {
      const msg = JSON.parse(evt.data);
      if (msg.type === 'ready') {
        ready = true;
        pendingChunks.forEach(c => ws.send(c));
        pendingChunks.length = 0;
      } else if (msg.type === 'partial') {
        if (msg.text) { input.value = (_asrState ? _asrState.baseText : '') + msg.text; autoResizeTextarea(input); }
      } else if (msg.type === 'done') { _stopASR(); }
      else if (msg.type === 'error') { toast('语音识别错误: ' + (msg.text || '未知')); _stopASR(); }
    } catch (e) {}
  };
  ws.onerror = () => { toast('语音连接失败'); _stopASR(); };
  ws.onclose = () => { if (_asrState) _stopASR(); };

  const audioCtx = new (window.AudioContext || window.webkitAudioContext)({ sampleRate: 16000 });
  const source = audioCtx.createMediaStreamSource(stream);
  const bufSize = 4096;
  const processor = audioCtx.createScriptProcessor(bufSize, 1, 1);
  processor.onaudioprocess = (e) => {
    const float32 = e.inputBuffer.getChannelData(0);
    const int16 = new Int16Array(float32.length);
    for (let i = 0; i < float32.length; i++) {
      const s = Math.max(-1, Math.min(1, float32[i]));
      int16[i] = s < 0 ? s * 0x8000 : s * 0x7FFF;
    }
    const buf = int16.buffer;
    if (ready && ws.readyState === 1) ws.send(buf);
    else if (ws.readyState === 0) pendingChunks.push(buf);
  };
  source.connect(processor);
  processor.connect(audioCtx.destination);

  _asrState = { ws, audioCtx, source, processor, stream, key, baseText: input.value };
  sendBtn.innerHTML = _AI_MIC_ICON;
  sendBtn.classList.add('ai-mic-active');
  input.placeholder = '正在聆听… 松开结束';
}

function _stopASR() {
  if (!_asrState) return;
  const { ws, audioCtx, source, processor, stream, key } = _asrState;
  const state = aiPanels.get(key);
  try { processor.disconnect(); } catch(e) {}
  try { source.disconnect(); } catch(e) {}
  try { audioCtx.close(); } catch(e) {}
  stream.getTracks().forEach(t => t.stop());
  if (ws.readyState === 1) { try { ws.send(JSON.stringify({ type: 'stop' })); } catch(e) {} setTimeout(() => { try { ws.close(); } catch(e) {} }, 500); }
  _asrState = null;
  if (state) {
    const { input, sendBtn } = state.els;
    sendBtn.innerHTML = _AI_SEND_ICON;
    sendBtn.classList.remove('ai-mic-active');
    input.placeholder = '追问…';
  }
}
