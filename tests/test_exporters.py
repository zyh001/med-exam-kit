# tests/test_exporters.py
import json
import tempfile
from pathlib import Path
from med_exam_toolkit.loader import load_json_files
from med_exam_toolkit.dedup import deduplicate
from med_exam_toolkit.exporters import discover as discover_exporters, get_exporter
from med_exam_toolkit.filters import FilterCriteria, apply_filters
from med_exam_toolkit.stats import summarize

SAMPLES = [
    {
        "name": "test-a1", "cls": "口腔执业医师题库", "pkg": "ahuyikao.com",
        "numb": "1/16", "unit": "牙周组织疾病", "mode": "A1型题",
        "test": "牙龈瘤的病变性质多属于",
        "option": ["A.良性肿瘤", "B.恶性肿瘤", "C.局限性慢性炎性增生", "D.发育畸形", "E.自身免疫性疾病"],
        "answer": "C", "rate": "75%", "error_prone": "D", "discuss": "测试解析"
    },
    {
        "name": "test-a1-dup", "cls": "口腔执业医师题库", "pkg": "ahuyikao.com",
        "numb": "2/16", "unit": "牙周组织疾病", "mode": "A1型题",
        "test": "牙龈瘤的病变性质多属于",
        "option": ["A.良性肿瘤", "B.恶性肿瘤", "C.局限性慢性炎性增生", "D.发育畸形", "E.自身免疫性疾病"],
        "answer": "C", "rate": "75%", "error_prone": "D", "discuss": "测试解析"
    },
    {
        "name": "test-yk", "pkg": "com.yikaobang.yixue", "cls": "口腔颌面外科学",
        "numb": "1/10", "unit": "基础知识", "mode": "A1型题",
        "test": "超声检查适用于", "option": ["A.选项1", "B.选项2", "C.选项3", "D.选项4", "E.以上均适用"],
        "answer": "E", "point": "教材P13", "discuss": "超声检查解析"
    },
]


def _setup(tmpdir: Path):
    for i, s in enumerate(SAMPLES):
        (tmpdir / f"q_{i}.json").write_text(json.dumps(s, ensure_ascii=False), encoding="utf-8")
    parser_map = {"ahuyikao.com": "ahuyikao", "com.yikaobang.yixue": "yikaobang"}
    return load_json_files(str(tmpdir), parser_map)


def test_csv_export():
    discover_exporters()
    with tempfile.TemporaryDirectory() as tmpdir:
        questions = _setup(Path(tmpdir))
        questions = deduplicate(questions)
        out = Path(tmpdir) / "output" / "test"
        get_exporter("csv").export(questions, out)
        assert out.with_suffix(".csv").exists()


def test_xlsx_export():
    discover_exporters()
    with tempfile.TemporaryDirectory() as tmpdir:
        questions = _setup(Path(tmpdir))
        questions = deduplicate(questions)
        out = Path(tmpdir) / "output" / "test"
        get_exporter("xlsx").export(questions, out)
        assert out.with_suffix(".xlsx").exists()


def test_json_export():
    discover_exporters()
    with tempfile.TemporaryDirectory() as tmpdir:
        questions = _setup(Path(tmpdir))
        questions = deduplicate(questions)
        out = Path(tmpdir) / "output" / "test"
        get_exporter("json").export(questions, out)
        fp = out.with_suffix(".json")
        assert fp.exists()
        data = json.loads(fp.read_text(encoding="utf-8"))
        assert len(data) == 2  # 去重后应为 2


def test_db_export():
    discover_exporters()
    with tempfile.TemporaryDirectory() as tmpdir:
        questions = _setup(Path(tmpdir))
        questions = deduplicate(questions)
        out = Path(tmpdir) / "output" / "test"
        get_exporter("db").export(questions, out)
        assert out.with_suffix(".db").exists()


def test_filter_by_mode():
    with tempfile.TemporaryDirectory() as tmpdir:
        questions = _setup(Path(tmpdir))
        criteria = FilterCriteria(modes=["A1"])
        filtered = apply_filters(questions, criteria)
        assert len(filtered) == 3  # 全部都是 A1


def test_filter_by_keyword():
    with tempfile.TemporaryDirectory() as tmpdir:
        questions = _setup(Path(tmpdir))
        criteria = FilterCriteria(keyword="超声")
        filtered = apply_filters(questions, criteria)
        assert len(filtered) == 1


def test_filter_by_pkg():
    with tempfile.TemporaryDirectory() as tmpdir:
        questions = _setup(Path(tmpdir))
        criteria = FilterCriteria(pkgs=["yikaobang"])
        filtered = apply_filters(questions, criteria)
        assert len(filtered) == 1


def test_stats():
    with tempfile.TemporaryDirectory() as tmpdir:
        questions = _setup(Path(tmpdir))
        s = summarize(questions)
        assert s["total"] == 3
        assert "A1型题" in s["by_mode"]


if __name__ == "__main__":
    test_csv_export()
    test_xlsx_export()
    test_json_export()
    test_db_export()
    test_filter_by_mode()
    test_filter_by_keyword()
    test_filter_by_pkg()
    test_stats()
    print("All exporter tests passed!")
