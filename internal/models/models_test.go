package models

import "testing"

func TestEffAnswer_OfficialPriority(t *testing.T) {
	sq := SubQuestion{Answer: "C", AIAnswer: "B"}
	if got := sq.EffAnswer(); got != "C" {
		t.Fatalf("EffAnswer: want C, got %s", got)
	}
}

func TestEffAnswer_AIFallback(t *testing.T) {
	sq := SubQuestion{Answer: "", AIAnswer: "B"}
	if got := sq.EffAnswer(); got != "B" {
		t.Fatalf("EffAnswer: want B, got %s", got)
	}
}

func TestEffAnswer_Empty(t *testing.T) {
	sq := SubQuestion{}
	if got := sq.EffAnswer(); got != "" {
		t.Fatalf("EffAnswer: want empty, got %s", got)
	}
}

func TestAnswerSource_AIWhenBothSame(t *testing.T) {
	// Python behaviour: when AI matches effective answer → source = "ai"
	sq := SubQuestion{Answer: "C", AIAnswer: "C"}
	if got := sq.AnswerSource(); got != "ai" {
		t.Fatalf("AnswerSource: want ai, got %s", got)
	}
}

func TestAnswerSource_OfficialWhenAIDiffers(t *testing.T) {
	sq := SubQuestion{Answer: "C", AIAnswer: "B"}
	if got := sq.AnswerSource(); got != "official" {
		t.Fatalf("AnswerSource: want official, got %s", got)
	}
}

func TestAnswerSource_AIWhenOfficialEmpty(t *testing.T) {
	sq := SubQuestion{Answer: "", AIAnswer: "B"}
	if got := sq.AnswerSource(); got != "ai" {
		t.Fatalf("AnswerSource: want ai, got %s", got)
	}
}

func TestAnswerSource_EmptyWhenBothEmpty(t *testing.T) {
	sq := SubQuestion{}
	if got := sq.AnswerSource(); got != "" {
		t.Fatalf("AnswerSource: want empty, got %s", got)
	}
}

func TestDiscussSource_OfficialPriority(t *testing.T) {
	sq := SubQuestion{Discuss: "官方解析", AIDiscuss: "AI解析"}
	if got := sq.DiscussSource(); got != "official" {
		t.Fatalf("DiscussSource: want official, got %s", got)
	}
}

func TestDiscussSource_AIFallback(t *testing.T) {
	sq := SubQuestion{Discuss: "", AIDiscuss: "AI解析"}
	if got := sq.DiscussSource(); got != "ai" {
		t.Fatalf("DiscussSource: want ai, got %s", got)
	}
}

func TestEffDiscuss_OfficialPriority(t *testing.T) {
	sq := SubQuestion{Discuss: "官方", AIDiscuss: "AI"}
	if got := sq.EffDiscuss(); got != "官方" {
		t.Fatalf("EffDiscuss: want 官方, got %s", got)
	}
}

func TestSanitizeAnswerDiscuss_SwappedDetected(t *testing.T) {
	sq := SubQuestion{
		Answer:  "本题考查高血压的一线用药，答案选A，因为ACEI类药物（如卡托普利）是首选。",
		Discuss: "A",
	}
	if !sq.IsAnswerDiscussSwapped() {
		t.Fatal("expected swap detected")
	}
	if !sq.SanitizeAnswerDiscuss() {
		t.Fatal("expected SanitizeAnswerDiscuss to return true")
	}
	if sq.Answer != "A" {
		t.Fatalf("after fix: answer want A, got %q", sq.Answer)
	}
	if sq.Discuss == "A" {
		t.Fatal("after fix: discuss should not be 'A' anymore")
	}
}

func TestSanitizeAnswerDiscuss_NormalNotSwapped(t *testing.T) {
	sq := SubQuestion{
		Answer:  "B",
		Discuss: "本题考查心肌梗死的治疗，B选项阿司匹林抗血小板为首选。",
	}
	if sq.IsAnswerDiscussSwapped() {
		t.Fatal("should not detect swap for normal question")
	}
}

func TestSanitizeAnswerDiscuss_MultiAnswer(t *testing.T) {
	sq := SubQuestion{
		Answer:  "ABD",
		Discuss: "该题为多选，三者均为诊断标准。",
	}
	if sq.IsAnswerDiscussSwapped() {
		t.Fatal("multi-answer ABD should not trigger swap detection")
	}
}

func TestSanitizeQuestions_FixesSwapped(t *testing.T) {
	qs := []*Question{
		{SubQuestions: []SubQuestion{
			{Answer: "因为患者出现了典型的心肌梗死症状，应首选溶栓治疗。", Discuss: "C"},
			{Answer: "E", Discuss: "本题答案为E，该药物禁用于肾功能不全患者。"},
		}},
	}
	fixed := SanitizeQuestions(qs)
	if fixed != 1 {
		t.Fatalf("expected 1 fixed, got %d", fixed)
	}
	if qs[0].SubQuestions[0].Answer != "C" {
		t.Fatalf("first sq answer want C, got %q", qs[0].SubQuestions[0].Answer)
	}
	if qs[0].SubQuestions[1].Answer != "E" {
		t.Fatalf("second sq answer should remain E, got %q", qs[0].SubQuestions[1].Answer)
	}
}
