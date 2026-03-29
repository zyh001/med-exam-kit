package dedup

import (
	"testing"

	"github.com/zyh001/med-exam-kit/internal/models"
)

func makeQ(mode, text, answer string, opts []string) *models.Question {
	return &models.Question{
		Mode: mode,
		SubQuestions: []models.SubQuestion{
			{Text: text, Options: opts, Answer: answer},
		},
	}
}

var defaultOpts = []string{"A.选项A", "B.选项B", "C.选项C", "D.选项D", "E.选项E"}

func TestDeduplicate_RemovesDuplicates(t *testing.T) {
	q1 := makeQ("A1型题", "题目文字", "C", defaultOpts)
	q2 := makeQ("A1型题", "题目文字", "C", defaultOpts) // exact duplicate
	result := Deduplicate([]*models.Question{q1, q2}, "strict")
	if len(result) != 1 {
		t.Fatalf("want 1, got %d", len(result))
	}
}

func TestDeduplicate_KeepsDifferent(t *testing.T) {
	q1 := makeQ("A1型题", "题目文字A", "C", defaultOpts)
	q2 := makeQ("A1型题", "题目文字B", "C", defaultOpts)
	result := Deduplicate([]*models.Question{q1, q2}, "strict")
	if len(result) != 2 {
		t.Fatalf("want 2, got %d", len(result))
	}
}

func TestDeduplicate_MergesPkgExactly(t *testing.T) {
	// Bug fix: "com.ahu" must NOT be considered already present in "com.ahuxueshu"
	q1 := makeQ("A1型题", "题目文字", "A", defaultOpts)
	q1.Pkg = "com.ahuxueshu"
	q2 := makeQ("A1型题", "题目文字", "A", defaultOpts)
	q2.Pkg = "com.ahu" // substring of q1.Pkg — must still be appended
	result := Deduplicate([]*models.Question{q1, q2}, "strict")
	if len(result) != 1 {
		t.Fatalf("want 1 after dedup, got %d", len(result))
	}
	if result[0].Pkg != "com.ahuxueshu,com.ahu" {
		t.Fatalf("pkg merge wrong: %q", result[0].Pkg)
	}
}

func TestDeduplicate_MergesPkgNoDuplicate(t *testing.T) {
	// Same pkg should not be appended twice
	q1 := makeQ("A1型题", "题目文字", "A", defaultOpts)
	q1.Pkg = "com.ahuxueshu"
	q2 := makeQ("A1型题", "题目文字", "A", defaultOpts)
	q2.Pkg = "com.ahuxueshu"
	result := Deduplicate([]*models.Question{q1, q2}, "strict")
	if result[0].Pkg != "com.ahuxueshu" {
		t.Fatalf("pkg should not duplicate: %q", result[0].Pkg)
	}
}

func TestComputeFingerprint_Consistent(t *testing.T) {
	q1 := makeQ("A1型题", "题目文字", "C", defaultOpts)
	q2 := makeQ("A1型题", "题目文字", "C", defaultOpts)
	fp1 := ComputeFingerprint(q1, "strict")
	fp2 := ComputeFingerprint(q2, "strict")
	if fp1 != fp2 {
		t.Fatalf("fingerprints differ: %s vs %s", fp1, fp2)
	}
}

func TestComputeFingerprint_Length(t *testing.T) {
	q := makeQ("A1型题", "题目文字", "C", defaultOpts)
	fp := ComputeFingerprint(q, "strict")
	if len(fp) != 16 {
		t.Fatalf("want 16 chars, got %d: %s", len(fp), fp)
	}
}

func TestComputeFingerprint_ContentStrategyIgnoresOptions(t *testing.T) {
	q1 := makeQ("A1型题", "相同题干", "C", defaultOpts)
	q2 := makeQ("A1型题", "相同题干", "C", []string{"A.不同", "B.选项", "C.内容"})
	fp1 := ComputeFingerprint(q1, "content")
	fp2 := ComputeFingerprint(q2, "content")
	if fp1 != fp2 {
		t.Fatalf("content strategy should ignore options: %s vs %s", fp1, fp2)
	}
}

func TestComputeFingerprint_StrictStrategyDiffersOnOptions(t *testing.T) {
	q1 := makeQ("A1型题", "相同题干", "C", defaultOpts)
	q2 := makeQ("A1型题", "相同题干", "C", []string{"A.完全不同", "B.选项", "C.内容", "D.X", "E.Y"})
	fp1 := ComputeFingerprint(q1, "strict")
	fp2 := ComputeFingerprint(q2, "strict")
	if fp1 == fp2 {
		t.Fatal("strict strategy should differ on different options")
	}
}

func TestNormalise_PunctuationVariants(t *testing.T) {
	// Both forms should produce same fingerprint
	q1 := makeQ("A1型题", "题干（含中文括号）", "A", defaultOpts)
	q2 := makeQ("A1型题", "题干(含中文括号)", "A", defaultOpts)
	fp1 := ComputeFingerprint(q1, "content")
	fp2 := ComputeFingerprint(q2, "content")
	if fp1 != fp2 {
		t.Fatal("normalisation should unify （） and ()")
	}
}
