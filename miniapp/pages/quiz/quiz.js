// pages/quiz/quiz.js
const api  = require('../../utils/api')
const quiz = require('../../utils/quiz')
const app  = getApp()

Page({
  data: {
    mode: 'practice',
    questions: [],
    cur: 0,
    ans: {},          // { idx: [letters] }
    marked: new Set(),// 标记的题目 idx（考试模式）
    revealed: false,
    bankInfo: {},

    // 当前题目派生数据
    currentGroup: {},
    currentSq: {},
    currentSqGroupIdx: 0,
    progressPct: 0,
    userAnswerLetters: [],
    correctAnswers: [],
    isCorrectAns: false,
    isFav: false,

    // 计时器（考试模式）
    timerDisplay: '00:00',
    timerUrgent: false,
    examPaused: false,
    realPaused: false,

    // AI
    aiLoading: false,
    aiContent: '',
  },

  // ── 内部状态（非 data，不触发渲染）─────────────────────────────
  _groups: [],          // { fp, stem, mode, unit, items:[] }
  _groupForIdx: [],     // questions[i] → group
  _favorites: new Set(),
  _timerInterval: null,
  _examStart: 0,        // serverNow() 时刻
  _examLimit: 0,        // 秒
  _pauseOffsetMs: 0,    // 累计真实暂停时长（ms）
  _realPauseStartMs: 0, // 当前真实暂停开始时刻
  _pendingRecords: [],  // 待提交的答题记录
  _bankId: 0,

  onLoad(opts) {
    const { mode = 'practice', restore = '0' } = opts
    this._bankId = app.globalData.selectedBank || 0
    this.setData({ mode, bankInfo: app.globalData.bankInfo || {} })
    this._favorites = new Set(quiz.loadFavorites(this._bankId))

    if (restore === '1') {
      this._restoreSession()
    } else {
      const state = app.globalData.quizState
      if (!state || !state.questions.length) {
        wx.showToast({ title: '题目加载失败', icon: 'error' })
        wx.navigateBack()
        return
      }
      this._initQuiz(state.questions, mode, state.examTime || 90 * 60)
    }
  },

  onUnload() {
    this._stopTimer()
    this._flushRecords()
  },

  // ── 初始化 ──────────────────────────────────────────────────────
  _initQuiz(questions, mode, examTimeSec) {
    this._groups = quiz.buildQuestionGroups(questions)
    this._groupForIdx = []
    this._groups.forEach(g => g.items.forEach(sq => { this._groupForIdx[sq.globalIdx] = g }))

    this.setData({ questions, mode, cur: 0, ans: {}, revealed: false })
    this._renderCurrent()

    if (mode === 'exam') {
      this._examLimit = examTimeSec
      this._examStart = Date.now()
      this._pauseOffsetMs = 0
      this._startTimer()
    }
  },

  _restoreSession() {
    const s = quiz.loadExamSession(this._bankId)
    if (!s) { wx.navigateBack(); return }
    this._groups = quiz.buildQuestionGroups(s.questions)
    this._groupForIdx = []
    this._groups.forEach(g => g.items.forEach(sq => { this._groupForIdx[sq.globalIdx] = g }))

    const pauseOffset = s.examPauseOffsetMs || 0
    this._pauseOffsetMs = pauseOffset
    this._realPauseStartMs = 0
    const elapsed = Math.floor((Date.now() - s.savedAt) / 1000)
    const remaining = Math.max(0, s.remaining - elapsed)

    this.setData({
      questions: s.questions,
      mode: s.mode,
      cur: s.cur || 0,
      ans: quiz.deserializeAns(s.ans),
      revealed: false,
    })
    this._renderCurrent()

    if (s.mode === 'exam') {
      this._examLimit = s.examLimit
      // 通过反推：examStart = now - (examLimit - remaining) + pauseOffset
      this._examStart = Date.now() - (s.examLimit - remaining) * 1000 + pauseOffset
      this._startTimer()
    }
  },

  // ── 渲染当前题目 ─────────────────────────────────────────────────
  _renderCurrent() {
    const { questions, cur, ans } = this.data
    if (!questions.length) return
    const sq = questions[cur]
    const group = this._groupForIdx[cur] || { items: [sq], mode: sq.mode, unit: sq.unit }
    const groupIdx = group.items.findIndex(i => i.globalIdx === cur)
    const userAns = ans[cur] || []
    const correctAns = quiz.parseAnswer(sq.answer)
    const isCorrect = quiz.isCorrect(sq, userAns)

    this.setData({
      currentGroup: group,
      currentSq: sq,
      currentSqGroupIdx: groupIdx,
      progressPct: Math.round((cur + 1) / questions.length * 100),
      userAnswerLetters: userAns,
      correctAnswers: correctAns,
      isCorrectAns: isCorrect,
      isFav: this._favorites.has(sq.fp),
      aiContent: '',
      aiLoading: false,
    })
  },

  // ── 选项交互 ─────────────────────────────────────────────────────
  selectOpt(e) {
    const { letter } = e.currentTarget.dataset
    const { cur, ans, revealed, mode, currentSq } = this.data
    if (revealed) return

    const correctAns = quiz.parseAnswer(currentSq.answer)
    const isMulti = correctAns.length > 1

    let current = [...(ans[cur] || [])]
    if (isMulti) {
      const idx = current.indexOf(letter)
      if (idx >= 0) current.splice(idx, 1)
      else current.push(letter)
    } else {
      current = [letter]
      // 单选：自动揭示
      const newAns = { ...ans, [cur]: current }
      this.setData({ ans: newAns })
      this._afterAnswer(cur, current, newAns)
      return
    }
    this.setData({ [`ans.${cur}`]: current })
  },

  // 多选确认提交 / 练习模式的「查看答案」
  revealAnswer() {
    const { cur, ans, mode } = this.data
    const userAns = ans[cur] || []
    if (!userAns.length) {
      wx.showToast({ title: '请先选择答案', icon: 'none' })
      return
    }
    const newAns = { ...ans, [cur]: userAns }
    this.setData({ ans: newAns })
    this._afterAnswer(cur, userAns, newAns)
  },

  _afterAnswer(idx, userAns, newAns) {
    const { questions } = this.data
    const sq = questions[idx]
    const isCorrect = quiz.isCorrect(sq, userAns)
    const correctAns = quiz.parseAnswer(sq.answer)

    this.setData({
      revealed: true,
      userAnswerLetters: [...userAns].sort(),
      correctAnswers: correctAns,
      isCorrectAns: isCorrect,
    })

    // 记录答题
    this._pendingRecords.push({
      fp: sq.fp,
      si: sq.si ?? 0,
      correct: isCorrect,
      time_ms: 0,
    })
    if (this._pendingRecords.length >= 5) this._flushRecords()
  },

  // ── 导航 ─────────────────────────────────────────────────────────
  prevQ() {
    const { cur } = this.data
    if (cur <= 0) return
    this.setData({ cur: cur - 1, revealed: false })
    this._renderCurrent()
  },

  nextQ() {
    const { cur, questions, mode } = this.data
    if (cur >= questions.length - 1) {
      if (mode === 'exam') {
        this.submitExam()
      } else {
        this._showResults()
      }
      return
    }
    this.setData({ cur: cur + 1, revealed: false })
    this._renderCurrent()
    if (mode === 'exam') this._saveSession()
  },

  // ── 考试交卷 ─────────────────────────────────────────────────────
  submitExam() {
    const { questions, ans } = this.data
    const unanswered = questions.filter((_, i) => !(ans[i] || []).length).length
    if (unanswered > 0) {
      wx.showModal({
        title: '确认交卷',
        content: `还有 ${unanswered} 道题未作答，确认交卷？`,
        success: (res) => { if (res.confirm) this._doSubmit() }
      })
    } else {
      this._doSubmit()
    }
  },

  _doSubmit() {
    this._stopTimer()
    this._flushRecords()
    quiz.clearExamSession(this._bankId)
    this.setData({ examPaused: false, realPaused: false })
    this._showResults()
  },

  _showResults() {
    const { questions, ans, mode } = this.data
    const result = quiz.calcScore(questions, ans)
    app.globalData.lastResult = { ...result, mode, questions, ans }
    wx.redirectTo({ url: '/pages/results/results' })
  },

  // ── 计时器 ────────────────────────────────────────────────────────
  _startTimer() {
    this._stopTimer()
    this._timerInterval = setInterval(() => this._tick(), 1000)
    this._tick()
  },

  _stopTimer() {
    if (this._timerInterval) {
      clearInterval(this._timerInterval)
      this._timerInterval = null
    }
  },

  _tick() {
    if (this._realPauseStartMs) return // 真正暂停中冻结
    const elapsed = Math.floor((Date.now() - this._examStart) / 1000)
    const rem = Math.max(0, this._examLimit - elapsed)
    const urgent = rem < 300
    this.setData({ timerDisplay: quiz.fmtSec(rem), timerUrgent: urgent })
    if (rem <= 0) {
      this._stopTimer()
      wx.showToast({ title: '时间到！自动交卷', icon: 'none', duration: 2000 })
      setTimeout(() => this._doSubmit(), 2000)
    }
  },

  // ── 暂停 ──────────────────────────────────────────────────────────
  pauseExam() {
    if (this.data.mode !== 'exam') return
    this.setData({ examPaused: true })
    this._saveSession()
  },

  onResumeTap() {
    // 真正暂停状态下，普通点击也先恢复真正暂停
    if (this._realPauseStartMs) {
      this._endRealPause()
    }
    this.setData({ examPaused: false })
  },

  // 长按"继续答题" 3s → 切换真正暂停
  onResumeLongPress() {
    if (this._realPauseStartMs) {
      // 已经真正暂停 → 恢复
      this._endRealPause()
      wx.showToast({ title: '▶ 已恢复计时', icon: 'none' })
    } else {
      // 进入真正暂停
      this._realPauseStartMs = Date.now()
      this.setData({ realPaused: true })
      this._saveSession() // 立刻落盘冻结时刻
      wx.showToast({ title: '⏸ 已真正冻结计时', icon: 'none' })
    }
  },

  _endRealPause() {
    if (!this._realPauseStartMs) return
    const dur = Date.now() - this._realPauseStartMs
    if (dur > 0) {
      this._pauseOffsetMs += dur
      this._examStart += dur // 推迟 examStart，使 elapsed 不包含暂停时间
    }
    this._realPauseStartMs = 0
    this.setData({ realPaused: false })
  },

  // ── 会话持久化 ────────────────────────────────────────────────────
  _saveSession() {
    if (this.data.mode !== 'exam') return
    let pauseOffset = this._pauseOffsetMs
    if (this._realPauseStartMs) pauseOffset += Date.now() - this._realPauseStartMs
    const effectiveStart = this._examStart + pauseOffset
    const elapsed = Math.floor((Date.now() - effectiveStart) / 1000)
    const remaining = Math.max(0, this._examLimit - elapsed)
    quiz.saveExamSession(this._bankId, {
      mode: this.data.mode,
      questions: this.data.questions,
      ans: quiz.serializeAns(this.data.ans),
      cur: this.data.cur,
      examLimit: this._examLimit,
      remaining,
      savedAt: Date.now(),
      examPauseOffsetMs: pauseOffset,
    })
  },

  // ── 收藏 ─────────────────────────────────────────────────────────
  toggleFav() {
    const { currentSq } = this.data
    const fp = currentSq.fp
    const isFav = this._favorites.has(fp)
    if (isFav) this._favorites.delete(fp)
    else this._favorites.add(fp)
    quiz.saveFavorites(this._bankId, [...this._favorites])
    this.setData({ isFav: !isFav })
    api.syncFavorites(this._bankId, [...this._favorites]).catch(() => {})
  },

  // ── 标记（考试模式）─────────────────────────────────────────────
  toggleMark() {
    const { cur } = this.data
    const marked = new Set(this.data._markedSet || [])
    if (marked.has(cur)) marked.delete(cur)
    else marked.add(cur)
    this.setData({ marked: marked.has(cur), _markedSet: [...marked] })
  },

  // ── AI 解析 ───────────────────────────────────────────────────────
  async askAI() {
    const { currentSq, ans, cur } = this.data
    this.setData({ aiLoading: true, aiContent: '' })
    try {
      const res = await api.aiChat(
        this._bankId,
        currentSq,
        (ans[cur] || []).join(''),
      )
      const text = (res.content || []).map(c => c.text || '').join('')
      this.setData({ aiContent: text, aiLoading: false })
    } catch(e) {
      this.setData({ aiLoading: false })
      wx.showToast({ title: 'AI 解析失败', icon: 'error' })
    }
  },

  // ── 提交答题记录 ──────────────────────────────────────────────────
  async _flushRecords() {
    if (!this._pendingRecords.length) return
    const records = [...this._pendingRecords]
    this._pendingRecords = []
    try {
      await api.postRecords(records, this._bankId)
    } catch(e) {
      // 失败：退回队列，下次再试
      this._pendingRecords = [...records, ...this._pendingRecords]
    }
  },

  // ── 返回 ──────────────────────────────────────────────────────────
  onBack() {
    if (this.data.mode === 'exam' && !this.data.examPaused) {
      this.pauseExam()
      return
    }
    wx.navigateBack()
  },

  // ── WXS 函数替代（在 JS 里计算样式类）───────────────────────────
  // 注意：WXML 里 {{getOptClass(...)}} 调用无效，改用计算属性
  // 实际选项 class 在 _renderCurrent 里通过 setData 计算

  getOptClass(letter) {
    // 此函数在 WXML 里无法直接调用，通过 WXS 模块实现
    return ''
  },
})
