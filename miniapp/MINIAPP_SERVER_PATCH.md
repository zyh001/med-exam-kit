# 服务端改造说明（小程序接入）

微信小程序不支持 Cookie，需要对后端做以下改造。

## 1. 支持 X-Auth-Token 请求头鉴权

小程序登录后，把 token 存在 `wx.storage`，每次请求通过 `X-Auth-Token` header 传递。

### Go 版（internal/server/server.go）

在 `auth.IsAuthenticated` 的调用处之前，新增 Header 鉴权逻辑：

```go
// miniapp.go 新增文件（或在 server.go 的 isAuthenticated 封装里）

func (s *Server) isAuthenticatedReq(r *http.Request) bool {
    // 原 Cookie 方式
    if auth.IsAuthenticated(r, s.cfg.CookieSecret, s.cfg.AccessCode) {
        return true
    }
    // 小程序：X-Auth-Token header
    token := r.Header.Get("X-Auth-Token")
    if token == "" { return false }
    // 用相同的 HMAC 验证（与 Cookie 值相同的格式）
    return auth.ValidateToken(token, s.cfg.CookieSecret, s.cfg.AccessCode)
}
```

### 登录接口改造（POST /api/auth）

登录成功时，除了设置 Cookie 外，在响应 header 里也写入 token：

```go
func (s *Server) handleAPIAuth(...) {
    // ...验证成功后：
    token := auth.MakeToken(s.cfg.CookieSecret, s.cfg.AccessCode)
    auth.SetAuthCookie(w, r, s.cfg.CookieSecret, s.cfg.AccessCode)
    w.Header().Set("X-Auth-Token", token)    // ← 新增
    w.Header().Set("Access-Control-Expose-Headers", "X-Auth-Token")  // ← CORS
    jsonOK(w, map[string]any{"ok": true})
}
```

### Python 版（src/med_exam_toolkit/quiz.py）

在 `before_request` 里增加 Header 鉴权：

```python
def _is_authenticated_req():
    # 原 Cookie 方式
    if is_authenticated(_cookie_secret, _access_code):
        return True
    # 小程序 Header 方式
    token = request.headers.get('X-Auth-Token', '')
    if not token:
        return False
    return verify_token(token, _cookie_secret, _access_code)
```

## 2. CORS 配置（如果小程序走非 80/443 端口）

微信开发工具本地调试时需要：

```go
// 允许小程序开发工具域名
w.Header().Set("Access-Control-Allow-Origin", "*")
w.Header().Set("Access-Control-Allow-Headers",
    "Content-Type, X-Auth-Token, X-Session-Token")
```

## 3. 微信小程序后台配置

1. 登录 [小程序后台](https://mp.weixin.qq.com)
2. 开发 → 开发管理 → 服务器域名
3. 把你的服务器域名加到 **request 合法域名** 中（必须 HTTPS）

## 4. 本地开发调试

开发阶段在微信开发者工具中：
- 勾选 **不校验合法域名**（工具 → 详情 → 本地设置）
- 可以用 HTTP 调试本地服务器

## 5. 发布

1. `project.config.json` 里填入你的 AppID
2. 微信开发者工具 → 上传代码
3. 小程序后台 → 提交审核

---

> **注意**：执业医师题库属于医疗类内容，微信小程序需要资质审核。
> 建议以「学习工具」类目申请，备注用于考前自测。
