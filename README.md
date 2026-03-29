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
