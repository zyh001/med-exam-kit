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
        max_age=7 * 24 * 3600,
        httponly=True,
        samesite="Strict",
        secure=secure,
    )


# ════════════════════════════════════════════════════════════════════════
# 暴力破解防护
# ════════════════════════════════════════════════════════════════════════

_WINDOW_SEC   = 600
_MAX_FAILURES = 10
_LOCKOUT_SEC  = 900

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
            _brute[ip] = {"failures": [], "locked_until": 0}
        state = _brute[ip]
        state["failures"] = [t for t in state["failures"] if now - t < _WINDOW_SEC]
        state["failures"].append(now)
        if len(state["failures"]) >= _MAX_FAILURES:
            state["locked_until"] = now + _LOCKOUT_SEC
            state["failures"] = []


def record_success(ip: str) -> None:
    with _brute_lock:
        _brute.pop(ip, None)


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
        "img-src 'self' data:; "
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

def render_pin_page(app_name: str, error: str = "", pin_len: int = 0) -> str:
    """渲染 PIN 验证页面。

    pin_len > 0 时显示固定长度限制和占位符（自动生成的 PIN），
    pin_len == 0 时不限长度（自定义 PIN）。
    """
    error_html = f'<div class="error">{error}</div>' if error else ""
    max_attr = f' maxlength="{pin_len}"' if pin_len > 0 else ""
    placeholder = "X" * pin_len if pin_len > 0 else "请输入访问码"
    hint = (
        f"请输入终端显示的 {pin_len} 位访问码<br>每次重启服务器后需重新验证"
        if pin_len > 0
        else "访问码在服务器启动时打印到终端<br>每次重启服务器后需重新验证"
    )
    return f"""<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{app_name} · 访问验证</title>
<style>
  *{{box-sizing:border-box;margin:0;padding:0}}
  body{{
    font-family:'PingFang SC','Hiragino Sans GB','Microsoft YaHei',-apple-system,sans-serif;
    background:#0d1117;color:#e6edf3;
    display:flex;align-items:center;justify-content:center;
    min-height:100vh;padding:20px;
  }}
  .card{{
    background:#161b22;border:1px solid #30363d;
    border-radius:16px;padding:40px 36px;width:100%;max-width:380px;
    box-shadow:0 8px 32px rgba(0,0,0,.5);
  }}
  .icon{{
    width:52px;height:52px;border-radius:14px;
    background:linear-gradient(135deg,#4493f8,#7c5ef8);
    display:flex;align-items:center;justify-content:center;
    font-size:24px;margin-bottom:20px;
  }}
  h1{{font-size:20px;font-weight:600;margin-bottom:6px;color:#e6edf3}}
  p{{font-size:13px;color:#7d8590;margin-bottom:28px;line-height:1.6}}
  label{{font-size:12px;color:#7d8590;display:block;margin-bottom:8px;letter-spacing:.04em}}
  input{{
    width:100%;padding:12px 16px;border-radius:10px;
    background:#0d1117;border:1.5px solid #30363d;
    color:#e6edf3;font-size:18px;letter-spacing:.18em;font-weight:600;
    text-align:center;text-transform:uppercase;transition:border-color .2s;
    outline:none;
  }}
  input:focus{{border-color:#4493f8}}
  input::placeholder{{color:#484f58;letter-spacing:.04em;font-size:14px;font-weight:400}}
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
  .hint{{font-size:11px;color:#484f58;text-align:center;margin-top:16px;line-height:1.5}}
</style>
</head>
<body>
<div class="card">
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
    <button class="btn" type="submit">验证并进入 →</button>
  </form>
  <p class="hint">{hint}</p>
</div>
<script>
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
