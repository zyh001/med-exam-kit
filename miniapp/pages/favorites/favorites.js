// pages/favorites/favorites.js
const api  = require('../../utils/api')
const quiz = require('../../utils/quiz')
const app  = getApp()

Page({
  data: { items: [] },

  async onShow() {
    const bankId = app.globalData.selectedBank || 0
    const fps = quiz.loadFavorites(bankId)
    if (!fps.length) { this.setData({ items: [] }); return }
    try {
      // 用 /api/questions?fps= 批量取题
      const data = await api.getQuestions({ bank: bankId, fps })
      this.setData({ items: (data.items || []).slice(0, 200) })
    } catch(e) {
      this.setData({ items: [] })
    }
  },

  startQuizFromFav(e) {
    const idx = e.currentTarget.dataset.idx
    const { items } = this.data
    app.globalData.quizState = {
      mode: 'practice',
      questions: items.slice(idx),
      bankId: app.globalData.selectedBank || 0,
    }
    wx.navigateTo({ url: '/pages/quiz/quiz?mode=practice' })
  },
})
