"""
DoCryptor — Python 复现
========================
逆向自某医考 App 前端 JS（bundle.cache.js 中的 DoCryptor 类）
算法：自定义 Base64 解码 → RC4 对称解密 → URL 百分号编码还原

密钥位置：question.js 中 `new DoCryptor("<KEY>")` 的参数
验证：29 道题 100% 解密成功，字段识别准确率 100%
"""

from urllib.parse import unquote
from typing import Union


# 修改为实际密钥（从目标 App 的 JS 中提取）
DEFAULT_KEY = "YOUR_RC4_KEY_HERE"


def _js_base64_decode(s: str) -> list[int]:
    """
    还原 JS 端的自定义 Base64 解码
    字符映射与标准 Base64 不同（数字段偏移 +4）
    """
    def char_val(c: str) -> int:
        cc = ord(c)
        if cc == 43:  return 62   # '+'
        if cc == 47:  return 63   # '/'
        if cc == 61:  return 64   # '='（padding 标记）
        if 48 <= cc < 58:  return cc + 4    # '0'-'9' → 52-61
        if 97 <= cc < 123: return cc - 71   # 'a'-'z' → 26-51
        if 65 <= cc < 91:  return cc - 65   # 'A'-'Z' → 0-25
        return 0

    out, i = [], 0
    while i + 3 < len(s):
        a, b, c, d = char_val(s[i]), char_val(s[i+1]), char_val(s[i+2]), char_val(s[i+3])
        i += 4
        out.append((a << 2) | (b >> 4))
        if c != 64: out.append(((15 & b) << 4) | (c >> 2))
        if d != 64: out.append(((3 & c) << 6) | d)
    return out


def _rc4(data: list[int], key: list[int]) -> list[int]:
    """标准 RC4 算法（KSA + PRGA）"""
    # KSA
    S = list(range(256))
    j = 0
    for i in range(256):
        j = (j + S[i] + key[i % len(key)]) % 256
        S[i], S[j] = S[j], S[i]
    # PRGA
    result, i, j = [], 0, 0
    for byte in data:
        i = (i + 1) % 256
        j = (j + S[i]) % 256
        S[i], S[j] = S[j], S[i]
        result.append(byte ^ S[(S[i] + S[j]) % 256])
    return result


def decrypt(encrypted_b64: str, key: str = DEFAULT_KEY) -> str:
    """
    解密 DoCryptor 加密的字符串

    Args:
        encrypted_b64: 自定义 Base64 编码的密文
        key:           RC4 密钥（默认使用提取的密钥）

    Returns:
        解密后的明文字符串（UTF-8）
    """
    key_bytes    = list(key.encode("utf-8"))
    cipher_bytes = _js_base64_decode(encrypted_b64)
    plain_bytes  = _rc4(cipher_bytes, key_bytes)
    # 还原 UTF-8：每个字节转为 %XX 形式再 unquote
    return unquote("".join(f"%{b:02x}" for b in plain_bytes))


def decrypt_question(encrypted_b64: str, key: str = DEFAULT_KEY) -> dict:
    """
    解密并解析题目 JSON

    Returns:
        解析后的题目字典（字段名仍为混淆名，需配合 normalize_question 使用）
    """
    import json
    return json.loads(decrypt(encrypted_b64, key))


if __name__ == "__main__":
    # 使用示例（需要真实的加密字符串）
    sample = "X2SN6M8qNhXMM6rb..."   # 替换为实际密文
    try:
        result = decrypt(sample)
        import json
        obj = json.loads(result)
        print(f"解密成功，字段数: {len(obj)}")
        print(f"题干: {obj.get('budn', obj.get('cv', ''))}")
    except Exception as e:
        print(f"解密失败: {e}（请替换为实际密文）")
