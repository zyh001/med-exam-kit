#!/usr/bin/env python3
"""
医海医考 SDK 使用示例

pip install pycryptodome requests
python demo.py
"""

from yhyk_sdk import YhykClient
import json

def main():
    client = YhykClient()

    # ── 1. 登录 ──
    phone = input("手机号: ")
    password = input("密码: ")

    print("\n[1] 登录中...")
    r = client.login(phone, password)
    if r.get('code') != 200:
        print(f"  登录失败: {r.get('msg')}")
        return
    print(f"  登录成功! Token: {client.token[:30]}...")

    # ── 2. 用户信息 ──
    print("\n[2] 获取用户信息...")
    u = client.get_user_info()
    if u.get('code') == 200:
        info = u['data'].get('data', u['data'])
        print(f"  用户ID: {info.get('id')}")
        et = info.get('exam_type', {})
        sp = info.get('speciality', {})
        print(f"  考试类型: {et.get('name', '未知')}")
        print(f"  专业: {sp.get('name', '未知')}")

    # ── 3. 题型列表 ──
    print("\n[3] 获取题型列表...")
    tl = client.topic_type_list()
    if tl.get('code') == 200:
        items = tl['data'] if isinstance(tl['data'], list) else tl['data'].get('data', [])
        print(f"  共 {len(items)} 个大类:")
        for i, t in enumerate(items[:5]):
            print(f"    {i+1}. {t['name']} (id={t['id']})")
        if len(items) > 5:
            print(f"    ... 还有 {len(items)-5} 个")

        # ── 4. 获取第一个叶子节点的题目 ──
        def find_leaf(node):
            if node.get('children'):
                for c in node['children']:
                    r = find_leaf(c)
                    if r: return r
            elif node.get('topic_count', 0) > 0:
                return node
            return None

        leaf = find_leaf(items[0]) if items else None
        if leaf:
            print(f"\n[4] 获取题目: {leaf['name']} (id={leaf['id']}, {leaf['topic_count']}题)...")
            q = client.topic_type(leaf['id'])
            if q.get('code') == 200:
                qs = q['data'] if isinstance(q['data'], list) else q['data'].get('data', [])
                for qi in qs[:3]:
                    print(f"\n  [{qi['id']}] {qi['title']}")
                    for opt in qi.get('option', []):
                        mark = '✓' if opt['value'] in qi.get('answer', []) else ' '
                        print(f"    [{mark}] {opt['key']}. {opt['value']}")
                    if qi.get('analysis'):
                        print(f"  解析: {qi['analysis'][:100]}...")

    print("\n完成!")


if __name__ == '__main__':
    main()
