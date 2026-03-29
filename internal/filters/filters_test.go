package filters

import (
	"testing"

	"github.com/med-exam-kit/med-exam-kit/internal/models"
)

func sq(text, rate string) models.SubQuestion {
	return models.SubQuestion{
		Text: text, Rate: rate,
		Options: []string{"A.甲", "B.乙", "C.丙", "D.丁", "E.戊"},
		Answer:  "A",
	}
}

func makeQ(mode, unit, pkg, cls string, subText, rate string) *models.Question {
	return &models.Question{
		Mode: mode, Unit: unit, Pkg: pkg, Cls: cls,
		SubQuestions: []models.SubQuestion{sq(subText, rate)},
	}
}

func TestFilterByMode(t *testing.T) {
	qs := []*models.Question{
		makeQ("A1型题", "第一章", "pkg1", "cls1", "题目1", "80%"),
		makeQ("A2型题", "第一章", "pkg1", "cls1", "题目2", "60%"),
		makeQ("B1型题", "第一章", "pkg1", "cls1", "题目3", "40%"),
	}
	c := FilterCriteria{Modes: []string{"A1"}, MaxRate: 100}
	result := Apply(qs, c)
	if len(result) != 1 {
		t.Fatalf("want 1, got %d", len(result))
	}
	if result[0].Mode != "A1型题" {
		t.Fatalf("wrong mode: %s", result[0].Mode)
	}
}

func TestFilterByPkg(t *testing.T) {
	qs := []*models.Question{
		makeQ("A1型题", "第一章", "ahuyikao.com", "cls1", "题目1", "80%"),
		makeQ("A1型题", "第一章", "yikaobang.com", "cls1", "题目2", "60%"),
	}
	c := FilterCriteria{Pkgs: []string{"yikaobang"}, MaxRate: 100}
	result := Apply(qs, c)
	if len(result) != 1 || result[0].Pkg != "yikaobang.com" {
		t.Fatalf("pkg filter failed, got %d results", len(result))
	}
}

func TestFilterByUnit(t *testing.T) {
	qs := []*models.Question{
		makeQ("A1型题", "第一章 基础知识", "pkg", "cls", "题目1", "80%"),
		makeQ("A1型题", "第二章 临床诊断", "pkg", "cls", "题目2", "60%"),
	}
	c := FilterCriteria{Units: []string{"第一章"}, MaxRate: 100}
	result := Apply(qs, c)
	if len(result) != 1 {
		t.Fatalf("want 1, got %d", len(result))
	}
}

func TestFilterByKeyword(t *testing.T) {
	qs := []*models.Question{
		makeQ("A1型题", "第一章", "pkg", "cls", "牙周炎的治疗方法", "80%"),
		makeQ("A1型题", "第一章", "pkg", "cls", "龋齿的预防措施", "60%"),
	}
	c := FilterCriteria{Keyword: "牙周炎", MaxRate: 100}
	result := Apply(qs, c)
	if len(result) != 1 {
		t.Fatalf("want 1, got %d", len(result))
	}
}

func TestFilterByMinRate(t *testing.T) {
	qs := []*models.Question{
		makeQ("A1型题", "ch1", "pkg", "cls", "题目1", "80%"),
		makeQ("A1型题", "ch1", "pkg", "cls", "题目2", "50%"),
		makeQ("A1型题", "ch1", "pkg", "cls", "题目3", "30%"),
	}
	c := FilterCriteria{MinRate: 70, MaxRate: 100}
	result := Apply(qs, c)
	if len(result) != 1 || result[0].SubQuestions[0].Rate != "80%" {
		t.Fatalf("min-rate filter failed")
	}
}

func TestFilterByMaxRate(t *testing.T) {
	qs := []*models.Question{
		makeQ("A1型题", "ch1", "pkg", "cls", "题目1", "80%"),
		makeQ("A1型题", "ch1", "pkg", "cls", "题目2", "50%"),
		makeQ("A1型题", "ch1", "pkg", "cls", "题目3", "30%"),
	}
	c := FilterCriteria{MinRate: 0, MaxRate: 49}
	result := Apply(qs, c)
	if len(result) != 1 || result[0].SubQuestions[0].Rate != "30%" {
		t.Fatalf("max-rate filter failed, got %d", len(result))
	}
}

func TestFilterRateRange(t *testing.T) {
	qs := []*models.Question{
		makeQ("A1型题", "ch1", "pkg", "cls", "题目1", "80%"),
		makeQ("A1型题", "ch1", "pkg", "cls", "题目2", "60%"),
		makeQ("A1型题", "ch1", "pkg", "cls", "题目3", "30%"),
	}
	c := FilterCriteria{MinRate: 50, MaxRate: 65}
	result := Apply(qs, c)
	if len(result) != 1 || result[0].SubQuestions[0].Rate != "60%" {
		t.Fatalf("rate-range filter failed, got %d", len(result))
	}
}

func TestFilterDefault_ReturnsAll(t *testing.T) {
	qs := []*models.Question{
		makeQ("A1型题", "ch1", "pkg1", "cls1", "题目1", "80%"),
		makeQ("A2型题", "ch2", "pkg2", "cls2", "题目2", "50%"),
	}
	c := NewCriteria()
	result := Apply(qs, c)
	if len(result) != 2 {
		t.Fatalf("default filter should return all, got %d", len(result))
	}
}

func TestFilterNoRateNotExcluded(t *testing.T) {
	qs := []*models.Question{
		makeQ("A1型题", "ch1", "pkg", "cls", "题目1", ""), // no rate
		makeQ("A1型题", "ch1", "pkg", "cls", "题目2", "80%"),
	}
	c := FilterCriteria{MinRate: 50, MaxRate: 100}
	result := Apply(qs, c)
	// question with no rate should pass through (can't be filtered by rate)
	if len(result) != 2 {
		t.Fatalf("no-rate question should not be excluded, got %d", len(result))
	}
}
