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

// ── Mermaid 流程图懒加载 ──────────────────────────────────────────
// Mermaid (~3MB) 仅在检测到 mermaid 代码块时才加载，避免影响首屏性能
// 从本地 /static/ 加载，无需外网访问
const MERMAID_LOCAL = AI_STATIC_BASE + '/mermaid.min.js';
let _mermaidLoaded = false;
let _mermaidPromise = null;
let _mermaidCounter = 0;

function loadMermaid() {
  if (_mermaidLoaded) return Promise.resolve();
  if (_mermaidPromise) return _mermaidPromise;
  _mermaidPromise = new Promise((resolve, reject) => {
    const sc = document.createElement('script');
    sc.src = MERMAID_LOCAL;
    sc.onload = () => {
      _mermaidLoaded = true;
      try {
        mermaid.initialize({
          startOnLoad: false,
          theme: document.documentElement.getAttribute('data-theme') === 'dark' ? 'dark' : 'default',
          securityLevel: 'loose',
          fontFamily: 'inherit',
        });
      } catch(e) {}
      resolve();
    };
    sc.onerror = () => reject(new Error('mermaid.min.js 加载失败'));
    document.body.appendChild(sc);
  });
  return _mermaidPromise;
}

/**
 * 渲染容器内所有 mermaid 代码块。
 * marked 会将 ```mermaid ... ``` 渲染为 <code class="language-mermaid">。
 * 本函数找到这些节点，用 mermaid.render() 替换为 SVG。
 * @param {HTMLElement} container
 */
/**
 * 清理 AI 生成的 mermaid 定义里混入的 HTML 污染。
 *
 * 常见污染来源：
 *  1. marked 对 <> 做了 HTML 实体转义 → &lt; &gt; &amp; 等
 *  2. AI 在节点文本里内嵌了 <br>、<b>、<i>、<span> 等真实 HTML 标签
 *  3. AI 加了 HTML 注释 <!-- ... -->
 *  4. AI 在代码块外层包了 <p>...</p> 等块级标签
 *  5. 中文引号被 marked 转成 &ldquo; &rdquo;
 */
function cleanMermaidDef(raw) {
  let s = raw;

  // ① 去掉外层 HTML 块标签（marked 有时会把代码块包在 <p> 里）
  s = s.replace(/^<(?:p|div|section|article)[^>]*>([\s\S]*?)<\/(?:p|div|section|article)>\s*$/i, '$1');

  // ② 还原 HTML 实体 → 原始字符（marked 渲染时已对特殊符号做了转义）
  s = s
    .replace(/&lt;/g, '<')
    .replace(/&gt;/g, '>')
    .replace(/&amp;/g, '&')
    .replace(/&quot;/g, '"')
    .replace(/&#39;/g, "'")
    .replace(/&ldquo;/g, '\u201c')
    .replace(/&rdquo;/g, '\u201d')
    .replace(/&laquo;/g, '\u00ab')
    .replace(/&raquo;/g, '\u00bb')
    .replace(/&nbsp;/g, ' ');

  // ③ 删除 HTML 注释
  s = s.replace(/<!--[\s\S]*?-->/g, '');

  // ④ 把节点文本里的内联 HTML 标签替换为文本等价物
  //    <br> → 换行（mermaid 节点标签支持 \n）
  s = s.replace(/<br\s*\/?>/gi, '\n');
  //    格式标签（<b>/<strong>/<i>/<em>...）→ 只保留内部文本
  s = s.replace(/<\/?(b|strong|i|em|u|s|del|ins|mark|span|small|sup|sub|code|font)[^>]*>/gi, '');
  //    其余残留 HTML 标签一律删掉，避免 mermaid parser 报错
  s = s.replace(/<[a-zA-Z][^>]*\/?>/g, '').replace(/<\/[a-zA-Z]+>/g, '');

  // ⑤ 清理连续空行
  s = s.replace(/\n{3,}/g, '\n\n');

  return s.trim();
}

async function renderMermaidBlocks(container) {
  // 检测 language-mermaid 或 lang-mermaid class
  const codeEls = container.querySelectorAll('code.language-mermaid, code.lang-mermaid');
  if (codeEls.length === 0) return;

  try {
    await loadMermaid();
  } catch(e) {
    codeEls.forEach(code => {
      const pre = code.closest('pre') || code;
      const warn = document.createElement('p');
      warn.className = 'ai-mermaid-err';
      warn.textContent = '⚠ 流程图渲染失败（mermaid.min.js 未能加载）';
      pre.parentNode.insertBefore(warn, pre);
    });
    return;
  }

  for (const code of codeEls) {
    const pre = code.closest('pre') || code;
    // textContent 含 marked 转义，cleanMermaidDef 负责还原并去除 HTML 污染
    const graphDef = cleanMermaidDef(code.textContent.trim());
    if (!graphDef) continue;

    const id = 'mermaid-' + (++_mermaidCounter);
    try {
      const { svg } = await mermaid.render(id, graphDef);
      const wrapper = document.createElement('div');
      wrapper.className = 'ai-mermaid-wrap';
      wrapper.innerHTML = svg;
      const svgEl = wrapper.querySelector('svg');
      if (svgEl) { svgEl.style.maxWidth = '100%'; svgEl.style.height = 'auto'; }
      pre.replaceWith(wrapper);
    } catch(e) {
      // 语法错误：显示可折叠的错误详情 + 清理后的原始定义（方便复制调试）
      const errDetails = document.createElement('details');
      errDetails.className = 'ai-mermaid-err';
      const summary = document.createElement('summary');
      summary.textContent = '⚠ 流程图语法错误，点击查看详情';
      const errText = document.createElement('p');
      errText.style.cssText = 'margin:6px 0 4px;font-size:12px;opacity:.8';
      errText.textContent = (e.message || String(e)).slice(0, 200);
      const rawPre = document.createElement('pre');
      rawPre.style.cssText = 'font-size:11px;opacity:.6;margin-top:6px;white-space:pre-wrap;word-break:break-all;max-height:160px;overflow-y:auto';
      rawPre.textContent = graphDef;
      errDetails.appendChild(summary);
      errDetails.appendChild(errText);
      errDetails.appendChild(rawPre);
      pre.parentNode.insertBefore(errDetails, pre.nextSibling);
    }
  }
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

// ── Shared marked render (table fix + LaTeX) ─────────────────────
// CJK flanking delimiter fix is in marked.min.js itself (E/H/W/ue/De/qe
// regexes extended with \u3400-\u9FFF\uF900-\uFAFF CJK ranges).
function markedRender(text) {
  if (typeof marked !== 'undefined' && marked.parse) {
    return marked.parse(joinBrokenTableRows(fixTableSeparators(text)), { async: false });
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

// ── Streaming renderer — adaptive-buffer feeding smd, final pass through marked ──
//
// 设计要点：
//   · 流式阶段用 **smd (streaming-markdown)** 直接渲染到 DOM：用户可以边流出边看到
//     已渲染的 markdown（加粗、标题、列表、代码块、链接都立即成形），不再看到原始
//     `**xxx**` 这种 markdown 语法字面量。
//   · 保留自适应缓冲：跟踪近 1.5s 的 token 到达速率，按输出速率把 fullText 喂给 smd
//     的 parser，而不是把每个 token chunk 直接倾倒进 DOM。这样快写慢出 / 慢写慢出，
//     不会因模型忽快忽慢导致视觉跳跃。
//     - backlog < 8  字：下限 30 字/s
//     - backlog 8~120:  匹配输入速率，并轻微向 24 字目标 backlog 靠拢
//     - backlog > 120: 输入速率 × 1.35（或 0.8s 内耗尽）追赶
//     - 全程 clamp 到 [30, 240] 字/s
//   · 新块级元素 (p/li/h*/pre/blockquote/table) 由 CSS 自动淡入上浮（aiBlockIn），
//     保留"丝滑"视觉但不需要逐字符 DOM 操作。
//   · flush() → 把剩下的字符快速喂完，smd parser_end 收尾，然后用 markedRender 对
//     fullText 整体重渲：这一步才会触发 LaTeX (KaTeX) 与 mermaid 的正确渲染，smd
//     在流式阶段只会把它们显示为占位元素 (<equation-inline> 等)。
//   · 尾部保留 .ai-typing-dots 三点动画作为"仍在生成"提示。
//   · smd 未加载时自动降级：按原生文本追加到 <pre>，仍保证不丢内容。

function makeStreamingRenderer(container, scrollTarget) {
  // DOM 布局：
  //   container  (.ai-stream-live 在流式阶段；flush 后换成 .ai-stream-final)
  //     ├─ streamEl  (.ai-stream-body)  → smd 向此节点直接 append
  //     └─ dotsEl    (.ai-typing-dots)  → 流式期间显示，flush 时移除
  container.innerHTML = '';
  container.classList.add('ai-stream-live');
  container.classList.remove('ai-stream-final');

  const streamEl = document.createElement('div');
  streamEl.className = 'ai-stream-body';
  container.appendChild(streamEl);

  const dotsEl = document.createElement('span');
  dotsEl.className = 'ai-typing-dots';
  dotsEl.innerHTML = '<span></span><span></span><span></span>';
  container.appendChild(dotsEl);

  // 构建 smd 解析器；若 smd 未加载（首次调用 AI 时可能还在下载），降级为 <pre>
  //
  // 逐字水流动画：包装 smd.default_renderer 的 add_text 回调，把原本的
  //   document.createTextNode(c)
  // 替换成逐字符 <span class="ai-ch">。smd 内部只保存 e.nodes[e.index]（当前
  // 父块级元素），不关心子节点是 text node 还是 span，因此不会破坏解析树。
  // 这样既保留了 smd 的实时 markdown 渲染（加粗/标题/列表立即成形），又拿回
  // Session A 那种水流逐字动画；CJK 字符得到更长的动画时长，节奏更符合中文阅读。
  function _isCJK(ch) {
    const c = ch.charCodeAt(0);
    return (c >= 0x3400 && c <= 0x9FFF) ||  // 统一汉字 + 扩展 A
           (c >= 0xF900 && c <= 0xFAFF) ||  // 兼容汉字
           (c >= 0x3040 && c <= 0x30FF) ||  // 日文假名
           (c >= 0xAC00 && c <= 0xD7AF) ||  // 韩文
           (c >= 0x3000 && c <= 0x303F) ||  // CJK 标点
           (c >= 0xFF00 && c <= 0xFFEF);    // 全宽
  }
  function _makeFlowRenderer(root) {
    const base = window.smd.default_renderer(root);
    base.add_text = function (data, text) {
      const parent = data.nodes[data.index];
      if (!parent || !text) return;
      const frag = document.createDocumentFragment();
      for (let i = 0; i < text.length; i++) {
        const ch = text[i];
        const span = document.createElement('span');
        span.className = _isCJK(ch) ? 'ai-ch ai-ch-cjk' : 'ai-ch';
        span.textContent = ch;
        frag.appendChild(span);
      }
      parent.appendChild(frag);
    };
    return base;
  }

  let smdParser = null;
  let plainPre  = null;
  if (typeof window !== 'undefined' && window.smd &&
      typeof window.smd.default_renderer === 'function') {
    try {
      const renderer = _makeFlowRenderer(streamEl);
      smdParser = window.smd.parser(renderer);
    } catch (e) { smdParser = null; }
  }
  if (!smdParser) {
    plainPre = document.createElement('pre');
    plainPre.className = 'ai-stream-fallback';
    plainPre.style.whiteSpace = 'pre-wrap';
    streamEl.appendChild(plainPre);
  }

  // ── 状态 ──────────────────────────────────────────────────────────
  let fullText   = '';  // 收到的全部原始 markdown
  let fedLen     = 0;   // 已喂给 smd/plainPre 的前缀长度
  const pushSamples = []; // [{t, n}] 最近 RATE_WINDOW_MS 到达样本
  let finished = false, rafId = null, lastTickTs = 0, fracChars = 0;

  const RATE_WINDOW_MS = 1500;
  const TARGET_BACKLOG = 24;
  const MIN_RATE = 30, MAX_RATE = 240;

  function _computeRate(backlog) {
    const cutoff = performance.now() - RATE_WINDOW_MS;
    while (pushSamples.length && pushSamples[0].t < cutoff) pushSamples.shift();
    let chars = 0;
    for (const s of pushSamples) chars += s.n;
    const inputRate = chars / (RATE_WINDOW_MS / 1000);
    let target;
    if (backlog < 8) {
      target = MIN_RATE;
    } else if (backlog <= 120) {
      const bias = (backlog - TARGET_BACKLOG) / TARGET_BACKLOG;
      target = (inputRate || MIN_RATE) * (1 + 0.25 * bias);
    } else {
      target = Math.max(inputRate * 1.35, backlog / 0.8);
    }
    if (!isFinite(target) || target <= 0) target = MIN_RATE;
    return Math.max(MIN_RATE, Math.min(MAX_RATE, target));
  }

  function _feedChars(n) {
    if (n <= 0) return;
    const target = Math.min(fedLen + n, fullText.length);
    if (target === fedLen) return;
    const chunk = fullText.slice(fedLen, target);
    if (smdParser) {
      try { window.smd.parser_write(smdParser, chunk); }
      catch (e) {
        // smd 解析异常：降级到 <pre> 追加剩余内容
        if (!plainPre) {
          plainPre = document.createElement('pre');
          plainPre.className = 'ai-stream-fallback';
          plainPre.style.whiteSpace = 'pre-wrap';
          streamEl.appendChild(plainPre);
        }
        plainPre.textContent += chunk;
        smdParser = null;
      }
    } else if (plainPre) {
      plainPre.textContent += chunk;
    }
    fedLen = target;
  }

  function _tick(now) {
    if (!lastTickTs) lastTickTs = now;
    const dt = Math.min(0.1, (now - lastTickTs) / 1000);
    lastTickTs = now;

    const backlog = fullText.length - fedLen;
    let rate;
    if (finished) {
      rate = Math.max(MAX_RATE, backlog / 1.0);
    } else {
      rate = _computeRate(backlog);
    }
    fracChars += rate * dt;
    const toFeed = Math.floor(fracChars);
    if (toFeed > 0) {
      fracChars -= toFeed;
      _feedChars(toFeed);
    }

    if (scrollTarget) scrollMessages(scrollTarget);

    if (finished && fedLen >= fullText.length) {
      _finalize();
      return;
    }
    rafId = requestAnimationFrame(_tick);
  }

  function _finalize() {
    rafId = null;
    // 把剩余字符喂完
    if (fedLen < fullText.length) _feedChars(fullText.length - fedLen);
    // 关闭 smd
    if (smdParser) {
      try { window.smd.parser_end(smdParser); } catch (e) {}
    }
    // 切换到 final 状态（CSS 不再对新块做入场动画）
    container.classList.remove('ai-stream-live');
    container.classList.add('ai-stream-final');
    // 整体用 marked 重渲一次：LaTeX (KaTeX) / 表格 / 任务列表 / mermaid code block
    // 等 smd 流式阶段无法正确处理的元素，在这一步得到最终形态。
    try { container.innerHTML = markedRender(fullText); }
    catch (e) { /* 保留 smd 已渲染的结果 */ }
    renderMermaidBlocks(container).catch(() => {});
    if (scrollTarget) scrollMessages(scrollTarget);
  }

  function _start() {
    if (rafId == null && !finished) {
      lastTickTs = 0;
      rafId = requestAnimationFrame(_tick);
    }
  }

  function push(text) {
    if (!text) return;
    fullText += text;
    pushSamples.push({ t: performance.now(), n: text.length });
    _start();
  }

  function flush() {
    finished = true;
    if (rafId == null) _finalize();
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
    // 短按 → 正常发送（不在这里触发，让 onclick 处理）
  }
  function _onPressCancel() {
    if (_longPressTimer) { clearTimeout(_longPressTimer); _longPressTimer = null; }
    // 如果已经在录音，手指移出按钮不停止，等 touchend 停止
  }

  sendBtn.addEventListener('touchstart', _onPressStart, { passive: false });
  sendBtn.addEventListener('touchend', _onPressEnd);
  sendBtn.addEventListener('touchcancel', _onPressCancel);
  sendBtn.addEventListener('mousedown', _onPressStart);
  sendBtn.addEventListener('mouseup', _onPressEnd);
  sendBtn.addEventListener('mouseleave', _onPressCancel);
  // 防止长按触发 contextmenu
  sendBtn.addEventListener('contextmenu', (e) => { if (_asrEnabled) e.preventDefault(); });

  sendBtn.onclick = (e) => {
    if (_longPressTriggered) { e.preventDefault(); return; }
    sendAIMessage(key);
  };
  input.onkeydown = (e) => {
    if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); sendAIMessage(key); }
  };
  // 自适应高度：随内容增长，最多 4 行
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
  // 切题时 abort 所有正在进行的 AI 请求
  for (const state of aiPanels.values()) {
    if (state.abortController) { state.abortController.abort(); state.abortController = null; }
  }
  aiPanels.clear();
}

function toggleAIPanel(key) {
  const state = aiPanels.get(key);
  if (!state) return;
  const { panel } = state.els;
  const isOpen = panel.style.display !== 'none';
  if (isOpen) {
    panel.style.display = 'none';
    // 关闭面板时取消正在进行的 SSE 请求，避免后台继续消耗连接
    if (state.abortController) { state.abortController.abort(); state.abortController = null; }
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
  let truncated = false; // true when server signals finish_reason=length
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
                if (obj.truncated) truncated = true;
                if (obj.content) { if (!contentRenderer) contentRenderer = makeStreamingRenderer(contentWrap, messages); contentRenderer.push(obj.content); fullRawText += obj.content; }
                if (obj.reasoning) { if (!reasoningRenderer) reasoningRenderer = makeStreamingRenderer(thinkingBody, messages); reasoningRenderer.push(obj.reasoning); fullReasoning += obj.reasoning; }
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
                reasoningRenderer = makeStreamingRenderer(thinkingBody, messages);
              }
              reasoningRenderer.push(obj.reasoning);
              fullReasoning += obj.reasoning;
              // scroll handled inside makeStreamingRenderer.scheduleScroll()
            }

            // truncated 标志：服务端在 [DONE] 前发送 {"truncated":true}
            if (obj.truncated) truncated = true;

            // Handle main content — stream it paragraph by paragraph
            if (obj.content) {
              // If we had reasoning and now content starts, collapse thinking
              if (hasReasoning && !thinkingCollapsed) {
                thinkingCollapsed = true;
                collapseThinking(thinkingHeader, thinkingBody);
              }
              // Lazily create streaming renderer on first content chunk
              if (!contentRenderer) {
                contentRenderer = makeStreamingRenderer(contentWrap, messages);
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
    // AbortError 是用户主动取消（关闭面板），不算错误
    if (err && err.name === 'AbortError') { aborted = true; return; }
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
    // Final flush of streaming renderers
    if (reasoningRenderer) {
      reasoningRenderer.flush();
      reasoningRenderer = null;
    }
    if (contentRenderer) {
      contentRenderer.flush();
      contentRenderer = null;
    }
    // Collapse thinking after content is rendered
    if (hasReasoning && !thinkingCollapsed) {
      thinkingCollapsed = true;
      setTimeout(() => collapseThinking(thinkingHeader, thinkingBody), 50);
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

      // 输出被截断：在消息末尾插入提示 + 「继续输出」按钮
      if (truncated) {
        const contWrap = document.createElement('div');
        contWrap.className = 'ai-truncated-notice';
        contWrap.innerHTML =
          '<span class="ai-truncated-label">⚠ 已达输出长度限制</span>' +
          '<button class="ai-continue-btn">继续输出 →</button>';
        contWrap.querySelector('.ai-continue-btn').addEventListener('click', () => {
          contWrap.remove();            // 移除提示条
          // 将「继续」填入输入框并发送，消耗一次对话次数
          input.value = '请继续上面未完成的输出，从断点处接续，不要重复已有内容';
          sendAIMessage(key);
        });
        msgEl.appendChild(contWrap);
      }
    }
    scrollMessages(messages);
  }
}

/**
 * Render markdown content into a container, optionally appending a cursor element.
 */
function renderContent(container, text, cursor) {
  container.innerHTML = markedRender(text);
  // Post-render: replace mermaid code blocks with SVG diagrams
  renderMermaidBlocks(container).catch(() => {});
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
  if (_scrollPaused) return;  // 用户已手动上滑，不自动滚动
  if (_scrollRafId) return;
  function step() {
    if (_scrollPaused) { _scrollRafId = null; return; }  // 动画中途被打断
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

/** 绑定用户滚动检测到 messages 容器 */
function _bindScrollPause(messagesEl) {
  _scrollTarget = messagesEl;
  // 触摸/鼠标按下 → 立即暂停自动滚动（消除顿挫感）
  function onPress() {
    _scrollPaused = true;
    if (_scrollRafId) { cancelAnimationFrame(_scrollRafId); _scrollRafId = null; }
  }
  messagesEl.addEventListener('touchstart', onPress, { passive: true });
  messagesEl.addEventListener('mousedown',  onPress);
  // 触摸/鼠标释放 → 检查是否在底部，在底部则恢复自动滚动
  function onRelease() {
    // 短延迟等惯性滚动稳定
    setTimeout(() => {
      var atBottom = messagesEl.scrollHeight - messagesEl.scrollTop - messagesEl.clientHeight < 40;
      if (atBottom) _scrollPaused = false;
    }, 100);
  }
  messagesEl.addEventListener('touchend',   onRelease, { passive: true });
  messagesEl.addEventListener('mouseup',    onRelease);
  // 滚轮事件（桌面端）→ 立即暂停，检查位置
  messagesEl.addEventListener('wheel', () => {
    _scrollPaused = true;
    if (_scrollRafId) { cancelAnimationFrame(_scrollRafId); _scrollRafId = null; }
    setTimeout(() => {
      var atBottom = messagesEl.scrollHeight - messagesEl.scrollTop - messagesEl.clientHeight < 40;
      if (atBottom) _scrollPaused = false;
    }, 50);
  }, { passive: true });
}

/** 重置滚动状态（新消息开始流式输出时） */
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
let _asrState = null;  // { ws, audioCtx, source, processor, key }

async function _startASR(key) {
  if (_asrState) return;

  const state = aiPanels.get(key);
  if (!state) return;
  const { input, sendBtn } = state.els;

  // 请求麦克风权限
  let stream;
  try {
    stream = await navigator.mediaDevices.getUserMedia({
      audio: {
        echoCancellation: true,
        noiseSuppression: true,
        // sampleRate 在 AudioContext 上设 16000 统一重采样，不写进 getUserMedia
        // channelCount 不强制指定，避免 OverconstrainedError
      }
    });
  } catch (e) {
    let msg = '无法访问麦克风';
    if (e.name === 'NotAllowedError' || e.name === 'PermissionDeniedError') {
      msg = '麦克风权限被拒绝，请在浏览器地址栏点击🔒图标允许麦克风';
    } else if (e.name === 'NotFoundError' || e.name === 'DevicesNotFoundError') {
      msg = '未检测到麦克风设备';
    } else if (e.name === 'NotReadableError') {
      msg = '麦克风被其他应用占用，请关闭后重试';
    } else if (e.name === 'OverconstrainedError') {
      msg = '麦克风不支持当前参数，请换个设备试试';
    } else if (e.name === 'SecurityError') {
      msg = '需要 HTTPS 才能使用麦克风';
    }
    toast(msg);
    console.error('[ASR] getUserMedia error:', e.name, e.message);
    return;
  }

  // 连接 ASR WebSocket
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
        if (msg.text) {
          input.value = (_asrState ? _asrState.baseText : '') + msg.text;
          autoResizeTextarea(input);
        }
      } else if (msg.type === 'done') {
        _stopASR();
      } else if (msg.type === 'error') {
        toast('语音识别错误: ' + (msg.text || '未知'));
        _stopASR();
      }
    } catch (e) {}
  };
  ws.onerror = () => { toast('语音连接失败'); _stopASR(); };
  ws.onclose = () => { if (_asrState) _stopASR(); };

  // Web Audio API: 采集 PCM 16kHz 16-bit mono
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

  // UI：发送按钮变为录音状态
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

  if (ws.readyState === 1) {
    try { ws.send(JSON.stringify({ type: 'stop' })); } catch(e) {}
    setTimeout(() => { try { ws.close(); } catch(e) {} }, 500);
  }

  _asrState = null;

  // UI：发送按钮恢复
  if (state) {
    const { input, sendBtn } = state.els;
    sendBtn.innerHTML = _AI_SEND_ICON;
    sendBtn.classList.remove('ai-mic-active');
    input.placeholder = '追问…';
  }
}
