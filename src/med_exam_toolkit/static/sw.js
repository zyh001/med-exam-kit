// Service Worker for 医考练习 PWA
const CACHE_NAME = 'med-quiz-v2.11.0';

const STATIC_ASSETS = [
    '/static/common.css',
    '/static/quiz.css',
    '/static/common.js',
    '/static/quiz.js',
    '/static/quiz_ai.js',
    '/static/quiz_sync.js',
    '/static/marked.min.js',
    '/static/smd.min.js',
    '/static/katex.min.js',
    '/static/katex.min.css',
    '/static/auto-render.min.js',
];

const OPTIONAL_ASSETS = [
    '/static/icon.svg',
    '/static/icon-192.png',
    '/static/icon-512.png',
];

self.addEventListener('install', event => {
    event.waitUntil(
        caches.open(CACHE_NAME).then(async cache => {
            await cache.addAll(STATIC_ASSETS);
            for (const url of OPTIONAL_ASSETS) {
                try { await cache.add(url); } catch (_) {}
            }
        })
    );
    // 不自动 skipWaiting：等用户在页面上确认后才切换，
    // 避免新 SW 接管时旧页面 JS 与新缓存文件版本不一致。
});

self.addEventListener('activate', event => {
    event.waitUntil(
        caches.keys().then(keys =>
            Promise.all(keys.filter(k => k !== CACHE_NAME).map(k => caches.delete(k)))
        )
    );
    self.clients.claim();
});

// 页面发来 SKIP_WAITING 消息时才激活新版本
self.addEventListener('message', event => {
    if (event.data && event.data.type === 'SKIP_WAITING') {
        self.skipWaiting();
    }
});

// ── Fetch 策略 ──────────────────────────────────────────────────────────
self.addEventListener('fetch', event => {
    const url = new URL(event.request.url);

    if (url.pathname.startsWith('/api/')) {
        // SSE/streaming requests must not be intercepted (cannot clone stream body)
        const accept = event.request.headers.get('accept') || '';
        if (accept.includes('text/event-stream') || event.request.method !== 'GET') return;

        event.respondWith(
            fetch(event.request).catch(() => new Response('{"error":"offline"}', {
                headers: { 'Content-Type': 'application/json' }
            }))
        );
        return;
    }

    const accept = event.request.headers.get('accept') || '';
    if (accept.includes('text/html') || url.pathname === '/' || url.pathname === '') {
        event.respondWith(
            fetch(event.request).catch(() =>
                new Response('离线中，请检查网络连接', {
                    headers: { 'Content-Type': 'text/plain; charset=utf-8' }
                })
            )
        );
        return;
    }

    event.respondWith(
        caches.match(event.request).then(cached => {
            if (cached) return cached;
            return fetch(event.request).then(response => {
                if (response.ok && url.pathname.startsWith('/static/')) {
                    caches.open(CACHE_NAME).then(cache => cache.put(event.request, response.clone()));
                }
                return response;
            });
        }).catch(() => new Response('', { status: 404 }))
    );
});

// ── Web Push 通知 ────────────────────────────────────────────────────────
self.addEventListener('push', event => {
    let data = { title: '医考练习', body: '你有待复习的题目，点击开始今日复习！', due: 0 };
    try { data = Object.assign(data, event.data.json()); } catch (_) {}

    const title = data.title || '医考练习';
    const body  = data.due > 0
        ? `今天有 ${data.due} 道题等待复习，趁热打铁！`
        : (data.body || '点击打开应用');

    event.waitUntil(
        self.registration.showNotification(title, {
            body,
            icon:  '/static/icon-192.png',
            badge: '/static/icon-192.png',
            tag:   'daily-review',
            renotify: true,
            data: { url: '/' },
        })
    );
});

self.addEventListener('notificationclick', event => {
    event.notification.close();
    const target = (event.notification.data && event.notification.data.url) || '/';
    event.waitUntil(
        clients.matchAll({ type: 'window', includeUncontrolled: true }).then(list => {
            for (const c of list) {
                if (c.url.includes(self.location.origin) && 'focus' in c) {
                    return c.focus();
                }
            }
            return clients.openWindow(target);
        })
    );
});
