# `med-exam` 命令行工具使用手册

`med-exam` 是一个专业的医学考试题目处理工具，支持题目去重、过滤、题库构建、自动组卷及多格式导出功能。基于 Click 框架构建，提供清晰易用的命令行接口。

## 📌 目录

- [全局选项](#-全局选项)
- [子命令详解](#-子命令详解)
  - [`export` - 题目导出](#export---题目导出)
  - [`generate` - 自动组卷](#generate---自动组卷)
  - [`build` - 构建题库缓存](#build---构建题库缓存)
  - [`info` - 题库统计](#info---题库统计)
- [配置文件说明](#️-配置文件说明)
- [典型工作流示例](#-典型工作流示例)
- [安全提示](#-安全提示)

---

## 🌐 全局选项

所有子命令均支持以下全局选项：

```bash
-c, --config CONFIG_PATH  # 配置文件路径（默认: config.yaml）
```

> 💡 配置文件采用 YAML 格式，可集中管理输入/输出路径、解析器映射、导出格式等参数。

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
| `-f, --format FORMAT` | 导出格式（可多次指定）<br>支持：`csv`/`xlsx`/`docx`/`pdf`/`db` | `xlsx` |
| `--split-options` / `--merge-options` | 选项列处理方式：<br>• `--split-options`（默认）：每选项独立列（A/B/C/D 各一列）<br>• `--merge-options`：合并为单列 | 拆分 |
| `--dedup` / `--no-dedup` | 是否执行去重 | 启用 |
| `--strategy [content\|strict]` | 去重策略：<br>• `content`：仅比对题干<br>• `strict`：题干+选项+答案全匹配 | `strict` |
| `--db-url CONNECTION_STRING` | 数据库连接串（导出为 `db` 格式时必需） | 从配置文件读取 |
| `--mode MODE` | 按题型过滤（可多次指定，如 `--mode A1 --mode B1`） | 无 |
| `--unit UNIT` | 按章节关键词过滤（可多次指定） | 无 |
| `--keyword TEXT` | 题干关键词搜索 | 无 |
| `--min-rate INT` | 最低正确率（0-100） | 0 |
| `--max-rate INT` | 最高正确率（0-100） | 100 |
| `--stats` / `--no-stats` | 是否显示统计摘要 | 显示 |
| `--bank PATH` | 从 `.mqb` 题库文件直接加载（跳过 JSON 解析） | 无 |
| `--password TEXT` | 题库解密密码（加密题库必需） | 无 |

#### 使用示例

```bash
# 基础导出（XLSX格式）
med-exam export -i ./raw_data -o ./exports

# 多格式导出 + 题型过滤
med-exam export -f csv -f xlsx --mode A1型题 --mode A2型题

# 导出到数据库（需配置连接串）
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
| `--subtitle TEXT` | 副标题（如"2026年执业医师模拟卷"） | 空 |
| `--unit UNIT` | 限定章节范围（可多次指定） | 无 |
| `--mode MODE` | 限定题型范围（可多次指定） | 无 |
| `-n, --count INT` | 总题目数量 | 50 |
| `--per-mode TEXT` | 按题型精确分配数量：<br>格式1（JSON）：`'{"A1型题":30,"A2型题":20}'`<br>格式2（简写）：`A1型题:30,A2型题:20` | 无（均匀抽样） |
| `--seed INT` | 随机种子（固定值可复现相同试卷） | 无 |
| `--show-answers` / `--hide-answers` | 题目中是否显示答案 | 隐藏 |
| `--answer-sheet` / `--no-answer-sheet` | 是否在末尾生成答案页 | 生成 |
| `--show-discuss` / `--no-discuss` | 答案页是否包含解析 | 隐藏 |
| `--score FLOAT` | 每题分值（0=不显示分值） | 2.0 |
| `--time-limit INT` | 考试时长（分钟） | 120 |
| `--dedup` / `--no-dedup` | 组卷前是否去重 | 启用 |
| `--bank PATH` | 从 `.mqb` 题库加载 | 无 |
| `--password TEXT` | 题库解密密码 | 无 |

#### 使用示例

```bash
# 生成50题标准试卷
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
| `--rebuild` | 强制重建（忽略已有题库，全量重写） | 无 |

#### 使用示例

```bash
# 构建非加密题库
med-exam build -i ./raw_data -o ./bank/medical_questions

# 构建加密题库
med-exam build --password "MySecurePass2026"

# 增量追加新题目（自动去重）
med-exam build  # 检测到 existing.mqb 后自动追加

# 强制重建题库
med-exam build --rebuild
```

> 💡 题库文件（`.mqb`）是二进制格式，支持密码保护，适合长期存储和快速加载。

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
# 查看原始JSON目录统计
med-exam info -i ./raw_data

# 查看加密题库统计
med-exam info --bank questions.mqb --password "secret123"
```

> ✅ 输出包含：总题数、题型分布、章节分布、难度分布、正确率统计等。

---

## ⚙️ 配置文件说明 (`config.yaml`)

配置文件可简化命令行参数，推荐结构：

```yaml
# 基础路径配置
input_dir: "./data/raw"      # 默认输入目录
output_dir: "./data/output"  # 默认输出目录
dedup_strategy: "strict"     # 全局去重策略

# APP包名 → 解析器映射（用于自动识别JSON来源）
parser_map:
  com.ahuxueshu: "ahuyikao"
  com.yikaobang.yixue: "yikaobang"

# 导出配置
export:
  formats: ["xlsx", "csv"]   # 默认导出格式
  database:
    url: "sqlite:///exam.db" # 数据库连接串
```

> 💡 命令行参数优先级高于配置文件，可覆盖配置值。

---

## 🚀 典型工作流示例

### 场景：构建题库 → 生成模拟试卷 → 导出分析报表

```bash
# 1. 构建加密题库（增量追加）
med-exam build \
  -i ./raw_questions \
  -o ./bank/2026_medical \
  --password "Exam2026"

# 2. 生成带答案页的模拟试卷
med-exam generate \
  --bank ./bank/2026_medical.mqb \
  --password "Exam2026" \
  --title "2026执业医师冲刺卷" \
  --per-mode 'A1型题:40,A2型题:30,B1型题:20' \
  --answer-sheet \
  --show-discuss \
  --score 1.0 \
  --time-limit 150 \
  -o ./papers/final_mock

# 3. 导出全量题目供数据分析
med-exam export \
  --bank ./bank/2026_medical.mqb \
  --password "Exam2026" \
  -f csv -f xlsx \
  -o ./analysis/dataset
```

### 场景：快速筛选高正确率题目用于教学

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

- 题库加密使用 **AES-256** 算法，密码需妥善保管
- 导出数据库时注意连接串安全性（避免明文密码泄露）
- 生产环境建议使用交互式密码输入（而非命令行明文）
- 敏感题库文件建议设置文件系统权限控制

> 本工具专为医学教育场景设计，符合《医学考试命题规范》要求，支持题型标准化处理（A1/A2/A3/A4/B1等）。