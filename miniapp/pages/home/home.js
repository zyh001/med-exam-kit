// pages/home/home.js
const api  = require('../../utils/api')
const quiz = require('../../utils/quiz')
const app  = getApp()

Page({
  data: {
    banks: [],
    currentBank: {},
    bankInfo: { total_sq: 0, units: [], modes: [] },
    history: [],
    savedSession: null,
    todayCount: 0,
    cfg: {
      count: 50,
      shuffle: true,
      examTime: 90,
      units: [],          // [] = 全部
      modes: [],          // [] = 全部
      difficulty: null,   // null = 不限
      unitLabel: '全部章节',
      modeLabel: '全部题型',
      difficultyLabel: '不限难度',
    },
  },

  async onLoad() {
    try {
      const banks = await api.getBanks()
      app.globalData.banksInfo = banks
      const saved = wx.getStorageSync('med_selected_bank') || 0
      const bankId = saved < banks.length ? saved : 0
      app.globalData.selectedBank = bankId
      this.setData({ banks, currentBank: banks[bankId] || {} })
      await this.loadBankInfo(bankId)
      this.loadHistory()
      this.checkSession()
    } catch(e) {
      if (e.code === 401) return // 已在 api.js 跳转
      wx.showToast({ title: '连接失败', icon: 'error' })
    }
  },

  onShow() {
    this.loadHistory()
    this.checkSession()
  },

  async loadBankInfo(bankId) {
    try {
      const info = await api.getBankInfo(bankId)
      app.globalData.bankInfo = info
      // 今日答题数
      const stats = await api.getStats(bankId)
      const today = stats.today_count || 0
      this.setData({ bankInfo: info, todayCount: today })
    } catch(e) {}
  },

  async loadHistory() {
    try {
      const bankId = app.globalData.selectedBank || 0
      const data = await api.getHistory(bankId)
      const sessions = (data.sessions || []).slice(0, 10).map(s => ({
        ...s,
        accuracy: s.total > 0 ? Math.round(s.correct / s.total * 100) : 0,
        dateStr: new Date(s.finished_at || s.created_at).toLocaleString('zh-CN', {
          month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit'
        })
      }))
      this.setData({ history: sessions })
    } catch(e) {}
  },

  checkSession() {
    const bankId = app.globalData.selectedBank || 0
    const s = quiz.loadExamSession(bankId)
    if (!s) { this.setData({ savedSession: null }); return }
    const elapsed = Math.floor((Date.now() - s.savedAt) / 1000)
    const remaining = Math.max(0, s.remaining - elapsed)
    if (remaining <= 0 && s.mode === 'exam') {
      quiz.clearExamSession(bankId)
      this.setData({ savedSession: null })
      return
    }
    this.setData({ savedSession: { ...s, remainingFmt: quiz.fmtSec(remaining) } })
  },

  resumeSession() {
    const s = this.data.savedSession
    if (!s) return
    wx.navigateTo({ url: `/pages/quiz/quiz?restore=1&mode=${s.mode}` })
  },

  discardSession() {
    wx.showModal({
      title: '丢弃进度',
      content: '确定要丢弃这次未完成的考试吗？',
      success: (res) => {
        if (res.confirm) {
          quiz.clearExamSession(app.globalData.selectedBank || 0)
          this.setData({ savedSession: null })
        }
      }
    })
  },

  // ── 开始练习 ────────────────────────────────────────────────────
  startPractice() {
    this._startQuiz('practice')
  },

  async startExam() {
    this._startQuiz('exam')
  },

  async startReview() {
    try {
      wx.showLoading({ title: '加载中' })
      const bankId = app.globalData.selectedBank || 0
      const data = await api.getReviewDue(bankId)
      wx.hideLoading()
      if (!data.items || !data.items.length) {
        wx.showToast({ title: '今日无待复习题目 🎉', icon: 'none', duration: 3000 })
        return
      }
      app.globalData.quizState = { mode: 'review', questions: data.items, bankId }
      wx.navigateTo({ url: '/pages/quiz/quiz?mode=review' })
    } catch(e) {
      wx.hideLoading()
      wx.showToast({ title: '加载失败', icon: 'error' })
    }
  },

  async startWrongbook() {
    try {
      wx.showLoading({ title: '加载中' })
      const bankId = app.globalData.selectedBank || 0
      const { cfg } = this.data
      const data = await api.getQuestions({
        bank: bankId,
        count: cfg.count,
        shuffle: true,
        wrongbook: true,
      })
      wx.hideLoading()
      if (!data.items || !data.items.length) {
        wx.showToast({ title: '错题本为空', icon: 'none' })
        return
      }
      app.globalData.quizState = { mode: 'practice', questions: data.items, bankId }
      wx.navigateTo({ url: '/pages/quiz/quiz?mode=practice' })
    } catch(e) {
      wx.hideLoading()
      wx.showToast({ title: '加载失败', icon: 'error' })
    }
  },

  async _startQuiz(mode) {
    const { cfg } = this.data
    const bankId  = app.globalData.selectedBank || 0
    try {
      wx.showLoading({ title: '组卷中...' })
      const params = {
        bank: bankId,
        count: cfg.count,
        shuffle: cfg.shuffle,
      }
      if (cfg.units.length) params.units = cfg.units
      if (cfg.modes.length)  params.modes = cfg.modes
      if (cfg.difficulty)    params.difficulty = cfg.difficulty
      const data = await api.getQuestions(params)
      wx.hideLoading()
      if (!data.items || !data.items.length) {
        wx.showToast({ title: '未找到匹配题目', icon: 'none' })
        return
      }
      app.globalData.quizState = {
        mode,
        questions: data.items,
        bankId,
        examTime: cfg.examTime * 60,
      }
      wx.navigateTo({ url: `/pages/quiz/quiz?mode=${mode}` })
    } catch(e) {
      wx.hideLoading()
      wx.showToast({ title: '加载失败', icon: 'error' })
    }
  },

  // ── 题库选择 ────────────────────────────────────────────────────
  showBankPicker() {
    const { banks } = this.data
    wx.showActionSheet({
      itemList: banks.map(b => b.name),
      success: async (res) => {
        const bankId = res.tapIndex
        app.globalData.selectedBank = bankId
        wx.setStorageSync('med_selected_bank', bankId)
        this.setData({ currentBank: banks[bankId] })
        await this.loadBankInfo(bankId)
        this.checkSession()
      }
    })
  },

  // ── 配置 ────────────────────────────────────────────────────────
  decCount() {
    const c = Math.max(10, this.data.cfg.count - 10)
    this.setData({ 'cfg.count': c })
  },
  incCount() {
    const c = Math.min(200, this.data.cfg.count + 10)
    this.setData({ 'cfg.count': c })
  },
  decExamTime() {
    const t = Math.max(30, this.data.cfg.examTime - 15)
    this.setData({ 'cfg.examTime': t })
  },
  incExamTime() {
    const t = Math.min(300, this.data.cfg.examTime + 15)
    this.setData({ 'cfg.examTime': t })
  },
  onShuffleChange(e) {
    this.setData({ 'cfg.shuffle': e.detail.value })
  },

  showUnitPicker() {
    const info = this.data.bankInfo
    const units = ['全部章节', ...(info.units || [])]
    wx.showActionSheet({
      itemList: units,
      success: (res) => {
        if (res.tapIndex === 0) {
          this.setData({ 'cfg.units': [], 'cfg.unitLabel': '全部章节' })
        } else {
          const unit = units[res.tapIndex]
          this.setData({ 'cfg.units': [unit], 'cfg.unitLabel': unit })
        }
      }
    })
  },

  showModePicker() {
    const info = this.data.bankInfo
    const modes = ['全部题型', ...(info.modes || [])]
    wx.showActionSheet({
      itemList: modes,
      success: (res) => {
        if (res.tapIndex === 0) {
          this.setData({ 'cfg.modes': [], 'cfg.modeLabel': '全部题型' })
        } else {
          const mode = modes[res.tapIndex]
          this.setData({ 'cfg.modes': [mode], 'cfg.modeLabel': mode })
        }
      }
    })
  },

  showDifficultyPicker() {
    const opts = ['不限难度', '简单 (≥80%)', '中等 (60-80%)', '较难 (40-60%)', '困难 (<40%)']
    const maps = [null,
      { easy: 100 },
      { medium: 100 },
      { hard: 100 },
      { extreme: 100 },
    ]
    wx.showActionSheet({
      itemList: opts,
      success: (res) => {
        this.setData({
          'cfg.difficulty': maps[res.tapIndex],
          'cfg.difficultyLabel': opts[res.tapIndex],
        })
      }
    })
  },
})
