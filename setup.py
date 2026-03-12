from setuptools import setup, find_packages

setup(
    name="med-exam-toolkit",
    version="0.1.7-R260312",
    package_dir={"": "src"},
    packages=find_packages(where="src"),
    python_requires=">=3.10",
    install_requires=[
        "click>=8.0",
        "openpyxl>=3.0",
        "python-docx>=0.8",
        "reportlab>=3.6",
        "cryptography>=41.0",
        "sqlalchemy>=2.0",
        "pyyaml>=6.0",
        "openai>=2.0.0",
        "flask>=3.0.0",
        "flask-compress>=1.20",
    ],
    entry_points={
        "console_scripts": [
            "med-exam=med_exam_toolkit.cli:main",
        ],
    },
)
