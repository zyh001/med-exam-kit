package parsers

import "testing"

var ahuyikaoA1 = map[string]any{
	"name": "2026-02-10-22-14-54-684",
	"cls":  "口腔执业医师题库",
	"pkg":  "ahuyikao.com",
	"numb": "1/16",
	"unit": "第十一节 牙周组织疾病",
	"mode": "A1型题",
	"test": "牙龈瘤的病变性质多属于",
	"option": []any{
		"A.良性肿瘤", "B.恶性肿瘤", "C.局限性慢性炎性增生", "D.发育畸形", "E.自身免疫性疾病",
	},
	"answer":      "C",
	"rate":        "75%",
	"error_prone": "D",
	"discuss":     "牙龈瘤是局限生长的慢性炎性增生物。",
}

var ahuyikaoB = map[string]any{
	"name":           "2026-02-10-22-34-06-809",
	"cls":            "口腔执业医师题库",
	"pkg":            "ahuyikao.com",
	"unit":           "唾液腺疾病",
	"mode":           "B1型题",
	"shared_options": []any{"A.腮腺", "B.颌下腺", "C.舌下腺", "D.唇腺", "E.腭腺"},
	"sub_questions": []any{
		map[string]any{"test": "纯浆液性腺", "answer": "A", "rate": "92%"},
		map[string]any{"test": "混合性腺", "answer": "B", "rate": "81%"},
	},
	"discuss": "第1题: 腮腺属于纯浆液性腺",
}

var yikaobangA1 = map[string]any{
	"name": "2026-02-10-23-53-12-370",
	"pkg":  "com.yikaobang.yixue",
	"cls":  "口腔颌面外科学（120分）",
	"unit": "第二章 基础知识",
	"mode": "A1型题",
	"test": "超声检查适用于",
	"option": []any{
		"A.确定有无占位性病变", "B.确定囊性或实性肿物",
		"C.评价肿瘤性质", "D.确定深部肿物与血管关系", "E.以上均适用",
	},
	"answer":  "E",
	"point":   "口腔颌面外科学第八版",
	"discuss": "超声检查考点。",
}

var yikaobangBSingle = map[string]any{
	"name": "2026-02-19-21-11-36-410",
	"pkg":  "com.yikaobang.yixue",
	"cls":  "中医基础理论",
	"unit": "绪论",
	"mode": "B型题",
	"test": `以下属于"证候"的是`,
	"option": []any{
		"A.痢疾", "B.角弓反张", "C.心脉痹阻", "D.恶寒发热", "E.脉象沉迟",
	},
	"answer":  "C",
	"rate":    "55.6%",
	"discuss": "证候是一系列相互关联的症状总称。",
}

func TestAhuyikaoParser_A1(t *testing.T) {
	p := &AhuyikaoParser{}
	q, err := p.Parse(ahuyikaoA1)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.SubQuestions) != 1 {
		t.Fatalf("want 1 subq, got %d", len(q.SubQuestions))
	}
	if q.SubQuestions[0].Answer != "C" {
		t.Fatalf("want answer C, got %s", q.SubQuestions[0].Answer)
	}
	if len(q.SubQuestions[0].Options) != 5 {
		t.Fatalf("want 5 options, got %d", len(q.SubQuestions[0].Options))
	}
}

func TestAhuyikaoParser_B1_IndependentOptions(t *testing.T) {
	p := &AhuyikaoParser{}
	q, err := p.Parse(ahuyikaoB)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.SubQuestions) != 2 {
		t.Fatalf("want 2 subq, got %d", len(q.SubQuestions))
	}
	// Each subquestion must have its own independent slice (not same pointer)
	q.SubQuestions[0].Options[0] = "MODIFIED"
	if q.SubQuestions[1].Options[0] == "MODIFIED" {
		t.Fatal("B-type sub-questions share the same options slice — should be independent copies")
	}
	if q.SubQuestions[1].Options[0] == "MODIFIED" {
		t.Fatal("shared_options was also modified — copy not made")
	}
}

func TestYikaobangParser_A1_PointField(t *testing.T) {
	p := &YikaobangParser{}
	q, err := p.Parse(yikaobangA1)
	if err != nil {
		t.Fatal(err)
	}
	if q.SubQuestions[0].Point == "" {
		t.Fatal("point field should be populated for yikaobang A1")
	}
}

func TestYikaobangParser_SingleBType(t *testing.T) {
	p := &YikaobangParser{}
	q, err := p.Parse(yikaobangBSingle)
	if err != nil {
		t.Fatal(err)
	}
	if len(q.SubQuestions) != 1 {
		t.Fatalf("single B-type: want 1 subq, got %d", len(q.SubQuestions))
	}
	if q.SubQuestions[0].Answer != "C" {
		t.Fatalf("want C, got %s", q.SubQuestions[0].Answer)
	}
	if len(q.SubQuestions[0].Options) != 5 {
		t.Fatalf("want 5 options, got %d", len(q.SubQuestions[0].Options))
	}
}

func TestRegistry_DefaultParsers(t *testing.T) {
	for name := range DefaultParserMap {
		parserName := DefaultParserMap[name]
		if _, err := Get(parserName); err != nil {
			t.Fatalf("parser %q not registered: %v", parserName, err)
		}
	}
}
