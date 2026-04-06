// Service Worker for 医考练习 PWA
const CACHE_NAME = 'med-quiz-v2';

// 只缓存纯静态资源（CSS/JS），不缓存 HTML 页面。
// HTML 内嵌动态 SESSION_TOKEN，缓存后服务器重启会导致 API 全返回 401，
// 前端因此误报"学习记录未启用"。
const STATIC_ASSETS = [
    '/static/common.css',
    '/static/quiz.css',
    '/static/common.js',
    '/static/quiz.js',
];

// 可选缓存：图标由路由生成，按需缓存，失败不阻断 SW 安装
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
    self.skipWaiting();
});

self.addEventListener('activate', event => {
    event.waitUntil(
        caches.keys().then(keys =>
            Promise.all(keys.filter(k => k !== CACHE_NAME).map(k => caches.delete(k)))
        )
    );
    self.clients.claim();
});

// Fetch 策略：
//   /api/*    → 始终走网络（保证 Token 实时有效，防止 401 误报"记录未启用"）
//   HTML 页面 → 网络优先（含 SESSION_TOKEN，不可缓存旧版本）
//   静态资源  → 缓存优先，回退网络
self.addEventListener('fetch', event => {
    const url = new URL(event.request.url);

    // API：始终走网络
    if (url.pathname.startsWith('/api/')) {
        event.respondWith(
            fetch(event.request).catch(() => new Response('{"error":"offline"}', {
                headers: { 'Content-Type': 'application/json' }
            }))
        );
        return;
    }

    // HTML 页面：网络优先
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

    // 静态资源：缓存优先，回退网络并缓存
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
