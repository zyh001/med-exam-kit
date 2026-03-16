"""访问码验证模块（quiz 和 editor 共用）

流程：
  1. 服务器启动时调用 generate_access_code() 生成 8 位码，打印到终端
  2. 用户首次访问 GET / → 未通过验证 → 返回 PIN 输入页
  3. 用户提交 POST /auth → 验证访问码 → 写入 HMAC 签名 Cookie → 重定向 GET /
  4. 后续所有请求在 _guard 中先检查 Cookie，通过后才进入正常逻辑

Cookie 安全：
  - Cookie 值 = HMAC-SHA256(cookie_secret, access_code)，服务端签名
  - 客户端无法伪造（不知道 cookie_secret）
  - httponly + samesite=Strict，无法被 JS 读取或跨站发送
  - 每次服务器重启 cookie_secret 随机刷新，旧 Cookie 自动失效
"""
from __future__ import annotations

import hmac
import hashlib
import secrets
import string
from flask import request, make_response, redirect, url_for

# ── 访问码字符集：去掉易混淆字符（0/O、1/I、l/L、S/5、Z/2）──────────
_CHARSET = "ABCDEFGHJKMNPQRTUVWXY346789"   # 26 个，输入友好
_CODE_LEN = 8

_AUTH_COOKIE = "med_exam_auth"


def generate_access_code() -> tuple[str, str]:
    """生成访问码和 Cookie 签名密钥，返回 (access_code, cookie_secret)。"""
    code   = "".join(secrets.choice(_CHARSET) for _ in range(_CODE_LEN))
    secret = secrets.token_hex(32)
    return code, secret


def _sign(cookie_secret: str, access_code: str) -> str:
    """用 cookie_secret 对 access_code HMAC 签名，得到 Cookie 值。"""
    return hmac.new(
        cookie_secret.encode(),
        access_code.encode(),
        hashlib.sha256,
    ).hexdigest()


def is_authenticated(cookie_secret: str, access_code: str) -> bool:
    """检查当前请求是否持有有效的访问 Cookie。"""
    cookie_val = request.cookies.get(_AUTH_COOKIE, "")
    if not cookie_val:
        return False
    expected = _sign(cookie_secret, access_code)
    return hmac.compare_digest(cookie_val, expected)


def set_auth_cookie(response, cookie_secret: str, access_code: str) -> None:
    """在 response 上写入已验证的 Cookie（有效期 7 天）。"""
    response.set_cookie(
        _AUTH_COOKIE,
        _sign(cookie_secret, access_code),
        max_age=7 * 24 * 3600,
        httponly=True,
        samesite="Strict",
    )


def render_pin_page(app_name: str, error: str = "") -> str:
    """返回 PIN 输入页的 HTML（纯内联，不依赖任何静态文件）。"""
    error_html = (
        f'<div class="error">{error}</div>' if error else ""
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
    font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;
    background:#0f1117;color:#e8e6e1;
    display:flex;align-items:center;justify-content:center;
    min-height:100vh;padding:20px;
  }}
  .card{{
    background:#1a1d27;border:1px solid rgba(255,255,255,.08);
    border-radius:16px;padding:40px 36px;width:100%;max-width:380px;
    box-shadow:0 20px 60px rgba(0,0,0,.4);
  }}
  .icon{{
    width:52px;height:52px;border-radius:14px;
    background:linear-gradient(135deg,#4493f8,#7c5ef8);
    display:flex;align-items:center;justify-content:center;
    font-size:24px;margin-bottom:20px;
  }}
  h1{{font-size:20px;font-weight:600;margin-bottom:6px;color:#f0ede8}}
  p{{font-size:13px;color:#888;margin-bottom:28px;line-height:1.6}}
  label{{font-size:12px;color:#888;display:block;margin-bottom:8px;letter-spacing:.04em}}
  input{{
    width:100%;padding:12px 16px;border-radius:10px;
    background:#0f1117;border:1.5px solid rgba(255,255,255,.1);
    color:#f0ede8;font-size:18px;letter-spacing:.18em;font-weight:600;
    text-align:center;text-transform:uppercase;transition:border-color .2s;
    outline:none;
  }}
  input:focus{{border-color:#4493f8}}
  input::placeholder{{color:#444;letter-spacing:.04em;font-size:14px;font-weight:400}}
  .btn{{
    width:100%;padding:13px;border-radius:10px;border:none;
    background:linear-gradient(135deg,#4493f8,#7c5ef8);
    color:#fff;font-size:15px;font-weight:600;cursor:pointer;
    margin-top:16px;transition:opacity .2s;
  }}
  .btn:hover{{opacity:.88}}
  .btn:active{{opacity:.75}}
  .error{{
    background:rgba(220,53,69,.12);border:1px solid rgba(220,53,69,.3);
    color:#f87171;border-radius:8px;padding:10px 14px;
    font-size:13px;margin-bottom:16px;
  }}
  .hint{{font-size:11px;color:#555;text-align:center;margin-top:16px;line-height:1.5}}
</style>
</head>
<body>
<div class="card">
  <div class="icon">🔑</div>
  <h1>{app_name}</h1>
  <p>服务已启动，请输入终端显示的 8 位访问码继续。</p>
  {error_html}
  <form method="POST" action="/auth" autocomplete="off">
    <label>访问码</label>
    <input
      type="text" name="code" id="code"
      maxlength="8" placeholder="XXXX XXXX"
      autofocus autocomplete="off" spellcheck="false"
    >
    <button class="btn" type="submit">验证并进入 →</button>
  </form>
  <p class="hint">访问码在服务器启动时打印到终端<br>每次重启服务器后需重新验证</p>
</div>
<script>
  // 自动大写、忽略空格
  document.getElementById('code').addEventListener('input', function() {{
    this.value = this.value.toUpperCase().replace(/[^A-Z0-9]/g, '');
  }});
</script>
</body>
</html>"""