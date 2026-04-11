/* ================================================================
   quiz_ai.js  — AI 答疑面板模块
   依赖：common.js (apiFetch), quiz.js (S, esc, isMultiQ)
         smd.min.js, katex.min.js, auto-render.min.js
   不再依赖 marked.min.js — 全程使用 smd 增量渲染 + KaTeX
   ================================================================ */

const AI_MAX_ROUNDS = 3;

// key: "fingerprint-idx" → { round, history, streaming, els }
const aiPanels = new Map();

// ── KaTeX auto-render helper ──────────────────────────────────────
// 对一个 DOM 节点运行 KaTeX auto-render（如果可用）
const KATEX_DELIMITERS = [
  { left: '$$', right: '$$', display: true  },
  { left: '$',  right: '$',  display: false },
];
function renderLatexIn(el) {
  if (typeof renderMathInElement === 'function') {
    try {
      renderMathInElement(el, { delimiters: KATEX_DELIMITERS, throwOnError: false });
    } catch(e) {}
  }
}

// ── Plain text fallback render ────────────────────────────────────
// 当 smd 不可用时，把原始文本放进 <pre>
function plainRender(container, text) {
  container.innerHTML = '<pre style="white-space:pre-wrap">' + esc(text) + '</pre>';
}

// ── renderContent：用于恢复历史对话 ──────────────────────────────
// 历史消息已经完整，用 smd 一次性渲染整段文本
function renderContent(container, text) {
  container.innerHTML = '';
  if (!text) return;
  if (typeof smd !== 'undefined' && smd.default_renderer && smd.parser) {
    const renderer = smd.default_renderer(container);
    const parser   = smd.parser(renderer);
    smd.parser_write(parser, text);
    smd.parser_end(parser);
    renderLatexIn(container);
  } else {
    plainRender(container, text);
  }
}

// ── makeStreamingRenderer ─────────────────────────────────────────
// 流式阶段：smd 增量追加 DOM，MutationObserver 实时触发 KaTeX
function makeStreamingRenderer(container, cursor, scrollTarget) {
  let pending  = '';
  let rafId    = null;
  let smdParser = null;

  if (typeof smd !== 'undefined' && smd.default_renderer && smd.parser) {
    const renderer = smd.default_renderer(container);
    smdParser = smd.parser(renderer);
  }

  // MutationObserver：新块级节点 → 滑入动画 + 立即渲染 LaTeX
  let observer = null;
  try {
    observer = new MutationObserver(function(mutations) {
      for (const m of mutations) {
        for (const node of m.addedNodes) {
          if (node.nodeType !== 1) continue;
          const tag = node.tagName;
          if (/^(P|H[1-6]|UL|OL|LI|TABLE|BLOCKQUOTE|PRE|CODE)$/i.test(tag)) {
            node.classList.add('ai-block-in');
          }
          // 实时 LaTeX 渲染（含 SPAN，katex 会生成 span）
          renderLatexIn(node);
        }
      }
    });
    observer.observe(container, { childList: true, subtree: true });
  } catch(e) {}

  function placeCursor() {
    if (cursor) container.appendChild(cursor);
  }

  function drip() {
    if (pending.length === 0) { rafId = null; return; }
    // 词级释放：找到下一个空白边界
    let end = 1;
    if (/\s/.test(pending[0])) {
      while (end < pending.length && /\s/.test(pending[end])) end++;
    } else {
      while (end < pending.length && !/\s/.test(pending[end])) end++;
    }
    const slice = pending.slice(0, end);
    pending = pending.slice(end);

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

  function flush(fullText) {
    // 停止动画，写入剩余
    if (rafId) { cancelAnimationFrame(rafId); rafId = null; }
    if (pending.length > 0) {
      if (smdParser) smd.parser_write(smdParser, pending);
      pending = '';
    }
    if (smdParser) { smd.parser_end(smdParser); smdParser = null; }
    if (observer) { observer.disconnect(); observer = null; }
    // flush 后对整个容器再跑一次 LaTeX，确保公式完整渲染
    renderLatexIn(container);
  }

  return { push, flush };
}


/**
 * 创建并插入 AI 答疑区
 */
function initAIPanel(container, q, sqIdx, userAnswer) {
  if (typeof S === 'undefined' || !S.bankInfo || !S.bankInfo.ai_enabled) return;

  const key = q.fingerprint + '-' + sqIdx;

  let cachedRound = 0, cachedHistory = [];
  if (aiPanels.has(key)) {
    const old = aiPanels.get(key);
    cachedRound   = old.round;
    cachedHistory = old.history;
    aiPanels.delete(key);
  }

  const entryBtn = document.createElement('button');
  entryBtn.className = 'ai-entry-btn';
  entryBtn.innerHTML = cachedHistory.length > 0
    ? '<span class="ai-sparkle">&#10024;</span> AI 解析 (已有对话)'
    : '<span class="ai-sparkle">&#10024;</span> AI 帮你解析';
  entryBtn.onclick = () => toggleAIPanel(key);

  const panel = document.createElement('div');
  panel.className = 'ai-panel';
  panel.style.display = 'none';

  const header = document.createElement('div');
  header.className = 'ai-panel-header';
  header.innerHTML = '<span class="ai-panel-title">AI 答疑</span><span class="ai-round-badge">1/' + AI_MAX_ROUNDS + '</span>';

  const messages = document.createElement('div');
  messages.className = 'ai-messages';

  const inputArea = document.createElement('div');
  inputArea.className = 'ai-input-area';
  const input = document.createElement('input');
  input.type = 'text';
  input.className = 'ai-input';
  input.placeholder = '追问…';
  input.maxLength = 500;
  const sendBtn = document.createElement('button');
  sendBtn.className = 'ai-send-btn';
  sendBtn.textContent = '发送';
  sendBtn.onclick  = () => sendAIMessage(key);
  input.onkeydown  = (e) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); sendAIMessage(key); } };
  inputArea.appendChild(input);
  inputArea.appendChild(sendBtn);

  panel.appendChild(header);
  panel.appendChild(messages);
  panel.appendChild(inputArea);

  container.appendChild(entryBtn);
  container.appendChild(panel);

  aiPanels.set(key, {
    round: cachedRound, history: cachedHistory,
    streaming: false, q, sqIdx, userAnswer,
    els: { entryBtn, panel, header, messages, input, sendBtn },
  });

  if (cachedHistory.length > 0) {
    restoreMessages(key);
    panel.style.display = '';
  }
}

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

      if (msg.reasoning) {
        const tw = document.createElement('div');
        tw.className = 'ai-thinking-wrap';
        const th = document.createElement('div');
        th.className = 'ai-thinking-header ai-thinking-collapsed';
        th.innerHTML = '<span class="ai-thinking-icon">&#128161;</span> <span class="ai-thinking-label">思考过程</span>';
        const tb = document.createElement('div');
        tb.className = 'ai-thinking-body';
        tb.style.display = 'none';
        renderContent(tb, msg.reasoning);
        tw.appendChild(th); tw.appendChild(tb);
        msgEl.appendChild(tw);
        th.onclick = () => {
          const hidden = tb.style.display === 'none';
          tb.style.display = hidden ? '' : 'none';
          th.classList.toggle('ai-thinking-collapsed', !hidden);
          th.classList.toggle('ai-thinking-expanded', hidden);
        };
      }

      const cw = document.createElement('div');
      cw.className = 'ai-content-wrap';
      renderContent(cw, msg.content);
      msgEl.appendChild(cw);
    }
  }

  if (state.round >= AI_MAX_ROUNDS) {
    input.disabled = true; sendBtn.disabled = true;
    input.placeholder = '答疑次数已用完';
    const notice = document.createElement('div');
    notice.className = 'ai-closed';
    notice.textContent = '答疑次数已用完';
    messages.appendChild(notice);
  }
  scrollMessages(messages);
}

function clearAICache() { aiPanels.clear(); }

function toggleAIPanel(key) {
  const state = aiPanels.get(key);
  if (!state) return;
  const { panel } = state.els;
  if (panel.style.display !== 'none') { panel.style.display = 'none'; return; }
  panel.style.display = '';
  if (state.round === 0 && !state.streaming) sendAIMessage(key);
}

function sendAIMessage(key) {
  const state = aiPanels.get(key);
  if (!state || state.streaming || state.round >= AI_MAX_ROUNDS) return;

  const { q, sqIdx, userAnswer, els } = state;
  const { messages, input, sendBtn, header } = els;

  let userText = '';
  if (state.round > 0) {
    userText = input.value.trim();
    if (!userText) return;
    appendMsg(messages, 'user', userText);
    state.history.push({ role: 'user', content: userText });
    input.value = '';
  }

  state.streaming = true;
  state.round++;
  updateRoundBadge(header, state.round);
  input.disabled = true; sendBtn.disabled = true;

  const msgEl = appendMsg(messages, 'assistant', '');
  msgEl.classList.add('ai-typing');

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

  const contentWrap = document.createElement('div');
  contentWrap.className = 'ai-content-wrap';
  msgEl.appendChild(contentWrap);

  const cursor = document.createElement('span');
  cursor.className = 'ai-cursor';
  cursor.textContent = '\u258D';

  const reqBody = {
    fingerprint: q.fingerprint, sq_index: sqIdx,
    user_answer: userAnswer,
    bank: typeof S !== 'undefined' && S.bankID != null ? S.bankID : 0,
    history: state.history.slice(),
  };

  const uid = typeof _getUIDCookie === 'function' ? _getUIDCookie() : '';
  const headers = {
    'Content-Type': 'application/json',
    'Accept': 'text/event-stream',
    'X-Session-Token': typeof window !== 'undefined' && window.SESSION_TOKEN ? window.SESSION_TOKEN : '',
  };
  if (uid) headers['X-User-ID'] = uid;

  let fullReasoning = '', hasReasoning = false, thinkingCollapsed = false;
  let aborted = false;
  let contentRenderer = null, reasoningRenderer = null;
  let fullRawText = '';

  fetch('/api/ai/chat', { method: 'POST', headers, body: JSON.stringify(reqBody) })
  .then(res => {
    if (!res.ok) return res.text().then(t => { throw new Error(t); });
    if (!res.body) throw new Error('ReadableStream not available');
    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';

    function read() {
      return reader.read().then(({ done, value }) => {
        if (done || aborted) { finishStream(); return; }
        buffer += decoder.decode(value, { stream: true });
        const lines = buffer.split('\n');
        buffer = lines.pop();
        for (const line of lines) {
          if (!line.startsWith('data: ')) continue;
          const data = line.slice(6);
          if (data === '[DONE]') { finishStream(); return; }
          try {
            const obj = JSON.parse(data);
            if (obj.error) {
              contentWrap.textContent = '[错误] ' + obj.error;
              fullRawText = '[错误] ' + obj.error;
              scrollMessages(messages);
              finishStream(); return;
            }
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
            if (obj.content) {
              if (hasReasoning && !thinkingCollapsed) {
                thinkingCollapsed = true;
                collapseThinking(thinkingHeader, thinkingBody);
              }
              if (!contentRenderer) {
                contentRenderer = makeStreamingRenderer(contentWrap, cursor, messages);
              }
              contentRenderer.push(obj.content);
              fullRawText += obj.content;
            }
          } catch(e) { console.warn('[AI] parse error:', e); }
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

    if (hasReasoning && !thinkingCollapsed) {
      thinkingCollapsed = true;
      collapseThinking(thinkingHeader, thinkingBody);
    }
    if (contentRenderer) contentRenderer.flush(fullRawText);
    if (cursor.parentNode) cursor.parentNode.removeChild(cursor);

    if (fullRawText) {
      state.history.push({ role: 'assistant', content: fullRawText, reasoning: fullReasoning || '' });
    }

    if (state.round >= AI_MAX_ROUNDS) {
      input.disabled = true; sendBtn.disabled = true;
      input.placeholder = '答疑次数已用完';
      const notice = document.createElement('div');
      notice.className = 'ai-closed';
      notice.textContent = '答疑次数已用完';
      messages.appendChild(notice);
    } else {
      input.disabled = false; sendBtn.disabled = false;
      input.focus();
    }
    scrollMessages(messages);
  }
}

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
function scrollMessages(container) {
  if (_scrollRafId) return;
  function step() {
    const target = container.scrollHeight - container.clientHeight;
    const diff   = target - container.scrollTop;
    if (diff <= 1) { container.scrollTop = target; _scrollRafId = null; return; }
    container.scrollTop += diff * 0.18;
    _scrollRafId = requestAnimationFrame(step);
  }
  _scrollRafId = requestAnimationFrame(step);
}

function updateRoundBadge(header, round) {
  const badge = header.querySelector('.ai-round-badge');
  if (badge) badge.textContent = round + '/' + AI_MAX_ROUNDS;
}
