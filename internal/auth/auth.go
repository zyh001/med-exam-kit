// Package auth implements access-code verification, brute-force protection,
// and security headers — identical in behaviour to the Python auth.py.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	charset  = "ABCDEFGHJKMNPQRTUVWXY346789"
	codeLen  = 8
	authCook = "med_exam_auth"

	windowSec   = 600
	maxFailures = 10
	lockoutSec  = 900
)

// GenerateAccessCode creates a random 8-char code and a signing secret.
func GenerateAccessCode() (code, secret string) {
	buf := make([]byte, codeLen)
	src := []byte(charset)
	rand.Read(buf)
	for i := range buf {
		buf[i] = src[int(buf[i])%len(src)]
	}
	secBytes := make([]byte, 32)
	rand.Read(secBytes)
	return string(buf), hex.EncodeToString(secBytes)
}

func sign(secret, code string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(code))
	return fmt.Sprintf("%x", mac.Sum(nil))
}

// IsAuthenticated returns true when the request carries a valid auth cookie.
func IsAuthenticated(r *http.Request, secret, accessCode string) bool {
	if accessCode == "" {
		return true
	}
	cookie, err := r.Cookie(authCook)
	if err != nil {
		return false
	}
	expected := sign(secret, accessCode)
	return hmac.Equal([]byte(cookie.Value), []byte(expected))
}

// SetAuthCookie writes the HMAC-signed auth cookie to the response.
func SetAuthCookie(w http.ResponseWriter, r *http.Request, secret, accessCode string) {
	proto := r.Header.Get("X-Forwarded-Proto")
	var secure bool
	if proto != "" {
		secure = strings.EqualFold(proto, "https")
	} else {
		secure = r.TLS != nil
	}
	http.SetCookie(w, &http.Cookie{
		Name:     authCook,
		Value:    sign(secret, accessCode),
		MaxAge:   7 * 24 * 3600,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   secure,
		Path:     "/",
	})
}

// ApplySecurityHeaders sets common security response headers.
func ApplySecurityHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("X-Frame-Options", "DENY")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
	h.Set("Content-Security-Policy",
		"default-src 'self'; script-src 'self' 'unsafe-inline'; "+
			"style-src 'self' 'unsafe-inline'; img-src 'self' data:; "+
			"connect-src 'self'; frame-ancestors 'none';")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
}

// ── Brute-force protection ─────────────────────────────────────────────

type ipState struct {
	failures    []time.Time
	lockedUntil time.Time
}

var (
	mu    sync.Mutex
	brute = map[string]*ipState{}
)

// CheckBruteForce returns (allowed, retryAfterSeconds).
func CheckBruteForce(ip string) (bool, int) {
	mu.Lock()
	defer mu.Unlock()
	now := time.Now()
	s, ok := brute[ip]
	if !ok {
		return true, 0
	}
	if now.Before(s.lockedUntil) {
		return false, int(s.lockedUntil.Sub(now).Seconds())
	}
	cutoff := now.Add(-windowSec * time.Second)
	fresh := s.failures[:0]
	for _, t := range s.failures {
		if t.After(cutoff) {
			fresh = append(fresh, t)
		}
	}
	s.failures = fresh
	return true, 0
}

// RecordFailure records a failed login attempt.
func RecordFailure(ip string) {
	mu.Lock()
	defer mu.Unlock()
	now := time.Now()
	s, ok := brute[ip]
	if !ok {
		s = &ipState{}
		brute[ip] = s
	}
	cutoff := now.Add(-windowSec * time.Second)
	fresh := s.failures[:0]
	for _, t := range s.failures {
		if t.After(cutoff) {
			fresh = append(fresh, t)
		}
	}
	s.failures = append(fresh, now)
	if len(s.failures) >= maxFailures {
		s.lockedUntil = now.Add(lockoutSec * time.Second)
		s.failures = nil
	}
}

// RecordSuccess clears brute-force state for an IP.
func RecordSuccess(ip string) {
	mu.Lock()
	delete(brute, ip)
	mu.Unlock()
}

// RenderPINPage returns the HTML login page.
// pinLen hints the expected PIN length (0 = unknown/custom).
func RenderPINPage(appName, errMsg string, pinLen int) string {
	errHTML := ""
	if errMsg != "" {
		errHTML = `<div class="error">` + errMsg + `</div>`
	}
	maxAttr := ""
	placeholder := "请输入访问码"
	hint := "访问码在服务器启动时打印到终端<br>每次重启服务器后需重新验证"
	if pinLen > 0 {
		maxAttr = fmt.Sprintf(` maxlength="%d"`, pinLen)
		placeholder = strings.Repeat("X", pinLen)
		hint = fmt.Sprintf("请输入终端显示的 %d 位访问码<br>每次重启服务器后需重新验证", pinLen)
	}

	const tpl = `<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{APP}} · 访问验证</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:'PingFang SC','Hiragino Sans GB','Microsoft YaHei',-apple-system,sans-serif;
  background:#0d1117;color:#e6edf3;display:flex;align-items:center;
  justify-content:center;min-height:100vh;padding:20px}
.card{background:#161b22;border:1px solid #30363d;border-radius:16px;
  padding:40px 36px;width:100%;max-width:380px;box-shadow:0 8px 32px rgba(0,0,0,.5)}
.icon{width:52px;height:52px;border-radius:14px;
  background:linear-gradient(135deg,#4493f8,#7c5ef8);
  display:flex;align-items:center;justify-content:center;font-size:24px;margin-bottom:20px}
h1{font-size:20px;font-weight:600;margin-bottom:6px;color:#e6edf3}
p{font-size:13px;color:#7d8590;margin-bottom:28px;line-height:1.6}
label{font-size:12px;color:#7d8590;display:block;margin-bottom:8px;letter-spacing:.04em}
input{width:100%;padding:12px 16px;border-radius:10px;background:#0d1117;
  border:1.5px solid #30363d;color:#e6edf3;font-size:18px;
  letter-spacing:.18em;font-weight:600;text-align:center;text-transform:uppercase;
  transition:border-color .2s;outline:none}
input:focus{border-color:#4493f8}
input::placeholder{color:#484f58;letter-spacing:.04em;font-size:14px;font-weight:400}
.btn{width:100%;padding:13px;border-radius:10px;border:none;
  background:linear-gradient(135deg,#4493f8,#7c5ef8);color:#fff;
  font-size:15px;font-weight:600;cursor:pointer;margin-top:16px;transition:opacity .2s}
.btn:hover{opacity:.88}
.btn:active{opacity:.75}
.error{background:rgba(248,81,73,.12);border:1px solid rgba(248,81,73,.3);
  color:#f85149;border-radius:8px;padding:10px 14px;font-size:13px;margin-bottom:16px}
.hint{font-size:11px;color:#484f58;text-align:center;margin-top:16px;line-height:1.5}
</style></head><body>
<div class="card">
  <div class="icon">🔑</div>
  <h1>{{APP}}</h1>
  <p>服务已启动，请输入访问码继续。</p>
  {{ERROR}}
  <form method="POST" action="/auth" autocomplete="off">
    <label>访问码</label>
    <input type="text" name="code" id="code"{{MAXATTR}}
      placeholder="{{PLACEHOLDER}}" autofocus autocomplete="off" spellcheck="false">
    <button class="btn" type="submit">验证并进入 →</button>
  </form>
  <p class="hint">{{HINT}}</p>
</div>
<script>
document.getElementById('code').addEventListener('input',function(){
  this.value=this.value.toUpperCase().replace(/[^A-Z0-9]/g,'');
});
</script></body></html>`

	r := strings.NewReplacer(
		"{{APP}}", appName,
		"{{ERROR}}", errHTML,
		"{{MAXATTR}}", maxAttr,
		"{{PLACEHOLDER}}", placeholder,
		"{{HINT}}", hint,
	)
	return r.Replace(tpl)
}

// PrintPublicWarning prints a security warning when listening on non-localhost.
func PrintPublicWarning(port int) {
	fmt.Printf("\n⚠️  安全警告：服务监听在所有网卡\n")
	fmt.Printf("   若有公网 IP，建议搭配 HTTPS 反向代理\n")
	fmt.Printf("   示例 Caddy 配置：\n")
	fmt.Printf("     yourdomain.com { reverse_proxy 127.0.0.1:%d }\n\n", port)
}
