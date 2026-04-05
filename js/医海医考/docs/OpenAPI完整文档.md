# 医海医考 API 完整文档

> 通过逆向工程还原，所有接口已通过真实调用验证 ✅  
> Base URL: `https://examapi.yhykwch.com`

---

## 一、请求流程总览

```
┌─────────────────────────────────────────────────────────────────┐
│                        完整请求流程                               │
│                                                                 │
│  1. 生成 32 位随机字符串 random                                    │
│  2. 取时间戳 ts = round(time(), 3)  // 精确到毫秒                 │
│  3. 计算 sign:                                                   │
│     sign = MD5(                                                  │
│       "random={random}&time={ts}"                                │
│       "&ua={MD5(UA.substr(12,85))}"                              │
│       "&method={GET/POST}"                                       │
│       "&token={已登录 ? storedToken : 'tourist:'+random}"         │
│       "&unique_code=fe514637e1eb8c298e3e5c2b35fbb786"            │
│       "&url={/api/xxx}"                                          │
│     )                                                            │
│  4. 构建 AppVerify JSON:                                         │
│     { route, query, random, time, source:"app",                  │
│       randomKey:"", appId, appSign, sign }                       │
│  5. AES-128-CBC 加密 → Base64 → 前缀 "aes:"                     │
│     Key: cfd0f9faab3726e5   IV: yhyk2026xzdlcxwc                 │
│  6. 设置请求头:                                                    │
│     AppVersion: 4.1.2                                            │
│     AppVerify:  aes:<base64>                                     │
│     Token:      <登录后获取> (登录请求不发)                          │
│  7. 发送请求，收到加密响应                                          │
│                                                                  │
│  ════════ 响应解密 ════════                                       │
│                                                                  │
│  8.  ivPos = (len(encryptData) - 24) % 128                       │
│  9.  embeddedIV = encryptData[ivPos : ivPos+24]                  │
│  10. ciphertext = encryptData 去掉 embeddedIV                    │
│  11. keySource = random + embeddedIV + unique_code               │
│  12. key = MD5(keySource)[7:23]                                  │
│  13. iv  = MD5(embeddedIV)[11:27]                                │
│  14. AES-128-CBC 解密 → 业务数据 JSON                             │
└─────────────────────────────────────────────────────────────────┘
```

### 时序图

```
客户端                                             服务端
  │  POST /api/login                                 │
  │  Header: AppVerify (含sign), 无Token              │
  │  Body: {"phone":"...","password":"..."}           │
  │ ───────────────────────────────────────────────> │
  │                                                  │
  │  {code:200, encryptData:"...", encryptSign:"..."} │
  │ <─────────────────────────────────────────────── │
  │  解密 → 获得 token                                │
  │                                                  │
  │  GET /api/get_user_info                           │
  │  Header: AppVerify + Token                        │
  │ ───────────────────────────────────────────────> │
  │  {code:200, encryptData:"...", encryptSign:"..."} │
  │ <─────────────────────────────────────────────── │
  │  解密 → 用户信息                                   │
```

---

## 二、密钥汇总

| 参数 | 值 | 推导方式 |
|------|-----|---------|
| AES Key | `cfd0f9faab3726e5` | `MD5("AppVerify")[7:23]` |
| AES IV | `yhyk2026xzdlcxwc` | 硬编码于 app-service.js |
| unique_code | `fe514637e1eb8c298e3e5c2b35fbb786` | `MD5("__UNI__7EE4208-yhyk2026-")` |
| appSign | `e65425ed05100e7ead3b9fda147e3b31c2c8c7d9` | APK 签名证书 SHA1 |

---

## 三、Token 机制

- **生成**: 每次 `POST /api/login` 成功后返回新 Token
- **失效**: 重新登录时旧 Token **立即失效**（单会话模型）
- **修改密码**: `PUT /api/user_update` 成功后 Token 立即失效
- **自然过期**: 未观察到自然过期，但建议做 401 自动重登逻辑
- **格式**: Base64 编码的加密字符串，约 300 字符

---

## 四、全部 API 接口 (13个)

### 4.1 POST /api/login — 登录

**认证**: 仅需 AppVerify，不需要 Token

```
请求体:
{
  "phone": "138****1234",
  "password": "your_password"
}

解密后响应:
{
  "data": {
    "id": 12345,
    "phone": "138****1234",
    "exam_type_id": 47,
    "speciality_id": 75,
    "token": "<约300字符的加密token>",
    "notice_id": "49,50",
    "created_at": "2026-04-01 14:29:01",
    "updated_at": "2026-04-05 09:06:54",
    "institutionName": "",
    "student": null,
    "topic": [],
    "video": []
  }
}
```

---

### 4.2 GET /api/get_user_info — 获取用户信息

**认证**: AppVerify + Token

```
解密后响应:
{
  "isCahce": 0,
  "data": {
    "id": 12345,
    "phone": "138****1234",
    "exam_type_id": 47,
    "speciality_id": 75,
    "exam_type": {
      "id": 47,
      "name": "全国住培结业考试",
      "pid": 0,
      "level": 1,
      "is_show": 1,
      "sort": 1
    },
    "speciality": {
      "id": 75,
      "name": "2800口腔全科-住培结业理论题库",
      "pid": 47,
      "level": 2,
      "is_show": 1,
      "sort": 28
    },
    "student": null,
    "topic": [],
    "video": []
  }
}
```

---

### 4.3 GET /api/topic_type_list — 题型分类树

**认证**: AppVerify + Token  
**说明**: 返回约 60KB 的完整分类树

```
解密后响应 (截取):
[
  {
    "id": 4687,
    "name": "口腔全科公共理论",
    "children": [
      {
        "id": 3415,
        "name": "第一章 政策法规",
        "children": [
          {
            "id": 3422,
            "name": "第一节 卫生法基本理论",
            "children": [
              {
                "id": 17672,
                "name": "单选题",
                "topic_count": 17,    // 总题数
                "answer_count": 7,    // 已答题数
                "right_count": 2,     // 正确数
                "last_answer_record": 1
              }
            ]
          }
        ]
      }
    ]
  }
]
```

---

### 4.4 GET /api/topic_type — 获取题目列表

**认证**: AppVerify + Token  
**参数**: `?topic_type_id=17672`

```
解密后响应 (截取):
[
  {
    "id": 651269,
    "topic_type_id": 17669,
    "type": 1,                        // 1=单选 2=多选 3=判断 4=填空 5=简答
    "title": "《医疗事故处理条例》规定，造成患者轻度残疾...",
    "image": null,
    "analysis": "根据《医疗事故处理条例》...",
    "option": [
      {"key": "A", "value": "一级医疗事故"},
      {"key": "B", "value": "二级医疗事故"},
      {"key": "C", "value": "三级医疗事故"},
      {"key": "D", "value": "四级医疗事故"},
      {"key": "E", "value": "不属于医疗事故"}
    ],
    "answer": ["三级医疗事故"],         // 正确答案 (选项文本, 非ABCDE)
    "sort": 1
  }
]
```

---

### 4.5 POST /api/topic_type_answer — 提交答案

**认证**: AppVerify + Token

```
请求体:
{
  "topic_id": 651269,
  "answer": ["三级医疗事故"]           // 选项文本数组, 多选传多个
}

解密后响应:
{
  "data": {
    "is_right": true,
    "answer": ["三级医疗事故"]
  }
}
```

---

### 4.6 GET /api/topic_type_comment_comment_list — 题目评论

**认证**: AppVerify + Token  
**参数**: `?page=1&per_page=10&topic_id=651269`

```
解密后响应:
{
  "test": 1,
  "data": [],
  "pagination": {
    "total": 0,
    "per_page": "10",
    "last_page": 1,
    "page": 1
  }
}
```

---

### 4.7 GET /api/advertisement — 广告/公告

**认证**: AppVerify + Token

```
解密后响应:
{
  "data": [
    {
      "id": 7,
      "status": 1,
      "image": "https://cdn.yhykwch.com/uploads/image/xxx.jpg",
      "url": null,
      "sort": 0,
      "desc": null
    }
  ]
}
```

---

### 4.8 POST /api/dict — 系统字典

**认证**: AppVerify + Token  
**请求体**: `{"key": ["lxwm"]}`  (lxwm=联系我们)

```
解密后响应:
{
  "data": [
    {
      "id": 1,
      "key": "lxwm",
      "value": "{\"lxwm\":{\"value\":\"199****3895\"}}",
      "desc": ""
    }
  ]
}
```

---

### 4.9 GET /api/activation_topic_type — 已激活题型

**认证**: AppVerify + Token

```
解密后响应:
{"data": []}
```

---

### 4.10 GET /api/video_type — 视频课程分类

**认证**: AppVerify + Token

```
解密后响应 (截取):
{
  "data": [
    {
      "id": 76,
      "name": "口腔全科-住培结业-理论考试课程",
      "children": [
        {"id": 425, "name": "口腔全科-强化班"},
        {"id": 389, "name": "口腔全科-冲刺视频（无讲义）"}
      ]
    }
  ]
}
```

---

### 4.11 GET /api/video — 视频列表

**认证**: AppVerify + Token  
**参数**: `?video_type_id=425`

---

### 4.12 GET /api/is_user_login — 检查登录状态

**认证**: 仅需 AppVerify  
**响应**: 已登录 `code:200` + 加密数据，未登录 `code:401`

---

### 4.13 PUT /api/user_update — 修改密码

**认证**: AppVerify + Token  
**请求体**: `{"current_password":"旧密码","new_password":"新密码"}`  
**副作用**: 修改成功后当前 Token 立即失效

---

## 五、错误码

| code | msg | 说明 |
|------|-----|------|
| 200 | 操作成功 | 业务数据在 encryptData 中 |
| 401 | 登录信息已被退出，请重新登录 | Token 无效或已过期 |
| 1011 | 签名错误！ | AppVerify sign 计算错误 |
| 1012 | 请求过期 | 时间戳超出有效范围 |
