# med-exam-kit (Go 版)

医学考试题库工具的 Go 重构版，单二进制分发，前端静态资源已内嵌可执行文件，无需额外安装。

---

## 下载

从 [GitHub Releases](https://github.com/zyh001/med-exam-kit/releases) 下载最新版，无需安装 Go 或任何依赖：

| 平台 | 文件名 |
|------|--------|
| Linux x86_64 | `med-exam-linux-amd64` |
| Linux ARM64 | `med-exam-linux-arm64` |
| Linux ARMv7（树莓派等） | `med-exam-linux-armv7` |
| Linux 龙芯 | `med-exam-linux-loong64` |
| macOS Intel | `med-exam-darwin-amd64` |
| macOS Apple Silicon | `med-exam-darwin-arm64` |
| Windows x64 | `med-exam-windows-amd64.exe` |
| Windows ARM64 | `med-exam-windows-arm64.exe` |

```bash
# macOS / Linux 首次使用：授予执行权限
chmod +x med-exam-linux-amd64

# macOS 还需解除 Gatekeeper：系统设置 → 隐私与安全 → 「仍要打开」
```

---

## 快速开始

```bash
# 从 JSON 原始数据构建题库
./med-exam build -i data/raw -o data/output/内科学

# 启动刷题 Web 应用（自动打开浏览器）
./med-exam quiz -b data/output/内科学.mqb

# 多题库
./med-exam quiz -b 内科.mqb -b 外科.mqb

# 局域网共享（手机也能访问）
./med-exam quiz -b 内科.mqb --host 0.0.0.0

# 服务器部署
./med-exam quiz -b 内科.mqb --host 0.0.0.0 --port 8080 --no-browser

# 带加密密码
./med-exam quiz -b 内科.mqb --password 你的密码
```

启动后浏览器访问 `http://127.0.0.1:5174`

---

## 配置文件

在可执行文件同目录下创建 `med-exam-kit.yaml`，以后只需 `./med-exam quiz -b xxx.mqb`：

```yaml
# med-exam-kit.yaml

# ── 基础 ──────────────────────────────────
access_code:    ""          # 访问码（留空则自动生成并打印到控制台）
port:           5174
host:           "127.0.0.1"

# ── AI 答疑（可选）────────────────────────
ai_provider:    deepseek    # openai / deepseek / qwen / kimi / ollama
ai_model:       ""          # 留空使用 provider 默认模型
ai_api_key:     ""          # API 密钥
ai_base_url:    ""          # 自定义接口地址（留空使用官方）
ai_thinking:    ""          # true=强制开启思考 / false=关闭 / 留空=自动

# ── ASR 语音识别（可选，阿里云 DashScope）──
asr_api_key:    ""
asr_model:      ""          # 留空使用 qwen3-asr-flash

# ── S3 图片存储（可选，兼容 MinIO / RustFS）──
# 题库编辑器中上传图片到题干/解析需要此配置
s3_endpoint:    ""          # 例：http://localhost:9000
s3_bucket:      ""          # 例：med-images
s3_access_key:  ""
s3_secret_key:  ""
```

---

## Web 应用功能

### 三种学习模式

| 模式 | 特点 |
|------|------|
| **练习模式** | 答题后即时显示对错和解析，适合日常学习 |
| **考试模式** | 计时限时，交卷后统一评分，模拟正式考试 |
| **背题模式** | 卡片翻转，自评掌握程度，SM-2 算法智能安排复习 |

### 键盘快捷键

| 按键 | 功能 |
|------|------|
| `1`–`5` / `A`–`E` | 选择对应选项 |
| `←` / `→` | 上一题 / 下一题 |
| `M` / `Space` | 标记/取消标记当前题目 |
| `E` | 展开/定位到解析区 |
| `Enter` | 下一题 |
| `Esc` | 关闭弹窗 |

### AI 答疑

配置 `ai_api_key` 后，每道题右下角出现「AI 帮你解析」按钮，支持多轮追问，AI 回复中的流程图自动渲染为 Mermaid 图表。

### 语音输入

配置 `asr_api_key`（阿里云 DashScope）后，AI 答疑输入框**长按发送按钮**触发语音识别，实时转写。

### PWA 安装

- **Android / Chrome**：地址栏右侧「安装到主屏幕」按钮
- **iPhone / Safari**：底部「分享」→「添加到主屏幕」

---

## 命令速查

```bash
./med-exam quiz     -b FILE [...]   # 刷题 Web 应用
./med-exam edit     -b FILE         # 题库编辑器（浏览器中修改题目）
./med-exam build    -i DIR -o PATH  # 从 JSON 原始数据构建 .mqb 题库
./med-exam export   -b FILE -f FMT  # 导出 xlsx/docx/pdf/csv/json/db
./med-exam enrich   -b FILE         # AI 批量补全缺失答案/解析
./med-exam generate ...             # 随机组卷导出 Word 试卷
./med-exam info     -b FILE         # 查看题库统计
./med-exam inspect  -b FILE         # 逐题浏览题库内容

# 任意命令加 --help 查看详细参数
./med-exam quiz --help
```

---

## 命令参数详解

### `quiz` — 刷题服务器

| 标志 | 说明 | 默认值 |
|------|------|--------|
| `-b, --bank` | `.mqb` 文件路径（可多次指定） | — |
| `--port` | 监听端口 | `5174` |
| `--host` | 监听地址 | `127.0.0.1` |
| `--pin` | 自定义访问码 | 自动生成 |
| `--no-pin` | 禁用访问码验证 | false |
| `--no-record` | 禁用做题记录 | false |
| `--no-browser` | 不自动打开浏览器 | false |
| `--ai-provider` | AI provider | — |
| `--ai-key` | AI API 密钥 | — |
| `--asr-key` | ASR API 密钥 | — |

### `build` — 构建题库

| 标志 | 说明 | 默认值 |
|------|------|--------|
| `-i, --input` | JSON 文件目录 | `data/raw` |
| `-o, --output` | 输出路径（自动加 `.mqb`） | `data/output/bank` |
| `--strategy` | 去重策略 `strict` / `content` | `strict` |
| `--password` | 加密密码 | 空 |

### `export` — 导出题库

| 标志 | 说明 | 默认值 |
|------|------|--------|
| `-b, --bank` | `.mqb` 文件路径 | — |
| `-f, --format` | 格式（可多次）`xlsx,csv,docx,pdf,json,db` | `xlsx` |
| `--mode` | 题型过滤（可多次指定） | 全部 |
| `--unit` | 章节关键词过滤 | 全部 |
| `--keyword` | 题干关键词 | — |
| `--min-rate` | 最低正确率 | 0 |
| `--max-rate` | 最高正确率 | 100 |

---

## 自行编译

如需从源码编译，本项目使用纯 Go SQLite（`modernc.org/sqlite`），**无需 GCC 或 CGo**：

```bash
git clone https://github.com/zyh001/med-exam-kit.git -b golang-version
cd med-exam-kit
go mod tidy

# Linux / macOS
go build -ldflags="-s -w" -o med-exam .

# Windows
go build -ldflags="-s -w" -o med-exam.exe .
```

---

## 与 Python 版兼容性

| 功能 | 兼容性 |
|------|--------|
| `.mqb` MQB2 格式（读写） | ✅ 完全兼容，同一文件可互读 |
| `.mqb` MQB1 旧版 pickle 格式 | ⚠ 仅提示，需先用 Python 版 `migrate` 命令迁移 |
| Fernet 加密（相同密码互通） | ✅ 完全兼容 |
| zlib 压缩 | ✅ 完全兼容 |
| SQLite 进度数据库 | ✅ 完全兼容 |

---

## 架构一览

```
main.go                    # 入口 + //go:embed assets
cmd/                       # CLI 子命令
  build.go / export.go / info.go / quiz.go / enrich.go ...
internal/
  models/                  # Question / SubQuestion 数据模型
  bank/                    # .mqb 读写 + Fernet + PBKDF2
  dedup/                   # SHA-256 指纹去重
  filters/                 # 过滤器
  parsers/                 # 阿虎医考 / 医考帮 parser
  progress/                # SQLite SM-2 + 错题本 + 多用户
  auth/                    # 访问码 + HMAC Cookie + 暴力破解防护
  server/                  # HTTP API + 图片代理 + S3 上传
  exporters/               # CSV / XLSX / DOCX / PDF / JSON / SQLite
assets/                    # 已嵌入二进制，无需手动复制
  static/                  # quiz.js / quiz.css / editor.js ...
  templates/               # quiz.html / editor.html
```
