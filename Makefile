.PHONY: install test export clean

install:
	pip install -e .

test:
	python -m pytest tests/ -v

export-xlsx:
	med-exam export -f xlsx

export-all:
	med-exam export -f csv -f xlsx -f docx -f pdf -f db

export-db:
	med-exam export -f db --db-url "sqlite:///data/output/questions.db"

info:
	med-exam info

# 只导出易错题（正确率 < 50%）
export-hard:
	med-exam export -f xlsx --max-rate 50 -o ./data/output/hard

# 按题型导出
export-a1:
	med-exam export -f xlsx --mode A1 -o ./data/output/a1

clean:
	rm -rf data/output/*
	find . -type d -name __pycache__ -exec rm -rf {} +
