# `med-exam` 命令行工具使用手册

`med-exam` 是一个专业的医学考试题目处理工具，支持题目去重、过滤、题库构建、自动组卷及多格式导出功能。基于 Click 框架构建，提供清晰易用的命令行接口。

## 📌 目录

- [全局选项](#-全局选项)
- [子命令详解](#-子命令详解)
  - [`export` - 题目导出](#export---题目导出)
  - [`generate` - 自动组卷](#generate---自动组卷)
  - [`build` - 构建题库缓存](#build---构建题库缓存)
  - [`info` - 题库统计](#info---题库统计)
  - [`enrich` - AI 解析补全](#enrich---ai-解析补全)
  - [`inspect` - 查看题库内容](#inspect---查看题库内容)
  - [`edit` - Web 编辑器](#edit---web-编辑器)
- [配置文件说明](#️-配置文件说明)
- [典型工作流示例](#-典型工作流示例)
- [安全提示](#-安全提示)

---

## 🌐 全局选项

所有子命令均支持以下全局选项：

```bash
med-exam --version            # 查看版本号
med-exam --help               # 查看帮助
-c, --config CONFIG_PATH      # 配置文件路径（默认: config.yaml）
```

> 💡 配置文件采用 YAML 格式，可集中管理输入/输出路径、解析器映射、导出格式、AI 参数等。

---

## 🔍 子命令详解

### `export` - 题目导出

**功能**：加载题目 → 去重 → 过滤 → 导出为多种格式（CSV/XLSX/DOCX/PDF/数据库）

```bash
med-exam export [OPTIONS]
```

#### 核心选项

| 选项 | 说明 | 默认值 |
|------|------|--------|
| `-i, --input-dir PATH` | JSON 题目源目录 | `./data/raw`（或配置文件指定） |
| `-o, --output-dir PATH` | 导出目标目录 | `./data/output` |
| `-f, --format FORMAT` | 导出格式（可多次指定）<br>支持：`csv` / `xlsx` / `docx` / `pdf` / `db` | `xlsx` |
| `--split-options` / `--merge-options` | 选项列处理方式：<br>• `--split-options`（默认）：每选项独立列（A/B/C/D 各一列）<br>• `--merge-options`：合并为单列 | 拆分 |
| `--dedup` / `--no-dedup` | 是否执行去重 | 启用 |
| `--strategy [content\|strict]` | 去重策略：<br>• `content`：仅比对题干<br>• `strict`：题干+选项+答案全匹配 | `strict` |
| `--db-url CONNECTION_STRING` | 数据库连接串（导出为 `db` 格式时必需） | 从配置文件读取 |
| `--mode MODE` | 按题型过滤（可多次指定，如 `--mode A1型题 --mode A2型题`） | 无 |
| `--unit UNIT` | 按章节关键词过滤（可多次指定） | 无 |
| `--keyword TEXT` | 题干关键词搜索 | 无 |
| `--min-rate INT` | 最低正确率（0-100） | 0 |
| `--max-rate INT` | 最高正确率（0-100） | 100 |
| `--stats` / `--no-stats` | 是否显示统计摘要 | 显示 |
| `--bank PATH` | 从 `.mqb` 题库文件直接加载（跳过 JSON 解析） | 无 |
| `--password TEXT` | 题库解密密码（加密题库必需） | 无 |

#### 使用示例

```bash
# 基础导出（XLSX 格式）
med-exam export -i ./raw_data -o ./exports

# 多格式导出 + 题型过滤
med-exam export -f csv -f xlsx --mode A1型题 --mode A2型题

# 导出到数据库
med-exam export -f db --db-url "sqlite:///exam.db"

# 从加密题库导出
med-exam export --bank questions.mqb --password "secret123"

# 合并选项列 + 关键词过滤
med-exam export --merge-options --keyword "心肌梗死" --min-rate 70
```

---

### `generate` - 自动组卷

**功能**：从题库随机抽题生成标准化考试试卷（Word 格式）

```bash
med-exam generate [OPTIONS]
```

#### 核心选项

| 选项 | 说明 | 默认值 |
|------|------|--------|
| `-i, --input-dir PATH` | 题目源目录 | `./data/raw` |
| `-o, --output PATH` | 试卷输出路径（不含扩展名，自动添加 `.docx`） | `./data/output/exam` |
| `--title TEXT` | 试卷标题 | `模拟考试` |
| `--subtitle TEXT` | 副标题 | 空 |
| `--cls TEXT` | 限定题库分类（可多次指定） | 无 |
| `--unit TEXT` | 限定章节范围（可多次指定） | 无 |
| `--mode TEXT` | 限定题型范围（可多次指定） | 无 |
| `-n, --count INT` | 总题目数量 | 50 |
| `--count-mode [sub\|question]` | 计数模式：<br>• `sub`（默认）：按小题计数<br>• `question`：按大题计数 | `sub` |
| `--per-mode TEXT` | 按题型精确分配数量：<br>格式1（JSON）：`'{"A1型题":30,"A2型题":20}'`<br>格式2（简写）：`A1型题:30,A2型题:20` | 无（均匀抽样） |
| `--difficulty TEXT` | 按难度比例分配题目：<br>格式：`easy:20,medium:40,hard:30,extreme:10`<br>• `easy`：正确率 ≥ 80%<br>• `medium`：60%–80%<br>• `hard`：40%–60%<br>• `extreme`：< 40% | 无（均匀抽样） |
| `--difficulty-mode [global\|per_mode]` | 难度分配策略：<br>• `global`（默认）：先按难度后按题型<br>• `per_mode`：先按题型后按难度 | `global` |
| `--seed INT` | 随机种子（固定值可复现相同试卷） | 无 |
| `--show-answers` / `--hide-answers` | 题目中是否显示答案 | 隐藏 |
| `--answer-sheet` / `--no-answer-sheet` | 是否在末尾生成答案页 | 生成 |
| `--show-discuss` / `--no-discuss` | 答案页是否包含解析 | 隐藏 |
| `--total-score INT` | 试卷总分 | 100 |
| `--score FLOAT` | 每题分值（0=不显示分值，不指定则由总分自动计算） | 自动计算 |
| `--time-limit INT` | 考试时长（分钟） | 120 |
| `--dedup` / `--no-dedup` | 组卷前是否去重 | 启用 |
| `--bank PATH` | 从 `.mqb` 题库加载 | 无 |
| `--password TEXT` | 题库解密密码 | 无 |

#### 使用示例

```bash
# 生成 50 题标准试卷
med-exam generate --title "内科模拟考试" -n 50

# 按题型精确分配 + 显示答案
med-exam generate --per-mode 'A1型题:30,A2型题:15,B1型题:5' --show-answers

# 限定章节 + 固定随机种子（可复现）
med-exam generate --unit "心血管系统" --unit "呼吸系统" --seed 42

# 从题库生成带解析的答案页
med-exam generate \
  --bank questions.mqb \
  --password "secret123" \
  --answer-sheet \
  --show-discuss \
  --score 1.5 \
  --time-limit 90
```

---

### `build` - 构建题库缓存

**功能**：将分散的 JSON 题目合并为加密/非加密的 `.mqb` 题库文件（支持增量追加）

```bash
med-exam build [OPTIONS]
```

#### 核心选项

| 选项 | 说明 | 默认值 |
|------|------|--------|
| `-i, --input-dir PATH` | JSON 题目源目录 | `./data/raw` |
| `-o, --output PATH` | 输出路径（自动添加 `.mqb` 后缀） | `./data/output/questions` |
| `--password TEXT` | 加密密码（留空则不加密） | 无 |
| `--strategy [content\|strict]` | 去重策略 | `strict` |
| `--rebuild` | 强制重建（忽略已有题库，全量重写） | 否 |

#### 使用示例

```bash
# 构建非加密题库
med-exam build -i ./raw_data -o ./bank/medical_questions

# 构建加密题库
med-exam build --password "MySecurePass2026"

# 增量追加新题目（自动去重，检测到已有 .mqb 时自动追加）
med-exam build

# 强制重建题库
med-exam build --rebuild
```

> 💡 `.mqb` 是二进制格式，支持密码保护，适合长期存储和快速加载。

---

### `info` - 题库统计

**功能**：快速查看题库统计信息（题型分布、章节覆盖、正确率分布等）

```bash
med-exam info [OPTIONS]
```

#### 核心选项

| 选项 | 说明 | 默认值 |
|------|------|--------|
| `-i, --input-dir PATH` | JSON 题目源目录 | `./data/raw` |
| `--bank PATH` | 从 `.mqb` 题库加载 | 无 |
| `--password TEXT` | 题库解密密码 | 无 |

#### 使用示例

```bash
# 查看原始 JSON 目录统计
med-exam info -i ./raw_data

# 查看加密题库统计
med-exam info --bank questions.mqb --password "secret123"
```

---

### `enrich` - AI 解析补全

**功能**：调用 AI 大模型为缺少答案或解析的题目自动补全内容，支持 OpenAI、DeepSeek、Qwen 等主流 Provider，以及推理模型和混合思考模式。

```bash
med-exam enrich [OPTIONS]
```

#### 数据来源（二选一）

| 选项 | 说明 |
|------|------|
| `--bank PATH` | 从已有 `.mqb` 题库文件读取 |
| `-i, --input-dir PATH` | 从 JSON 原始文件目录读取（自动去重） |

#### 输出规则

| 用法 | 效果 |
|------|------|
| 默认 | AI 结果存入 `ai_answer`/`ai_discuss` 字段，另存为 `*_ai.mqb` |
| `--apply-ai` | 同时写入 `answer`/`discuss` 正式字段 |
| `--apply-ai --in-place` | 就地覆盖原 `.mqb` 文件 |
| `-i + --write-json` | 结果写回每个原始 JSON 文件 |

#### 核心选项

| 选项 | 说明 | 默认值 |
|------|------|--------|
| `--provider TEXT` | AI 服务商：`openai` / `deepseek` / `qwen` / `qwen-intl` / `ollama` | `openai` |
| `--model TEXT` | 模型名称 | `gpt-4o` |
| `--api-key TEXT` | API Key（也可用环境变量 `OPENAI_API_KEY`） | 环境变量 |
| `--base-url TEXT` | 自定义 API Base URL | Provider 默认值 |
| `--max-workers INT` | 并发请求数（推理/思考模型建议 1~2） | 4 |
| `--resume` / `--no-resume` | 是否断点续跑 | 启用 |
| `--checkpoint-dir PATH` | 断点文件存储目录 | `data/checkpoints` |
| `--mode MODE` | 仅处理指定题型（可多次指定） | 无 |
| `--unit UNIT` | 仅处理包含关键词的章节（可多次指定） | 无 |
| `--limit INT` | 最多处理多少小题（0=不限制） | 0 |
| `--dry-run` | 仅预览待处理列表，不实际调用 AI | 否 |
| `--only-missing` / `--force` | 仅补缺失字段（默认）/ 强制重新生成所有 | `--only-missing` |
| `--apply-ai` | 将 AI 结果写入 `answer`/`discuss` 正式字段 | 否 |
| `--in-place` | 就地修改原 `.mqb` 文件（配合 `--apply-ai`） | 否 |
| `--write-json` | `-i` 模式下将结果写回原始 JSON 文件 | 否 |
| `--timeout FLOAT` | 请求超时秒数（0=自动：推理模型 180s，普通 60s） | 自动 |
| `--thinking` / `--no-thinking` | 混合思考模型（如 Qwen3）是否开启深度思考 | 否 |

#### 模型类型与参数建议

| 模型类型 | 代表模型 | 建议参数 |
|---------|---------|---------|
| 普通模型 | `gpt-4o`、`deepseek-chat` | 默认并发 4 |
| 纯推理模型 | `o3-mini`、`deepseek-reasoner` | `--max-workers 1` `--timeout 180` |
| 混合思考模型 | `qwen3-235b-a22b` | 思考模式加 `--thinking --max-workers 1` |

#### 使用示例

```bash
# 使用 OpenAI 补全（仅补缺失字段）
med-exam enrich --bank data/output/题库.mqb --api-key sk-xxx

# 使用 DeepSeek（国内访问更稳定）
med-exam enrich --bank data/output/题库.mqb \
  --provider deepseek --model deepseek-chat --api-key sk-xxx

# DeepSeek-R1 深度推理
med-exam enrich --bank data/output/题库.mqb \
  --provider deepseek --model deepseek-reasoner \
  --max-workers 1 --api-key sk-xxx

# Qwen3 混合思考模型（开启深度思考）
med-exam enrich --bank data/output/题库.mqb \
  --provider qwen --model qwen3-235b-a22b \
  --thinking --max-workers 1 --api-key sk-xxx

# Qwen3 关闭思考（更快，适合大批量）
med-exam enrich --bank data/output/题库.mqb \
  --provider qwen --model qwen3-235b-a22b \
  --no-thinking --api-key sk-xxx

# 本地 Ollama 模型
med-exam enrich --bank data/output/题库.mqb \
  --provider ollama --model qwen2.5:14b

# 写入正式字段并覆盖原文件
med-exam enrich --bank data/output/题库.mqb \
  --apply-ai --in-place --api-key sk-xxx

# 仅处理某章节，预览不实际调用
med-exam enrich --bank data/output/题库.mqb \
  --unit "口腔修复学" --dry-run --api-key sk-xxx

# 断点续跑（中断后继续上次进度）
med-exam enrich --bank data/output/题库.mqb \
  --resume --api-key sk-xxx
```

> 💡 API Key 建议通过环境变量传入：
> ```bash
> # Windows PowerShell
> $env:OPENAI_API_KEY = "sk-xxx"
> # macOS / Linux
> export OPENAI_API_KEY="sk-xxx"
> ```

---

### `inspect` - 查看题库内容

**功能**：在命令行中查看 `.mqb` 题库的题目内容，支持多维度过滤与搜索，并显示 AI 补全状态。

```bash
med-exam inspect [OPTIONS]
```

#### 核心选项

| 选项 | 说明 | 默认值 |
|------|------|--------|
| `--bank PATH` | `.mqb` 题库路径（必填） | — |
| `--password TEXT` | 题库解密密码 | 无 |
| `--mode MODE` | 按题型过滤（可多次指定） | 无 |
| `--unit UNIT` | 按章节关键词过滤（可多次指定） | 无 |
| `--keyword TEXT` | 题干或题目关键词搜索 | 无 |
| `--has-ai` | 只显示含 AI 补全内容的题 | 否 |
| `--missing` | 只显示缺答案或缺解析的题 | 否 |
| `--limit INT` | 最多显示多少小题（0=全部） | 20 |
| `--full` | 显示完整解析（默认截断至 150 字） | 否 |
| `--show-ai` | 同时显示 AI 原始输出，方便与官方内容对比 | 否 |

#### 答案来源标记说明

| 标记 | 含义 |
|------|------|
| ✅ | 该字段有官方内容 |
| 🤖 | 仅有 AI 补全内容 |
| ❓ | 字段为空 |
| `[AI]` | 该小题存在 AI 补全数据 |
| `(AI)` | 该字段的有效值来自 AI（官方字段为空，或 AI 与官方一致） |

#### 使用示例

```bash
# 查看题库概览及前 20 条题目
med-exam inspect --bank data/output/题库.mqb

# 只看缺答案或缺解析的题（全部显示）
med-exam inspect --bank data/output/题库.mqb --missing --limit 0

# 查看 AI 补全内容，对比官方与 AI 原文
med-exam inspect --bank data/output/题库.mqb --has-ai --show-ai

# 搜索关键词，显示完整解析
med-exam inspect --bank data/output/题库.mqb \
  --keyword 心肌梗死 --full

# 按题型和章节双重过滤
med-exam inspect --bank data/output/题库.mqb \
  --mode A1型题 --unit 口腔修复学 --limit 10
```

---

### `edit` - Web 编辑器

**功能**：启动本地 Web 服务器，在浏览器中可视化编辑 `.mqb` 题库，支持修改题目内容、批量替换文本、删除题目等操作。

> ⚠️ 需要额外安装 Flask：`pip install flask`

```bash
med-exam edit [OPTIONS]
```

#### 核心选项

| 选项 | 说明 | 默认值 |
|------|------|--------|
| `--bank PATH` | `.mqb` 题库路径（必填） | — |
| `--password TEXT` | 题库解密密码 | 无 |
| `--port INT` | 本地端口 | 5173 |
| `--no-browser` | 不自动打开浏览器 | 否 |

#### 编辑器功能

| 功能 | 说明 |
|------|------|
| 搜索/过滤 | 按关键词、题型、章节实时筛选，支持「含 AI 内容」「缺答案/解析」快速过滤 |
| 编辑题目 | 修改题目文字、选项、答案、解析、考点、题型、章节等所有字段 |
| AI 内容对比 | AI 字段高亮显示（橙色），原始 AI 解析单独展示供参考 |
| 批量替换 | 支持指定字段（题目/解析/答案/考点）和范围（题型/章节）的全局文本替换 |
| 删除题目 | 带确认弹窗防止误删 |
| 快捷保存 | 顶栏保存按钮或 **Ctrl+S**，未保存时顶栏显示橙点提示 |

#### 使用示例

```bash
# 启动编辑器（自动打开浏览器）
med-exam edit --bank data/output/题库.mqb

# 加密题库
med-exam edit --bank data/output/题库.mqb --password "secret123"

# 指定端口，不自动打开浏览器
med-exam edit --bank data/output/题库.mqb --port 8080 --no-browser
# 然后手动访问 http://127.0.0.1:8080
```

> 按 **Ctrl+C** 退出编辑器服务。

---

## ⚙️ 配置文件说明 (`config.yaml`)

配置文件可简化命令行参数，推荐结构：

```yaml
# 基础路径配置
input_dir: "./data/raw"      # 默认输入目录
output_dir: "./data/output"  # 默认输出目录
dedup_strategy: "strict"     # 全局去重策略

# APP 包名 → 解析器映射（用于自动识别 JSON 来源）
parser_map:
  com.ahuxueshu: "ahuyikao"
  com.yikaobang.yixue: "yikaobang"

# 导出配置
export:
  formats: ["xlsx", "csv"]   # 默认导出格式
  database:
    url: "sqlite:///exam.db" # 数据库连接串

# AI 补全配置（可省略，也可在命令行传入）
ai:
  provider: "deepseek"
  model: "deepseek-chat"
  api_key: ""               # 建议留空，用环境变量 OPENAI_API_KEY 传入
  base_url: ""
  max_workers: 4
  checkpoint_dir: "data/checkpoints"
```

> 💡 命令行参数优先级高于配置文件，可随时覆盖配置值。

---

## 🚀 典型工作流示例

### 场景一：完整流程（爬取 → 构建 → 补全 → 导出）

```bash
# 1. 构建加密题库
med-exam build \
  -i ./data/raw \
  -o ./data/output/medical_2026 \
  --password "Exam2026"

# 2. AI 补全缺失解析（断点续跑，直接覆盖原题库）
med-exam enrich \
  --bank ./data/output/medical_2026.mqb \
  --password "Exam2026" \
  --provider deepseek --model deepseek-chat \
  --apply-ai --in-place

# 3. 查看补全效果
med-exam inspect \
  --bank ./data/output/medical_2026.mqb \
  --password "Exam2026" \
  --has-ai --show-ai --limit 5

# 4. 导出全量 Excel
med-exam export \
  --bank ./data/output/medical_2026.mqb \
  --password "Exam2026" \
  -f xlsx
```

### 场景二：生成带解析的模拟试卷

```bash
med-exam generate \
  --bank ./data/output/medical_2026.mqb \
  --password "Exam2026" \
  --title "2026执业医师冲刺卷" \
  --per-mode 'A1型题:40,A2型题:30,B1型题:20' \
  --answer-sheet \
  --show-discuss \
  --score 1.0 \
  --time-limit 150 \
  -o ./papers/final_mock
```

### 场景三：在浏览器中批量修正错误解析

```bash
# 启动编辑器
med-exam edit --bank ./data/output/medical_2026.mqb

# 浏览器中：顶栏「批量替换」→ 查找"参见教材" → 替换为空字符串
# 字段选择「解析」，范围不限 → 执行替换 → Ctrl+S 保存
```

### 场景四：快速筛选高正确率题目用于教学

```bash
med-exam export \
  --mode A2型题 \
  --unit "消化系统" \
  --min-rate 85 \
  --max-rate 100 \
  -f docx \
  -o ./teaching/high_accuracy_questions
```

---

## 🔐 安全提示

- 题库加密使用 **AES-256** 算法，密码需妥善保管，遗失后无法恢复。
- API Key 建议通过环境变量传入，避免明文写入命令行历史或配置文件。
- 导出数据库时注意连接串安全性（避免明文密码泄露）。
- 敏感题库文件建议设置文件系统权限控制。

> 本工具专为医学教育场景设计，支持题型标准化处理（A1/A2/A3/A4/B1 等）。