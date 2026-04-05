# Android App 逆向工程全流程实战：从 APK 解包到题库解密

> **免责声明**：本文所有操作均在自有账号下进行，仅用于技术研究与学习。所有域名、密钥、用户标识均已脱敏处理。请勿将相关技术用于任何非法用途。

---

## 一、背景与动机

出于学习目的，我希望：
1. 理解移动端的完整 API 接口
2. 把题目数据存到本地，自己实现一套 Web 刷题界面

这篇文章记录整个逆向过程的思路与踩坑，希望对有类似需求的同学有所参考。

---

## 二、整体逆向路线图

```
APK 文件
   │
   ├─ apktool 解包
   │     ├─ AndroidManifest.xml  ← 权限、包名、SDK版本、第三方密钥
   │     ├─ smali 字节码         ← 壳结构、类名混淆
   │     ├─ assets/              ← 加密 DEX、前端资源
   │     └─ lib/*.so             ← Native 库（含解壳逻辑）
   │
   ├─ mitmproxy 动态抓包         ← 捕获所有 HTTPS 流量
   │     ├─ 接口地址与参数
   │     ├─ 认证机制
   │     └─ 响应结构
   │
   └─ JS 源码逆向                ← 从抓包的前端 JS 找到加密实现
         └─ DoCryptor 类 → RC4 + 自定义 Base64
```

---

## 三、第一步：APK 解包与静态分析

### 3.1 基础信息提取

```bash
# 使用 apktool 解包
apktool d target.apk -o output/

# 查看 AndroidManifest.xml
cat output/AndroidManifest.xml
```

从 `AndroidManifest.xml` 能立刻读出大量信息：

**包名与版本**

```xml
<manifest android:versionCode="636" android:versionName="6.0.12"
          package="app.example" ...>
  <uses-sdk android:minSdkVersion="21" android:targetSdkVersion="30" />
```

**Deep Link（URL Scheme）**

```xml
<data android:scheme="https" />
<data android:host="example.com" />
<data android:host="*.example.com" />
```

这直接给出了所有后端域名。

**硬编码的第三方 SDK 密钥**（以下均已脱敏）

```xml
<meta-data android:name="UMENG_APPKEY"    android:value="<UMENG_KEY>" />
<meta-data android:name="WX_APPID"        android:value="<WX_APPID>" />
<meta-data android:name="WX_SECRET"       android:value="<WX_SECRET>" />
<meta-data android:name="MIPUSH_APPID"    android:value="<MI_APPID>" />
<meta-data android:name="GETUI_APPID"     android:value="<GT_APPID>" />
<meta-data android:name="com.huawei.hms.client.appid" android:value="<HW_APPID>" />
```

> ⚠️ **安全警示**：微信 AppSecret 以明文形式写在 Manifest 里，这是一个严重的安全风险。任何拿到 APK 的人都能提取出来。

### 3.2 识别加固壳

看 `smali/` 目录结构：

```
smali/
└── com/wrapper/proxyapplication/
    ├── WrapperProxyApplication.smali   ← 代理壳入口
    ├── Util.smali                      ← 解壳工具类
    └── CustomerClassLoader.smali       ← 自定义类加载器
```

真正的 Application 类是 `app.example.EhafoApplication`，通过壳动态加载。

```java
// WrapperProxyApplication.smali 反编译关键逻辑
const-string v1, "app.example.EhafoApplication"  // 真实 Application 类
const-string v1, "libshell-super.app.example.so"  // Native 解壳库
```

**加密资产文件**（`assets/` 下的混淆命名文件）：

```
0OO00l111l1l    ← 主业务 DEX，6.5MB，完全加密
o0oooOO0ooOo.dat
0OO00oo01l1l
0OO00oo11l1l
```

启动流程：

```
App 启动
  → StubWrapperProxyApplication.onCreate()
  → Util.PrepareSecurefiles() 解密 DEX 到私有目录
  → 加载 libshell-super.so（Native 解密）
  → CustomerClassLoader 加载真实 DEX
  → EhafoApplication.onCreate() 真正启动
```

### 3.3 分析 `.dat` 文件结构

`o0oooOO0ooOo.dat`（168字节）是一个有规律的二进制文件：

```python
# 按16字节分块分析
with open('o0oooOO0ooOo.dat', 'rb') as f:
    dat = f.read()

for i in range(0, len(dat), 16):
    chunk = dat[i:i+16]
    print(f"{i:3d}: {chunk.hex()}  |{''.join(chr(b) if 32<=b<127 else '.' for b in chunk)}|")
```

输出：

```
  0: 3037474930000000b9a92ad7911c7f45  |07GI0.....*....|
 16: d121b51b0061f182534d5a3430000000  |.!...a..SMZ40...|
 32: 2b496cf0068357bcaeb3b5c4a75f2c82  |+Il...W......_,.|
 48: 534d5a3431000000f0f2eedbe543e4ba  |SMZ41........C..|
```

关键发现：`SMZ40`、`SMZ41`、`XNC3S` 等字符串，`SMZ` 推测是 SM4（国密对称加密算法），这是内部加密配置结构。

### 3.4 so 文件字符串混淆分析

`libshell-super.so` 对字符串做了 XOR 混淆：

```python
import subprocess, re

result = subprocess.run(['strings', 'libshell-super.so'], capture_output=True, text=True)

# 尝试 XOR 0x0C 解密
for line in result.stdout.split('\n'):
    try:
        dec = ''.join(chr(ord(c) ^ 0x0c) for c in line if ord(c) < 128)
        if all(32 <= ord(c) < 127 for c in dec) and 'com/' in dec:
            print(f"原文: {line}\n解密: {dec}\n")
    except: pass
```

输出（部分）：

```
原文: oca#{~m||i~#|~ctum||`eomxecb#Oy
解密: com/wrapper/proxyapplication/Cu

原文: Vp{l{5v{t}5Yv{iiVu{~
解密: Ljava/lang/ClassLoad     (XOR 0x1a)
```

---

## 四、第二步：配置 mitmproxy 动态抓包

### 4.1 为什么这款 App 可以直接抓包

查看 `res/xml/network_security_config.xml`：

```xml
<network-security-config>
    <base-config cleartextTrafficPermitted="true">
        <trust-anchors>
            <certificates src="system" />
            <certificates src="user" />   <!-- ✅ 信任用户证书！ -->
        </trust-anchors>
    </base-config>
</network-security-config>
```

`certificates src="user"` 意味着只要在手机里安装自签名 CA 证书，就能解密所有 HTTPS 流量，无需 Frida bypass。

### 4.2 搭建抓包环境

```bash
pip install mitmproxy

# 启动代理（含 Web 界面）
mitmweb --listen-port 8080

# 手机设置代理 → 电脑 IP:8080
# 访问 http://mitm.it 安装 mitmproxy CA 证书
```

### 4.3 自定义抓包脚本

编写 mitmproxy 插件，实现请求体解析 + 自动分类：

```python
# capture.py - 核心逻辑
from mitmproxy import http, ctx
import json, re

KEY_WORDS = ["example.com"]  # 目标域名（已脱敏）

class Capture:
    def response(self, flow: http.HTTPFlow):
        host = flow.request.pretty_host
        if not any(k in host for k in KEY_WORDS): return

        # 解析请求体
        req_body = self._parse_body(
            flow.request.content,
            flow.request.headers.get("content-type", "")
        )
        # 解析响应体
        res_body = self._parse_body(
            flow.response.content,
            flow.response.headers.get("content-type", "")
        )
        # 保存记录...

    def _parse_body(self, content, content_type):
        if "json" in content_type:
            return json.loads(content)
        if "form" in content_type:
            from urllib.parse import parse_qs
            return {k: v[0] for k, v in parse_qs(content.decode()).items()}
        return content.decode("utf-8", errors="replace")[:2000]

addon = Capture()
```

运行：

```bash
mitmweb -s capture.py
```

---

## 五、第三步：接口分析

### 5.1 服务端架构

抓包后发现请求分布在 7 个子域名：

| 域名 | 用途 |
|---|---|
| `sdk.example.com` | 主业务 API（PhalAPI 框架） |
| `wxserver.example.com` | 微信端 API |
| `core.example.com` | REST API（新版） |
| `quiz.example.com` | Web 前端 SPA |
| `imgcdn.example.com` | 图片 CDN |
| `sdk.dev.example.com` | 开发环境 API（意外暴露！） |
| `spy.example.com` | PageSpy 前端监控 |

### 5.2 PhalAPI 接口格式

所有主要接口遵循统一格式：

```
POST https://sdk.example.com/phalapi/public/?service={Module.Class.Method}
Content-Type: application/x-www-form-urlencoded

sessionid=<SESSION_ID>&cid=115&__local_time=1712345678901&...业务参数
```

响应格式统一：

```json
{
  "ret": 0,
  "code": 0,
  "msg": "success",
  "data": { /* 业务数据 */ },
  "errorcode": 0,
  "errormsg": ""
}
```

### 5.3 认证机制

登录接口 `App.User.appLogin` 参数：

```json
{
  "login_type": "wx",
  "device_type": "android",
  "unionid": "<WX_UNIONID>",
  "userinfo": "{\"openid\":\"<WX_OPENID>\",\"nickname\":\"...\",\"headimgurl\":\"...\"}",
  "uuid": "<DEVICE_UUID>",
  "sessionid": "",
  "server_status": "on"
}
```

响应返回的 `sessionid` 是一个 32 位 MD5 字符串，**无固定过期时间**。通过测试验证：距离初次抓包 22 小时后，同一个 sessionid 仍然有效。Session 仅在主动调用 `App.User.userLogout` 或服务端强制清除时失效。

### 5.4 关键接口：获取题库结构

```
POST /phalapi/public/?service=App.Struct.getStructs

Body: sessionid=<SESSION_ID>&cid=115
```

响应（节选）：

```json
{
  "data": {
    "subject_list": [{
      "list": [{
        "id": "12082",
        "name": "第五部分 口腔医学综合",
        "question_nums": "5202",
        "children": [{
          "id": "207121",
          "name": "第一章 口腔组织病理学",
          "children": [{
            "id": "207122",
            "name": "第一节 牙体组织",
            "question_nums": "23"
          }]
        }]
      }]
    }]
  }
}
```

---

## 六、第四步：题目加密逆向（核心难点）

### 6.1 发现加密

抓包后发现，章节题目接口（`App.Daily.getMultipleTikuQuestion`）的响应中，`question_info` 字段是一段 Base64 样式的密文：

```json
{
  "question_list": {
    "1": {
      "question_info": "X2SN6M8qNhXMM6rbD6bA9Wa8W34C6vFb+KIH..."
    }
  }
}
```

而每日一练接口（`App.Daydayup.getQuestions`）却返回了明文：

```json
{
  "data": [{
    "qid": "130090",
    "question": "关于义齿基托树脂，下列描述错误的是",
    "answer": "C",
    "a": "自凝树脂可用于制作腭护板",
    "b": "..."
  }]
}
```

这说明不同接口对题目数据有不同的加密策略。

### 6.2 分析加密数据特征

```python
import base64, json

# 收集多个加密块，分析前缀
encrypted_samples = ["X2SN6M8q...", "X2SG88Us...", "X2Sc6NUz..."]

for s in encrypted_samples:
    b = base64.b64decode(s + "==")
    print(f"前4字节: {b[:4].hex()}")
    # 输出:
    # 5f648de8
    # 5f6486f3
    # 5f649ce8
```

发现前两字节固定为 `5f 64`（即 ASCII `_d`），这是某种自定义格式的魔数。

### 6.3 在 JS 源码中找到密钥

抓包到的 JS 文件中，大多数都被截断了（mitmproxy 默认响应体大小限制）。直接 curl 拉取完整 JS：

```bash
curl "https://quiz.example.com/v5/static/js/v4/learning/question.js" -o question.js
```

在 `question.js` 里搜索关键词：

```javascript
// 发现！
isNull(A) && (A = new DoCryptor("fmsfwIeJJTctOSxyjBRbt8xUUe6XY7ss"));
```

`DoCryptor` 类的定义在 `bundle.cache.js` 中（260KB）：

```javascript
function DoCryptor(key) {
    this.key = this.toBytes(key);
}

DoCryptor.prototype.decrypt = function(e) {
    e = e || "";
    e = this.base64Decode(e);      // 先自定义 Base64 解码
    return this.toChars(this.rc4(e, this.key));  // 再 RC4 解密
};

DoCryptor.prototype.rc4 = function(e, t) {
    // 标准 RC4 KSA + PRGA
    var r, a, n, i, o, s, l, u, c, p, d, h, f;
    for (o = t.length, r = e.length, d = new Array(256), a = i = 0; i < 256; a = ++i)
        d[a] = a;
    for (a = s = n = 0; s < 256; a = ++s)
        u = [d[n = (n + d[a] + t[a % o]) % 256], d[a]],
        d[a] = u[0], d[n] = u[1];
    // PRGA...
};
```

### 6.4 Python 复现 DoCryptor

```python
from urllib.parse import unquote

RC4_KEY = "fmsfwIeJJTctOSxyjBRbt8xUUe6XY7ss"

def rc4_decrypt(encrypted_b64: str, key: str = RC4_KEY) -> str:
    """
    完整复现 JS 端的 DoCryptor.decrypt()
    算法：自定义Base64解码 → RC4解密 → UTF-8还原
    """
    # Step 1: 自定义 Base64 解码（字符表与标准不同）
    def char_val(c):
        cc = ord(c)
        if cc == 43: return 62   # '+'
        if cc == 47: return 63   # '/'
        if cc == 61: return 64   # '=' (padding)
        if 48 <= cc < 58: return cc + 4   # '0'-'9'
        if 97 <= cc < 123: return cc - 71  # 'a'-'z'
        if 65 <= cc < 91: return cc - 65   # 'A'-'Z'
        return 0

    data = []
    i = 0
    while i + 3 < len(encrypted_b64):
        a, b, c, d = [char_val(encrypted_b64[i+j]) for j in range(4)]
        i += 4
        data.append((a << 2) | (b >> 4))
        if c != 64: data.append(((15 & b) << 4) | (c >> 2))
        if d != 64: data.append(((3 & c) << 6) | d)

    # Step 2: RC4 解密
    key_bytes = list(key.encode('utf-8'))
    S = list(range(256))
    j = 0
    for i in range(256):
        j = (j + S[i] + key_bytes[i % len(key_bytes)]) % 256
        S[i], S[j] = S[j], S[i]

    i = j = 0
    result = []
    for byte in data:
        i = (i + 1) % 256
        j = (j + S[i]) % 256
        S[i], S[j] = S[j], S[i]
        result.append(byte ^ S[(S[i] + S[j]) % 256])

    # Step 3: 百分号编码还原为 UTF-8
    return unquote(''.join(f'%{b:02x}' for b in result))

# 测试
plain = rc4_decrypt("X2SN6M8qNhXMM6rb...")
obj   = json.loads(plain)
print(obj['question'])  # 打印题干
# → "牙龈的组织学特征是"
```

### 6.5 解密后的数据结构

普通题解密后得到的 JSON（字段名被混淆，需要二次识别）：

```json
{
  "caseid": "0",
  "show_name": "A1单选题",
  "book_dirs_str_new": "《2026年人卫版 口腔执业医师...》",
  "mk_content_datas": [{"kname": "牙周组织", "content": [...]}],

  "svkdkvyjwl": "10290147",   ← qid（题目ID）
  "budn": "牙龈的组织学特征是",  ← question（题干）
  "zhnscq": "没有角化层",       ← option_a
  "lex": "血管丰富",            ← option_b
  "aqlmvx": "无黏膜下层",      ← option_c
  "yhpqctpnj": "缺乏颗粒层",   ← option_d
  "th": "固有层为疏松结缔组织",  ← option_e
  "iturd": "single_select",   ← model（题型）
  "cobimpmtscje": "C",        ← answer（正确答案）
  "dgs": "牙龈由上皮层和固有层组成...", ← analysis（解析）
  "acn": "41507",             ← do_nums（做题人数）
  "vtgnpqfpz": "12104"        ← err_nums（错误人数）
}
```

病例组合题（共用选项）：

```json
{
  "caseid": "93364",
  "case_options": {"A":"牙槽嵴组","B":"水平组","C":"斜行组","D":"根间组","E":"根尖组"},
  "case_answer": [
    {"id":"14798","answer":"E","analysis":"根尖组：起于根尖区牙骨质..."},
    {"id":"14799","answer":"D","analysis":"根间组：只存在于多根牙..."},
    {"id":"14800","answer":"A","analysis":"牙槽嵴组：纤维起于牙槽嵴顶..."}
  ]
}
```

字段识别规则（按值特征推断）：

```python
# 通过值的模式识别混淆字段的语义
import re

ANSWER_PAT  = re.compile(r'^[A-E]{1,5}$')
MODEL_VALS  = {'single_select','multi_select','fill','essay','judge'}
QUESTION_CUE = re.compile(r'以下|下列|关于|哪|不正确|错误|描述|特征|[？?]$|位于$|属于$')

def identify_field(key, value):
    if ANSWER_PAT.match(str(value)):       return 'answer'
    if str(value) in MODEL_VALS:           return 'model'
    if '单选' in str(value):              return 'type_name'
    if re.match(r'^\d{5,8}$', str(value)):return 'qid'
    if '《' in str(value) and len(str(value)) > 30: return 'book_source'
    if '<strong>' in str(value):           return 'kp_html'
    if QUESTION_CUE.search(str(value)):    return 'question'
    # 按数值范围区分统计字段
    if isinstance(value, str) and value.isdigit():
        n = int(value)
        if 12000 <= n <= 13000: return 'subject_id'  # 已知科目ID范围
        if n > 100000:          return 'qid'
        if n < 100:             return 'ticlassid'
        return 'stat_number'  # do_nums/err_nums
    return 'unknown'
```

**验证结果（19道普通题 + 10道病例题）：**

| 字段 | 覆盖率 |
|---|---|
| qid（题目ID） | 100% ✅ |
| answer（答案） | 100% ✅ |
| model（题型） | 100% ✅ |
| question（题干） | 100% ✅ |
| option_a~e（选项） | 100% ✅ |
| book_source（书目） | 100% ✅ |
| do_nums（做题量） | 100% ✅ |
| kp_html（知识点） | 100% ✅ |
| analysis（解析） | 68% 📝（服务端部分题目未返回，非解密问题） |

---

## 七、第五步：自动化登录与爬虫

### 7.1 Session 获取

微信登录在 App 内部使用 HBuilder 原生 `plus.oauth` 实现，无法从网页直接模拟。

解决方案：Playwright 自动化浏览器，打开网页登录页，用户扫码后从 `localStorage` 读取 sessionid：

```python
from playwright.sync_api import sync_playwright

def login_via_browser() -> str:
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=False)  # 显示浏览器
        page = browser.new_page(
            viewport={"width":480,"height":800},
            user_agent="Mozilla/5.0 (Linux; Android 11...)"
        )
        page.goto("https://quiz.example.com/v5/v4/public/wxlogin.html")

        # 等待用户扫码完成
        deadline = time.time() + 180
        while time.time() < deadline:
            time.sleep(1.5)
            sid = page.evaluate("localStorage.getItem('sessionid')")
            if sid and len(sid) == 32:
                return sid  # ✅ 获取成功

        browser.close()
```

### 7.2 CID 自动获取

登录成功后，通过 `App.Index.IndexInfo` 获取用户已报名的考试列表：

```python
res = api_call("App.Index.IndexInfo", {"sessionid": sid, "cid": ""})
exams = res["data"]["category_relations"]
# [{"cid": "115", "name": "口腔执业医师", "is_vip": 1}]
```

### 7.3 题目爬取策略

```
App.Struct.getStructs        → 获取全量章节结构（科目→章→节）
    ↓
App.Daily.getMultipleTikuQuestion  → 按节批量拉题（每次10题，RC4解密）
    ↓
App.Daydayup.getQuestions    → 每日一练补充（repair_day=0~400，明文返回）
```

章节接口一次返回 10 题，7217 道题约需 722 次请求，按 1.2 秒间隔约 **15 分钟**跑完。

### 7.4 数据库结构

```sql
-- 科目树
CREATE TABLE subjects (id TEXT, cid TEXT, name TEXT, question_nums INTEGER);
CREATE TABLE chapters (id TEXT, subject_id TEXT, name TEXT);
CREATE TABLE sections (id TEXT, chapter_id TEXT, subject_id TEXT, name TEXT, scraped INTEGER DEFAULT 0);

-- 题目核心表
CREATE TABLE questions (
    qid         TEXT PRIMARY KEY,
    section_id  TEXT,
    model       TEXT,           -- single_select / multi_select / ...
    type_name   TEXT,           -- A1单选题 / A2单选题 / ...
    question    TEXT,           -- 题干
    option_a    TEXT,
    option_b    TEXT,
    option_c    TEXT,
    option_d    TEXT,
    option_e    TEXT,
    answer      TEXT,           -- 正确答案
    analysis    TEXT,           -- 解析
    kp_html     TEXT,           -- 知识点 HTML
    book_source TEXT,           -- 书目来源
    do_nums     INTEGER,        -- 做题人数
    err_nums    INTEGER,        -- 错误人数
    source      TEXT            -- chapter / daydayup
);
```

---

## 八、完整请求流程

```
┌─────────────┐    ①登录（微信OAuth）
│  用户扫码    │ ──────────────────────────────────────────────────┐
└─────────────┘                                                    │
                                                         POST /phalapi/public/
                                                         ?service=App.User.appLogin
                                                         Body: unionid, userinfo, ...
                                                                   │
                                                         ← 返回 sessionid (32位MD5)
                                                                   │
┌─────────────────────────────────────────────────────────────────┘
│ ②初始化（每次启动）
│
│  Common.Common.getSysConfigs      → 全局配置（AB测试/功能开关）
│  App.Index.IndexInfo              → 用户状态/考试进度/CID
│  App.Struct.getStructs            → 题库结构树
│  App.Daydayup.getQuestions        → 今日题目（明文）
│  Common.Common.Polls              → 长轮询（消息/通知）
│
│ ③做题流程
│
│  App.Daily.getMultipleTikuQuestion  → 加密题目
│      └─ RC4解密(key="fmsfw...") → 明文JSON
│
│  [用户答题后]
│  App.Common.getQueStatInfo          → 题目热度数据
│  App.Daily.checkAndSyncAnswerCard   → 同步答题卡
│  App.Medal.grantUserMedalNew        → 检查勋章
│  App.Integral.GetTaskList           → 积分任务进度
│
│ ④埋点上报（后台）
│  App.Track.Report  →  sdk.example.com  (批量上报用户行为)
│
└──────────────────────────────────────────────────────────────────
```

---

## 九、Session 有效期分析

| 测试项 | 结果 |
|---|---|
| 抓包后 22 小时再次使用 | ✅ 仍然有效 |
| 调用 `App.Common.getServerTime` 验证 | ✅ ret=0 |
| Manifest 中是否有过期配置 | ❌ 无 TTL 相关字段 |
| `getSysConfigs` 返回的登录配置 | `need_login: 1`（仅标记是否需要登录，无时效） |

**结论**：sessionid 无固定过期时间，服务端基于数据库 session 管理，长期不用后可能被清理，但正常使用周期内不会自动失效。

---

## 十、总结与反思

### 逆向难点与突破

| 难点 | 解决方法 |
|---|---|
| APK 加固（DEX 加密）| 静态分析壳结构，确认加密但不影响接口分析 |
| HTTPS 拦截 | 发现 `network_security_config` 信任用户证书，直接安装 mitmproxy CA |
| 题目数据加密 | 从 JS bundle 里找到 `DoCryptor` 类，发现 RC4 算法和硬编码密钥 |
| JS 文件被截断 | 直接 HTTP 请求拉取完整 JS 文件 |
| 混淆字段名 | 根据值的类型/范围/语义特征做规则匹配 |
| 微信 OAuth 无法模拟 | 改用 Playwright 打开真实登录页，读取 localStorage |

### 安全启示

1. **密钥不要硬编码在客户端**：本文的 RC4 密钥直接写在前端 JS 里，任何人都能提取
2. **Manifest 中的第三方 SDK Secret 是高危风险**：微信 AppSecret 明文存储，一旦 APK 被反编译即告泄露
3. **加固 ≠ 安全**：壳保护了 Java 代码，但前端 JS 完全暴露；加密了题目，但密钥也在前端
4. **network_security_config 的 `user` 证书**：方便了开发调试，但也让流量抓包毫无门槛

---

*本文仅用于技术学习与安全研究，请合法合规使用相关技术。*
