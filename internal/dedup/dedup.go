package dedup

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"unicode"

	"github.com/med-exam-kit/med-exam-kit/internal/models"
)

// Deduplicate removes duplicate questions, keeping the first occurrence.
// Duplicates have their pkg merged into the surviving question's Pkg field.
// strategy: "strict" (default) or "content".
func Deduplicate(questions []*models.Question, strategy string) []*models.Question {
	if strategy == "" {
		strategy = "strict"
	}
	seen := make(map[string]*models.Question, len(questions))
	order := make([]string, 0, len(questions))
	dupes := 0

	for _, q := range questions {
		fp := ComputeFingerprint(q, strategy)
		q.Fingerprint = fp
		if existing, ok := seen[fp]; ok {
			dupes++
			// Merge source pkg using exact set membership
			existingPkgs := pkgSet(existing.Pkg)
			if _, already := existingPkgs[q.Pkg]; !already && q.Pkg != "" {
				existing.Pkg += "," + q.Pkg
			}
		} else {
			seen[fp] = q
			order = append(order, fp)
		}
	}

	result := make([]*models.Question, 0, len(order))
	for _, fp := range order {
		result = append(result, seen[fp])
	}
	_ = dupes
	return result
}

// ComputeFingerprint calculates a 16-char hex fingerprint for a question.
func ComputeFingerprint(q *models.Question, strategy string) string {
	parts := make([]string, 0, 16)

	if q.Stem != "" {
		parts = append(parts, normalise(q.Stem))
	}
	if len(q.SharedOptions) > 0 && strategy == "strict" {
		sorted := sortedNorm(q.SharedOptions)
		parts = append(parts, sorted...)
	}
	for _, sq := range q.SubQuestions {
		parts = append(parts, normalise(sq.Text))
		if strategy == "strict" {
			parts = append(parts, sortedNorm(sq.Options)...)
			parts = append(parts, resolveAnswerText(&sq))
		}
	}

	raw := strings.Join(parts, "|")
	sum := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%x", sum[:8]) // 16 hex chars
}

// ── helpers ────────────────────────────────────────────────────────────

func normalise(s string) string {
	// collapse whitespace
	s = strings.Join(strings.Fields(s), "")
	// unify common punctuation variants
	replacer := strings.NewReplacer(
		"，", ",", "。", ".", "；", ";",
		"：", ":", "（", "(", "）", ")",
	)
	s = replacer.Replace(s)
	return strings.Map(unicode.ToLower, s)
}

func sortedNorm(opts []string) []string {
	normed := make([]string, len(opts))
	for i, o := range opts {
		normed[i] = normalise(o)
	}
	// simple insertion sort (lists are short)
	for i := 1; i < len(normed); i++ {
		for j := i; j > 0 && normed[j] < normed[j-1]; j-- {
			normed[j], normed[j-1] = normed[j-1], normed[j]
		}
	}
	return normed
}

func resolveAnswerText(sq *models.SubQuestion) string {
	ans := strings.TrimSpace(sq.Answer)
	if len(ans) == 1 {
		idx := int(ans[0]|0x20) - int('a') // case-insensitive
		if idx >= 0 && idx < len(sq.Options) {
			return normalise(sq.Options[idx])
		}
	}
	return strings.ToUpper(ans)
}

func pkgSet(pkg string) map[string]struct{} {
	parts := strings.Split(pkg, ",")
	m := make(map[string]struct{}, len(parts))
	for _, p := range parts {
		m[strings.TrimSpace(p)] = struct{}{}
	}
	return m
}
