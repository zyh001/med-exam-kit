# Android App 逆向工程实战 — 源码包

> 配套博客文章：《Android App 逆向工程全流程实战：从 APK 解包到题库解密》  
> **所有域名、密钥、用户标识均已脱敏，请勿用于任何非法用途。**

## 目录结构

```
├── docs/
│   ├── openapi.json               OpenAPI 3.0 接口文档（脱敏）
│   └── blog_reverseengineering.md 逆向思路博客正文
│
├── sdk/
│   ├── docryptor.py               RC4 解密模块（Python 复现）
│   └── field_normalizer.py        题目字段标准化器
│
├── scraper/
│   ├── README.md                  爬虫模块使用说明
│   ├── mitmproxy_capture.py       mitmproxy 抓包插件
│   ├── auth_manager.py            登录认证管理（Playwright 自动化）
│   ├── scraper.py                 题库爬虫主程序（RC4解密版）
│   └── run.py                     一键启动入口
│
├── demo/
│   ├── README.md                  演示脚本使用说明
│   ├── ehafo_auth.py              认证流程演示
│   ├── ehafo_scraper_v2.py        爬虫演示版本
│   └── run_scraper.py             爬虫运行示例
│
├── apk_upgrade_guide.docx         APK 升级指南
└── README.md
```

## 博客文章

详细逆向过程请参阅：[Android App 逆向工程全流程实战](./docs/blog_reverseengineering.md)

## 快速开始

```bash
# 安装依赖
pip install requests playwright mitmproxy
playwright install chromium

# 方式一：mitmproxy 抓包
mitmweb -s scraper/mitmproxy_capture.py

# 方式二：直接爬取（需要有效 session）
python scraper/run.py

# 查看 session 状态
python scraper/run.py --check

# 重新登录
python scraper/run.py --relogin
```

## 使用 SDK 解密单个题目

```python
from sdk.docryptor import decrypt, decrypt_question
from sdk.field_normalizer import normalize_question

# 解密（将 YOUR_RC4_KEY_HERE 替换为实际密钥）
encrypted = "X2SN6M8q..."
plain = decrypt(encrypted)
obj   = decrypt_question(encrypted)

# 字段标准化
q = normalize_question(obj)
print(f"QID: {q['qid']}")
print(f"题干: {q['question']}")
print(f"答案: {q['answer']}")
print(f"解析: {q.get('analysis', '暂无')}")
```

## 关键发现

| 项目 | 内容 |
|---|---|
| 接口框架 | PhalAPI，统一入口 `/phalapi/public/?service=Xxx` |
| 认证方式 | Body 传 `sessionid`（32位MD5），无固定过期时间 |
| 题目加密 | RC4 + 自定义Base64，密钥硬编码于前端 bundle.cache.js |
| 字段混淆 | 题目 JSON 字段名每题随机化，通过值的语义特征识别 |
| 抓包难度 | 极低（network_security_config 明确信任用户证书） |

## 法律与道德

本项目仅用于技术学习与安全研究。使用者需自行承担相关法律责任。
