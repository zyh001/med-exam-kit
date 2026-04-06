// Package auth implements access-code verification, brute-force protection,
// and security headers — identical in behaviour to the Python auth.py.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	charset  = "ABCDEFGHJKMNPQRTUVWXY346789"
	codeLen  = 8
	authCook = "med_exam_auth"
)

// ── 递进封锁阈值 ──────────────────────────────────────────────────────────
// 每个阶段的（累计失败次数, 封锁时长）
var lockStages = []struct {
	after    int
	duration time.Duration
}{
	{5, 5 * time.Minute},
	{10, 1 * time.Hour},
	{20, 24 * time.Hour},
}

const windowDur = 10 * time.Minute // 失败计数滑动窗口

// ── 图形验证码 ────────────────────────────────────────────────────────────
const captchaThreshold = 3 // 累计失败几次后开始要求验证码

type captchaEntry struct {
	answer  int
	expires time.Time
}

// ── IP 状态 ───────────────────────────────────────────────────────────────

type ipState struct {
	failures    []time.Time // 滑动窗口内的失败时间
	totalFails  int         // 累计总失败次数（不清零，用于递进惩罚）
	lockedUntil time.Time
}

var (
	mu      sync.Mutex
	brute   = map[string]*ipState{}
	captchas = map[string]*captchaEntry{} // token → entry
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
			"connect-src 'self'; worker-src 'self'; frame-ancestors 'none';")
	h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
}

// ── 暴力破解防护 ─────────────────────────────────────────────────────────

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
	// 清理过期失败记录
	cutoff := now.Add(-windowDur)
	fresh := s.failures[:0]
	for _, t := range s.failures {
		if t.After(cutoff) {
			fresh = append(fresh, t)
		}
	}
	s.failures = fresh
	return true, 0
}

// RecordFailure records a failed login attempt and applies progressive lockout.
func RecordFailure(ip string) {
	mu.Lock()
	defer mu.Unlock()
	now := time.Now()
	s, ok := brute[ip]
	if !ok {
		s = &ipState{}
		brute[ip] = s
	}
	cutoff := now.Add(-windowDur)
	fresh := s.failures[:0]
	for _, t := range s.failures {
		if t.After(cutoff) {
			fresh = append(fresh, t)
		}
	}
	s.failures = append(fresh, now)
	s.totalFails++

	// 递进封锁：从最严阶段往前找
	for i := len(lockStages) - 1; i >= 0; i-- {
		if s.totalFails >= lockStages[i].after {
			s.lockedUntil = now.Add(lockStages[i].duration)
			s.failures = nil
			break
		}
	}
}

// RecordSuccess clears brute-force state for an IP.
func RecordSuccess(ip string) {
	mu.Lock()
	delete(brute, ip)
	mu.Unlock()
}

// NeedsCaptcha returns true if the IP has enough failures to require a captcha.
func NeedsCaptcha(ip string) bool {
	mu.Lock()
	defer mu.Unlock()
	s, ok := brute[ip]
	if !ok {
		return false
	}
	return s.totalFails >= captchaThreshold
}

// ── 图形验证码（SVG 数学题，纯标准库，无第三方依赖） ──────────────────────

func randInt(n int) int {
	v, _ := rand.Int(rand.Reader, big.NewInt(int64(n)))
	return int(v.Int64())
}

// NewCaptcha generates a math captcha, stores the answer, and returns
// (token, svgHTML). Token must be submitted with the form.
// 题型随机从四种运算中选取，均可被整数快速心算：
//   加法：个位数 + 个位数
//   减法：较大数 − 较小数（结果 ≥ 0）
//   乘法：2-9 × 2-5（积 ≤ 45，便于心算）
//   除法：先选商和除数，再算被除数（保证整除）
func NewCaptcha() (token string, svgHTML string) {
	var question string
	var answer int
	switch randInt(4) {
	case 0: // 加法
		a, b := randInt(9)+1, randInt(9)+1
		question = fmt.Sprintf("%d + %d = ?", a, b)
		answer = a + b
	case 1: // 减法（结果 ≥ 0）
		a, b := randInt(9)+1, randInt(9)+1
		if a < b {
			a, b = b, a
		}
		question = fmt.Sprintf("%d − %d = ?", a, b)
		answer = a - b
	case 2: // 乘法（2-9 × 2-5，积 ≤ 45）
		a := randInt(8) + 2 // 2-9
		b := randInt(4) + 2 // 2-5
		question = fmt.Sprintf("%d × %d = ?", a, b)
		answer = a * b
	default: // 除法（先定商和除数，保证整除）
		divisor  := randInt(4) + 2          // 除数 2-5
		quotient := randInt(7) + 2          // 商 2-8
		dividend := divisor * quotient
		question = fmt.Sprintf("%d ÷ %d = ?", dividend, divisor)
		answer = quotient
	}

	// 随机 token
	tokBytes := make([]byte, 12)
	rand.Read(tokBytes)
	token = hex.EncodeToString(tokBytes)

	mu.Lock()
	captchas[token] = &captchaEntry{
		answer:  answer,
		expires: time.Now().Add(5 * time.Minute),
	}
	// 顺手清理过期验证码
	for k, v := range captchas {
		if time.Now().After(v.expires) {
			delete(captchas, k)
		}
	}
	mu.Unlock()

	// 生成 SVG（带随机干扰线和抖动字符）
	svgHTML = renderCaptchaSVG(question)
	return token, svgHTML
}

// VerifyCaptcha checks the submitted answer. Returns true if correct.
// The token is consumed (one-time use) regardless of result.
func VerifyCaptcha(token, answer string) bool {
	mu.Lock()
	defer mu.Unlock()
	entry, ok := captchas[token]
	delete(captchas, token) // 单次有效
	if !ok || time.Now().After(entry.expires) {
		return false
	}
	// 解析答案
	var got int
	_, err := fmt.Sscanf(strings.TrimSpace(answer), "%d", &got)
	return err == nil && got == entry.answer
}

func renderCaptchaSVG(question string) string {
	w, h := 200, 60
	// 随机干扰线
	lines := ""
	for i := 0; i < 4; i++ {
		x1, y1 := randInt(w), randInt(h)
		x2, y2 := randInt(w), randInt(h)
		r, g, b := randInt(180)+40, randInt(180)+40, randInt(180)+40
		lines += fmt.Sprintf(
			`<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="rgb(%d,%d,%d)" stroke-width="1.2" opacity="0.5"/>`,
			x1, y1, x2, y2, r, g, b)
	}
	// 字符逐个渲染，带随机 Y 抖动
	chars := ""
	xPos := 18
	for _, ch := range question {
		dy := randInt(8) - 4
		rot := randInt(20) - 10
		chars += fmt.Sprintf(
			`<text x="%d" y="%d" transform="rotate(%d,%d,%d)" font-size="22" font-weight="700" fill="#4493f8" font-family="monospace">%s</text>`,
			xPos, 38+dy, rot, xPos, 38+dy, string(ch))
		xPos += 16
	}
	return fmt.Sprintf(
		`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" style="background:#0d1117;border-radius:8px">%s%s</svg>`,
		w, h, lines, chars)
}

// ── 登录页渲染 ────────────────────────────────────────────────────────────

// RenderPINPage returns the HTML login page.
// pinLen hints the expected PIN length (0 = unknown/custom).
// captchaToken/captchaSVG are empty when captcha is not required.
func RenderPINPage(appName, errMsg string, pinLen int, captchaToken, captchaSVG string) string {
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

	captchaHTML := ""
	if captchaToken != "" {
		captchaHTML = fmt.Sprintf(`
  <div class="captcha-block">
    <label>请完成验证</label>
    <div class="captcha-img">%s</div>
    <input type="text" name="captcha_answer" id="captcha_answer"
      placeholder="输入计算结果" autocomplete="off" inputmode="numeric"
      style="margin-top:10px;letter-spacing:.1em;font-size:16px">
    <input type="hidden" name="captcha_token" value="%s">
  </div>`, captchaSVG, captchaToken)
	}

	const tpl = `<!DOCTYPE html>
<html lang="zh-CN" data-theme="dark"><head><meta charset="UTF-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{APP}} · 访问验证</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
:root,[data-theme="dark"]{
  --bg:#0d1117;--surface:#161b22;--border:#30363d;
  --text:#e6edf3;--muted:#7d8590;--muted2:#484f58;
  --input-bg:#0d1117;--shadow:0 8px 32px rgba(0,0,0,.5);
}
[data-theme="light"]{
  --bg:#f0f4f8;--surface:#ffffff;--border:#d0d7de;
  --text:#1f2328;--muted:#656d76;--muted2:#8c959f;
  --input-bg:#f6f8fa;--shadow:0 4px 16px rgba(0,0,0,.12);
}
body{font-family:'PingFang SC','Hiragino Sans GB','Microsoft YaHei',-apple-system,sans-serif;
  background:var(--bg);color:var(--text);display:flex;align-items:center;
  justify-content:center;min-height:100vh;padding:20px;transition:background .25s,color .25s}
.card{background:var(--surface);border:1px solid var(--border);border-radius:16px;
  padding:40px 36px;width:100%;max-width:380px;box-shadow:var(--shadow);position:relative;
  transition:background .25s,border-color .25s}
.theme-btn{position:absolute;top:16px;right:16px;background:none;border:none;
  cursor:pointer;font-size:18px;opacity:.6;padding:4px;border-radius:6px;
  line-height:1;transition:opacity .2s}
.theme-btn:hover{opacity:1}
.icon{width:52px;height:52px;border-radius:14px;
  background:linear-gradient(135deg,#4493f8,#7c5ef8);
  display:flex;align-items:center;justify-content:center;font-size:24px;margin-bottom:20px}
h1{font-size:20px;font-weight:600;margin-bottom:6px;color:var(--text)}
p{font-size:13px;color:var(--muted);margin-bottom:28px;line-height:1.6}
label{font-size:12px;color:var(--muted);display:block;margin-bottom:8px;letter-spacing:.04em}
input[type=text]{width:100%;padding:12px 16px;border-radius:10px;background:var(--input-bg);
  border:1.5px solid var(--border);color:var(--text);font-size:18px;
  letter-spacing:.18em;font-weight:600;text-align:center;text-transform:uppercase;
  transition:border-color .2s,background .25s;outline:none}
input[type=text]:focus{border-color:#4493f8}
input[type=text]::placeholder{color:var(--muted2);letter-spacing:.04em;font-size:14px;font-weight:400}
.btn{width:100%;padding:13px;border-radius:10px;border:none;
  background:linear-gradient(135deg,#4493f8,#7c5ef8);color:#fff;
  font-size:15px;font-weight:600;cursor:pointer;margin-top:16px;transition:opacity .2s}
.btn:hover{opacity:.88}
.btn:active{opacity:.75}
.error{background:rgba(248,81,73,.12);border:1px solid rgba(248,81,73,.3);
  color:#f85149;border-radius:8px;padding:10px 14px;font-size:13px;margin-bottom:16px}
.hint{font-size:11px;color:var(--muted2);text-align:center;margin-top:16px;line-height:1.5}
.captcha-block{margin-top:16px;padding-top:16px;border-top:1px solid var(--border)}
.captcha-block label{margin-bottom:10px}
.captcha-img{display:flex;justify-content:center}
.captcha-img svg{max-width:100%;border-radius:8px}
</style></head><body>
<div class="card">
  <button class="theme-btn" onclick="toggleTheme()" title="切换主题">🌓</button>
  <div class="icon">🔑</div>
  <h1>{{APP}}</h1>
  <p>服务已启动，请输入访问码继续。</p>
  {{ERROR}}
  <form method="POST" action="/auth" autocomplete="off">
    <label>访问码</label>
    <input type="text" name="code" id="code"{{MAXATTR}}
      placeholder="{{PLACEHOLDER}}" autofocus autocomplete="off" spellcheck="false">
    {{CAPTCHA}}
    <button class="btn" type="submit">验证并进入 →</button>
  </form>
  <p class="hint">{{HINT}}</p>
</div>
<script>
function toggleTheme(){
  var next=document.documentElement.getAttribute('data-theme')==='dark'?'light':'dark';
  document.documentElement.setAttribute('data-theme',next);
  localStorage.setItem('quiz-theme',next);
}
(function(){
  var t=localStorage.getItem('quiz-theme')||'dark';
  document.documentElement.setAttribute('data-theme',t);
})();
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
		"{{CAPTCHA}}", captchaHTML,
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
