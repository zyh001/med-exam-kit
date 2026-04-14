package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ── S3 私有上传 / 下载（AWS Signature V4，兼容 MinIO / RustFS）──────

const s3UploadPrefix = "images/uploads/"
const s3Region       = "us-east-1"

// s3Put 上传 data 到 S3，key 为对象路径（含 prefix）
func s3Put(ctx context.Context, endpoint, bucket, ak, sk, key, contentType string, data []byte) error {
	now := time.Now().UTC()
	dateStr := now.Format("20060102")
	timeStr := now.Format("20060102T150405Z")

	payloadHash := hex.EncodeToString(s3sha256(data))
	reqURL := strings.TrimRight(endpoint, "/") + "/" + bucket + "/" + key

	parsed, _ := url.Parse(endpoint)
	host := parsed.Host
	signedHeaders := "content-type;host;x-amz-content-sha256;x-amz-date"
	canonicalHeaders := fmt.Sprintf(
		"content-type:%s\nhost:%s\nx-amz-content-sha256:%s\nx-amz-date:%s\n",
		contentType, host, payloadHash, timeStr,
	)
	canonicalReq := strings.Join([]string{
		"PUT",
		"/" + bucket + "/" + key,
		"",
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := dateStr + "/" + s3Region + "/s3/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + timeStr + "\n" + scope + "\n" +
		hex.EncodeToString(s3sha256([]byte(canonicalReq)))

	sigKey := s3hmac(s3hmac(s3hmac(s3hmac([]byte("AWS4"+sk), []byte(dateStr)), []byte(s3Region)), []byte("s3")), []byte("aws4_request"))
	signature := hex.EncodeToString(s3hmac(sigKey, []byte(stringToSign)))
	authHeader := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s,SignedHeaders=%s,Signature=%s",
		ak, scope, signedHeaders, signature)

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, reqURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.ContentLength = int64(len(data))
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("x-amz-date", timeStr)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	req.Header.Set("Authorization", authHeader)

	resp, err := imgProxyClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("S3 PUT %d: %s", resp.StatusCode, body)
	}
	return nil
}

// s3Get 从 S3 下载对象，返回 Body（调用方负责关闭）和 Content-Type
func s3Get(ctx context.Context, endpoint, bucket, ak, sk, key string) (io.ReadCloser, string, error) {
	now := time.Now().UTC()
	dateStr := now.Format("20060102")
	timeStr := now.Format("20060102T150405Z")
	reqURL := strings.TrimRight(endpoint, "/") + "/" + bucket + "/" + key

	parsed, _ := url.Parse(endpoint)
	host := parsed.Host
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	emptyHash := hex.EncodeToString(s3sha256([]byte{}))
	canonicalHeaders := fmt.Sprintf("host:%s\nx-amz-content-sha256:%s\nx-amz-date:%s\n",
		host, emptyHash, timeStr)
	canonicalReq := strings.Join([]string{
		"GET",
		"/" + bucket + "/" + key,
		"",
		canonicalHeaders,
		signedHeaders,
		emptyHash,
	}, "\n")

	scope := dateStr + "/" + s3Region + "/s3/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + timeStr + "\n" + scope + "\n" +
		hex.EncodeToString(s3sha256([]byte(canonicalReq)))

	sigKey := s3hmac(s3hmac(s3hmac(s3hmac([]byte("AWS4"+sk), []byte(dateStr)), []byte(s3Region)), []byte("s3")), []byte("aws4_request"))
	signature := hex.EncodeToString(s3hmac(sigKey, []byte(stringToSign)))
	authHeader := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s,SignedHeaders=%s,Signature=%s",
		ak, scope, signedHeaders, signature)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("x-amz-date", timeStr)
	req.Header.Set("x-amz-content-sha256", emptyHash)
	req.Header.Set("Authorization", authHeader)

	resp, err := imgProxyClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, "", fmt.Errorf("not found")
	}
	if resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, "", fmt.Errorf("S3 GET %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "image/jpeg"
	}
	return resp.Body, ct, nil
}

func s3sha256(data []byte) []byte { h := sha256.Sum256(data); return h[:] }
func s3hmac(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key); mac.Write(data); return mac.Sum(nil)
}

// ── HTTP 处理器 ───────────────────────────────────────────────────────

// handleImgUpload  POST /api/img/upload
// 接收 multipart/form-data 的 file 字段，上传到 S3，返回本站访问 URL。
// 仅在 S3 已配置时可用。
func (s *Server) handleImgUpload(w http.ResponseWriter, r *http.Request) {
	if s.cfg.S3Endpoint == "" || s.cfg.S3Bucket == "" {
		jsonError(w, "S3 未配置", http.StatusServiceUnavailable)
		return
	}

	// 限制 10 MB
	const maxSize = 10 << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxSize+1024)
	if err := r.ParseMultipartForm(maxSize); err != nil {
		jsonError(w, "文件过大或格式错误（最大 10 MB）", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		jsonError(w, "缺少 file 字段", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// 只接受图片
	ct := header.Header.Get("Content-Type")
	if ct == "" {
		ct = "image/jpeg"
	}
	if !strings.HasPrefix(ct, "image/") {
		jsonError(w, "只允许上传图片文件", http.StatusBadRequest)
		return
	}

	// 读取全部内容（已由 MaxBytesReader 限制大小）
	data, err := io.ReadAll(file)
	if err != nil {
		jsonError(w, "读取文件失败", http.StatusInternalServerError)
		return
	}

	// 生成安全文件名：uuid + 推断扩展名
	ext := imgExtFromContentType(ct)
	if e := path.Ext(header.Filename); e != "" && isAllowedExt(e) {
		ext = strings.ToLower(e)
	}
	filename := uuid.New().String() + ext
	key := s3UploadPrefix + filename

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	if err := s3Put(ctx, s.cfg.S3Endpoint, s.cfg.S3Bucket,
		s.cfg.S3AccessKey, s.cfg.S3SecretKey, key, ct, data); err != nil {
		jsonError(w, "上传到 S3 失败："+err.Error(), http.StatusBadGateway)
		return
	}

	jsonOK(w, map[string]any{
		"url":      "/api/img/local/" + filename,
		"filename": filename,
		"size":     len(data),
	})
}

// handleImgLocal  GET /api/img/local/{filename}
// 从私有 S3 取图并回传，受 Session Token 保护（/api/* 中间件已验证）。
func (s *Server) handleImgLocal(w http.ResponseWriter, r *http.Request) {
	if s.cfg.S3Endpoint == "" || s.cfg.S3Bucket == "" {
		http.Error(w, "S3 未配置", http.StatusServiceUnavailable)
		return
	}

	filename := strings.TrimPrefix(r.URL.Path, "/api/img/local/")
	if filename == "" || strings.ContainsAny(filename, "/\\..") {
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}
	// 额外校验扩展名白名单
	ext := strings.ToLower(path.Ext(filename))
	if !isAllowedExt(ext) {
		http.Error(w, "invalid file type", http.StatusBadRequest)
		return
	}

	key := s3UploadPrefix + filename

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	body, ct, err := s3Get(ctx, s.cfg.S3Endpoint, s.cfg.S3Bucket,
		s.cfg.S3AccessKey, s.cfg.S3SecretKey, key)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, "图片不存在", http.StatusNotFound)
		} else {
			http.Error(w, "获取图片失败："+err.Error(), http.StatusBadGateway)
		}
		return
	}
	defer body.Close()

	w.Header().Set("Content-Type", ct)
	// 私有缓存，不允许 CDN 缓存，但允许浏览器缓存 1 小时
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	io.Copy(w, body)
}

func imgExtFromContentType(ct string) string {
	switch {
	case strings.Contains(ct, "png"):  return ".png"
	case strings.Contains(ct, "gif"):  return ".gif"
	case strings.Contains(ct, "webp"): return ".webp"
	case strings.Contains(ct, "svg"):  return ".svg"
	default:                           return ".jpg"
	}
}

func isAllowedExt(ext string) bool {
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".svg":
		return true
	}
	return false
}
