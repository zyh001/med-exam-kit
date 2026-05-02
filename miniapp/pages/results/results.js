// pages/results/results.js
const quiz = require('../../utils/quiz')
const app  = getApp()

Page({
  data: {
    result: {},
    wrongItems: [],
  },

  onLoad() {
    const r = app.globalData.lastResult
    if (!r) { wx.navigateBack(); return }
    const wrongItems = r.questions
      .map((sq, i) => {
        const userAns = (r.ans[i] || []).sort().join('')
        const correctAns = quiz.parseAnswer(sq.answer).join('')
        if (!quiz.isCorrect(sq, r.ans[i] || [])) {
          return {
            num: i + 1,
            stem: (sq.stem || '').slice(0, 60) + (sq.stem && sq.stem.length > 60 ? '...' : ''),
            userAns,
            userEmpty: !userAns,
            correctAns,
            idx: i,
          }
        }
        return null
      })
      .filter(Boolean)
    this.setData({ result: r, wrongItems })
  },

  reviewAnswers() {
    // 跳回 quiz 页并在 review 模式显示
    wx.navigateTo({ url: '/pages/quiz/quiz?mode=review_done' })
  },

  goHome() {
    wx.switchTab({ url: '/pages/home/home' })
  },

  onBack() {
    wx.switchTab({ url: '/pages/home/home' })
  },
})
