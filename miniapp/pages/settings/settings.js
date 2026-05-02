// pages/settings/settings.js
const app = getApp()

Page({
  data: {
    serverUrl: '',
    authStatus: '已登录',
    darkMode: true,
  },

  onLoad() {
    this.setData({
      serverUrl: app.globalData.serverUrl || '',
      darkMode: wx.getStorageSync('med_dark_mode') !== false,
    })
  },

  onUrlInput(e) { this.setData({ serverUrl: e.detail.value }) },

  saveServer() {
    const url = this.data.serverUrl.trim().replace(/\/$/, '')
    wx.setStorageSync('med_server_url', url)
    app.globalData.serverUrl = url
    wx.showToast({ title: '已保存', icon: 'success' })
  },

  onThemeChange(e) {
    const dark = e.detail.value
    wx.setStorageSync('med_dark_mode', dark)
    this.setData({ darkMode: dark })
    // 通知页面刷新主题
    const pages = getCurrentPages()
    pages.forEach(p => {
      if (p.loadTheme) p.loadTheme()
    })
  },

  logout() {
    wx.showModal({
      title: '退出登录',
      content: '确认退出？需要重新输入访问码',
      success: (res) => {
        if (res.confirm) app.logout()
      }
    })
  },
})
