# med-exam-kit (Go 版)

医学考试题库工具的 Go 重构版，单二进制分发，前端静态资源嵌入可执行文件。

---

## 快速开始

### 1. 复制前端文件（一次性）

```bash
# 方法 A：使用 Makefile（推荐）
make copy-assets PYTHON_SRC=../med-exam-kit-main

# 方法 B：手动复制
cp -r src/med_exam_toolkit/static/            med-exam-kit-go/assets/static/
cp    src/med_exam_toolkit/templates/quiz.html   med-exam-kit-go/assets/templates/
cp    src/med_exam_toolkit/templates/editor.html med-exam-kit-go/assets/templates/
cp    src/med_exam_toolkit/templates/_base.html  med-exam-kit-go/assets/templates/
```

目录结构应该是：

```
assets/
├── static/
│   ├── quiz.css
│   ├── quiz.js
│   ├── editor.css
│   ├── editor.js
│   └── ...
└── templates/
    ├── quiz.html
    ├── editor.html
    └── _base.html
```

### 2. 编译

本项目使用纯 Go 实现的 SQLite（`modernc.org/sqlite`），无需 GCC 或 CGo。

```bat
REM Windows
go mod tidy
go build -ldflags="-s -w" -o med-exam.exe .

# Linux / macOS
go mod tidy
go build -ldflags="-s -w" -o med-exam .
```

### 3. 使用

```bash
# 从 JSON 原始数据构建题库
./med-exam build -i data/raw -o data/output/bank

# 启动刷题服务器（自动打开浏览器）
./med-exam quiz -b data/output/bank.mqb

# 带加密密码
./med-exam -p mypassword build -i data/raw -o data/output/bank_enc
./med-exam -p mypassword quiz  -b data/output/bank_enc.mqb

# 导出多种格式
./med-exam export -b data/output/bank.mqb -f xlsx,csv,docx,pdf,json,db

# 查看题库信息
./med-exam info -b data/output/bank.mqb

# 局域网共享（禁用 PIN 码）
./med-exam quiz -b data/output/bank.mqb --host 0.0.0.0 --no-pin
```

---

## 常见问题

### ❌ 页面没有 CSS 样式

`assets/static/` 目录为空或未复制前端文件，按步骤 1 复制后重新编译。

---

## 命令参考

### `build` — 构建题库

| 标志 | 说明 | 默认值 |
|------|------|--------|
| `-i, --input` | JSON 文件目录 | `data/raw` |
| `-o, --output` | 输出路径（自动加 `.mqb`） | `data/output/bank` |
| `--strategy` | 去重策略 `strict` / `content` | `strict` |
| `--no-compress` | 禁用 zlib 压缩 | false |
| `-p, --password` | 加密密码（根级标志） | 空 |

### `export` — 导出题库

| 标志 | 说明 | 默认值 |
|------|------|--------|
| `-b, --bank` | `.mqb` 文件路径（根级标志） | — |
| `-o, --output-dir` | 输出目录 | `data/output` |
| `-f, --format` | 格式（逗号分隔）`xlsx,csv,docx,pdf,json,db` | `xlsx` |
| `--split-options` | 每个选项独立一列 | false |
| `--mode` | 题型过滤（可多次指定） | 全部 |
| `--unit` | 章节关键词过滤 | 全部 |
| `--keyword` | 题干关键词 | — |
| `--min-rate` | 最低正确率 | 0 |
| `--max-rate` | 最高正确率 | 100 |

### `quiz` — 刷题服务器

| 标志 | 说明 | 默认值 |
|------|------|--------|
| `--port` | 监听端口 | `5174` |
| `--host` | 监听地址 | `127.0.0.1` |
| `--no-record` | 禁用做题记录 | false |
| `--no-pin` | 禁用访问码验证 | false |

### `info` — 题库信息

```bash
./med-exam info -b data/output/bank.mqb
```

---

## 与 Python 版兼容性

| 功能 | 兼容性 |
|------|--------|
| `.mqb` MQB2 格式（读写） | ✅ 完全兼容，同一文件可互读 |
| `.mqb` MQB1 旧版 pickle 格式 | ⚠ 仅提示，需先用 Python 版迁移 |
| Fernet 加密（相同密码互通） | ✅ 完全兼容 |
| zlib 压缩 | ✅ 完全兼容 |
| SQLite 进度数据库表结构 | ✅ 完全兼容 |
| 前端 JS / CSS / HTML | ✅ 零改动，直接复用 |

---

## 架构一览

```
main.go                    # 入口 + //go:embed assets
cmd/                       # CLI 子命令
  build.go                 # med-exam build
  export.go                # med-exam export
  info.go                  # med-exam info
  quiz.go                  # med-exam quiz
internal/
  models/                  # Question / SubQuestion 数据模型
  bank/                    # .mqb 读写 + Fernet + PBKDF2
  dedup/                   # SHA-256 指纹去重
  filters/                 # 过滤器
  loader/                  # JSON 目录扫描
  parsers/                 # 阿虎医考 / 医考帮 parser
  progress/                # SQLite SM-2 + 错题本 + 多用户
  auth/                    # 访问码 + HMAC Cookie + 暴力破解防护
  server/                  # HTTP API 服务器（10 条端点）
  exporters/               # CSV / XLSX / DOCX / PDF / JSON / SQLite
assets/
  static/                  # ← 复制自 Python 版 static/
  templates/               # ← 复制自 Python 版 templates/
```

---

## 下载预编译版本

无需安装 Go 环境，直接从 [GitHub Releases](https://github.com/zyh001/med-exam-kit/releases) 下载对应平台的二进制文件：

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

### 首次运行

```bash
# macOS / Linux：先授予执行权限
chmod +x med-exam-linux-amd64

# macOS 还需解除 Gatekeeper 限制
# 系统设置 → 隐私与安全 → 点击「仍要打开」

# 直接运行
./med-exam-linux-amd64 quiz -b 内科学.mqb
```

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

# ── ASR 语音识别（可选，阿里云 DashScope）──
asr_api_key:    ""
asr_model:      ""          # 留空使用 qwen3-asr-flash

# ── S3 图片存储（可选，兼容 MinIO / RustFS）
s3_endpoint:    ""          # 例：http://localhost:9000
s3_bucket:      ""          # 例：med-images
s3_access_key:  ""
s3_secret_key:  ""
```

---

## 命令速查

### `quiz` — 刷题 Web 应用

```bash
./med-exam quiz -b 内科.mqb                          # 单题库
./med-exam quiz -b 内科.mqb -b 外科.mqb              # 多题库
./med-exam quiz -b 内科.mqb --host 0.0.0.0           # 局域网共享
./med-exam quiz -b 内科.mqb --host 0.0.0.0 --port 8080 --no-browser  # 服务器部署
./med-exam quiz -b 内科.mqb --pin 12345678           # 自定义访问码
./med-exam quiz -b 内科.mqb --no-pin                 # 禁用访问码（内网可信环境）
```

### `edit` — 题库编辑器

```bash
./med-exam edit -b 内科.mqb
```

### `build` — 构建题库

```bash
./med-exam build -i data/raw -o data/output/内科学
./med-exam build -i data/raw -o data/output/内科学 --password 密码
```

### `export` — 导出

```bash
./med-exam export -b 内科.mqb -f xlsx
./med-exam export -b 内科.mqb -f xlsx -f docx -f pdf
```

### `enrich` — AI 补全答案/解析

```bash
./med-exam enrich -b 内科.mqb --ai-provider deepseek --ai-key sk-xxx
```

---

## Web 应用功能说明

启动 `quiz` 命令后，浏览器访问 `http://127.0.0.1:5174`。

### 三种学习模式

| 模式 | 特点 |
|------|------|
| **练习模式** | 答题后即时显示对错和解析，适合日常学习 |
| **考试模式** | 计时限时，交卷后统一评分，模拟正式考试 |
| **背题模式** | 卡片翻转，自评掌握程度，SM-2 智能复习 |

### 键盘快捷键（练习/考试模式）

| 按键 | 功能 |
|------|------|
| `1`–`5` / `A`–`E` | 选择对应选项 |
| `←` / `→` | 上一题 / 下一题 |
| `M` / `Space` | 标记/取消标记当前题目 |
| `E` | 展开/定位到解析区 |
| `F` | 同 M（向后兼容） |
| `Enter` | 下一题 |
| `Esc` | 关闭弹窗 |

### AI 答疑

配置 `ai_api_key` 后，做题时每道题右下角出现「AI 帮你解析」按钮，支持多轮追问。

### 语音输入

配置 `asr_api_key`（阿里云 DashScope）后，AI 答疑输入框支持**长按发送按钮**触发语音识别，实时转写到输入框。

### PWA 安装

- **Android / Chrome**：地址栏右侧点击「安装到主屏幕」（或 topbar 📲 按钮）
- **iPhone / Safari**：底部工具栏点击「分享」→「添加到主屏幕」

---

## 完整命令参数

| 命令 | 关键参数 |
|------|---------|
| `quiz` | `-b FILE` 题库路径（可多次）；`--port`；`--host`；`--pin`；`--no-pin`；`--no-record`；`--ai-*`；`--asr-*` |
| `edit` | `-b FILE`；`--port`；`--host` |
| `build` | `-i DIR` 输入目录；`-o PATH` 输出路径；`--password`；`--strategy strict/content` |
| `export` | `-b FILE`；`-f FORMAT`（可多次）；`--mode`；`--unit`；`--keyword` |
| `info` | `-b FILE` |
| `enrich` | `-b FILE`；`--ai-provider`；`--ai-key`；`--limit`；`--dry-run` |
| `inspect` | `-b FILE`；`--missing`；`--has-ai`；`--keyword`；`--limit` |
| `generate` | 随机组卷导出 Word，见 `--help` |

详细参数：`./med-exam <命令> --help`
