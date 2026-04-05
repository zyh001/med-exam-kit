#!/usr/bin/env python3
"""
SDK 健康检查 — 检测 API 是否仍可正常调用
用法: python health_check.py <phone> <password>
配合 cron 定时运行，失败时检查是否需要升级 SDK
"""
import sys
from yhyk_sdk import YhykClient

if len(sys.argv) < 3:
    print("用法: python health_check.py <phone> <password>")
    sys.exit(1)

c = YhykClient()
r = c.login(sys.argv[1], sys.argv[2])
code = r.get('code')

if code == 200:
    u = c.get_user_info()
    if u.get('code') == 200:
        print("✅ 登录+接口全部正常")
        sys.exit(0)
    else:
        print(f"⚠️ 登录成功但接口异常: code={u.get('code')}, msg={u.get('msg')}")
        sys.exit(2)
elif code == 1011:
    print("❌ 签名错误 — APP 可能升级了")
    print("   请参考 docs/版本升级应对指南.md")
    sys.exit(1)
elif code == 1012:
    print("⚠️ 请求过期 — 检查系统时间是否准确")
    sys.exit(2)
else:
    print(f"❓ 未知: code={code}, msg={r.get('msg')}")
    sys.exit(3)
