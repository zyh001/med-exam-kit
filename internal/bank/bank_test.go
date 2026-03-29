package bank

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/med-exam-kit/med-exam-kit/internal/models"
)

func makeTestQuestions() []*models.Question {
	return []*models.Question{
		{
			Fingerprint: "fp1",
			Name:        "test-001",
			Pkg:         "ahuyikao.com",
			Cls:         "口腔执业医师题库",
			Unit:        "第一章",
			Mode:        "A1型题",
			SubQuestions: []models.SubQuestion{
				{
					Text:    "牙龈瘤的性质是",
					Options: []string{"A.肿瘤", "B.增生", "C.炎症", "D.囊肿", "E.坏死"},
					Answer:  "B",
					Rate:    "75%",
					Discuss: "牙龈瘤是增生性病变",
				},
			},
		},
		{
			Fingerprint:   "fp2",
			Name:          "test-002",
			Pkg:           "com.yikaobang.yixue",
			Mode:          "B1型题",
			SharedOptions: []string{"A.腮腺", "B.颌下腺", "C.舌下腺"},
			SubQuestions: []models.SubQuestion{
				{Text: "纯浆液性", Options: []string{"A.腮腺", "B.颌下腺", "C.舌下腺"}, Answer: "A"},
				{Text: "混合性", Options: []string{"A.腮腺", "B.颌下腺", "C.舌下腺"}, Answer: "B"},
			},
		},
	}
}

func TestSaveLoadBank_NoPassword(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "test")
	questions := makeTestQuestions()

	saved, err := SaveBank(questions, outPath, "", true, 6)
	if err != nil {
		t.Fatalf("SaveBank: %v", err)
	}
	if _, err := os.Stat(saved); err != nil {
		t.Fatalf("output file missing: %v", err)
	}

	loaded, err := LoadBank(saved, "")
	if err != nil {
		t.Fatalf("LoadBank: %v", err)
	}
	if len(loaded) != len(questions) {
		t.Fatalf("want %d questions, got %d", len(questions), len(loaded))
	}
	if loaded[0].Fingerprint != questions[0].Fingerprint {
		t.Fatalf("fingerprint mismatch")
	}
	if loaded[0].SubQuestions[0].Answer != "B" {
		t.Fatalf("answer mismatch")
	}
}

func TestSaveLoadBank_WithPassword(t *testing.T) {
	dir := t.TempDir()
	questions := makeTestQuestions()
	saved, err := SaveBank(questions, filepath.Join(dir, "enc"), "s3cr3t", true, 6)
	if err != nil {
		t.Fatalf("SaveBank: %v", err)
	}

	// Wrong password must fail
	if _, err := LoadBank(saved, "wrong"); err == nil {
		t.Fatal("wrong password should fail")
	}

	// Correct password must succeed
	loaded, err := LoadBank(saved, "s3cr3t")
	if err != nil {
		t.Fatalf("LoadBank with correct password: %v", err)
	}
	if len(loaded) != len(questions) {
		t.Fatalf("want %d, got %d", len(questions), len(loaded))
	}
}

func TestSaveLoadBank_NoCompress(t *testing.T) {
	dir := t.TempDir()
	questions := makeTestQuestions()
	saved, err := SaveBank(questions, filepath.Join(dir, "nocomp"), "", false, 6)
	if err != nil {
		t.Fatalf("SaveBank no-compress: %v", err)
	}
	loaded, err := LoadBank(saved, "")
	if err != nil {
		t.Fatalf("LoadBank no-compress: %v", err)
	}
	if len(loaded) != 2 {
		t.Fatalf("want 2, got %d", len(loaded))
	}
}

func TestSaveLoadBank_DifferentSaltEachSave(t *testing.T) {
	dir := t.TempDir()
	questions := makeTestQuestions()
	p1 := filepath.Join(dir, "save1")
	p2 := filepath.Join(dir, "save2")
	SaveBank(questions, p1, "pw", true, 6)
	SaveBank(questions, p2, "pw", true, 6)

	b1, _ := os.ReadFile(p1 + ".mqb")
	b2, _ := os.ReadFile(p2 + ".mqb")
	// Files should differ due to different random salt and Fernet IV
	different := false
	for i := range b1 {
		if i >= len(b2) || b1[i] != b2[i] {
			different = true
			break
		}
	}
	if !different {
		t.Fatal("two saves should produce different bytes (random salt + IV)")
	}
}

func TestLoadBank_InvalidMagic(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.mqb")
	os.WriteFile(bad, []byte("XXXX\x00\x00\x00\x00{}"), 0o600)
	if _, err := LoadBank(bad, ""); err == nil {
		t.Fatal("invalid magic should return error")
	}
}

func TestLoadBank_MQB1RejectsWithMessage(t *testing.T) {
	dir := t.TempDir()
	mqb1 := filepath.Join(dir, "old.mqb")
	// Minimal MQB1 header: magic + 4-byte meta_len + empty meta
	data := []byte{'M', 'Q', 'B', '1', 0, 0, 0, 2, '{', '}'}
	os.WriteFile(mqb1, data, 0o600)
	_, err := LoadBank(mqb1, "")
	if err == nil {
		t.Fatal("MQB1 should be rejected")
	}
	if len(err.Error()) < 10 {
		t.Fatal("error message should be informative")
	}
}

func TestSaveBank_RawFieldStripped(t *testing.T) {
	dir := t.TempDir()
	q := &models.Question{
		Fingerprint: "fp",
		Mode:        "A1型题",
		Raw:         map[string]any{"huge_field": "this should not appear in .mqb"},
		SubQuestions: []models.SubQuestion{
			{Text: "题目", Options: []string{"A.甲"}, Answer: "A"},
		},
	}
	saved, _ := SaveBank([]*models.Question{q}, filepath.Join(dir, "bank"), "", true, 6)
	loaded, err := LoadBank(saved, "")
	if err != nil {
		t.Fatalf("LoadBank: %v", err)
	}
	if loaded[0].Raw != nil && len(loaded[0].Raw) > 0 {
		t.Fatal("Raw field should be empty in saved bank")
	}
}
