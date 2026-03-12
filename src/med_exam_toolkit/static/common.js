/* ================================================================
   common.js  医考工具公共 JS
   依赖：_base.html 内联的 window.SESSION_TOKEN（Jinja2 变量）
   ================================================================ */

/** 统一封装的 fetch，自动添加 X-Session-Token 请求头 */
function apiFetch(url, opts = {}) {
  opts.headers = Object.assign({}, opts.headers, {
    "X-Session-Token": window.SESSION_TOKEN,
  });
  return fetch(url, opts);
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
