package cmd

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zyh001/med-exam-kit/internal/bank"
)

var imgMigrateCmd = &cobra.Command{
	Use:   "img-migrate",
	Short: "扫描题库外链图片，下载并上传到 S3/MinIO/RustFS",
	Long: `扫描题库(.mqb)中所有外链图片，下载后上传到本地 S3 兼容对象存储。
输出 JSON 映射文件（原始 URL → S3 URL），可用于批量替换题库图片地址。

使用示例：
  # 1. 启动 MinIO（Docker）
  docker run -p 9000:9000 -p 9001:9001 \
    -e MINIO_ROOT_USER=minioadmin -e MINIO_ROOT_PASSWORD=minioadmin \
    minio/minio server /data --console-address ":9001"

  # 2. 迁移图片
  med-exam-kit img-migrate -b exam.mqb \
    --endpoint http://localhost:9000 \
    --bucket med-images \
    --access-key minioadmin \
    --secret-key minioadmin \
    --out url-map.json

  # 3. 预览（不上传）
  med-exam-kit img-migrate -b exam.mqb --dry-run`,
	RunE: runImgMigrate,
}

func init() {
	rootCmd.AddCommand(imgMigrateCmd)
	imgMigrateCmd.Flags().StringArrayP("bank", "b", nil, "题库路径（.mqb，可重复）")
	imgMigrateCmd.Flags().String("endpoint", "http://localhost:9000", "S3 / MinIO / RustFS 端点地址")
	imgMigrateCmd.Flags().String("bucket", "med-images", "存储桶名称")
	imgMigrateCmd.Flags().String("access-key", "minioadmin", "Access Key")
	imgMigrateCmd.Flags().String("secret-key", "minioadmin", "Secret Key")
	imgMigrateCmd.Flags().String("region", "us-east-1", "S3 region（MinIO/RustFS 使用默认 us-east-1）")
	imgMigrateCmd.Flags().String("prefix", "images/", "对象键前缀")
	imgMigrateCmd.Flags().String("out", "url-map.json", "输出映射文件路径（原始URL→S3 URL）")
	imgMigrateCmd.Flags().String("public-base", "", "图片公开访问 base URL（留空则自动推导）")
	imgMigrateCmd.Flags().Bool("dry-run", false, "只扫描列出图片，不执行下载或上传")
	imgMigrateCmd.Flags().String("password", "", "题库密码（加密题库使用）")
	imgMigrateCmd.Flags().Bool("skip-existing", true, "跳过已存在于映射文件中的 URL（断点续传）")
}

// 匹配 HTML src / CSS url() / 纯文本中的 http(s) 图片链接
var imgURLRe = regexp.MustCompile(`https?://[^\s"'<>\)\]]+`)

// 常见图片扩展名
var imgExts = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true,
	".gif": true, ".webp": true, ".bmp": true,
	".svg": true, ".avif": true,
}

func runImgMigrate(cmd *cobra.Command, args []string) error {
	bankPaths, _ := cmd.Flags().GetStringArray("bank")
	endpoint, _  := cmd.Flags().GetString("endpoint")
	bucket, _    := cmd.Flags().GetString("bucket")
	ak, _        := cmd.Flags().GetString("access-key")
	sk, _        := cmd.Flags().GetString("secret-key")
	region, _    := cmd.Flags().GetString("region")
	prefix, _    := cmd.Flags().GetString("prefix")
	outPath, _   := cmd.Flags().GetString("out")
	publicBase, _:= cmd.Flags().GetString("public-base")
	dryRun, _    := cmd.Flags().GetBool("dry-run")
	password, _  := cmd.Flags().GetString("password")
	skipExisting,_:= cmd.Flags().GetBool("skip-existing")

	if len(bankPaths) == 0 {
		return fmt.Errorf("请用 -b 指定至少一个题库路径")
	}

	// 推导公开访问 base URL
	if publicBase == "" {
		publicBase = strings.TrimRight(endpoint, "/") + "/" + bucket
	}

	// 断点续传：加载已有映射
	urlMap := make(map[string]string)
	if skipExisting {
		if data, err := os.ReadFile(outPath); err == nil {
			_ = json.Unmarshal(data, &urlMap)
			if len(urlMap) > 0 {
				fmt.Printf("📂 读取已有映射文件 %s，已有 %d 条记录（跳过重复）\n", outPath, len(urlMap))
			}
		}
	}

	// ── 扫描题库，收集所有外链图片 URL ──────────────────────────────
	seen := make(map[string]bool)
	var imgURLs []string

	for _, bp := range bankPaths {
		fmt.Printf("📂 扫描题库：%s\n", bp)
		questions, err := bank.LoadBank(bp, password)
		if err != nil {
			return fmt.Errorf("加载 %s 失败: %w", bp, err)
		}
		for _, q := range questions {
			// 从题干、选项、解析、子题干中提取图片 URL
			var texts []string
			texts = append(texts, q.Stem)
			for _, sq := range q.SubQuestions {
				texts = append(texts, sq.Text, sq.Discuss)
				for _, opt := range sq.Options {
					texts = append(texts, opt)
				}
			}
			for _, t := range texts {
				for _, u := range imgURLRe.FindAllString(t, -1) {
					u = strings.TrimRight(u, ".,;)")
					if imgExtMatch(u) && !seen[u] {
						seen[u] = true
						imgURLs = append(imgURLs, u)
					}
				}
			}
		}
	}

	fmt.Printf("🔍 发现 %d 张外链图片\n", len(imgURLs))

	if dryRun {
		fmt.Println("\n── 预览（--dry-run，不执行上传）──")
		for i, u := range imgURLs {
			tag := ""
			if urlMap[u] != "" { tag = " [已迁移]" }
			fmt.Printf("  [%d] %s%s\n", i+1, u, tag)
		}
		return nil
	}

	if ak == "" || sk == "" {
		return fmt.Errorf("请提供 --access-key 和 --secret-key")
	}

	// ── 下载 + 上传 ──────────────────────────────────────────────────
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 { return fmt.Errorf("too many redirects") }
			return nil
		},
	}

	success, skipped, failed := 0, 0, 0

	for i, imgURL := range imgURLs {
		// 断点续传：已有则跳过
		if skipExisting && urlMap[imgURL] != "" {
			skipped++
			continue
		}

		fmt.Printf("[%d/%d] ⬇  %s\n", i+1, len(imgURLs), truncateStr(imgURL, 80))

		data, ct, err := downloadImage(client, imgURL)
		if err != nil {
			fmt.Printf("         ⚠ 下载失败: %v\n", err)
			failed++
			continue
		}

		ext := extFromContentType(ct, imgURL)
		key := prefix + hashKey(imgURL) + ext

		if err := putObjectS3(client, endpoint, bucket, key, ak, sk, region, data, ct); err != nil {
			fmt.Printf("         ⚠ 上传失败: %v\n", err)
			failed++
			continue
		}

		publicURL := strings.TrimRight(publicBase, "/") + "/" + key
		urlMap[imgURL] = publicURL
		fmt.Printf("         ✅ → %s\n", publicURL)
		success++

		// 每10张保存一次（防止中途崩溃丢数据）
		if success%10 == 0 {
			_ = saveURLMap(outPath, urlMap)
		}
	}

	// 最终保存
	if err := saveURLMap(outPath, urlMap); err != nil {
		return fmt.Errorf("写入映射文件失败: %w", err)
	}

	fmt.Printf("\n📊 结果：成功 %d，跳过 %d，失败 %d\n", success, skipped, failed)
	fmt.Printf("📄 映射文件：%s（共 %d 条）\n", outPath, len(urlMap))
	fmt.Println("\n💡 下一步：将 S3 配置填入 med-exam-kit.yaml，重启服务后图片将从 S3 加载")
	fmt.Println("   s3_endpoint:  " + endpoint)
	fmt.Println("   s3_bucket:    " + bucket)
	fmt.Println("   s3_public_base: " + publicBase)
	return nil
}

// ── 工具函数 ──────────────────────────────────────────────────────

func imgExtMatch(u string) bool {
	lower := strings.ToLower(u)
	// 匹配扩展名
	for ext := range imgExts {
		idx := strings.Index(lower, ext)
		if idx < 0 { continue }
		after := lower[idx+len(ext):]
		// 扩展名后面跟 ? / # / 空白 / 结尾 都算图片 URL
		if after == "" || after[0] == '?' || after[0] == '#' {
			return true
		}
	}
	// 没有扩展名但包含 image/img 路径关键词的也尝试下载
	return strings.Contains(lower, "/image/") || strings.Contains(lower, "/img/") ||
		strings.Contains(lower, "/photo/") || strings.Contains(lower, "/pic/")
}

func downloadImage(client *http.Client, imgURL string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, imgURL, nil)
	if err != nil { return nil, "", err }
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; med-exam-kit-migrate/1.0)")
	parsed, _ := url.Parse(imgURL)
	if parsed != nil {
		req.Header.Set("Referer", parsed.Scheme+"://"+parsed.Host+"/")
	}
	resp, err := client.Do(req)
	if err != nil { return nil, "", err }
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	data, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20)) // 20 MB 上限
	return data, ct, err
}

func hashKey(u string) string {
	h := sha256.Sum256([]byte(u))
	return hex.EncodeToString(h[:8]) // 16 hex chars = 64-bit hash
}

func extFromContentType(ct, fallbackURL string) string {
	ct = strings.ToLower(ct)
	switch {
	case strings.Contains(ct, "png"):         return ".png"
	case strings.Contains(ct, "gif"):         return ".gif"
	case strings.Contains(ct, "webp"):        return ".webp"
	case strings.Contains(ct, "svg"):         return ".svg"
	case strings.Contains(ct, "avif"):        return ".avif"
	case strings.Contains(ct, "jpeg"), strings.Contains(ct, "jpg"): return ".jpg"
	}
	// 从 URL 推断
	lower := strings.ToLower(fallbackURL)
	for ext := range imgExts {
		if strings.Contains(lower, ext) {
			return ext
		}
	}
	return ".jpg"
}

func saveURLMap(path string, m map[string]string) error {
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil { return err }
	return os.WriteFile(path, out, 0644)
}

func truncateStr(s string, n int) string {
	if len(s) <= n { return s }
	return s[:n-3] + "..."
}

// ── AWS Signature V4 上传（兼容 MinIO / RustFS / AWS S3）────────

func putObjectS3(client *http.Client, endpoint, bucket, key, ak, sk, region string, data []byte, ct string) error {
	if ct == "" || !strings.HasPrefix(strings.ToLower(ct), "image/") {
		ct = "application/octet-stream"
	}

	now := time.Now().UTC()
	dateStr := now.Format("20060102")
	timeStr := now.Format("20060102T150405Z")
	service := "s3"

	payloadHash := hex.EncodeToString(sha256bytes(data))

	// 构造请求 URL（path-style：endpoint/bucket/key）
	reqURL := strings.TrimRight(endpoint, "/") + "/" + bucket + "/" + key

	// Canonical request
	signedHeaders := "content-type;host;x-amz-content-sha256;x-amz-date"
	parsed, _ := url.Parse(endpoint)
	host := parsed.Host
	canonicalHeaders := fmt.Sprintf(
		"content-type:%s\nhost:%s\nx-amz-content-sha256:%s\nx-amz-date:%s\n",
		ct, host, payloadHash, timeStr,
	)
	canonicalReq := strings.Join([]string{
		"PUT",
		"/" + bucket + "/" + key,
		"", // no query string
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	// String to sign
	scope := dateStr + "/" + region + "/" + service + "/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + timeStr + "\n" + scope + "\n" +
		hex.EncodeToString(sha256bytes([]byte(canonicalReq)))

	// Signing key
	sigKey := hmacSHA256bytes(
		hmacSHA256bytes(
			hmacSHA256bytes(
				hmacSHA256bytes([]byte("AWS4"+sk), []byte(dateStr)),
				[]byte(region)),
			[]byte(service)),
		[]byte("aws4_request"))
	signature := hex.EncodeToString(hmacSHA256bytes(sigKey, []byte(stringToSign)))

	authHeader := fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s,SignedHeaders=%s,Signature=%s",
		ak, scope, signedHeaders, signature)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPut, reqURL,
		strings.NewReader(string(data)))
	if err != nil { return err }
	req.ContentLength = int64(len(data))
	req.Header.Set("Content-Type", ct)
	req.Header.Set("x-amz-date", timeStr)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	req.Header.Set("Authorization", authHeader)

	resp, err := client.Do(req)
	if err != nil { return err }
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("S3 HTTP %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func sha256bytes(data []byte) []byte {
	h := sha256.Sum256(data)
	return h[:]
}

func hmacSHA256bytes(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}
