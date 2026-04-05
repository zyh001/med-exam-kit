# Scraper 爬虫模块

本目录包含 ehafo 题库爬虫的核心实现，所有敏感信息已脱敏处理。

> **注意**：使用前需将 `RC4_KEY`、`API_BASE` 等占位符替换为实际值。

## 文件说明

| 文件 | 说明 |
|------|------|
| `auth_manager.py` | 认证管理器 — 浏览器扫码登录、sessionid 缓存、cid 选择 |
| `scraper.py` | 完整爬虫 — RC4 解密、字段还原、SQLite 存储 |
| `mitmproxy_capture.py` | mitmproxy 插件 — API 抓包、自动生成报告 |
| `run.py` | 一键启动入口 — 自动完成认证 → 爬取 → 存储 |

## 快速开始

```bash
# 安装依赖
pip install requests playwright mitmproxy
playwright install chromium

# 一键运行
python run.py

# 检查 session 状态
python run.py --check

# 强制重新登录
python run.py --relogin

# 清除登录缓存
python run.py --clear
```

## 使用 mitmproxy 抓包

```bash
# Web 界面（推荐）
mitmweb -s mitmproxy_capture.py

# 命令行模式
mitmdump -s mitmproxy_capture.py
```

抓包完成后自动生成：
- `ehafo_apis.json` — 所有请求的完整记录
- `ehafo_report.md` — 接口汇总报告

## 模块依赖关系

```
run.py
    ├── auth_manager.py  → 提供 sessionid、cid
    └── scraper.py       → 提供爬虫逻辑
```

## 需要替换的占位符

| 文件 | 占位符 | 说明 |
|------|--------|------|
| `scraper.py` | `YOUR_RC4_KEY_HERE` | RC4 解密密钥 |
| `scraper.py` | `API_BASE` | API 基础地址 |
| `auth_manager.py` | `API_BASE` | API 基础地址 |
| `auth_manager.py` | `LOGIN_URL` | 微信登录页地址 |

## 输出文件

| 文件 | 说明 |
|------|------|
| `ehafo.db` | SQLite 数据库 |
| `scraper.log` | 运行日志 |
| `~/.ehafo_session.json` | 登录凭据缓存 |
| `ehafo_apis.json` | 抓包记录 |
| `ehafo_report.md` | 抓包报告 |

## 数据库表结构

| 表名 | 说明 |
|------|------|
| `subjects` | 科目 |
| `chapters` | 章 |
| `sections` | 节 |
| `questions` | 题目（含题干、选项、答案、解析） |
| `daily_records` | 每日一练记录 |

## API 接口

| 接口 | 说明 |
|------|------|
| `App.Struct.getStructs` | 获取科目/章/节结构树 |
| `App.Daily.getMultipleTikuQuestion` | 获取章节题目（加密） |
| `App.Daydayup.getQuestions` | 获取每日一练题目（明文） |
| `App.Common.getServerTime` | 获取服务器时间（用于验证 session） |
