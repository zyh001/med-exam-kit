# med-exam-kit Makefile
# Pure Go SQLite (modernc.org/sqlite) - no CGO required.

BINARY  := med-exam
MODULE  := github.com/med-exam-kit/med-exam-kit
LDFLAGS := -ldflags="-s -w"

# ── 默认：本机编译 ─────────────────────────────────────────────────────
.PHONY: build
build:
	go build $(LDFLAGS) -o $(BINARY) .

# ── Windows（在 Windows 上本机编译）──────────────────────────────────
.PHONY: build-windows
build-windows:
	go build $(LDFLAGS) -o $(BINARY).exe .

# ── Linux amd64（在 Linux 或 macOS 上编译）───────────────────────────
# 需要安装 gcc-multilib 或 zig cc 作为 CGo 交叉编译器
.PHONY: build-linux
build-linux:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go build $(LDFLAGS) -o $(BINARY)-linux-amd64 .

# ── macOS arm64（M 系列芯片，在 macOS 上编译）─────────────────────────
.PHONY: build-mac
build-mac:
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 go build $(LDFLAGS) -o $(BINARY)-darwin-arm64 .

# ── 运行测试 ───────────────────────────────────────────────────────────
.PHONY: test
test:
	go test ./internal/...

.PHONY: test-v
test-v:
	go test -v ./internal/...

# ── 复制前端资源（从 Python 版项目，需手动指定路径）─────────────────
# 用法：make copy-assets PYTHON_SRC=../med-exam-kit-main
.PHONY: copy-assets
copy-assets:
	@if [ -z "$(PYTHON_SRC)" ]; then \
		echo "用法: make copy-assets PYTHON_SRC=<Python版项目目录>"; exit 1; fi
	cp -rv $(PYTHON_SRC)/src/med_exam_toolkit/static/   assets/static/
	cp -v  $(PYTHON_SRC)/src/med_exam_toolkit/templates/quiz.html    assets/templates/
	cp -v  $(PYTHON_SRC)/src/med_exam_toolkit/templates/editor.html  assets/templates/ 2>/dev/null || true
	cp -v  $(PYTHON_SRC)/src/med_exam_toolkit/templates/_base.html   assets/templates/ 2>/dev/null || true

.PHONY: clean
clean:
	rm -f $(BINARY) $(BINARY).exe $(BINARY)-linux-amd64 $(BINARY)-darwin-arm64
