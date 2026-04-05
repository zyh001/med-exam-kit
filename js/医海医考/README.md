# 医某医考 API 逆向工程

对 uni-app 医学考试 APP 的完整逆向，还原了全套 AES 加密体系，实现了纯 Python API 客户端。

## 项目结构

```
├── yhyk_sdk.py                         # Python SDK (可直接使用)
├── demo.py                             # 使用示例
├── health_check.py                     # 健康检查脚本
├── docs/
│   ├── OpenAPI完整文档.md               # 13个API接口文档 + 请求流程
│   ├── 逆向工程详解_博客文章.md           # 完整逆向思路和过程
│   └── 版本升级应对指南.md               # APP升级后的应对策略
└── scripts/
    ├── hook_capture.js                  # Frida Hook: 抓取配对数据
    ├── decode_obfuscation.js            # Node.js: 混淆字符串解码器
    └── analyze_capture.py               # Python: 分析抓包数据
```

## 文档链接

- [逆向工程详解](./docs/逆向工程详解_博客文章.md) — 完整逆向思路和过程
- [OpenAPI 完整文档](./docs/OpenAPI完整文档.md) — 13个API接口文档 + 请求流程
- [版本升级应对指南](./docs/版本升级应对指南.md) — APP升级后的应对策略

## 快速开始

```bash
pip install pycryptodome requests
python demo.py
```

## 破解的密钥

| 参数 | 值 | 来源 |
|------|-----|------|
| AES Key | `cfd0f9faab3726e5` | `MD5("AppVerify")[7:23]` |
| AES IV | `yhyk2026xzdlcxwc` | 硬编码 |
| unique_code | `fe514637e1eb8c298e3e5c2b35fbb786` | `MD5("__UNI__7EE4208-yhyk2026-")` |

## 技术栈

- 目标: DCloud uni-app (Vue 3 + Weex V8)
- 加密: AES-128-CBC + PKCS7 + MD5 密钥派生
- 工具: apktool, Frida, Node.js, Python

## 免责声明

本项目仅用于安全研究和个人学习目的。
