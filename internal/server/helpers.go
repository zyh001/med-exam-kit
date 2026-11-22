package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/zyh001/med-exam-kit/internal/logger"
	"github.com/zyh001/med-exam-kit/internal/progress"
)

func greedyFill(pool []group, target int) []group {
	return greedyFillRNG(pool, target, nil)
}

// greedyFillRNG is like greedyFill but randomises tie-breaks when an RNG is
// supplied, so the tail of the selection is not deterministic.

func greedyFillRNG(pool []group, target int, rng *lcgRNG) []group {
	var picked []group
	n := 0
	var remaining []group
	for _, grp := range pool {
		c := len(grp)
		if n+c <= target {
			picked = append(picked, grp)
			n += c
			if n == target {
				return picked
			}
		} else {
			remaining = append(remaining, grp)
		}
	}
	// Second pass: pick a group whose size is the largest that still fits.
	// Multiple groups may tie on size — collect them all and pick one at
	// random, otherwise the same group is always chosen and the tail of the
	// paper becomes deterministic.
	for n < target && len(remaining) > 0 {
		gap := target - n
		bestC := 0
		var bestIdxs []int
		for i, grp := range remaining {
			c := len(grp)
			if c > gap {
				continue
			}
			if c > bestC {
				bestC = c
				bestIdxs = bestIdxs[:0]
				bestIdxs = append(bestIdxs, i)
			} else if c == bestC {
				bestIdxs = append(bestIdxs, i)
			}
		}
		if len(bestIdxs) == 0 {
			break
		}
		chosen := bestIdxs[0]
		if rng != nil && len(bestIdxs) > 1 {
			chosen = bestIdxs[int(rng.next()%uint64(len(bestIdxs)))]
		}
		picked = append(picked, remaining[chosen])
		n += bestC
		remaining = append(remaining[:chosen], remaining[chosen+1:]...)
	}
	// Third pass: truncate a group that is larger than the remaining gap.
	// Pick the smallest oversized group (least waste), breaking ties
	// randomly. When truncating, take a random window of the group instead of
	// always taking the first N sub-questions.
	if n < target && len(remaining) > 0 {
		gap := target - n
		bestSize := 0
		var bestIdxs []int
		for i, grp := range remaining {
			c := len(grp)
			if c <= gap {
				continue
			}
			if len(bestIdxs) == 0 || c < bestSize {
				bestSize = c
				bestIdxs = bestIdxs[:0]
				bestIdxs = append(bestIdxs, i)
			} else if c == bestSize {
				bestIdxs = append(bestIdxs, i)
			}
		}
		if len(bestIdxs) > 0 {
			chosen := bestIdxs[0]
			if rng != nil && len(bestIdxs) > 1 {
				chosen = bestIdxs[int(rng.next()%uint64(len(bestIdxs)))]
			}
			src := remaining[chosen]
			startMax := len(src) - gap
			start := 0
			if rng != nil && startMax > 0 {
				start = int(rng.next() % uint64(startMax+1))
			}
			truncated := make(group, gap)
			copy(truncated, src[start:start+gap])
			picked = append(picked, truncated)
		}
	}
	return picked
}

func distributeByRatio(total int, weights map[string]int) map[string]int {
	wSum := 0
	for _, w := range weights {
		wSum += w
	}
	result := map[string]int{}
	if wSum == 0 {
		for k := range weights {
			result[k] = 0
		}
		return result
	}
	allocated := 0
	keys := make([]string, 0, len(weights))
	for k := range weights {
		keys = append(keys, k)
	}
	for i, k := range keys {
		if i == len(keys)-1 {
			v := total - allocated
			if v < 0 {
				v = 0
			}
			result[k] = v
		} else {
			n := (total*weights[k] + wSum/2) / wSum
			result[k] = n
			allocated += n
		}
	}
	return result
}

// ── Rate limiter ───────────────────────────────────────────────────────

// bumpBucket 是三个限流器（通用 / img-proxy / ai-chat）共用的核心逻辑。
// 它把 map 传进来以便共享 rateMu 一把锁，避免多把锁导致的死锁风险。
// 返回 true 表示本次请求被允许（已计入 bucket），false 表示触发限流。
//
// 注意：调用方必须持有 s.rateMu。

func bumpBucket(buckets map[string][]time.Time, ip string, window time.Duration, limit int) bool {
	now := time.Now()
	cutoff := now.Add(-window)
	bucket := buckets[ip]
	fresh := bucket[:0]
	for _, t := range bucket {
		if t.After(cutoff) {
			fresh = append(fresh, t)
		}
	}
	if len(fresh) >= limit {
		buckets[ip] = fresh
		return false
	}
	buckets[ip] = append(fresh, now)
	return true
}

func (s *Server) checkRate(ip string) bool {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	return bumpBucket(s.rateBuckets, ip, rateWindow, rateLimit)
}

// checkImgProxyRate 对 /api/img/proxy 和 /api/img/local/* 做额外限流。
// 正常做题一屏 5-10 张图的峰值在窗口内完全够用；恶意批量请求被挡住。

func (s *Server) checkImgProxyRate(ip string) bool {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	return bumpBucket(s.imgProxyBuckets, ip, imgProxyRateWindow, imgProxyRateLimit)
}

// checkAIChatRate 对 /api/ai/chat 和 /api/ai/report 做额外限流。
// 每次 AI 请求都消耗 LLM token 成本，10 req/min 是个合理的上限。

func (s *Server) checkAIChatRate(ip string) bool {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	return bumpBucket(s.aiChatBuckets, ip, aiChatRateWindow, aiChatRateLimit)
}

// banIP 封禁指定 IP 一段时间（duration=0 表示永久）。

func (s *Server) banIP(ip string, duration time.Duration) {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	var until time.Time
	if duration > 0 {
		until = time.Now().Add(duration)
	}
	s.scanBans[ip] = until
}

// isBanned 检查 IP 是否在封禁名单内（过期自动移除）。

func (s *Server) isBanned(ip string) bool {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	until, ok := s.scanBans[ip]
	if !ok {
		return false
	}
	// 零值 = 永久封禁
	if until.IsZero() {
		return true
	}
	if time.Now().Before(until) {
		return true
	}
	// 过期，自动移除
	delete(s.scanBans, ip)
	return false
}

// isScannerPath 判断请求路径是否为扫描器特征路径。
// 只要命中任意一条规则，立即封禁该 IP。

func (s *Server) isScannerPath(path, method string) bool {
	// 常见扫描路径前缀
	scanPrefixes := []string{
		"/wp-", "/wordpress", "/xmlrpc", "/wp-admin", "/wp-login",
		"/phpmyadmin", "/pma", "/myadmin",
		"/.env", "/.git", "/.svn", "/.htaccess", "/.well-known/acme-challenge",
		"/admin", "/administrator",
		"/cgi-bin", "/cgi/",
		"/shell", "/cmd", "/exec",
		"/boaform", "/boa/",
		"/solr", "/actuator", "/jolokia",
		"/telescope", "/vendor/",
		"/config", "/setup",
		"/invoke.js",                 // Ivanti/Pulse 漏洞扫描
		"/dana-na/", "/dana-cached/", // Juniper VPN 扫描
		"/remote/", "/remote/fgt", // Fortinet 扫描
		"/Autodiscover", "/autodiscover",
		"/owa/", "/ecp/", // Exchange 扫描
		"/.aws",
		"/etc/passwd",
		"/proc/",
	}
	p := strings.ToLower(path)
	for _, prefix := range scanPrefixes {
		if strings.HasPrefix(p, strings.ToLower(prefix)) {
			return true
		}
	}

	// 常见扫描文件后缀
	scanSuffixes := []string{
		".php", ".asp", ".aspx", ".jsp", ".cgi",
		".bak", ".sql", ".tar", ".gz", ".zip",
		".pem", ".key", ".crt", ".p12",
		".DS_Store",
		".xml", // 单独的 .xml 不拦（manifest.xml 合法），由前缀规则过滤
	}
	for _, suffix := range scanSuffixes {
		if strings.HasSuffix(p, suffix) {
			return true
		}
	}

	// 合法路径白名单（避免误封）：到这里才检查，短路逻辑
	_ = method
	return false
}

func (s *Server) validHost(r *http.Request) bool {
	host := r.Host
	if s.cfg.Host == "127.0.0.1" || s.cfg.Host == "localhost" {
		allowed := map[string]bool{
			fmt.Sprintf("127.0.0.1:%d", s.cfg.Port): true,
			fmt.Sprintf("localhost:%d", s.cfg.Port): true,
			"127.0.0.1":                             true, "localhost": true,
		}
		return allowed[host]
	}
	if idx := strings.LastIndex(host, ":"); idx >= 0 {
		if p, err := strconv.Atoi(host[idx+1:]); err == nil && p != s.cfg.Port {
			return false
		}
	}
	return true
}

func getUserID(r *http.Request) string {
	// Prefer explicit header (sent by apiFetch, works through any proxy)
	if uid := r.Header.Get("X-User-ID"); uid != "" {
		return uid
	}
	// Fallback: cookie (same-origin direct access)
	c, err := r.Cookie("med_exam_uid")
	if err != nil {
		return progress.LegacyUser
	}
	return c.Value
}

// remoteIP returns the client IP.
//
// 安全加固（Session J）：只有当**直连的** TCP 源地址属于可信代理列表
// （s.cfg.TrustedProxies，含 loopback 时会把 127/8、::1 也视为可信）时，
// 才会信任 X-Real-IP / X-Forwarded-For header。否则一律忽略 header，
// 回落到 r.RemoteAddr。
//
// 背景：早期实现无条件信任 header，任何人都可以用 X-Real-IP 伪造源 IP：
//   - 绕过 checkRate（每次换个假 IP 就无限请求）
//   - 绕过 banIP（扫描器被封后换 header 继续）
//   - 绕过 auth.NeedsCaptcha / CheckBruteForce（匿名暴力破解访问码）
//
// 注意：该函数依赖 Server 的配置（TrustedProxies），因此改为方法而非
// 包级函数；旧的 `remoteIP(r)` 调用点全部迁移为 `s.remoteIP(r)`。

func (s *Server) remoteIP(r *http.Request) string {
	// 先拿到直连 TCP 源地址
	direct, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		direct = r.RemoteAddr
	}
	// 如果直连地址不可信，直接返回它（忽略任何 header）
	if !s.isTrustedProxy(direct) {
		return direct
	}
	// 可信代理：读 header
	if ip := r.Header.Get("X-Real-IP"); ip != "" {
		return strings.TrimSpace(strings.SplitN(ip, ",", 2)[0])
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.SplitN(xff, ",", 2)[0])
	}
	return direct
}

// isTrustedProxy 判断给定 IP 是否在可信代理列表中。
//
// 默认行为：loopback (127.0.0.0/8, ::1) 始终可信，因为服务通常就是
// 被本机上的 nginx/caddy 反代包一层。管理员可以通过 TrustedProxies
// 追加其它 CIDR / 具体 IP。

func (s *Server) isTrustedProxy(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	if parsed.IsLoopback() {
		return true
	}
	for _, entry := range s.cfg.TrustedProxies {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		// 支持 CIDR 和单 IP 两种写法
		if strings.Contains(entry, "/") {
			_, cidr, err := net.ParseCIDR(entry)
			if err == nil && cidr.Contains(parsed) {
				return true
			}
		} else {
			if ip2 := net.ParseIP(entry); ip2 != nil && ip2.Equal(parsed) {
				return true
			}
		}
	}
	return false
}

// isLoopbackRemote 判断请求的**直连**TCP 源地址是否为 loopback (127/8, ::1)。
//
// 和 s.remoteIP(r) 不同：这里只看 r.RemoteAddr，完全忽略任何 X-Real-IP /
// X-Forwarded-For header。调试端点等敏感路径必须用这个函数，防止在可信代理
// 场景下被 header 伪造绕过。

func isLoopbackRemote(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func jsonOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(v)
}

// decodeJSONBody 读取并解析请求体 JSON，并强制限制 body 大小，防止恶意
// 客户端用超大 JSON body 消耗服务器内存（OOM DoS）。1 MiB 对于本项目
// 所有 API（答题记录、分享配置、AI 聊天历史等）都绰绰有余；极少数真正
// 需要更大 body 的端点（目前没有）可以自行调用 http.MaxBytesReader 覆盖。
//
// 返回任何错误都意味着请求无效，调用方应立即返回 BadRequest 并中止处理。
const defaultJSONMaxBody = 1 << 20 // 1 MiB

func decodeJSONBody(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, defaultJSONMaxBody)
	return json.NewDecoder(r.Body).Decode(v)
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// s3SignedGet 发送 AWS Signature V4 签名的 GET 请求，兼容私有 bucket。

func s3SignedGet(ctx context.Context, client *http.Client, s3URL, ak, sk, region string) (*http.Response, error) {
	parsed, err := url.Parse(s3URL)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	dateStr := now.Format("20060102")
	timeStr := now.Format("20060102T150405Z")
	service := "s3"
	payloadHash := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" // SHA-256 of empty body

	host := parsed.Host
	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalHeaders := fmt.Sprintf("host:%s\nx-amz-content-sha256:%s\nx-amz-date:%s\n", host, payloadHash, timeStr)
	canonicalReq := strings.Join([]string{"GET", path, "", canonicalHeaders, signedHeaders, payloadHash}, "\n")

	scope := dateStr + "/" + region + "/" + service + "/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + timeStr + "\n" + scope + "\n" +
		hex.EncodeToString(s3sha256([]byte(canonicalReq)))

	sigKey := s3hmac(s3hmac(s3hmac(s3hmac([]byte("AWS4"+sk), []byte(dateStr)), []byte(region)), []byte(service)), []byte("aws4_request"))
	signature := hex.EncodeToString(s3hmac(sigKey, []byte(stringToSign)))

	authHeader := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s,SignedHeaders=%s,Signature=%s", ak, scope, signedHeaders, signature)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s3URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-amz-date", timeStr)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	req.Header.Set("Authorization", authHeader)
	return client.Do(req)
}

type lcgRNG struct{ state uint64 }

func newRNG(seed string) *lcgRNG {
	if seed == "" {
		n, _ := rand.Int(rand.Reader, new(big.Int).SetUint64(^uint64(0)))
		return &lcgRNG{state: n.Uint64()}
	}
	h := uint64(14695981039346656037)
	for _, b := range []byte(seed) {
		h ^= uint64(b)
		h *= 1099511628211
	}
	return &lcgRNG{state: h}
}

func (r *lcgRNG) next() uint64 {
	r.state = r.state*6364136223846793005 + 1442695040888963407
	return r.state
}

func (r *lcgRNG) shuffle(groups []group) {
	for i := len(groups) - 1; i > 0; i-- {
		j := int(r.next() % uint64(i+1))
		groups[i], groups[j] = groups[j], groups[i]
	}
}

// ── Logging middleware ─────────────────────────────────────────────────

type responseWriterWrapper struct {
	http.ResponseWriter
	statusCode int
	written    bool
}

func (w *responseWriterWrapper) WriteHeader(code int) {
	if !w.written {
		w.statusCode = code
		w.written = true
		w.ResponseWriter.WriteHeader(code)
	}
}

func (w *responseWriterWrapper) Write(b []byte) (int, error) {
	if !w.written {
		w.statusCode = 200
		w.written = true
	}
	return w.ResponseWriter.Write(b)
}

func (w *responseWriterWrapper) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func logRequest(s *Server, r *http.Request, status int, duration time.Duration) {
	// 使用 s.remoteIP(r) 获取真实客户端 IP（nginx 反代时从 X-Real-IP 读取）
	ip := s.remoteIP(r)
	method := r.Method
	path := r.URL.Path
	if path == "" {
		path = "/"
	}
	// 静态资源简化显示
	if strings.HasPrefix(path, "/static/") {
		path = "/static/..."
	}
	logger.Debugf("%s %s %s %d %v", ip, method, path, status, duration.Round(time.Millisecond))
}

// ── Push API handlers ────────────────────────────────────────────────────

// GET /api/push/vapid-key  →  {"publicKey":"..."}

func imgExtFromURL(u string) string {
	lower := strings.ToLower(u)
	for _, ext := range []string{".png", ".gif", ".webp", ".svg", ".avif", ".jpeg", ".jpg"} {
		idx := strings.Index(lower, ext)
		if idx < 0 {
			continue
		}
		after := lower[idx+len(ext):]
		if after == "" || after[0] == '?' || after[0] == '#' {
			return ext
		}
	}
	return ".jpg"
}

// GET /api/img/proxy?url=<encoded>
// 服务端代理外链图片请求，解决前端跨域（CORS）问题。
// 支持缓存头（Cache-Control: public, max-age=86400），可被 CDN / 浏览器缓存。
//
// 两个 client 对应两条独立的信任链：
//
//	s3ImgClient        — 用于调用 s.cfg.S3Endpoint（通常是本机 MinIO/RustFS
//	                     如 127.0.0.1:9000 或内网私有 S3）。内网地址是合法
//	                     使用，因此不能做 SSRF 过滤。
//
//	upstreamImgClient  — 用于代理用户题目里的外链图片。必须禁止任何内网
//	                     地址，包括跟随重定向之后：攻击者可以构造
//	                     `GET /api/img/proxy?url=https://a.com/redirect-to-metadata`
//	                     让 a.com 把 302 指到 169.254.169.254。下面的
//	                     CheckRedirect 对每一跳都重新跑 SSRF 检查。
var s3ImgClient = &http.Client{
	Timeout: 20 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects")
		}
		return nil
	},
}

var upstreamImgClient = &http.Client{
	Timeout: 20 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects")
		}
		if err := checkPublicURL(req.URL); err != nil {
			// 重定向目标落在内网/回环/metadata 地址，拒绝跟随
			return fmt.Errorf("redirect blocked: %w", err)
		}
		return nil
	},
}

// checkPublicURL 验证 URL 是否为可代理的"公网 http(s) 资源"。
// 同时被 handleImgProxy 主流程（第一次请求）与 upstreamImgClient.CheckRedirect
// （每次重定向）使用，两个位置的策略必须一致。

func checkPublicURL(u *url.URL) error {
	if u == nil {
		return fmt.Errorf("invalid url")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("only http/https allowed")
	}
	host := strings.ToLower(u.Hostname())
	// 封锁所有私网/回环地址，防止 SSRF
	// RFC1918: 10/8, 172.16/12, 192.168/16
	// 链路本地: 169.254/16, fe80::/10
	// 回环: 127/8, ::1, localhost
	// 特殊: 0.0.0.0, metadata (169.254.169.254)
	blockedPrefixes := []string{
		"localhost", "127.", "0.0.0.0",
		"10.",
		"192.168.",
		"169.254.",
		"::1", "[::1]", "fe80",
	}
	for _, blocked := range blockedPrefixes {
		if strings.HasPrefix(host, blocked) || host == "localhost" {
			return fmt.Errorf("private address blocked")
		}
	}
	// 172.16.0.0/12 覆盖 172.16.x.x - 172.31.x.x
	if strings.HasPrefix(host, "172.") {
		parts := strings.SplitN(host, ".", 3)
		if len(parts) >= 2 {
			if second, err := strconv.Atoi(parts[1]); err == nil && second >= 16 && second <= 31 {
				return fmt.Errorf("private address blocked")
			}
		}
	}
	return nil
}
