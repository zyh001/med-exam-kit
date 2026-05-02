// pages/login/login.js
const api = require('../../utils/api')
const app = getApp()

Page({
  data: {
    serverUrl: '',
    pin: '',
    showPin: false,
    loading: false,
    errMsg: '',
    needPin: true,   // 先假设需要 PIN，连接后确认
  },

  onLoad() {
    const url = wx.getStorageSync('med_server_url') || ''
    this.setData({ serverUrl: url })
  },

  onServerUrlInput(e) {
    this.setData({ serverUrl: e.detail.value, errMsg: '' })
  },

  onPinInput(e) {
    this.setData({ pin: e.detail.value.toUpperCase(), errMsg: '' })
  },

  toggleShowPin() {
    this.setData({ showPin: !this.data.showPin })
  },

  async onLogin() {
    let { serverUrl, pin } = this.data
    serverUrl = serverUrl.trim().replace(/\/$/, '')
    if (!serverUrl) {
      this.setData({ errMsg: '请输入服务器地址' })
      return
    }

    this.setData({ loading: true, errMsg: '' })
    // 保存服务器地址
    wx.setStorageSync('med_server_url', serverUrl)
    app.globalData.serverUrl = serverUrl

    try {
      // 先检查是否需要 PIN（无 PIN 模式直接过）
      const res = await api.login(pin || '')
      if (res.ok) {
        wx.reLaunch({ url: '/pages/home/home' })
      } else {
        this.setData({ errMsg: res.error || '验证失败，请检查访问码', loading: false })
      }
    } catch(e) {
      this.setData({
        errMsg: e.errMsg || e.message || '无法连接服务器，请检查地址',
        loading: false
      })
    }
  },
})
