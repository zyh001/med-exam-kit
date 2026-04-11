/* ================================================================
   quiz_ai.js  — AI 答疑面板模块
   依赖：common.js (apiFetch), quiz.js (S, esc, isMultiQ), marked.min.js, katex
   ================================================================ */

const AI_MAX_ROUNDS = 3;

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

// Configure marked for medical content + LaTeX math (used for restoring cached messages)
if (typeof marked !== 'undefined') {
  marked.use({
    breaks: true,
    gfm: true,
    extensions: [blockMathExt, inlineMathExt],
  });
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
  const CHARS_PER_FRAME = 4;

  // Initialize smd parser
  if (typeof smd !== 'undefined' && smd.default_renderer && smd.parser) {
    const renderer = smd.default_renderer(container);
    smdParser = smd.parser(renderer);
  }

  function placeCursor() {
    if (cursor) {
      // Always keep cursor at the very end of container
      container.appendChild(cursor);
    }
  }

  function drip() {
    if (pending.length === 0) { rafId = null; return; }
    const slice = pending.slice(0, CHARS_PER_FRAME);
    pending = pending.slice(CHARS_PER_FRAME);
    fullText += slice;

    if (smdParser) {
      smd.parser_write(smdParser, slice);
    } else {
      // Fallback: plain text append
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
      if (smdParser) {
        smd.parser_write(smdParser, pending);
      }
      pending = '';
    }
    if (smdParser) {
      smd.parser_end(smdParser);
      smdParser = null;
    }
    // Re-render with marked for LaTeX/GFM polish
    if (typeof marked !== 'undefined' && marked.parse && fullText) {
      try {
        container.innerHTML = marked.parse(fullText, { async: false });
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

  // Input area
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
  sendBtn.onclick = () => sendAIMessage(key);
  input.onkeydown = (e) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); sendAIMessage(key); } };
  inputArea.appendChild(input);
  inputArea.appendChild(sendBtn);

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
  panel.style.display = '';
  // Auto-send initial analysis on first open
  if (state.round === 0 && !state.streaming) {
    sendAIMessage(key);
  }
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
  input.disabled = true;
  sendBtn.disabled = true;

  // Create assistant message container
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
  const cursor = document.createElement('span');
  cursor.className = 'ai-cursor';
  cursor.textContent = '\u258D';

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
        if (done || aborted) {
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
                contentRenderer = makeStreamingRenderer(contentWrap, cursor, messages);
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
      input.placeholder = '答疑次数已用完';
      const notice = document.createElement('div');
      notice.className = 'ai-closed';
      notice.textContent = '答疑次数已用完';
      messages.appendChild(notice);
    } else {
      input.disabled = false;
      sendBtn.disabled = false;
      input.focus();
    }
    scrollMessages(messages);
  }
}

/**
 * Render markdown content into a container, optionally appending a cursor element.
 */
function renderContent(container, text, cursor) {
  if (typeof marked !== 'undefined' && marked.parse) {
    container.innerHTML = marked.parse(text);
  } else {
    // Fallback: escape and add line breaks
    container.textContent = text;
  }
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

function scrollMessages(container) {
  container.scrollTop = container.scrollHeight;
}

function updateRoundBadge(header, round) {
  const badge = header.querySelector('.ai-round-badge');
  if (badge) badge.textContent = round + '/' + AI_MAX_ROUNDS;
}
