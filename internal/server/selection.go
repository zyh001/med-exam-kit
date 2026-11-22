package server

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/zyh001/med-exam-kit/internal/models"
)

func selectQuestions(questions []*models.Question, opts selectOpts) ([]sqFlat, int) {
	var groups []group
	modeOrder := []string{}
	modeMap := map[string][]group{}

	for qi, q := range questions {
		if len(opts.modes) > 0 {
			found := false
			for _, m := range opts.modes {
				if q.Mode == m {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		if len(opts.units) > 0 {
			found := false
			for _, u := range opts.units {
				if u != "" && strings.Contains(q.Unit, u) {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		if opts.perUnit != nil {
			if _, ok := opts.perUnit[q.Unit]; !ok {
				continue
			}
		}
		if opts.fpSet != nil {
			if _, ok := opts.fpSet[q.Fingerprint]; !ok {
				continue
			}
		}
		var grp group
		shared := q.SharedOptions
		for si, sq := range q.SubQuestions {
			effOpts := sq.Options
			if len(effOpts) == 0 {
				effOpts = shared
			}
			grp = append(grp, sqFlat{
				QI: qi, SI: si, ID: fmt.Sprintf("%d-%d", qi, si),
				Mode: q.Mode, Unit: q.Unit, Cls: q.Cls,
				Stem: q.Stem, SharedOptions: shared,
				Text: sq.Text, Options: effOpts,
				Answer: sq.EffAnswer(), Discuss: sq.EffDiscuss(),
				Point: sq.Point, Rate: sq.Rate,
				HasAI:       sq.AIAnswer != "" || sq.AIDiscuss != "",
				Fingerprint: q.Fingerprint,
			})
		}
		if len(grp) == 0 {
			continue
		}

		// ── 合并共用题干 ─────────────────────────────────────────
		// 某些数据源（如 ahuyikao、yikaobang）将 A3/A4 共用题干题
		// 拆为多个独立 Question（相同 stem），而非一个 Question 含多个
		// SubQuestion。若直接按 Question 建组，shuffle 后子题会被打散。
		// 修复：将连续相同 stem 的 Question 合并为同一 group，
		// 使 shuffle 仅在"大题"级别打乱，子题保持原序。
		if q.Stem != "" && len(groups) > 0 {
			prev := groups[len(groups)-1]
			if len(prev) > 0 && prev[0].Stem == q.Stem && prev[0].Mode == q.Mode {
				groups[len(groups)-1] = append(prev, grp...)
				continue // 已合并，不再新建 group / modeMap entry
			}
		}

		groups = append(groups, grp)
		mk := q.Mode
		if _, ok := modeMap[mk]; !ok {
			modeOrder = append(modeOrder, mk)
		}
		modeMap[mk] = append(modeMap[mk], grp)
	}

	// ── 重建 modeMap（groups 中已合并共用题干，需保持一致）──
	// 只在有合并发生时需要重建：当 groups 总数 < modeMap 条目总数
	{
		mmTotal := 0
		for _, gs := range modeMap {
			mmTotal += len(gs)
		}
		if mmTotal != len(groups) {
			modeMap = map[string][]group{}
			for _, grp := range groups {
				mk := grp[0].Mode
				modeMap[mk] = append(modeMap[mk], grp)
			}
		}
	}

	// 按医学考试标准题型顺序排列：A1 → A2 → A3/A4 → B → 案例分析 → 其他
	sortModeOrder(modeOrder)

	// ── 难度筛选：按正确率区间加权，重建 modeMap ─────────────────────────
	if len(opts.difficulty) > 0 {
		groups = difficultyFilterGroups(groups, opts.difficulty, opts.limit, opts.rng)
		// 从过滤后的 groups 重建 modeMap（可能有空 mode，需剔除）
		modeMap = map[string][]group{}
		for _, grp := range groups {
			mk := grp[0].Mode
			modeMap[mk] = append(modeMap[mk], grp)
		}
		var newOrder []string
		for _, mk := range modeOrder {
			if len(modeMap[mk]) > 0 {
				newOrder = append(newOrder, mk)
			}
		}
		modeOrder = newOrder
	}

	var resultGroups []group
	switch {
	case opts.perUnit != nil:
		unitOrder := []string{}
		unitMap := map[string][]group{}
		for _, grp := range groups {
			uk := grp[0].Unit
			if _, ok := unitMap[uk]; !ok {
				unitOrder = append(unitOrder, uk)
			}
			unitMap[uk] = append(unitMap[uk], grp)
		}
		reorder := map[string][]group{}
		for _, uk := range unitOrder {
			need, ok := opts.perUnit[uk]
			if !ok || need <= 0 {
				continue
			}
			pool := append([]group(nil), unitMap[uk]...)
			opts.rng.shuffle(pool)
			for _, grp := range greedyFillRNG(pool, need, opts.rng) {
				mk := grp[0].Mode
				reorder[mk] = append(reorder[mk], grp)
			}
		}
		for _, mk := range modeOrder {
			resultGroups = append(resultGroups, reorder[mk]...)
		}

	case !opts.shuffle:
		for _, mk := range modeOrder {
			resultGroups = append(resultGroups, modeMap[mk]...)
		}
		if opts.limit > 0 {
			var cut []group
			n := 0
			for _, grp := range resultGroups {
				c := len(grp)
				if n+c > opts.limit {
					continue
				}
				cut = append(cut, grp)
				n += c
				if n >= opts.limit {
					break
				}
			}
			resultGroups = cut
		}

	case opts.perMode != nil:
		totalNeedPM := 0
		for _, mk := range modeOrder {
			need := opts.perMode[mk]
			if need <= 0 {
				continue
			}
			totalNeedPM += need
			pool := append([]group(nil), modeMap[mk]...)
			opts.rng.shuffle(pool)
			resultGroups = append(resultGroups, greedyFillRNG(pool, need, opts.rng)...)
		}
		actualPM := 0
		for _, grp := range resultGroups {
			actualPM += len(grp)
		}
		if shortfallPM := totalNeedPM - actualPM; shortfallPM > 0 {
			pickedPM := map[string]bool{}
			for _, grp := range resultGroups {
				if len(grp) > 0 {
					pickedPM[grp[0].ID] = true
				}
			}
			for _, mk := range modeOrder {
				if shortfallPM <= 0 {
					break
				}
				// Shuffle so we don't always fall back to the same questions
				// in original document order when there's a shortfall.
				fallback := append([]group(nil), modeMap[mk]...)
				opts.rng.shuffle(fallback)
				for _, grp := range fallback {
					if shortfallPM <= 0 {
						break
					}
					if !pickedPM[grp[0].ID] && len(grp) <= shortfallPM {
						resultGroups = append(resultGroups, grp)
						pickedPM[grp[0].ID] = true
						shortfallPM -= len(grp)
					}
				}
			}
		}

	default:
		modeSQTotal := map[string]int{}
		totalSQAll := 0
		for _, mk := range modeOrder {
			for _, grp := range modeMap[mk] {
				modeSQTotal[mk] += len(grp)
			}
			totalSQAll += modeSQTotal[mk]
		}
		totalNeed := opts.limit
		if totalNeed <= 0 {
			totalNeed = totalSQAll
		}
		quotas := distributeByRatio(totalNeed, modeSQTotal)
		for mk, total := range modeSQTotal {
			if total > 0 && quotas[mk] < 1 {
				quotas[mk] = 1
			}
		}
		overflow := -totalNeed
		for _, v := range quotas {
			overflow += v
		}
		if overflow > 0 {
			for _, mk := range modeOrder {
				if overflow <= 0 {
					break
				}
				red := quotas[mk] - 1
				if red <= 0 {
					continue
				}
				cut := min(red, overflow)
				quotas[mk] -= cut
				overflow -= cut
			}
		}
		for _, mk := range modeOrder {
			need := quotas[mk]
			if need <= 0 {
				continue
			}
			pool := append([]group(nil), modeMap[mk]...)
			opts.rng.shuffle(pool)
			resultGroups = append(resultGroups, greedyFillRNG(pool, need, opts.rng)...)
		}
		actual := 0
		for _, grp := range resultGroups {
			actual += len(grp)
		}
		if shortfall := totalNeed - actual; shortfall > 0 {
			picked := map[string]bool{}
			for _, grp := range resultGroups {
				if len(grp) > 0 {
					picked[grp[0].ID] = true
				}
			}
			for _, mk := range modeOrder {
				if shortfall <= 0 {
					break
				}
				// Shuffle the fallback pool so shortfall fill is not
				// deterministic (otherwise the same tail questions keep
				// appearing at the end of the generated paper).
				fallback := append([]group(nil), modeMap[mk]...)
				opts.rng.shuffle(fallback)
				for _, grp := range fallback {
					if shortfall <= 0 {
						break
					}
					if !picked[grp[0].ID] && len(grp) <= shortfall {
						resultGroups = append(resultGroups, grp)
						picked[grp[0].ID] = true
						shortfall -= len(grp)
					}
				}
			}
		}
	}

	var rows []sqFlat
	for _, grp := range resultGroups {
		rows = append(rows, grp...)
	}
	return rows, len(rows)
}

// ── 难度筛选辅助函数 ─────────────────────────────────────────────────────────

// parseRate 解析题目正确率字符串（支持 "75%"、"75"、"75.0"）。
// 返回 [0,100] 范围内的浮点数；若无效或 ≤0 则返回 (0, false)。
func parseRate(s string) (float64, bool) {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "%"))
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	return v, true
}

// groupDifficultyBucket 根据题组内各小题的平均正确率返回难度桶名。
// 正确率 ≥80% → easy；60–80% → medium；40–60% → hard；<40% → extreme；无数据 → ""。
func groupDifficultyBucket(grp group) string {
	sum, cnt := 0.0, 0
	for _, sq := range grp {
		if r, ok := parseRate(sq.Rate); ok {
			sum += r
			cnt++
		}
	}
	if cnt == 0 {
		return "" // 无正确率数据
	}
	avg := sum / float64(cnt)
	switch {
	case avg >= 80:
		return "easy"
	case avg >= 60:
		return "medium"
	case avg >= 40:
		return "hard"
	default:
		return "extreme"
	}
}

// difficultyFilterGroups 按难度权重从 groups 中采样，返回满足近似比例的子集。
//
// difficulty 是各桶权重（非负，不需要归一化），例如 {"easy":25,"medium":45,"hard":20,"extreme":10}。
// targetTotal：期望最终使用的数量（即 limit），用于计算各桶目标数；若 ≤0 则以总量为准。
//
// 算法：
//  1. 将 groups 按正确率分入四个桶及一个无数据桶。
//  2. 根据权重比例和 targetTotal 计算各桶目标数。
//  3. 各桶内随机采样；若某桶数量不足，产生 shortfall。
//  4. shortfall 优先由"无正确率"题组补充，再由其他有余量的桶补充。
func difficultyFilterGroups(groups []group, difficulty map[string]float64, targetTotal int, rng *lcgRNG) []group {
	knownBuckets := []string{"easy", "medium", "hard", "extreme"}

	totalW := 0.0
	for _, b := range knownBuckets {
		if w := difficulty[b]; w > 0 {
			totalW += w
		}
	}
	if totalW == 0 {
		return groups
	}

	// 分桶
	buckets := map[string][]group{}
	for _, grp := range groups {
		b := groupDifficultyBucket(grp)
		buckets[b] = append(buckets[b], grp)
	}

	// 洗牌各桶
	for k := range buckets {
		pool := append([]group(nil), buckets[k]...)
		rng.shuffle(pool)
		buckets[k] = pool
	}

	if targetTotal <= 0 || targetTotal > len(groups) {
		targetTotal = len(groups)
	}

	// 各桶目标数
	targets := map[string]int{}
	for _, b := range knownBuckets {
		w := difficulty[b]
		if w <= 0 {
			continue
		}
		targets[b] = int(math.Round(float64(targetTotal) * w / totalW))
	}

	// 从各桶采样，记录 shortfall
	// 用题组在原 groups 切片中的下标做去重
	type grpKey struct{ qi, si int }
	key := func(grp group) grpKey { return grpKey{grp[0].QI, grp[0].SI} }
	selected := map[grpKey]bool{}
	result := make([]group, 0, targetTotal)
	shortfall := 0

	for _, b := range knownBuckets {
		need := targets[b]
		if need <= 0 {
			continue
		}
		pool := buckets[b]
		take := need
		if take > len(pool) {
			shortfall += need - len(pool)
			take = len(pool)
		}
		for i := 0; i < take; i++ {
			k := key(pool[i])
			if !selected[k] {
				result = append(result, pool[i])
				selected[k] = true
			}
		}
	}

	// shortfall 优先由无正确率数据题组补充
	for _, grp := range buckets[""] {
		if shortfall <= 0 {
			break
		}
		k := key(grp)
		if !selected[k] {
			result = append(result, grp)
			selected[k] = true
			shortfall--
		}
	}

	// 仍有 shortfall 则从各桶余量中补
	if shortfall > 0 {
		for _, b := range knownBuckets {
			if shortfall <= 0 {
				break
			}
			for _, grp := range buckets[b] {
				if shortfall <= 0 {
					break
				}
				k := key(grp)
				if !selected[k] {
					result = append(result, grp)
					selected[k] = true
					shortfall--
				}
			}
		}
	}

	return result
}
