package exam

import (
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"strings"

	"github.com/zyh001/med-exam-kit/internal/models"
)

// GenerationError is returned when exam generation fails.
type GenerationError struct {
	Message string
}

func (e *GenerationError) Error() string { return e.Message }

// Generator produces a randomised exam from a question pool.
type Generator struct {
	pool   []*models.Question
	config Config
	rng    *rand.Rand
	bySub  bool // count by sub-questions
}

// NewGenerator creates a generator for the given pool and config.
func NewGenerator(pool []*models.Question, cfg Config) *Generator {
	seed := cfg.Seed
	if seed == 0 {
		seed = rand.Int63()
	}
	return &Generator{
		pool:   pool,
		config: cfg,
		rng:    rand.New(rand.NewSource(seed)),
		bySub:  cfg.CountMode == "sub",
	}
}

// Generate selects and returns questions for the exam paper.
func (g *Generator) Generate() ([]*models.Question, error) {
	filtered := g.filterPool()
	if len(filtered) == 0 {
		return nil, &GenerationError{"筛选后题库为空，请检查 units / modes 条件"}
	}

	var selected []*models.Question

	hasPerMode := len(g.config.PerMode) > 0
	hasDifficulty := len(g.config.DifficultyDist) > 0

	switch {
	case hasPerMode && hasDifficulty:
		selected = g.samplePerModeWithDifficulty(filtered)
	case hasPerMode:
		selected = g.samplePerMode(filtered)
	case hasDifficulty:
		selected = g.sampleByDifficulty(filtered)
	default:
		selected = g.sampleTotal(filtered)
	}

	// Shuffle then sort by mode order
	g.rng.Shuffle(len(selected), func(i, j int) {
		selected[i], selected[j] = selected[j], selected[i]
	})
	order := modeOrder()
	sort.SliceStable(selected, func(i, j int) bool {
		oi := order[selected[i].Mode]
		oj := order[selected[j].Mode]
		if oi != oj {
			return oi < oj
		}
		return selected[i].Unit < selected[j].Unit
	})

	return selected, nil
}

// Summary returns a human-readable summary of the selected questions.
func (g *Generator) Summary(selected []*models.Question) string {
	totalSubs := totalSubCount(selected)
	byModeSubs := map[string]int{}
	byModeQ := map[string]int{}
	byUnit := map[string]int{}
	for _, q := range selected {
		byModeSubs[q.Mode] += len(q.SubQuestions)
		byModeQ[q.Mode]++
		byUnit[q.Unit] += len(q.SubQuestions)
	}

	var modeParts []string
	for m, sc := range byModeSubs {
		modeParts = append(modeParts, fmt.Sprintf("%s: %d小题(%d大题)", m, sc, byModeQ[m]))
	}

	countLabel := "按小题"
	if !g.bySub {
		countLabel = "按大题"
	}

	lines := []string{
		fmt.Sprintf("试卷: %s", g.config.Title),
		fmt.Sprintf("计数模式: %s", countLabel),
		fmt.Sprintf("总题数: %d 小题（%d 大题）", totalSubs, len(selected)),
		fmt.Sprintf("题型分布: %s", strings.Join(modeParts, ", ")),
	}

	// Score info
	if g.config.ScorePerSub > 0 {
		total := g.config.ScorePerSub * float64(totalSubs)
		lines = append(lines, fmt.Sprintf("分值: 每小题 %.1f 分，总分 %.0f 分", g.config.ScorePerSub, total))
	} else if g.config.TotalScore > 0 && totalSubs > 0 {
		per := float64(g.config.TotalScore) / float64(totalSubs)
		lines = append(lines, fmt.Sprintf("分值: 总分 %d 分，每小题 %.2f 分", g.config.TotalScore, per))
	}

	return strings.Join(lines, "\n")
}

// ── Helpers ──

func (g *Generator) cost(q *models.Question) int {
	if g.bySub {
		return len(q.SubQuestions)
	}
	return 1
}

func (g *Generator) totalCost(qs []*models.Question) int {
	n := 0
	for _, q := range qs {
		n += g.cost(q)
	}
	return n
}

func totalSubCount(qs []*models.Question) int {
	n := 0
	for _, q := range qs {
		n += len(q.SubQuestions)
	}
	return n
}

// ── Filter ──

func (g *Generator) filterPool() []*models.Question {
	pool := g.pool
	if len(g.config.ClsList) > 0 {
		set := toLowerSet(g.config.ClsList)
		pool = filterQ(pool, func(q *models.Question) bool { return set[strings.ToLower(q.Cls)] })
	}
	if len(g.config.Units) > 0 {
		set := toLowerSet(g.config.Units)
		pool = filterQ(pool, func(q *models.Question) bool { return set[strings.ToLower(q.Unit)] })
	}
	if len(g.config.Modes) > 0 {
		set := toLowerSet(g.config.Modes)
		pool = filterQ(pool, func(q *models.Question) bool { return set[strings.ToLower(q.Mode)] })
	}
	return pool
}

func filterQ(qs []*models.Question, pred func(*models.Question) bool) []*models.Question {
	var out []*models.Question
	for _, q := range qs {
		if pred(q) {
			out = append(out, q)
		}
	}
	return out
}

func toLowerSet(ss []string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[strings.ToLower(s)] = true
	}
	return m
}

// ── Greedy Fill ──

func (g *Generator) greedyFill(pool []*models.Question, target int, used map[*models.Question]bool) []*models.Question {
	var available []*models.Question
	for _, q := range pool {
		if !used[q] {
			available = append(available, q)
		}
	}
	g.rng.Shuffle(len(available), func(i, j int) {
		available[i], available[j] = available[j], available[i]
	})

	var picked []*models.Question
	total := 0

	var remaining []*models.Question
	for _, q := range available {
		c := g.cost(q)
		if total+c <= target {
			picked = append(picked, q)
			used[q] = true
			total += c
			if total == target {
				return picked
			}
		} else {
			remaining = append(remaining, q)
		}
	}

	// Second pass: find best fit for remaining gap
	for total < target && len(remaining) > 0 {
		gap := target - total
		bestIdx := -1
		bestC := 0
		for i, q := range remaining {
			c := g.cost(q)
			if c <= gap && c > bestC {
				bestC = c
				bestIdx = i
			}
		}
		if bestIdx < 0 {
			break
		}
		picked = append(picked, remaining[bestIdx])
		used[remaining[bestIdx]] = true
		total += bestC
		remaining = append(remaining[:bestIdx], remaining[bestIdx+1:]...)
	}

	return picked
}

// ── Sampling strategies ──

func (g *Generator) sampleTotal(pool []*models.Question) []*models.Question {
	target := g.config.Count
	poolCost := g.totalCost(pool)
	if poolCost < target {
		fmt.Printf("[WARN] 可用题 %d 道，不足 %d，将全部使用\n", poolCost, target)
		return copySlice(pool)
	}
	return g.greedyFill(pool, target, make(map[*models.Question]bool))
}

func (g *Generator) samplePerMode(pool []*models.Question) []*models.Question {
	byMode := groupByMode(pool)
	var selected []*models.Question
	used := make(map[*models.Question]bool)

	for mode, need := range g.config.PerMode {
		available := byMode[mode]
		picked := g.greedyFill(available, need, used)
		selected = append(selected, picked...)
	}
	return selected
}

func (g *Generator) sampleByDifficulty(pool []*models.Question) []*models.Question {
	target := g.config.Count
	poolCost := g.totalCost(pool)
	if poolCost < target {
		return copySlice(pool)
	}
	return g.sampleFromPoolByDifficulty(pool, target, make(map[*models.Question]bool))
}

func (g *Generator) sampleFromPoolByDifficulty(pool []*models.Question, total int, used map[*models.Question]bool) []*models.Question {
	dist := g.config.DifficultyDist
	byDiff := map[string][]*models.Question{}
	for _, q := range pool {
		if !used[q] {
			byDiff[classifyDifficulty(q)] = append(byDiff[classifyDifficulty(q)], q)
		}
	}

	targets := distributeByRatio(total, dist)
	var selected []*models.Question

	shortfall := 0
	for level, need := range targets {
		available := byDiff[level]
		picked := g.greedyFill(available, need, used)
		got := g.totalCost(picked)
		if got < need {
			shortfall += need - got
		}
		selected = append(selected, picked...)
	}

	if shortfall > 0 {
		var remaining []*models.Question
		for _, q := range pool {
			if !used[q] {
				remaining = append(remaining, q)
			}
		}
		filled := g.greedyFill(remaining, shortfall, used)
		selected = append(selected, filled...)
	}

	return selected
}

func (g *Generator) samplePerModeWithDifficulty(pool []*models.Question) []*models.Question {
	byMode := groupByMode(pool)
	var selected []*models.Question
	used := make(map[*models.Question]bool)

	for mode, need := range g.config.PerMode {
		modePool := byMode[mode]
		if len(modePool) == 0 {
			fmt.Printf("[WARN] %s 无可用题目\n", mode)
			continue
		}
		batch := g.sampleFromPoolByDifficulty(modePool, need, used)
		selected = append(selected, batch...)
	}
	return selected
}

// ── Difficulty classification ──

func classifyDifficulty(q *models.Question) string {
	var rates []float64
	for _, sq := range q.SubQuestions {
		if r, ok := parseRateExam(sq.Rate); ok {
			rates = append(rates, r)
		}
	}
	if len(rates) == 0 {
		return "medium"
	}
	sum := 0.0
	for _, r := range rates {
		sum += r
	}
	avg := sum / float64(len(rates))
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

func parseRateExam(raw string) (float64, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, false
	}
	s = strings.TrimSuffix(s, "%")
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 || v > 100 {
		return 0, false
	}
	return v, true
}

// ── Utility ──

func groupByMode(qs []*models.Question) map[string][]*models.Question {
	m := map[string][]*models.Question{}
	for _, q := range qs {
		m[q.Mode] = append(m[q.Mode], q)
	}
	return m
}

func distributeByRatio(total int, ratios map[string]int) map[string]int {
	ratioSum := 0
	for _, v := range ratios {
		ratioSum += v
	}
	if ratioSum == 0 {
		result := map[string]int{}
		for k := range ratios {
			result[k] = 0
		}
		return result
	}
	result := map[string]int{}
	allocated := 0
	keys := make([]string, 0, len(ratios))
	for k := range ratios {
		keys = append(keys, k)
	}
	for i, k := range keys {
		if i == len(keys)-1 {
			v := total - allocated
			if v < 0 {
				v = 0
			}
			result[k] = v
		} else {
			n := (total*ratios[k] + ratioSum/2) / ratioSum
			result[k] = n
			allocated += n
		}
	}
	return result
}

func modeOrder() map[string]int {
	return map[string]int{
		"A1型题": 0, "A2型题": 1,
		"A3/A4型题": 2, "A3型题": 2, "A4型题": 2,
		"B1型题": 3, "B型题": 3,
		"案例分析": 4,
	}
}

func copySlice(qs []*models.Question) []*models.Question {
	out := make([]*models.Question, len(qs))
	copy(out, qs)
	return out
}
