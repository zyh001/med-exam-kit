/* ================================================================
   quiz_sync.js  离线优先同步管理器
   ----------------------------------------------------------------
   职责：
     1. 答题记录先写 IndexedDB（本地持久化，断网不丢）
     2. 联网时自动把 IndexedDB 队列 flush 到 Flask /api/sync
     3. 对外暴露 window.SyncManager，quiz.js 通过它记录会话

   数据流：
     答完题
       → SyncManager.record(payload)
           → 写 IndexedDB (sync_queue)    ← 本地，立即可靠
           → tryFlush()                   ← 尝试上传
               成功 → 删除队列条目
               失败 → 留在队列，下次重试

   冲突策略（"本地数据优先"）：
     - sessions / attempts 天然 append-only，session_id 唯一，
       服务端用 INSERT OR IGNORE，重复条目直接跳过，无冲突。
     - SM-2 由服务端从 attempts 派生，客户端不直接写 SM-2 表。

   IndexedDB schema:
     DB  : med_exam_sync_v1
     Store: sync_queue
       { id(auto), session_id(str), payload(obj), queued_at(ms), retries(int) }
   ================================================================ */

const SyncManager = (() => {
  const DB_NAME    = 'med_exam_sync_v1';
  const STORE      = 'sync_queue';
  const FLUSH_INTERVAL_MS = 30_000;   // 30 秒自动重试一次
  const MAX_RETRIES       = 10;        // 超过此次数的条目视为损坏，清除

  let _db    = null;   // IDBDatabase 实例（惰性初始化）
  let _timer = null;   // 定期 flush 的 setInterval handle
  let _flushing = false;

  // ── 状态（供 UI 读取）──────────────────────────────────────────
  const state = {
    pending:    0,   // 待同步条目数
    lastSync:   0,   // 最近一次成功同步的时间戳
    online:     navigator.onLine,
    syncing:    false,
  };

  // ── UI 回调（由 quiz.js 注入，可选）───────────────────────────
  let _onStateChange = null;

  function _notify() {
    _onStateChange && _onStateChange({ ...state });
  }

  // ══════════════════════════════════════════
  // IndexedDB 初始化
  // ══════════════════════════════════════════

  function _openDB() {
    if (_db) return Promise.resolve(_db);
    return new Promise((resolve, reject) => {
      const req = indexedDB.open(DB_NAME, 1);
      req.onupgradeneeded = e => {
        const db    = e.target.result;
        if (!db.objectStoreNames.contains(STORE)) {
          const store = db.createObjectStore(STORE, { keyPath: 'id', autoIncrement: true });
          store.createIndex('session_id', 'session_id', { unique: true });
          store.createIndex('queued_at',  'queued_at',  { unique: false });
        }
      };
      req.onsuccess = e => { _db = e.target.result; resolve(_db); };
      req.onerror   = e => reject(e.target.error);
    });
  }

  // ── 工具：IndexedDB transaction helpers ─────────────────────
  function _tx(mode, fn) {
    return _openDB().then(db => new Promise((resolve, reject) => {
      const tx    = db.transaction(STORE, mode);
      const store = tx.objectStore(STORE);
      const req   = fn(store);
      req.onsuccess = () => resolve(req.result);
      req.onerror   = () => reject(req.error);
    }));
  }

  function _txAll(mode, fn) {
    return _openDB().then(db => new Promise((resolve, reject) => {
      const tx    = db.transaction(STORE, mode);
      const store = tx.objectStore(STORE);
      const items = [];
      const cur   = store.openCursor();
      cur.onsuccess = e => {
        const c = e.target.result;
        if (c) { items.push(c.value); c.continue(); }
        else   { resolve(items); }
      };
      cur.onerror = () => reject(cur.error);
      fn && fn(store, tx);
    }));
  }

  // ══════════════════════════════════════════
  // 队列操作
  // ══════════════════════════════════════════

  /** 把一条 session payload 写入本地 IndexedDB 队列 */
  async function _enqueue(payload, bankIdx) {
    const entry = {
      session_id: payload.id,
      bank:      (bankIdx !== undefined && bankIdx !== null) ? Number(bankIdx) : 0,
      payload,
      queued_at: Date.now(),
      retries:   0,
    };
    try {
      await _tx('readwrite', store => store.add(entry));
    } catch (e) {
      // session_id 重复（unique index），说明已在队列中，忽略
      if (e && e.name !== 'ConstraintError') console.warn('[Sync] enqueue error', e);
    }
    await _refreshPendingCount();
  }

  /** 删除队列中指定 id 的条目（同步成功后） */
  function _dequeue(id) {
    return _tx('readwrite', store => store.delete(id));
  }

  /** 更新重试次数 */
  function _bumpRetry(id, retries) {
    return _openDB().then(db => new Promise((resolve, reject) => {
      const tx    = db.transaction(STORE, 'readwrite');
      const store = tx.objectStore(STORE);
      const get   = store.get(id);
      get.onsuccess = () => {
        if (!get.result) { resolve(); return; }
        const item = { ...get.result, retries: retries + 1 };
        const put  = store.put(item);
        put.onsuccess = () => resolve();
        put.onerror   = () => reject(put.error);
      };
      get.onerror = () => reject(get.error);
    }));
  }

  async function _refreshPendingCount() {
    const all       = await _txAll('readonly');
    state.pending   = all.length;
    _notify();
  }

  // ══════════════════════════════════════════
  // Flush（上传）
  // ══════════════════════════════════════════

  /** 把所有待同步条目批量 POST 到服务端 */
  async function flush() {
    if (_flushing || !state.online) return;
    _flushing   = true;
    state.syncing = true;
    _notify();

    try {
      const all = await _txAll('readonly');
      if (!all.length) return;

      // 按 queued_at 升序，最多一次上传 50 条
      const batch = all
        .sort((a, b) => a.queued_at - b.queued_at)
        .slice(0, 50);

      // 按题库分组，各自上传到正确的 ?bank=N
      const byBank = {};
      batch.forEach(entry => { (byBank[entry.bank||0] = byBank[entry.bank||0]||[]).push(entry); });
      for (const [bankIdx, bankItems] of Object.entries(byBank)) {
        const res = await apiFetch('/api/sync?bank=' + bankIdx, {
          method:  'POST',
          headers: { 'Content-Type': 'application/json' },
          body:    JSON.stringify({ sessions: bankItems.map(e => e.payload) }),
        });

        if (!res.ok) {
          console.warn('[Sync] Server returned', res.status, 'for bank', bankIdx);
          // 401/403 = token 过期或无权限，重试无意义，直接按失败计数
          // 其他错误也 bump retry，避免永久卡在队列中
          await Promise.all(bankItems.map(e => {
            if (e.retries >= MAX_RETRIES) {
              console.warn('[Sync] Dropping after max retries:', e.session_id);
              return _dequeue(e.id);
            }
            return _bumpRetry(e.id, e.retries);
          }));
          continue;
        }

        const json = await res.json();
        // 若服务端明确返回 ok:false（如 DB 未初始化），不清除队列，下次重试
        if (json.ok === false) {
          console.warn('[Sync] Server declined sync:', json.error || 'unknown');
          continue;
        }
        const { failed = [] } = json;
        const failedIds = new Set(failed.map(f => f.session_id));

        // 已成功处理的 → 从队列删除
        await Promise.all(
          bankItems
            .filter(e => !failedIds.has(e.session_id))
            .map(e => _dequeue(e.id))
        );
        // 失败次数太多的 → 也清除（避免永久卡住）
        await Promise.all(
          bankItems
            .filter(e => failedIds.has(e.session_id))
            .map(e => {
              if (e.retries >= MAX_RETRIES) {
                console.warn('[Sync] Dropping after max retries:', e.session_id);
                return _dequeue(e.id);
              }
              return _bumpRetry(e.id, e.retries);
            })
        );
      } // end for bankIdx
      state.lastSync = Date.now();
    } catch (e) {
      // 网络错误：静默，下次自动重试
      console.warn('[Sync] flush error:', e);
    } finally {
      _flushing     = false;
      state.syncing = false;
      await _refreshPendingCount();
    }
  }

  // ══════════════════════════════════════════
  // 公开 API
  // ══════════════════════════════════════════

  /**
   * 记录一次答题会话（替换直接 POST /api/record）
   * 1. 先写 IndexedDB（可靠，断网也不丢）
   * 2. 立即尝试 flush 到服务端
   */
  async function record(payload, bankIdx) {
    await _enqueue(payload, bankIdx);
    flush();   // 不 await，后台运行
  }

  /** 注册 UI 状态变化回调 */
  function onStateChange(fn) {
    _onStateChange = fn;
  }

  /** 返回当前快照（供 UI 初始渲染用） */
  function getState() {
    return { ...state };
  }

  /** 手动触发一次同步（用于"立即同步"按钮） */
  async function manualSync() {
    await flush();
  }

  /** 清空本地队列中已同步过的历史记录（保留未同步的） */
  async function purgeAll() {
    const all = await _txAll('readwrite');
    await Promise.all(all.map(item => _dequeue(item.id)));
    await _refreshPendingCount();
  }

  // ══════════════════════════════════════════
  // 网络状态监听 + 定期 flush
  // ══════════════════════════════════════════

  window.addEventListener('online', () => {
    state.online = true;
    _notify();
    flush();
  });
  window.addEventListener('offline', () => {
    state.online = false;
    _notify();
  });

  // 页面可见时也触发（用户切回标签页）
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible' && state.online) flush();
  });

  async function _init() {
    await _openDB();
    await _refreshPendingCount();
    // 启动时立即尝试一次
    if (state.online) flush();
    // 定期自动重试
    _timer = setInterval(() => { if (state.online) flush(); }, FLUSH_INTERVAL_MS);
  }

  _init().catch(e => console.warn('[Sync] init error:', e));

  return { record, flush: manualSync, onStateChange, getState, purgeAll };
})();
