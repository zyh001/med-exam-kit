import json
import tempfile
from pathlib import Path
from med_exam_toolkit.loader import load_json_files
from med_exam_toolkit.dedup import deduplicate, compute_fingerprint
from med_exam_toolkit.parsers import discover

# 测试数据
A1_SAMPLE = {
    "name": "2026-02-10-22-14-54-684",
    "cls": "口腔执业医师题库",
    "pkg": "ahuyikao.com",
    "numb": "1/16",
    "unit": "第十一节 牙周组织疾病",
    "mode": "A1型题",
    "test": "牙龈瘤的病变性质多属于",
    "option": ["A.良性肿瘤", "B.恶性肿瘤", "C.局限性慢性炎性增生", "D.发育畸形", "E.自身免疫性疾病"],
    "answer": "C",
    "rate": "75%",
    "error_prone": "D",
    "discuss": "牙龈瘤是牙龈上特别是龈乳头处局限生长的慢性炎性反应性瘤样增生物。"
}

B_SAMPLE = {
    "name": "2026-02-10-22-34-06-809",
    "cls": "口腔执业医师题库",
    "pkg": "ahuyikao.com",
    "numb": "10/574",
    "unit": "唾液腺疾病",
    "mode": "B1型题",
    "shared_options": ["A.腮腺", "B.颌下腺", "C.舌下腺", "D.唇腺", "E.腭腺"],
    "sub_questions": [
        {"test": "属于大涎腺，纯浆液性腺的是", "answer": "A", "rate": "92%", "error_prone": "B"},
        {"test": "属于大涎腺，混合性腺以浆液性腺泡为主的是", "answer": "B", "rate": "81%", "error_prone": "C"},
    ],
    "discuss": "第1题: 腮腺属于纯浆液性腺"
}

YIKAOBANG_SAMPLE = {
    "name": "2026-02-10-23-53-12-370",
    "pkg": "com.yikaobang.yixue",
    "cls": "口腔颌面外科学（120分）",
    "numb": "12/115",
    "unit": "第二章 口腔颌面外科基础知识与基本操作",
    "mode": "A1型题",
    "test": "超声检查在口腔颌面部适用于",
    "option": ["A.确定有无占位性病变", "B.确定囊性或实性肿物", "C.为评价肿瘤性质提供信息",
               "D.确定深部肿物与邻近重要血管的关系", "E.以上均适用"],
    "answer": "E",
    "point": "口腔颌面外科学第八版",
    "discuss": "本题考查超声检查。"
}


def _write_samples(tmpdir: Path, samples: list[dict]):
    for i, s in enumerate(samples):
        fp = tmpdir / f"sample_{i}.json"
        fp.write_text(json.dumps(s, ensure_ascii=False), encoding="utf-8")


def test_load_and_parse():
    with tempfile.TemporaryDirectory() as tmpdir:
        _write_samples(Path(tmpdir), [A1_SAMPLE, B_SAMPLE, YIKAOBANG_SAMPLE])
        parser_map = {"ahuyikao.com": "ahuyikao", "com.yikaobang.yixue": "yikaobang"}
        questions = load_json_files(tmpdir, parser_map)
        assert len(questions) == 3
        # A1 型应有 1 个 sub_question
        a1 = [q for q in questions if "A1" in q.mode and q.pkg == "ahuyikao.com"][0]
        assert len(a1.sub_questions) == 1
        assert a1.sub_questions[0].answer == "C"
        # B 型应有 2 个 sub_question
        b1 = [q for q in questions if "B" in q.mode][0]
        assert len(b1.sub_questions) == 2
        # yikaobang 应有 point 字段
        yk = [q for q in questions if "yikaobang" in q.pkg][0]
        assert yk.sub_questions[0].point != ""


def test_dedup_removes_duplicates():
    discover()
    from med_exam_toolkit.parsers import get_parser
    parser = get_parser("ahuyikao")
    q1 = parser.parse(A1_SAMPLE)
    q2 = parser.parse(A1_SAMPLE)  # 完全相同
    result = deduplicate([q1, q2], strategy="strict")
    assert len(result) == 1


def test_dedup_keeps_different():
    discover()
    from med_exam_toolkit.parsers import get_parser
    p1 = get_parser("ahuyikao")
    p2 = get_parser("yikaobang")
    q1 = p1.parse(A1_SAMPLE)
    q2 = p2.parse(YIKAOBANG_SAMPLE)
    result = deduplicate([q1, q2], strategy="strict")
    assert len(result) == 2


def test_fingerprint_consistency():
    discover()
    from med_exam_toolkit.parsers import get_parser
    parser = get_parser("ahuyikao")
    q1 = parser.parse(A1_SAMPLE)
    q2 = parser.parse(A1_SAMPLE)
    assert compute_fingerprint(q1) == compute_fingerprint(q2)


if __name__ == "__main__":
    test_load_and_parse()
    test_dedup_removes_duplicates()
    test_dedup_keeps_different()
    test_fingerprint_consistency()
    print("All tests passed!")
