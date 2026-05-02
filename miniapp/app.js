// app.js  —— 全局 App 实例
// 小程序没有 Cookie，鉴权改用 wx.setStorage 持久化 token

const DEFAULT_SERVER = 'https://your-server.example.com' // 用户可在设置页修改

App({
  globalData: {
    serverUrl: '',        // 服务器地址（由设置页写入）
    sessionToken: '',     // 服务端 session token（每次启动从 /api/banks 取）
    authToken: '',        // 访问码鉴权 token（登录成功后由 Set-Cookie 携带，这里用 header 传）
    banksInfo: [],        // 所有题库
    selectedBank: 0,      // 当前选中题库索引
    bankInfo: null,       // 当前题库详情
    // 考试状态（跨页面共享）
    quizState: null,
  },

  onLaunch() {
    // 读取持久化配置
    const stored = wx.getStorageSync('med_server_url')
    this.globalData.serverUrl = stored || DEFAULT_SERVER

    const authToken = wx.getStorageSync('med_auth_token')
    if (authToken) this.globalData.authToken = authToken
  },

  // ── API 基础请求 ─────────────────────────────────────────────────────
  request(opts) {
    const { method = 'GET', path, data, header = {} } = opts
    const url = this.globalData.serverUrl.replace(/\/$/, '') + path

    // 小程序不支持 Cookie，改用自定义请求头传递鉴权 token
    const authHeader = {}
    if (this.globalData.authToken) {
      authHeader['X-Auth-Token'] = this.globalData.authToken
    }
    if (this.globalData.sessionToken) {
      authHeader['X-Session-Token'] = this.globalData.sessionToken
    }

    return new Promise((resolve, reject) => {
      wx.request({
        url,
        method,
        data,
        header: {
          'Content-Type': 'application/json',
          ...authHeader,
          ...header,
        },
        success(res) {
          if (res.statusCode === 401) {
            // token 过期，清除并跳转登录
            wx.removeStorageSync('med_auth_token')
            getApp().globalData.authToken = ''
            wx.reLaunch({ url: '/pages/login/login' })
            reject(new Error('auth_expired'))
            return
          }
          resolve(res)
        },
        fail(err) {
          reject(err)
        }
      })
    })
  },

  // 登录后保存 token（服务端从 Set-Cookie 读，小程序从响应 header 读）
  saveAuthToken(token) {
    this.globalData.authToken = token
    wx.setStorageSync('med_auth_token', token)
  },

  // 清除登录状态
  logout() {
    this.globalData.authToken = ''
    this.globalData.sessionToken = ''
    wx.removeStorageSync('med_auth_token')
    wx.reLaunch({ url: '/pages/login/login' })
  }
})
