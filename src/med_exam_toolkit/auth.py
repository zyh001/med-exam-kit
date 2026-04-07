"""访问码验证模块（quiz 和 editor 共用）

流程：
  1. 服务器启动时调用 generate_access_code() 生成 8 位码，打印到终端
  2. 用户首次访问 GET / → 未通过验证 → 返回 PIN 输入页
  3. 用户提交 POST /auth → 验证访问码 → 写入 HMAC 签名 Cookie → 重定向 GET /
  4. 后续所有请求在 _guard 中先检查 Cookie，通过后才进入正常逻辑

当 access_code 为空字符串时，auth 功能完全关闭（--no-auth 模式），
is_authenticated() 始终返回 True，/auth 路由不会被注册。

Cookie 安全：
  - Cookie 值 = HMAC-SHA256(cookie_secret, access_code)，服务端签名
  - 客户端无法伪造（不知道 cookie_secret）
  - httponly + samesite=Strict，无法被 JS 读取或跨站发送
  - 非 localhost 时自动加 Secure 标志（适配 HTTPS 反向代理）
  - 每次服务器重启 cookie_secret 随机刷新，旧 Cookie 自动失效

暴力破解防护：
  - 每 IP 每 10 分钟最多 10 次失败尝试
  - 超限后锁定 15 分钟，期间返回 429 Too Many Requests
"""
from __future__ import annotations

import hashlib
import hmac
import secrets
import threading
import time

from flask import request

_CHARSET  = "ABCDEFGHJKMNPQRTUVWXY346789"
_CODE_LEN = 8
_AUTH_COOKIE = "med_exam_auth"


def generate_access_code() -> tuple[str, str]:
    code   = "".join(secrets.choice(_CHARSET) for _ in range(_CODE_LEN))
    secret = secrets.token_hex(32)
    return code, secret


def derive_secret(access_code: str) -> str:
    """从访问码确定性派生 cookie 签名密钥。
    同一访问码永远得到同一 secret，服务重启后 cookie 仍然有效。
    """
    import hmac as _hmac, hashlib as _hashlib
    salt = b"med-exam-kit:cookie-secret:v1"
    return _hmac.new(salt, access_code.encode(), _hashlib.sha256).hexdigest()


def _sign(cookie_secret: str, access_code: str) -> str:
    return hmac.new(
        cookie_secret.encode(),
        access_code.encode(),
        hashlib.sha256,
    ).hexdigest()


def is_authenticated(cookie_secret: str, access_code: str) -> bool:
    """access_code 为空字符串时始终返回 True（已禁用访问码）。"""
    if not access_code:
        return True
    cookie_val = request.cookies.get(_AUTH_COOKIE, "")
    if not cookie_val:
        return False
    expected = _sign(cookie_secret, access_code)
    return hmac.compare_digest(cookie_val, expected)


def set_auth_cookie(
    response,
    cookie_secret: str,
    access_code: str,
    secure: bool | None = None,
) -> None:
    """写入已验证的 Cookie（有效期 7 天）。
    secure=None 时自动判断：非 localhost → True，localhost → False。
    传入 True/False 可手动覆盖（例如 quiz.py 根据 _server_host 决定）。
    """
    if secure is None:
        forwarded_proto = request.headers.get("X-Forwarded-Proto", "")
        if forwarded_proto:
            secure = forwarded_proto.lower() == "https"
        else:
            secure = request.scheme == "https"
    response.set_cookie(
        _AUTH_COOKIE,
        _sign(cookie_secret, access_code),
        max_age=365 * 24 * 3600,
        httponly=True,
        samesite="Strict",
        secure=secure,
    )


# ════════════════════════════════════════════════════════════════════════
# 暴力破解防护
# ════════════════════════════════════════════════════════════════════════

_WINDOW_SEC = 600   # 滑动窗口（秒）
_CAPTCHA_THRESHOLD = 3   # 累计失败几次后要求验证码

# 递进封锁：(累计失败次数, 封锁秒数)
_LOCK_STAGES = [(5, 300), (10, 3600), (20, 86400)]

_brute: dict[str, dict] = {}
_brute_lock = threading.Lock()


def check_brute_force(ip: str) -> tuple[bool, int]:
    """返回 (allowed, retry_after_seconds)。"""
    now = time.monotonic()
    with _brute_lock:
        state = _brute.get(ip)
        if not state:
            return True, 0
        locked_until = state.get("locked_until", 0)
        if locked_until > now:
            return False, int(locked_until - now)
        state["failures"] = [t for t in state["failures"] if now - t < _WINDOW_SEC]
        return True, 0


def record_failure(ip: str) -> None:
    now = time.monotonic()
    with _brute_lock:
        if ip not in _brute:
            _brute[ip] = {"failures": [], "locked_until": 0, "total": 0}
        state = _brute[ip]
        state["failures"] = [t for t in state["failures"] if now - t < _WINDOW_SEC]
        state["failures"].append(now)
        state["total"] = state.get("total", 0) + 1
        # 递进封锁
        for threshold, secs in reversed(_LOCK_STAGES):
            if state["total"] >= threshold:
                state["locked_until"] = now + secs
                state["failures"] = []
                break


def record_success(ip: str) -> None:
    with _brute_lock:
        _brute.pop(ip, None)


def needs_captcha(ip: str) -> bool:
    """累计失败次数达到阈值后要求验证码。"""
    with _brute_lock:
        state = _brute.get(ip)
        return bool(state and state.get("total", 0) >= _CAPTCHA_THRESHOLD)


# ── 图形验证码（SVG 数学题） ──────────────────────────────────────────────

import secrets as _secrets
import html as _html

_captchas: dict[str, dict] = {}   # token → {answer, expires}
_captcha_lock = threading.Lock()


def new_captcha() -> tuple[str, str]:
    """生成验证码，返回 (token, svg_html)。
    四种题型随机出现：加法、减法、乘法（积≤45）、整除。
    """
    import random as _r
    op = _r.randint(0, 3)
    if op == 0:   # 加法
        a, b = _r.randint(1, 9), _r.randint(1, 9)
        question, answer = f"{a} + {b} = ?", a + b
    elif op == 1: # 减法（结果 ≥ 0）
        a, b = _r.randint(1, 9), _r.randint(1, 9)
        if a < b:
            a, b = b, a
        question, answer = f"{a} − {b} = ?", a - b
    elif op == 2: # 乘法（2-9 × 2-5，积 ≤ 45）
        a, b = _r.randint(2, 9), _r.randint(2, 5)
        question, answer = f"{a} × {b} = ?", a * b
    else:         # 除法（先定商和除数，保证整除）
        divisor  = _r.randint(2, 5)   # 除数 2-5
        quotient = _r.randint(2, 8)   # 商 2-8
        dividend = divisor * quotient
        question, answer = f"{dividend} ÷ {divisor} = ?", quotient

    token = _secrets.token_hex(12)
    expires = time.monotonic() + 300   # 5 分钟

    with _captcha_lock:
        _captchas[token] = {"answer": answer, "expires": expires}
        # 清理过期
        now = time.monotonic()
        expired = [k for k, v in _captchas.items() if v["expires"] < now]
        for k in expired:
            del _captchas[k]

    svg = _render_captcha_svg(question)
    return token, svg


def verify_captcha(token: str, answer: str, ip: str = "") -> bool:
    """校验验证码（单次有效）。
    答案错误或 token 过期均调用 record_failure(ip)，防止暴力破解验证码。
    """
    with _captcha_lock:
        entry = _captchas.pop(token, None)
    if not entry or time.monotonic() > entry["expires"]:
        if ip:
            record_failure(ip)
        return False
    try:
        ok = int(answer.strip()) == entry["answer"]
    except (ValueError, AttributeError):
        ok = False
    if not ok and ip:
        record_failure(ip)
    return ok


def _render_captcha_svg(question: str) -> str:
    import random as _r
    w, h = 200, 60
    lines = ""
    for _ in range(4):
        x1, y1 = _r.randint(0, w), _r.randint(0, h)
        x2, y2 = _r.randint(0, w), _r.randint(0, h)
        r, g, b = _r.randint(40, 220), _r.randint(40, 220), _r.randint(40, 220)
        lines += (f'<line x1="{x1}" y1="{y1}" x2="{x2}" y2="{y2}" '
                  f'stroke="rgb({r},{g},{b})" stroke-width="1.2" opacity="0.5"/>')
    chars = ""
    x = 18
    for ch in question:
        dy  = _r.randint(-4, 4)
        rot = _r.randint(-10, 10)
        esc = _html.escape(ch)
        chars += (f'<text x="{x}" y="{38+dy}" transform="rotate({rot},{x},{38+dy})" '
                  f'font-size="22" font-weight="700" fill="#4493f8" font-family="monospace">{esc}</text>')
        x += 16
    return (f'<svg xmlns="http://www.w3.org/2000/svg" width="{w}" height="{h}" '
            f'style="background:#0d1117;border-radius:8px">{lines}{chars}</svg>')


# ════════════════════════════════════════════════════════════════════════
# 安全响应头
# ════════════════════════════════════════════════════════════════════════

def apply_security_headers(response) -> None:
    """注入常用安全响应头（在 after_request 中调用）。"""
    h = response.headers
    h.setdefault("X-Frame-Options", "DENY")
    h.setdefault("X-Content-Type-Options", "nosniff")
    h.setdefault("Referrer-Policy", "strict-origin-when-cross-origin")
    h.setdefault(
        "Content-Security-Policy",
        "default-src 'self'; "
        "script-src 'self' 'unsafe-inline'; "
        "style-src 'self' 'unsafe-inline'; "
        "img-src 'self' data: https: http:; "
        "connect-src 'self'; "
        "worker-src 'self'; "
        "frame-ancestors 'none';",
    )
    h.setdefault(
        "Permissions-Policy",
        "camera=(), microphone=(), geolocation=(), payment=()",
    )


# ════════════════════════════════════════════════════════════════════════
# PIN 页面 HTML
# ════════════════════════════════════════════════════════════════════════

def render_pin_page(app_name: str, error: str = "", pin_len: int = 0,
                    captcha_token: str = "", captcha_svg: str = "") -> str:
    """渲染 PIN 验证页面，支持深色/亮色主题，与主页共享 quiz-theme 状态。"""
    error_html = f'<div class="error">{error}</div>' if error else ""
    max_attr = f' maxlength="{pin_len}"' if pin_len > 0 else ""
    placeholder = "X" * pin_len if pin_len > 0 else "请输入访问码"
    hint = (
        f"请输入终端显示的 {pin_len} 位访问码<br>每次重启服务器后需重新验证"
        if pin_len > 0
        else "访问码在服务器启动时打印到终端<br>每次重启服务器后需重新验证"
    )
    captcha_html = ""
    if captcha_token:
        captcha_html = f'''
  <div class="captcha-block">
    <label>请完成验证</label>
    <div class="captcha-img">{captcha_svg}</div>
    <input type="text" name="captcha_answer" id="captcha_answer"
      placeholder="输入计算结果" autocomplete="off" inputmode="numeric"
      style="margin-top:10px;letter-spacing:.1em;font-size:16px">
    <input type="hidden" name="captcha_token" value="{captcha_token}">
  </div>'''

    return f"""<!DOCTYPE html>
<html lang="zh-CN" data-theme="dark">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{app_name} · 访问验证</title>
<style>
  *{{box-sizing:border-box;margin:0;padding:0}}
  :root,[data-theme="dark"]{{
    --bg:#0d1117;--surface:#161b22;--border:#30363d;
    --text:#e6edf3;--muted:#7d8590;--muted2:#484f58;
    --input-bg:#0d1117;--shadow:0 8px 32px rgba(0,0,0,.5);
  }}
  [data-theme="light"]{{
    --bg:#f0f4f8;--surface:#ffffff;--border:#d0d7de;
    --text:#1f2328;--muted:#656d76;--muted2:#8c959f;
    --input-bg:#f6f8fa;--shadow:0 4px 16px rgba(0,0,0,.12);
  }}
  body{{
    font-family:'PingFang SC','Hiragino Sans GB','Microsoft YaHei',-apple-system,sans-serif;
    background:var(--bg);color:var(--text);
    display:flex;align-items:center;justify-content:center;
    min-height:100vh;padding:20px;transition:background .25s,color .25s;
  }}
  .card{{
    background:var(--surface);border:1px solid var(--border);
    border-radius:16px;padding:40px 36px;width:100%;max-width:380px;
    box-shadow:var(--shadow);position:relative;
    transition:background .25s,border-color .25s;
  }}
  .theme-btn{{
    position:absolute;top:16px;right:16px;background:none;border:none;
    cursor:pointer;font-size:18px;opacity:.6;padding:4px;border-radius:6px;
    line-height:1;transition:opacity .2s;
  }}
  .theme-btn:hover{{opacity:1}}
  .icon{{
    width:52px;height:52px;border-radius:14px;
    background:linear-gradient(135deg,#4493f8,#7c5ef8);
    display:flex;align-items:center;justify-content:center;
    font-size:24px;margin-bottom:20px;
  }}
  h1{{font-size:20px;font-weight:600;margin-bottom:6px;color:var(--text)}}
  p{{font-size:13px;color:var(--muted);margin-bottom:28px;line-height:1.6}}
  label{{font-size:12px;color:var(--muted);display:block;margin-bottom:8px;letter-spacing:.04em}}
  input{{
    width:100%;padding:12px 16px;border-radius:10px;
    background:var(--input-bg);border:1.5px solid var(--border);
    color:var(--text);font-size:18px;letter-spacing:.18em;font-weight:600;
    text-align:center;text-transform:uppercase;
    transition:border-color .2s,background .25s;outline:none;
  }}
  input:focus{{border-color:#4493f8}}
  input::placeholder{{color:var(--muted2);letter-spacing:.04em;font-size:14px;font-weight:400}}
  .btn{{
    width:100%;padding:13px;border-radius:10px;border:none;
    background:linear-gradient(135deg,#4493f8,#7c5ef8);
    color:#fff;font-size:15px;font-weight:600;cursor:pointer;
    margin-top:16px;transition:opacity .2s;
  }}
  .btn:hover{{opacity:.88}}
  .btn:active{{opacity:.75}}
  .error{{
    background:rgba(248,81,73,.12);border:1px solid rgba(248,81,73,.3);
    color:#f85149;border-radius:8px;padding:10px 14px;
    font-size:13px;margin-bottom:16px;
  }}
  .hint{{font-size:11px;color:var(--muted2);text-align:center;margin-top:16px;line-height:1.5}}
  .captcha-block{{margin-top:16px;padding-top:16px;border-top:1px solid var(--border)}}
  .captcha-block label{{margin-bottom:10px;display:block}}
  .captcha-img{{display:flex;justify-content:center;margin-bottom:4px}}
  .captcha-img svg{{max-width:100%;border-radius:8px}}
</style>
</head>
<body>
<div class="card">
  <button class="theme-btn" onclick="toggleTheme()" title="切换主题">🌓</button>
  <div class="icon">🔑</div>
  <h1>{app_name}</h1>
  <p>服务已启动，请输入访问码继续。</p>
  {error_html}
  <form method="POST" action="/auth" autocomplete="off">
    <label>访问码</label>
    <input
      type="text" name="code" id="code"{max_attr}
      placeholder="{placeholder}"
      autofocus autocomplete="off" spellcheck="false"
    >
    {captcha_html}
    <button class="btn" type="submit">验证并进入 →</button>
  </form>
  <p class="hint">{hint}</p>
</div>
<script>
  function toggleTheme(){{
    var next=document.documentElement.getAttribute('data-theme')==='dark'?'light':'dark';
    document.documentElement.setAttribute('data-theme',next);
    localStorage.setItem('quiz-theme',next);
  }}
  (function(){{
    var t=localStorage.getItem('quiz-theme')||'dark';
    document.documentElement.setAttribute('data-theme',t);
  }})();
  document.getElementById('code').addEventListener('input', function() {{
    this.value = this.value.toUpperCase().replace(/[^A-Z0-9]/g, '');
  }});
</script>
</body>
</html>"""


# ════════════════════════════════════════════════════════════════════════
# 公网安全警告
# ════════════════════════════════════════════════════════════════════════

def print_public_internet_warning(port: int) -> None:
    print(f"\n{'⚠️  ' * 3}  安全警告  {'⚠️  ' * 3}")
    print("━" * 48)
    print("  你正在将服务监听在 0.0.0.0（所有网卡），")
    print("  若服务器有公网 IP，此服务将对外网完全开放。")
    print()
    print("  已知安全风险：")
    print("  • HTTP 明文传输，访问码/Cookie 可被中间人截获")
    print("  • 即使有访问码，仍建议搭配 HTTPS 反向代理")
    print()
    print("  推荐配置（以 Caddy 为例）：")
    print(f"    yourdomain.com {{")
    print(f"      reverse_proxy 127.0.0.1:{port}")
    print(f"    }}")
    print()
    print("  配置反向代理后请改为 --host 127.0.0.1")
    print("━" * 48 + "\n")
