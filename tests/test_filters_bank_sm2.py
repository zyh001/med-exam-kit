from __future__ import annotations

import json
import logging
import tempfile
from pathlib import Path

import pytest

from med_exam_toolkit.dedup import deduplicate
from med_exam_toolkit.filters import FilterCriteria, apply_filters
from med_exam_toolkit.models import Question, SubQuestion
from med_exam_toolkit.parsers import DEFAULT_PARSER_MAP, discover, get_parser

# ── 测试数据工厂 ──────────────────────────────────────────────────────────

def _make_q(
    *,
    mode: str = "A1型题",
    unit: str = "第一章 基础知识",
    pkg: str = "ahuyikao.com",
    cls: str = "口腔执业医师题库",
    text: str = "测试题目",
    answer: str = "A",
    rate: str = "75%",
    options: list[str] | None = None,
) -> Question:
    opts = options or ["A.选项A", "B.选项B", "C.选项C", "D.选项D", "E.选项E"]
    sq = SubQuestion(text=text, options=opts, answer=answer, rate=rate)
    return Question(mode=mode, unit=unit, pkg=pkg, cls=cls, sub_questions=[sq])


# ═══════════════════════════════════════════════════
# 1. FilterCriteria
# ═══════════════════════════════════════════════════

class TestFilters:
    def setup_method(self):
        self.questions = [
            _make_q(mode="A1型题", unit="第一章 基础知识",   pkg="ahuyikao.com",   cls="口腔执业医师题库", text="A1题1", rate="80%"),
            _make_q(mode="A2型题", unit="第二章 临床诊断",   pkg="ahuyikao.com",   cls="口腔执业医师题库", text="A2题1", rate="50%"),
            _make_q(mode="B1型题", unit="第三章 实验室检查", pkg="yikaobang.com",  cls="口腔颌面外科学",  text="B1题1", rate="30%"),
            _make_q(mode="A1型题", unit="第一章 基础知识",   pkg="yikaobang.com",  cls="口腔颌面外科学",  text="A1题2", rate="60%"),
        ]

    # -- 单条件过滤 --

    def test_filter_by_mode(self):
        criteria = FilterCriteria(modes=["A1"])
        result = apply_filters(self.questions, criteria)
        assert len(result) == 2
        assert all("A1" in q.mode for q in result)

    def test_filter_by_pkg(self):
        criteria = FilterCriteria(pkgs=["yikaobang"])
        result = apply_filters(self.questions, criteria)
        assert len(result) == 2
        assert all("yikaobang" in q.pkg for q in result)

    def test_filter_by_unit_keyword(self):
        criteria = FilterCriteria(units=["第一章"])
        result = apply_filters(self.questions, criteria)
        assert len(result) == 2

    def test_filter_by_cls(self):
        criteria = FilterCriteria(cls_list=["口腔颌面外科学"])
        result = apply_filters(self.questions, criteria)
        assert len(result) == 2

    def test_filter_by_keyword_in_stem(self):
        criteria = FilterCriteria(keyword="A2题")
        result = apply_filters(self.questions, criteria)
        assert len(result) == 1
        assert result[0].sub_questions[0].text == "A2题1"

    def test_filter_by_max_rate(self):
        # max_rate 是闭区间（filters.py 用 > 排除），50% 满足 max_rate=50
        # 只保留正确率 < 50% 的题，需要 max_rate=49
        criteria = FilterCriteria(max_rate=49)
        result = apply_filters(self.questions, criteria)
        assert len(result) == 1
        assert result[0].sub_questions[0].rate == "30%"

    def test_filter_by_min_rate(self):
        # 只保留正确率 ≥ 70% 的题
        criteria = FilterCriteria(min_rate=70)
        result = apply_filters(self.questions, criteria)
        assert len(result) == 1
        assert result[0].sub_questions[0].rate == "80%"

    def test_filter_by_rate_range(self):
        # 50% ≤ 正确率 ≤ 65%
        criteria = FilterCriteria(min_rate=50, max_rate=65)
        result = apply_filters(self.questions, criteria)
        assert len(result) == 2  # 50% 和 60%

    # -- 边界情况 --

    def test_default_criteria_returns_all(self):
        """空 FilterCriteria（所有默认值）不应过滤任何题目"""
        criteria = FilterCriteria()
        result = apply_filters(self.questions, criteria)
        assert len(result) == len(self.questions)

    def test_min_rate_zero_max_rate_100_returns_all(self):
        """min_rate=0, max_rate=100 等价于不过滤正确率"""
        criteria = FilterCriteria(min_rate=0, max_rate=100)
        result = apply_filters(self.questions, criteria)
        assert len(result) == len(self.questions)

    def test_impossible_range_returns_empty(self):
        """min_rate > max_rate → 无题目匹配"""
        criteria = FilterCriteria(min_rate=90, max_rate=10)
        result = apply_filters(self.questions, criteria)
        assert result == []

    def test_filter_combined(self):
        """多条件取交集"""
        criteria = FilterCriteria(modes=["A1"], pkgs=["ahuyikao"])
        result = apply_filters(self.questions, criteria)
        assert len(result) == 1
        assert result[0].sub_questions[0].text == "A1题1"

    def test_no_print_in_filter(self, capfd):
        """filters.apply_filters 不应产生任何 stdout 输出（应使用 logging）"""
        criteria = FilterCriteria(modes=["A1"])
        apply_filters(self.questions, criteria)
        captured = capfd.readouterr()
        assert captured.out == "", "filters.py 不应使用 print()，请改用 logging"

    def test_filter_uses_logging(self, caplog):
        """过滤完成后应通过 logging 记录结果行数"""
        with caplog.at_level(logging.INFO, logger="med_exam_toolkit.filters"):
            criteria = FilterCriteria(modes=["A1"])
            apply_filters(self.questions, criteria)
        assert any("过滤完成" in r.message for r in caplog.records)

    def test_questions_without_rate_not_excluded_by_default(self):
        """无正确率字段的题目在默认过滤条件下不应被排除"""
        no_rate_q = _make_q(rate="")
        all_qs = self.questions + [no_rate_q]
        criteria = FilterCriteria()
        result = apply_filters(all_qs, criteria)
        assert no_rate_q in result

    def test_questions_without_rate_excluded_by_rate_filter(self):
        """无正确率的题目在设置了正确率范围时行为：
        因为 rates 列表为空，filters.py 中 `if rates:` 分支不进入，
        题目直接 append —— 即无正确率的题不被正确率过滤器排除。
        这是合理的宽松策略（无数据 ≠ 不满足条件）。
        """
        no_rate_q = _make_q(rate="")
        all_qs = self.questions + [no_rate_q]
        criteria = FilterCriteria(min_rate=60, max_rate=100)
        result = apply_filters(all_qs, criteria)
        # 满足条件的：80%、60%；无正确率的题被放行（宽松策略）
        assert no_rate_q in result
        # 不满足条件的：50%、30% 被过滤
        texts_in_result = {q.sub_questions[0].text for q in result}
        assert "A2题1" not in texts_in_result  # 50%
        assert "B1题1" not in texts_in_result  # 30%


# ═══════════════════════════════════════════════════
# 2. MQB2 加密 / 解密往返一致性
# ═══════════════════════════════════════════════════

class TestBankEncryption:
    def _sample_questions(self) -> list[Question]:
        discover()
        parser = get_parser("ahuyikao")
        samples = [
            {
                "name": "test-enc-1", "cls": "测试题库", "pkg": "ahuyikao.com",
                "numb": "1/2", "unit": "第一章", "mode": "A1型题",
                "test": "加密测试题目一",
                "option": ["A.选项A", "B.选项B", "C.选项C", "D.选项D", "E.选项E"],
                "answer": "B", "rate": "60%", "error_prone": "A", "discuss": "解析一",
            },
            {
                "name": "test-enc-2", "cls": "测试题库", "pkg": "ahuyikao.com",
                "numb": "2/2", "unit": "第二章", "mode": "A2型题",
                "test": "加密测试题目二",
                "option": ["A.甲", "B.乙", "C.丙", "D.丁", "E.戊"],
                "answer": "D", "rate": "40%", "error_prone": "B", "discuss": "解析二",
            },
        ]
        return [parser.parse(s) for s in samples]

    def test_roundtrip_no_password(self):
        """无密码：保存后重新加载，数据完全一致"""
        from med_exam_toolkit.bank import load_bank, save_bank
        questions = self._sample_questions()
        with tempfile.TemporaryDirectory() as tmpdir:
            out = Path(tmpdir) / "bank_plain"
            save_bank(questions, out)
            loaded = load_bank(out.with_suffix(".mqb"))

        assert len(loaded) == len(questions)
        for orig, back in zip(questions, loaded):
            assert orig.mode == back.mode
            assert orig.unit == back.unit
            assert len(orig.sub_questions) == len(back.sub_questions)
            assert orig.sub_questions[0].answer == back.sub_questions[0].answer
            assert orig.sub_questions[0].discuss == back.sub_questions[0].discuss

    def test_roundtrip_with_password(self):
        """有密码：正确密码可解密，数据与原始一致"""
        from med_exam_toolkit.bank import load_bank, save_bank
        questions = self._sample_questions()
        password = "test_password_123"
        with tempfile.TemporaryDirectory() as tmpdir:
            out = Path(tmpdir) / "bank_enc"
            save_bank(questions, out, password=password)
            loaded = load_bank(out.with_suffix(".mqb"), password=password)

        assert len(loaded) == len(questions)
        assert loaded[0].sub_questions[0].answer == questions[0].sub_questions[0].answer

    def test_wrong_password_raises(self):
        """错误密码应抛出 ValueError"""
        from med_exam_toolkit.bank import load_bank, save_bank
        questions = self._sample_questions()
        with tempfile.TemporaryDirectory() as tmpdir:
            out = Path(tmpdir) / "bank_enc"
            save_bank(questions, out, password="correct_password")
            with pytest.raises(ValueError, match="密码错误|损坏"):
                load_bank(out.with_suffix(".mqb"), password="wrong_password")

    def test_no_password_on_encrypted_raises(self):
        """加密文件不提供密码应抛出 ValueError"""
        from med_exam_toolkit.bank import load_bank, save_bank
        questions = self._sample_questions()
        with tempfile.TemporaryDirectory() as tmpdir:
            out = Path(tmpdir) / "bank_enc"
            save_bank(questions, out, password="any_password")
            with pytest.raises(ValueError, match="已加密|请提供"):
                load_bank(out.with_suffix(".mqb"), password=None)

    def test_mqb2_magic_header(self):
        """保存的文件应以 MQB2 magic bytes 开头"""
        from med_exam_toolkit.bank import save_bank
        questions = self._sample_questions()
        with tempfile.TemporaryDirectory() as tmpdir:
            out = Path(tmpdir) / "bank"
            fp = save_bank(questions, out)
            with open(fp, "rb") as fh:
                magic = fh.read(4)
        assert magic == b"MQB2"

    def test_invalid_file_raises(self):
        """非 mqb 文件应抛出 ValueError"""
        from med_exam_toolkit.bank import load_bank
        with tempfile.TemporaryDirectory() as tmpdir:
            bad = Path(tmpdir) / "bad.mqb"
            bad.write_bytes(b"NOT_A_VALID_FILE")
            with pytest.raises(ValueError):
                load_bank(bad)

    def test_different_salt_each_save(self):
        """每次保存应生成不同的盐值（即密文应不同）"""
        from med_exam_toolkit.bank import save_bank
        questions = self._sample_questions()
        with tempfile.TemporaryDirectory() as tmpdir:
            out1 = save_bank(questions, Path(tmpdir) / "bank1", password="pw")
            out2 = save_bank(questions, Path(tmpdir) / "bank2", password="pw")
            assert out1.read_bytes() != out2.read_bytes(), "相同密码两次加密结果应因随机盐而不同"

    def test_compression_reduces_size(self):
        """启用压缩后文件体积应小于未压缩（对文本 JSON 通常 >70% 压缩率）"""
        from med_exam_toolkit.bank import save_bank
        from med_exam_toolkit.models import Question, SubQuestion
        long_text = "这是一道医学考试题目，考查学生对相关医学知识的掌握程度，请认真作答。" * 10
        def _make_big_q():
            sq = SubQuestion(
                text=long_text, answer="A", rate="75%",
                options=[f"{c}.{'选项文字内容'*8}" for c in "ABCDE"],
                discuss=long_text,
            )
            return Question(mode="A1型题", unit="第一章", pkg="ahuyikao.com",
                            cls="测试题库", sub_questions=[sq])
        questions = [_make_big_q() for _ in range(30)]
        with tempfile.TemporaryDirectory() as tmpdir:
            fp_c = save_bank(questions, Path(tmpdir) / "compressed",   compress=True)
            fp_u = save_bank(questions, Path(tmpdir) / "uncompressed", compress=False)
            # 断言必须在 with 块内，临时目录清理后文件消失
            assert fp_c.stat().st_size < fp_u.stat().st_size, (
                f"压缩后 {fp_c.stat().st_size}B 应小于未压缩 {fp_u.stat().st_size}B"
            )


# ═══════════════════════════════════════════════════
# 3. SM-2 interval 计算正确性
# ═══════════════════════════════════════════════════

class TestSM2:
    """
    验证 progress._update_sm2 的核心行为：
      - 初次正确 → interval=1, reps=1
      - 第二次正确 → interval=6, reps=2
      - 第三次正确 → interval=round(6 * 2.5)=15, reps=3
      - 答错任意时 → reps=0, interval=1（重置）
      - EF 下界为 1.3
    """

    def _get_sm2_row(self, db_path: Path, fp: str, user_id: str = "_test_"):
        import sqlite3
        with sqlite3.connect(str(db_path)) as c:
            c.row_factory = sqlite3.Row
            return c.execute(
                "SELECT ef, interval, reps, next_due FROM sm2 WHERE user_id=? AND fingerprint=?",
                (user_id, fp),
            ).fetchone()

    def test_first_correct_answer(self):
        """首次答对：interval=1, reps=1, EF 不低于初始值
        SM-2 公式：quality=4 时 EF 恰好持平 2.5（+0.1 - 0.10 = 0）。
        """
        from med_exam_toolkit.progress import init_db, record_session
        from datetime import date
        with tempfile.TemporaryDirectory() as tmpdir:
            db = Path(tmpdir) / "test.db"
            init_db(db)
            record_session(db, {
                "id": "sess-1",
                "mode": "practice",
                "total": 1, "correct": 1, "wrong": 0, "skip": 0,
                "time_sec": 10,
                "items": [{"fingerprint": "fp001", "result": 1, "mode": "A1型题", "unit": "第一章"}],
            }, user_id="_test_")
            row = self._get_sm2_row(db, "fp001")
        assert row is not None
        assert row["reps"] == 1
        assert row["interval"] == 1
        # quality=4: ef_new = 2.5 + 0.1 - (5-4)*(0.08+(5-4)*0.02) = 2.5+0.1-0.10 = 2.5，持平
        assert abs(row["ef"] - 2.5) < 1e-9

    def test_second_correct_answer(self):
        """连续两次答对：interval 应变为 6"""
        from med_exam_toolkit.progress import init_db, record_session
        with tempfile.TemporaryDirectory() as tmpdir:
            db = Path(tmpdir) / "test.db"
            init_db(db)
            for i, sess_id in enumerate(["sess-1", "sess-2"]):
                record_session(db, {
                    "id": sess_id, "mode": "practice",
                    "total": 1, "correct": 1, "wrong": 0, "skip": 0, "time_sec": 5,
                    "items": [{"fingerprint": "fp002", "result": 1, "mode": "A1型题", "unit": "第一章"}],
                }, user_id="_test_")
            row = self._get_sm2_row(db, "fp002")
        assert row["reps"] == 2
        assert row["interval"] == 6

    def test_wrong_answer_resets(self):
        """先答对两次，再答错一次 → reps=0, interval=1"""
        from med_exam_toolkit.progress import init_db, record_session
        with tempfile.TemporaryDirectory() as tmpdir:
            db = Path(tmpdir) / "test.db"
            init_db(db)
            for sess_id in ["s1", "s2"]:
                record_session(db, {
                    "id": sess_id, "mode": "practice",
                    "total": 1, "correct": 1, "wrong": 0, "skip": 0, "time_sec": 5,
                    "items": [{"fingerprint": "fp003", "result": 1, "mode": "A1型题", "unit": "u"}],
                }, user_id="_test_")
            # 第三次答错
            record_session(db, {
                "id": "s3", "mode": "practice",
                "total": 1, "correct": 0, "wrong": 1, "skip": 0, "time_sec": 5,
                "items": [{"fingerprint": "fp003", "result": 0, "mode": "A1型题", "unit": "u"}],
            }, user_id="_test_")
            row = self._get_sm2_row(db, "fp003")
        assert row["reps"] == 0
        assert row["interval"] == 1

    def test_ef_floor(self):
        """连续多次答错，EF 不应低于 1.3（下界保护）"""
        from med_exam_toolkit.progress import init_db, record_session
        with tempfile.TemporaryDirectory() as tmpdir:
            db = Path(tmpdir) / "test.db"
            init_db(db)
            for i in range(20):
                record_session(db, {
                    "id": f"s{i}", "mode": "practice",
                    "total": 1, "correct": 0, "wrong": 1, "skip": 0, "time_sec": 2,
                    "items": [{"fingerprint": "fp004", "result": 0, "mode": "A1", "unit": "u"}],
                }, user_id="_test_")
            row = self._get_sm2_row(db, "fp004")
        assert row["ef"] >= 1.3

    def test_skip_does_not_create_sm2_record(self):
        """跳过（result=-1）不应写入 SM-2 表"""
        from med_exam_toolkit.progress import init_db, record_session
        import sqlite3
        with tempfile.TemporaryDirectory() as tmpdir:
            db = Path(tmpdir) / "test.db"
            init_db(db)
            record_session(db, {
                "id": "s1", "mode": "practice",
                "total": 1, "correct": 0, "wrong": 0, "skip": 1, "time_sec": 0,
                "items": [{"fingerprint": "fp005", "result": -1, "mode": "A1", "unit": "u"}],
            }, user_id="_test_")
            row = self._get_sm2_row(db, "fp005")
        assert row is None, "跳过的题目不应被写入 SM-2 调度表"

    def test_users_isolated(self):
        """不同用户的 SM-2 记录应完全隔离"""
        from med_exam_toolkit.progress import init_db, record_session
        with tempfile.TemporaryDirectory() as tmpdir:
            db = Path(tmpdir) / "test.db"
            init_db(db)
            # user_a 答对 2 次
            for i in range(2):
                record_session(db, {
                    "id": f"a-{i}", "mode": "practice",
                    "total": 1, "correct": 1, "wrong": 0, "skip": 0, "time_sec": 5,
                    "items": [{"fingerprint": "fp006", "result": 1, "mode": "A1", "unit": "u"}],
                }, user_id="user_a")
            # user_b 从未做过这道题
            row_b = self._get_sm2_row(db, "fp006", user_id="user_b")
            row_a = self._get_sm2_row(db, "fp006", user_id="user_a")
        assert row_b is None
        assert row_a is not None
        assert row_a["reps"] == 2


# ═══════════════════════════════════════════════════
# 4. DEFAULT_PARSER_MAP
# ═══════════════════════════════════════════════════

class TestDefaultParserMap:
    def test_default_parser_map_exists(self):
        """DEFAULT_PARSER_MAP 应可从 parsers 包导入"""
        from med_exam_toolkit.parsers import DEFAULT_PARSER_MAP
        assert isinstance(DEFAULT_PARSER_MAP, dict)
        assert len(DEFAULT_PARSER_MAP) >= 2

    def test_default_parser_map_keys(self):
        """内置两个 App 的包名应都在映射表中"""
        from med_exam_toolkit.parsers import DEFAULT_PARSER_MAP
        assert "com.ahuxueshu" in DEFAULT_PARSER_MAP
        assert "com.yikaobang.yixue" in DEFAULT_PARSER_MAP

    def test_default_parser_map_values_are_registered(self):
        """映射表中的解析器名称都应能被 get_parser 正常获取"""
        from med_exam_toolkit.parsers import DEFAULT_PARSER_MAP, get_parser
        discover()
        for pkg, parser_name in DEFAULT_PARSER_MAP.items():
            parser = get_parser(parser_name)
            assert parser is not None, f"解析器 {parser_name!r}（用于 {pkg!r}）未注册"


if __name__ == "__main__":
    import sys
    pytest.main([__file__, "-v"])
