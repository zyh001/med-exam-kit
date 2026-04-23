//go:build !nopg
// +build !nopg

package cmd

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/zyh001/med-exam-kit/internal/logger"
	"github.com/zyh001/med-exam-kit/internal/server"
	pgstore "github.com/zyh001/med-exam-kit/internal/store/postgres"
)

// BankWatcher 周期性检测题库变化 (.mqb 文件 mtime / PG banks 表元数据) ──
// 检测到变化时调用 srv.HotReload，复用服务端现有的热重载管道完成切换。
// 纯 stdlib 实现，避免引入 fsnotify 依赖。
//
// 触发规则：
//  1. 任一观察中的 .mqb 文件 mtime 变化 → 重载
//  2. 配置文件 YAML mtime 变化 → 重载（用户手工增改 banks 列表后不需要重启）
//  3. PG banks 表 count 变化（新增题目到已加载题库，或 AppendBank）→ 重载
//  4. PG banks 表条数变化（有新题库被导入）→ 重载
//
// 每次触发 HotReload 成功后，snapshot 会被刷新，避免对同一次变化反复触发。
type BankWatcher struct {
	srv      *server.Server
	interval time.Duration
	paths    []string // 启动时的 .mqb 路径快照
	pg       *pgstore.Store
	bankIDs  []int64 // 启动时的 PG bank ID 快照（仅用于日志）
	cfgFile  string  // YAML 配置文件路径（也监视其 mtime）

	mu        sync.Mutex
	fileMTime map[string]time.Time
	bankCount map[int64]int // bankID → count，来自 banks 表
	bankTotal int           // ListBanks 总数（检测新增 bank）
}

// NewBankWatcher 构造一个题库监视器。interval 不得低于 1 秒。
func NewBankWatcher(srv *server.Server, interval time.Duration,
	paths []string, pg *pgstore.Store, bankIDs []int64, cfgFile string) *BankWatcher {
	if interval < time.Second {
		interval = time.Second
	}
	return &BankWatcher{
		srv: srv, interval: interval, paths: append([]string(nil), paths...), pg: pg,
		bankIDs:   append([]int64(nil), bankIDs...),
		cfgFile:   cfgFile,
		fileMTime: map[string]time.Time{},
		bankCount: map[int64]int{},
	}
}

// Start 在后台 goroutine 中运行；ctx Done 时退出。
func (w *BankWatcher) Start(ctx context.Context) {
	w.snapshot(ctx)
	go w.loop(ctx)
}

// snapshot 刷新当前观察对象的基线。
func (w *BankWatcher) snapshot(ctx context.Context) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.fileMTime = map[string]time.Time{}
	for _, p := range w.paths {
		if len(p) > 3 && p[:3] == "pg:" {
			continue // pg:bank:N 不是文件路径，跳过
		}
		if fi, err := os.Stat(p); err == nil {
			w.fileMTime[p] = fi.ModTime()
		}
	}
	if w.cfgFile != "" {
		if fi, err := os.Stat(w.cfgFile); err == nil {
			w.fileMTime[w.cfgFile] = fi.ModTime()
		}
	}
	if w.pg != nil {
		if metas, err := w.pg.ListBanks(ctx); err == nil {
			w.bankTotal = len(metas)
			w.bankCount = map[int64]int{}
			for _, m := range metas {
				w.bankCount[m.ID] = m.Count
			}
		}
	}
}

func (w *BankWatcher) loop(ctx context.Context) {
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if w.checkAndReload(ctx) {
				w.snapshot(ctx)
			}
		}
	}
}

// checkAndReload 返回 true 表示已触发（且 HotReload 成功）重载。
func (w *BankWatcher) checkAndReload(ctx context.Context) bool {
	changed := false
	reason := ""

	// 1. 检查 .mqb / 配置文件 mtime
	w.mu.Lock()
	for p, oldMt := range w.fileMTime {
		fi, err := os.Stat(p)
		if err != nil {
			// 文件被删：也算变化，触发重载以便 runQuiz 报错或跳过
			changed = true
			reason = "file missing: " + p
			break
		}
		if !fi.ModTime().Equal(oldMt) {
			changed = true
			reason = "file mtime changed: " + p
			break
		}
	}
	w.mu.Unlock()

	// 2. 检查 PG banks 表
	if !changed && w.pg != nil {
		metas, err := w.pg.ListBanks(ctx)
		if err == nil {
			w.mu.Lock()
			if len(metas) != w.bankTotal {
				changed = true
				reason = "postgres bank table row count changed"
			} else {
				for _, m := range metas {
					if old, ok := w.bankCount[m.ID]; ok && old != m.Count {
						changed = true
						reason = "postgres bank \"" + m.Name + "\" question count changed"
						break
					}
				}
			}
			w.mu.Unlock()
		}
	}

	if !changed {
		return false
	}
	logger.Infof("[watcher] 检测到变化（%s）→ 触发热重载", reason)
	if err := w.srv.HotReload(nil, ""); err != nil {
		logger.Warnf("[watcher] 热重载失败: %v", err)
		return false
	}
	logger.Infof("[watcher] 热重载成功")
	return true
}
