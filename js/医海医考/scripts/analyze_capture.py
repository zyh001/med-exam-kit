#!/usr/bin/env python3
"""
分析 Frida hook_capture.js 的输出, 自动解密 AppVerify 和响应体

用法: python analyze_capture.py capture.txt
"""

import hashlib, base64, json, sys, re
from Crypto.Cipher import AES
from Crypto.Util.Padding import unpad

AV_KEY = b'cfd0f9faab3726e5'
AV_IV  = b'yhyk2026xzdlcxwc'
UNIQUE_CODE = 'fe514637e1eb8c298e3e5c2b35fbb786'

def md5(s): return hashlib.md5(s.encode()).hexdigest()

def decrypt_av(b64):
    return json.loads(unpad(AES.new(AV_KEY, AES.MODE_CBC, AV_IV).decrypt(base64.b64decode(b64)), 16).decode())

def decrypt_resp(enc_data, rand, uc=UNIQUE_CODE):
    iv_pos = (len(enc_data) - 24) % 128
    emb_iv = enc_data[iv_pos:iv_pos+24]
    ct_b64 = enc_data[:iv_pos] + enc_data[iv_pos+24:]
    ks = rand + emb_iv + uc
    key = md5(ks)[7:7+16]; iv = md5(emb_iv)[11:11+16]
    plain = unpad(AES.new(key.encode(), AES.MODE_CBC, iv.encode()).decrypt(base64.b64decode(ct_b64)), 16)
    return json.loads(plain.decode())

if len(sys.argv) < 2:
    print("用法: python analyze_capture.py capture.txt")
    sys.exit(1)

with open(sys.argv[1], 'r', errors='replace') as f:
    text = f.read()

# 提取配对数据
avs = re.findall(r'\[AppVerify\]\s*aes:([^\r\n]+)', text)
enc_datas = [s.strip() for s in re.findall(r'\[ENC-DATA\]\s*([^\r\n]+)', text)]
enc_signs = [s.strip() for s in re.findall(r'\[ENC-SIGN\]\s*([a-f0-9]+)', text)]
endpoints = re.findall(r'\[REQ\]\s*([^\r\n]+)', text)

print(f"找到 {len(avs)} 个请求, {len(enc_datas)} 个响应\n")

for i, av_b64 in enumerate(avs):
    try:
        av = decrypt_av(av_b64)
    except:
        print(f"[#{i}] AppVerify 解密失败"); continue

    ep = endpoints[i] if i < len(endpoints) else "?"
    print(f"{'='*60}")
    print(f"[#{i}] {ep}")
    print(f"  random: {av['random']}")
    print(f"  sign:   {av['sign']}")
    print(f"  time:   {av['time']}")
    print(f"  route:  {av['route']}")

    if i < len(enc_datas):
        try:
            data = decrypt_resp(enc_datas[i], av['random'])
            print(f"  ✅ 响应解密成功!")
            print(f"  {json.dumps(data, ensure_ascii=False, indent=2)[:500]}")
        except Exception as e:
            print(f"  ❌ 响应解密失败: {e}")
    print()
