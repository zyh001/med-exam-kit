// utils/api.js —— 所有接口封装

const app = getApp()

/**
 * 基础请求，自动带鉴权头，统一错误处理
 */
function request({ method = 'GET', path, data, raw = false }) {
  const base = app.globalData.serverUrl.replace(/\/$/, '')
  const url  = base + path
  const headers = { 'Content-Type': 'application/json' }

  if (app.globalData.authToken)   headers['X-Auth-Token']    = app.globalData.authToken
  if (app.globalData.sessionToken) headers['X-Session-Token'] = app.globalData.sessionToken

  return new Promise((resolve, reject) => {
    wx.request({
      url, method,
      data: method === 'GET' ? data : undefined,
      header: headers,
      // POST/PUT body
      ...(method !== 'GET' && data ? { data: JSON.stringify(data) } : {}),
      success(res) {
        if (res.statusCode === 401) {
          app.globalData.authToken = ''
          wx.removeStorageSync('med_auth_token')
          wx.reLaunch({ url: '/pages/login/login' })
          reject({ code: 401, message: '登录已过期' })
          return
        }
        if (raw) { resolve(res); return }
        resolve(res.data)
      },
      fail(err) {
        reject(err)
      }
    })
  })
}

// ── 鉴权 ─────────────────────────────────────────────────────────
/** 用访问码登录，返回 {ok, error} */
async function login(code) {
  const res = await request({ method: 'POST', path: '/api/auth', data: { code }, raw: true })
  // 服务端成功时会 Set-Cookie；小程序不支持 Cookie，改从响应 header 读 token
  // 需要服务端额外支持 X-Auth-Token 响应头（见服务端改造说明）
  if (res.statusCode === 200 && res.data && res.data.ok) {
    const token = res.header['X-Auth-Token'] || res.header['x-auth-token'] || ''
    if (token) {
      app.globalData.authToken = token
      wx.setStorageSync('med_auth_token', token)
    }
    return { ok: true }
  }
  return { ok: false, error: (res.data && res.data.error) || '验证失败' }
}

/** 检查是否已经登录（无 PIN 模式也返回 true） */
async function checkAuth() {
  try {
    const data = await request({ path: '/api/banks' })
    if (data && data.banks) {
      if (data.session_token) app.globalData.sessionToken = data.session_token
      return true
    }
    return false
  } catch (e) {
    return false
  }
}

// ── 题库 ─────────────────────────────────────────────────────────
/** 获取所有题库列表 */
async function getBanks() {
  const data = await request({ path: '/api/banks' })
  if (data.session_token) app.globalData.sessionToken = data.session_token
  return data.banks || []
}

/** 获取题库详情 */
async function getBankInfo(bankId = 0) {
  return await request({ path: `/api/info?bank=${bankId}` })
}

// ── 组卷 ─────────────────────────────────────────────────────────
/**
 * 获取题目列表
 * @param {Object} opts - { bank, count, shuffle, units, modes, difficulty, perMode, perUnit }
 */
async function getQuestions(opts = {}) {
  const params = new URLSearchParams()
  if (opts.bank != null)  params.set('bank',    String(opts.bank))
  if (opts.count)         params.set('count',   String(opts.count))
  if (opts.shuffle != null) params.set('shuffle', opts.shuffle ? '1' : '0')
  if (opts.units && opts.units.length)
    params.set('units', JSON.stringify(opts.units))
  if (opts.modes && opts.modes.length)
    params.set('modes', JSON.stringify(opts.modes))
  if (opts.difficulty)
    params.set('difficulty', JSON.stringify(opts.difficulty))
  if (opts.perMode)
    params.set('per_mode', JSON.stringify(opts.perMode))
  if (opts.perUnit)
    params.set('per_unit', JSON.stringify(opts.perUnit))
  return await request({ path: `/api/questions?${params}` })
}

// ── 答题记录 ─────────────────────────────────────────────────────
/**
 * 提交答题记录
 * @param {Object[]} records - [{fp, si, correct, time_ms}]
 * @param {number} bankId
 */
async function postRecords(records, bankId = 0) {
  return await request({
    method: 'POST',
    path: '/api/record',
    data: { bank: bankId, records }
  })
}

/** 获取答题统计 */
async function getStats(bankId = 0) {
  return await request({ path: `/api/stats?bank=${bankId}` })
}

/** 获取答题历史 */
async function getHistory(bankId = 0) {
  return await request({ path: `/api/history?bank=${bankId}` })
}

/** 获取错题本 */
async function getWrongbook(bankId = 0) {
  return await request({ path: `/api/wrongbook?bank=${bankId}` })
}

// ── 复习 / SM-2 ──────────────────────────────────────────────────
/** 获取今日待复习题目 */
async function getReviewDue(bankId = 0) {
  return await request({ path: `/api/review/due?bank=${bankId}` })
}

/** 同步进度 */
async function sync(bankId = 0, records = []) {
  return await request({
    method: 'POST',
    path: '/api/sync',
    data: { bank: bankId, records }
  })
}

// ── 考试模式 ──────────────────────────────────────────────────────
/** 获取考试封卷答案 */
async function getExamReveal(examId) {
  return await request({ path: `/api/exam/reveal?id=${examId}` })
}

/** 获取考试剩余时间（服务端时钟） */
async function getExamTime(examId) {
  return await request({ path: `/api/exam/time?id=${examId}` })
}

// ── 收藏 ─────────────────────────────────────────────────────────
/** 同步收藏列表（本地→服务端） */
async function syncFavorites(bankId = 0, favorites = []) {
  return await request({
    method: 'POST',
    path: '/api/favorites/sync',
    data: { bank: bankId, favorites }
  })
}

// ── AI ───────────────────────────────────────────────────────────
/** AI 解题分析（流式，小程序需用 SSE 或分段请求） */
async function aiChat(bankId, question, userAns, messages = []) {
  return await request({
    method: 'POST',
    path: '/api/ai/chat',
    data: { bank: bankId, question, user_ans: userAns, messages }
  })
}

module.exports = {
  request,
  login,
  checkAuth,
  getBanks,
  getBankInfo,
  getQuestions,
  postRecords,
  getStats,
  getHistory,
  getWrongbook,
  getReviewDue,
  sync,
  getExamReveal,
  getExamTime,
  syncFavorites,
  aiChat,
}
