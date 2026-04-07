/* ================================================================
   common.js  医考工具公共 JS
   依赖：_base.html 内联的 window.SESSION_TOKEN（Jinja2 变量）
   ================================================================ */

/** 统一封装的 fetch，自动添加 X-Session-Token 请求头 */
async function apiFetch(url, opts = {}) {
  opts.headers = Object.assign({}, opts.headers, {
    "X-Session-Token": window.SESSION_TOKEN,
  });
  const res = await fetch(url, opts);
  if (res.status === 401 && typeof window._onAuthExpired === 'function') {
    window._onAuthExpired();
  }
  return res;
}

/** 服务端重启/Token 失效后的统一处理（可被 quiz.js 覆盖） */
window._onAuthExpired = (function () {
  let _shown = false;
  return function () {
    if (_shown) return;
    _shown = true;
    // 显示持久横幅（页面内尚未注册 quiz.js 钩子时的兜底）
    _showAuthExpiredBanner();
  };
})();

function _showAuthExpiredBanner(saveMsg) {
  // 防止重复插入
  if (document.getElementById('auth-expired-banner')) return;
  const bar = document.createElement('div');
  bar.id = 'auth-expired-banner';
  bar.innerHTML =
    '<span>⚠️ 服务器已重启，会话已失效' + (saveMsg ? '。' + saveMsg : '') + '</span>' +
    '<button onclick="location.reload()">立即刷新</button>';
  Object.assign(bar.style, {
    position:   'fixed',
    bottom:     '0',
    left:       '0',
    right:      '0',
    zIndex:     '99999',
    display:    'flex',
    alignItems: 'center',
    justifyContent: 'center',
    gap:        '12px',
    padding:    '14px 20px',
    background: 'var(--danger, #cf222e)',
    color:      '#fff',
    fontSize:   '14px',
    fontWeight: '600',
    boxShadow:  '0 -2px 12px rgba(0,0,0,.3)',
    animation:  'fadeIn .25s ease',
  });
  bar.querySelector('button').style.cssText =
    'padding:6px 16px;border-radius:8px;border:2px solid #fff;' +
    'background:transparent;color:#fff;font-weight:700;cursor:pointer;white-space:nowrap;';
  document.body.appendChild(bar);
}

/** Toast 通知（err=true 时显示红色错误样式）*/
let toastTimer;
function toast(msg, err = false) {
  const t = document.getElementById('toast');
  if (!t) return;
  t.textContent = msg;
  t.className = 'show' + (err ? ' err' : '');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { t.className = ''; }, 2800);
}
