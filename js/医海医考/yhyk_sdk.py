#!/usr/bin/env python3
"""
医海医考 API SDK — 完全逆向还原 (已验证可用)

pip install pycryptodome requests

用法:
    from yhyk_sdk import YhykClient
    client = YhykClient()
    client.login("手机号", "密码")
    topics = client.topic_type_list()
    questions = client.topic_type(topic_type_id=17672)
"""

import hashlib, base64, json, time, random, string, requests
from urllib.parse import urlsplit
from Crypto.Cipher import AES
from Crypto.Util.Padding import pad, unpad


# ═══════════════════════════════════════════
# 已破解的密钥和常量
# ═══════════════════════════════════════════
AV_KEY       = b'cfd0f9faab3726e5'                              # MD5('AppVerify')[7:23]
AV_IV        = b'yhyk2026xzdlcxwc'                              # 硬编码默认IV
UNIQUE_CODE  = 'fe514637e1eb8c298e3e5c2b35fbb786'               # MD5("__UNI__7EE4208-yhyk2026-")
APP_ID       = '__UNI__7EE4208'
APP_SIGN     = 'e65425ed05100e7ead3b9fda147e3b31c2c8c7d9'       # APK签名证书SHA1
APP_VERSION  = '4.1.2'
BASE_URL     = 'https://examapi.yhykwch.com'

# UA 可自定义，但 substr(12,85) 的 MD5 必须一致
DEFAULT_UA   = ('Mozilla/5.0 (Linux; Android 12; Pixel 5 Build/SP1A.210812.016; wv) '
                'AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 '
                'Chrome/110.0.5481.154 Safari/537.36')


def md5(s: str) -> str:
    return hashlib.md5(s.encode()).hexdigest()


def _rand(n=32) -> str:
    return ''.join(random.choices(string.ascii_letters + string.digits, k=n))


# ═══════════════════════════════════════════
# AppVerify 生成 (请求签名)
# ═══════════════════════════════════════════

def gen_appverify(path: str, method: str, token: str = '',
                  ua: str = DEFAULT_UA,
                  route: str = 'pages/index/index',
                  query: dict = None) -> tuple:
    """生成 AppVerify 请求头

    重要: sign 计算只使用路径部分 (不含 query string)
          例如 /api/topic_type?topic_type_id=17672 → sign 里用 /api/topic_type
          查询参数不参与签名. 实际请求仍然发到完整的带 query 的 URL.

    Returns: (appverify_header_value, random_string)
    """
    # ★ 关键修正: sign 只用 path, 不含 query
    sign_path = urlsplit(path).path

    rand = _rand(32)
    ts = round(time.time(), 3)                     # JS: Date.now()/1000, 必须3位小数
    ua_hash = md5(ua[12:12+85])                    # appSystemInfo.ua.substr(12, 85)

    tk = token if token else f'tourist:{rand}'
    sign_str = (f'random={rand}&time={ts}&ua={ua_hash}&method={method}'
                f'&token={tk}&unique_code={UNIQUE_CODE}&url={sign_path}')
    sign = md5(sign_str)

    av = json.dumps({
        'route': route, 'query': query or {},
        'random': rand, 'time': ts, 'source': 'app',
        'randomKey': '', 'appId': APP_ID, 'appSign': APP_SIGN,
        'sign': sign,
    }, separators=(',', ':'))

    cipher = AES.new(AV_KEY, AES.MODE_CBC, AV_IV)
    encrypted = base64.b64encode(cipher.encrypt(pad(av.encode(), 16))).decode()
    return f'aes:{encrypted}', rand


# ═══════════════════════════════════════════
# 响应解密
# ═══════════════════════════════════════════

def decrypt_response(encrypt_data: str, request_random: str) -> dict:
    """解密 API 响应中的 encryptData

    算法:
      1. ivPos = (len(encryptData) - 24) % 128
      2. embeddedIV = encryptData[ivPos : ivPos+24]
      3. ciphertext = encryptData 去掉 embeddedIV
      4. keySource = random + embeddedIV + unique_code
      5. key = MD5(keySource)[7:23]
      6. iv  = MD5(embeddedIV)[11:27]
      7. AES-128-CBC 解密
    """
    iv_pos = (len(encrypt_data) - 24) % 128
    emb_iv = encrypt_data[iv_pos:iv_pos + 24]
    ct_b64 = encrypt_data[:iv_pos] + encrypt_data[iv_pos + 24:]

    ks = request_random + emb_iv + UNIQUE_CODE
    key = md5(ks)[7:7+16].encode()
    iv  = md5(emb_iv)[11:11+16].encode()

    plain = unpad(AES.new(key, AES.MODE_CBC, iv).decrypt(base64.b64decode(ct_b64)), 16)
    return json.loads(plain.decode())


def decrypt_appverify(av_header: str) -> dict:
    """解密捕获的 AppVerify (调试用)"""
    b64 = av_header.replace('aes:', '', 1)
    return json.loads(unpad(
        AES.new(AV_KEY, AES.MODE_CBC, AV_IV).decrypt(base64.b64decode(b64)), 16
    ).decode())


# ═══════════════════════════════════════════
# API 客户端
# ═══════════════════════════════════════════

class YhykClient:
    """医海医考 API 客户端

    Token 机制:
      - 每次 login 生成新 Token，旧 Token 立即失效（单会话模型）
      - 修改密码后 Token 也会失效
      - Token 无自然过期时间，但重新登录会刷新

    服务端错误码:
      - 200  成功
      - 201  AppVerify 验证失败 (sign 错/解密失败)
      - 211  App 版本过低 或 未携带 AppVerify/AppVersion
      - 401  Token 无效/未登录
      - 1011 签名错误 (sign 内容不对, 最常见: sign 里含了 query string)
      - 1012 请求过期 (时间戳超出有效范围)
    """

    def __init__(self, ua: str = DEFAULT_UA):
        self.token = ''
        self.ua = ua
        self.session = requests.Session()
        self.session.headers['User-Agent'] = ua + ' (Immersed/24.0) Html5Plus/1.0'

    def _req(self, method: str, path: str, body: dict = None) -> dict:
        av, rand = gen_appverify(path, method, self.token, self.ua)
        hdrs = {'AppVersion': APP_VERSION, 'AppVerify': av}
        if self.token:
            hdrs['Token'] = self.token
        if body is not None:
            hdrs['Content-Type'] = 'application/json'

        resp = self.session.request(method, BASE_URL + path,
                                    json=body, headers=hdrs, timeout=15)
        data = resp.json()

        if data.get('code') == 200 and data.get('encryptData'):
            data['data'] = decrypt_response(data['encryptData'], rand)
        return data

    # ---- 认证 ----

    def login(self, phone: str, password: str) -> dict:
        """登录，成功后自动保存 Token

        注意: 每次登录会使之前的 Token 失效
        """
        r = self._req('POST', '/api/login', {'phone': phone, 'password': password})
        if r.get('code') == 200 and isinstance(r.get('data'), dict):
            inner = r['data'].get('data', r['data'])
            self.token = inner.get('token', '')
        return r

    def is_user_login(self) -> dict:
        """检查登录状态 (200=已登录, 401=未登录)"""
        return self._req('GET', '/api/is_user_login')

    # ---- 用户 ----

    def get_user_info(self) -> dict:
        return self._req('GET', '/api/get_user_info')

    def user_update(self, current_password: str, new_password: str) -> dict:
        """修改密码 (成功后当前 Token 立即失效，需重新登录)"""
        return self._req('PUT', '/api/user_update',
                         {'current_password': current_password,
                          'new_password': new_password})

    # ---- 题库 ----

    def topic_type_list(self) -> dict:
        """获取全部题型分类树 (返回数据约60KB)"""
        return self._req('GET', '/api/topic_type_list')

    def topic_type(self, topic_type_id: int) -> dict:
        """获取某题型下的所有题目 (含题干、选项、答案、解析)"""
        return self._req('GET', f'/api/topic_type?topic_type_id={topic_type_id}')

    def topic_answer(self, topic_id: int, answer: list) -> dict:
        """提交答案 (answer 为选项文本数组，不是ABCDE)"""
        return self._req('POST', '/api/topic_type_answer',
                         {'topic_id': topic_id, 'answer': answer})

    def topic_comments(self, topic_id: int, page=1, per_page=10) -> dict:
        """获取题目评论"""
        return self._req('GET',
            f'/api/topic_type_comment_comment_list?page={page}&per_page={per_page}&topic_id={topic_id}')

    def activation_topic_type(self) -> dict:
        """获取已激活的题型"""
        return self._req('GET', '/api/activation_topic_type')

    # ---- 视频 ----

    def video_type(self) -> dict:
        """获取视频课程分类"""
        return self._req('GET', '/api/video_type')

    def video(self, video_type_id: int) -> dict:
        """获取视频列表"""
        return self._req('GET', f'/api/video?video_type_id={video_type_id}')

    # ---- 系统 ----

    def advertisement(self) -> dict:
        """获取广告/公告"""
        return self._req('GET', '/api/advertisement')

    def dict(self, keys: list) -> dict:
        """查询系统字典 (如 key=['lxwm'] 查联系方式)"""
        return self._req('POST', '/api/dict', {'key': keys})
