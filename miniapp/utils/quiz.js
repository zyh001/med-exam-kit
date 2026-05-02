// utils/quiz.js —— 题目处理、答案序列化、SM-2 相关工具

/** 将服务端返回的扁平化小题列表整理成带题干分组的结构 */
function buildQuestionGroups(items) {
  const groups = []
  let cur = null
  items.forEach((sq, idx) => {
    if (!cur || cur.fp !== sq.fp) {
      cur = { fp: sq.fp, stem: sq.stem, mode: sq.mode, unit: sq.unit, items: [], startIdx: idx }
      groups.push(cur)
    }
    cur.items.push({ ...sq, globalIdx: idx })
  })
  return groups
}

/** 判断答案是否正确（支持单选、多选、B型） */
function isCorrect(question, userAns) {
  if (!userAns || userAns.length === 0) return false
  const correct = (question.answer || '').toUpperCase().split('').filter(Boolean)
  if (correct.length === 0) return false

  const user = Array.isArray(userAns)
    ? userAns.map(a => a.toUpperCase()).sort()
    : [userAns.toUpperCase()]
  const sortedCorrect = [...correct].sort()

  if (user.length !== sortedCorrect.length) return false
  return user.every((a, i) => a === sortedCorrect[i])
}

/** 格式化秒数为 mm:ss */
function fmtSec(sec) {
  const m = String(Math.floor(sec / 60)).padStart(2, '0')
  const s = String(sec % 60).padStart(2, '0')
  return `${m}:${s}`
}

/** 序列化 ans map 为可存储格式（Set 转 Array） */
function serializeAns(ans) {
  const out = {}
  Object.keys(ans).forEach(k => {
    const v = ans[k]
    out[k] = v instanceof Array ? v : v ? [v] : []
  })
  return out
}

/** 反序列化 */
function deserializeAns(ans) {
  if (!ans) return {}
  return ans
}

/** 从 LocalStorage 读取考试会话 */
function loadExamSession(bankId) {
  const key = `exam_session_${bankId}`
  try {
    const raw = wx.getStorageSync(key)
    if (!raw) return null
    const s = JSON.parse(raw)
    if (!s || !Array.isArray(s.questions) || !s.questions.length) return null
    return s
  } catch(e) { return null }
}

/** 保存考试会话 */
function saveExamSession(bankId, state) {
  const key = `exam_session_${bankId}`
  try {
    wx.setStorageSync(key, JSON.stringify(state))
  } catch(e) { console.warn('保存考试进度失败', e) }
}

/** 清除考试会话 */
function clearExamSession(bankId) {
  wx.removeStorageSync(`exam_session_${bankId}`)
}

/** 收藏本地存储 */
function loadFavorites(bankId) {
  try {
    const raw = wx.getStorageSync(`favorites_${bankId}`)
    return raw ? JSON.parse(raw) : []
  } catch(e) { return [] }
}

function saveFavorites(bankId, list) {
  wx.setStorageSync(`favorites_${bankId}`, JSON.stringify(list))
}

/** 选项字母 0→A, 1→B, ... */
function optLetter(idx) {
  return String.fromCharCode(65 + idx)
}

/** 解析正确答案为字母数组 */
function parseAnswer(ansStr) {
  if (!ansStr) return []
  return ansStr.toUpperCase().split('').filter(c => /[A-Z]/.test(c))
}

/** 难度桶阈值（正确率 → 描述） */
function difficultyLabel(rate) {
  if (!rate || rate <= 0) return ''
  if (rate >= 80) return '简单'
  if (rate >= 60) return '中等'
  if (rate >= 40) return '较难'
  return '困难'
}

function difficultyColor(rate) {
  if (!rate || rate <= 0) return '#64748b'
  if (rate >= 80) return '#3fd27f'
  if (rate >= 60) return '#4493f8'
  if (rate >= 40) return '#f0a020'
  return '#f26c6c'
}

/** 简单计算答题得分（正确 1 分，多选部分正确 0.5 分，否则 0） */
function calcScore(questions, ans, scoringCfg = {}) {
  let total = 0, correct = 0
  questions.forEach((sq, idx) => {
    total++
    const userAns = ans[idx] || []
    if (isCorrect(sq, userAns)) correct++
  })
  return { total, correct, wrong: total - correct - countUnanswered(questions, ans),
           unanswered: countUnanswered(questions, ans) }
}

function countUnanswered(questions, ans) {
  return questions.filter((_, i) => !ans[i] || ans[i].length === 0).length
}

module.exports = {
  buildQuestionGroups,
  isCorrect,
  fmtSec,
  serializeAns,
  deserializeAns,
  loadExamSession,
  saveExamSession,
  clearExamSession,
  loadFavorites,
  saveFavorites,
  optLetter,
  parseAnswer,
  difficultyLabel,
  difficultyColor,
  calcScore,
  countUnanswered,
}
