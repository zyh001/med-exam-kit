# Demo 演示脚本

本目录包含 ehafo 题库爬虫的独立演示脚本，可脱离主项目单独运行。

## 文件说明

| 文件 | 说明 |
|------|------|
| `ehafo_auth.py` | 认证管理器 — 浏览器扫码登录、sessionid 缓存、cid 自动选择 |
| `ehafo_scraper_v2.py` | 完整爬虫 — RC4 解密、字段还原、数据库存储 |
| `run_scraper.py` | 一键启动入口 — 自动完成认证 → 爬取 → 存储 |

## 快速开始

```bash
# 安装依赖
pip install requests playwright
playwright install chromium

# 一键运行（首次会打开浏览器扫码登录）
python run_scraper.py

# 查看缓存状态
python run_scraper.py --check

# 强制重新登录
python run_scraper.py --relogin

# 清除登录缓存
python run_scraper.py --clear
```

## 单独使用认证模块

```python
from ehafo_auth import get_credentials, validate_session

# 获取凭据（自动登录或复用缓存）
sessionid, cid = get_credentials()

# 验证 sessionid 是否有效
if validate_session(sessionid):
    print("有效")
```

```bash
# 命令行独立运行
python ehafo_auth.py              # 登录并输出凭据
python ehafo_auth.py --check      # 检查缓存状态
python ehafo_auth.py --relogin    # 强制重新登录
python ehafo_auth.py --clear      # 清除缓存
```

## 单独使用爬虫模块

```python
from ehafo_scraper_v2 import Client, DB, Scraper, rc4_decrypt

# RC4 解密
plain = rc4_decrypt("加密字符串...")

# 完整爬取
client  = Client(sessionid="your_sessionid", cid="115")
db      = DB("ehafo.db")
scraper = Scraper(client, db)
scraper.run()
```

```bash
# 命令行独立运行（需手动提供 sessionid）
python ehafo_scraper_v2.py --sessionid YOUR_SESSION_ID --cid 115
```

## 输出文件

| 文件 | 说明 |
|------|------|
| `ehafo.db` | SQLite 数据库，存储题目、科目、章节结构 |
| `scraper.log` | 运行日志 |
| `~/.ehafo_session.json` | 登录凭据缓存 |

## 数据库表结构

- `subjects` — 科目
- `chapters` — 章
- `sections` — 节
- `questions` — 题目（含题干、选项、答案、解析）
- `daily_records` — 每日一练记录

## 依赖关系

```
run_scraper.py
    ├── ehafo_auth.py      → 提供 sessionid、cid
    └── ehafo_scraper_v2.py → 提供爬虫逻辑
```
