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
