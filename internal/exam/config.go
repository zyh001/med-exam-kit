// Package exam implements automatic exam paper generation.
package exam

// Config holds all settings for exam generation.
type Config struct {
	Title          string         // 试卷标题
	Subtitle       string         // 副标题
	TimeLimit      int            // 考试时间（分钟）
	TotalScore     int            // 总分
	ScorePerSub    float64        // 每小题分数（0=自动计算）
	ClsList        []string       // 限定题库分类
	Units          []string       // 限定章节
	Modes          []string       // 限定题型
	Count          int            // 总抽题数
	CountMode      string         // "sub" 按小题 / "question" 按大题
	PerMode        map[string]int // 按题型指定数量
	DifficultyDist map[string]int // 按难度比例 {easy:20, medium:40, hard:30, extreme:10}
	DifficultyMode string         // "global" / "per_mode"
	Seed           int64          // 随机种子（0=随机）
	ShowAnswers    bool           // 题目中显示答案
	AnswerSheet    bool           // 末尾附答案页
	ShowDiscuss    bool           // 答案页附解析
}

// DefaultConfig returns a config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Title:          "模拟考试",
		TimeLimit:      120,
		TotalScore:     100,
		Count:          50,
		CountMode:      "sub",
		AnswerSheet:    true,
		DifficultyMode: "global",
	}
}
