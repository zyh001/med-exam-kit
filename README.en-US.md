# Medical Exam Question Bank Scraper & Processing Tool

This project is a complete automated solution for medical exam question banks, consisting of two main parts:

1. **Scraper**: Uses AutoX.js scripts to automatically extract question data from medical exam Apps (such as Ahu Medical Exam, Yikao Bang).
2. **Post-processing**: Uses Python programs to deduplicate, format, and export the scraped JSON data.

With this tool, you can efficiently organize medical exam questions from your phone into multiple formats such as Excel, Word, and PDF, and can also automatically generate exam papers for convenient study and review.

## Features

- **Automated Scraping**: No need for manual copy-pasting, saving a lot of time.
- **Multi-platform Support**: Supports multiple medical exam Apps like Ahu Medical Exam, Yikao Bang, etc.
- **Smart Deduplication**: Automatically identifies and removes duplicate questions, ensuring question bank quality.
- **Multi-format Export**: Supports exporting to XLSX, CSV, DOCX, PDF, and other common formats.
- **Data Statistics**: Provides statistics such as question count and accuracy rate.
- **AI Parsing & Completion**: Calls AI large models to automatically complete missing explanations or answers for questions, supporting mainstream models like OpenAI, DeepSeek, Qwen3, and deep thinking modes.
- **Visual Editing**: Built-in local web editor for directly modifying questions, explanations, and batch text replacement in a browser.
- **Smart Quiz Practice**: Built-in local web quiz app supporting three learning modes—Practice Mode, Exam Mode, and Memorization Mode—with an error notebook feature using the SM-2 algorithm for memory scheduling.

## Directory Structure

```
med-exam-kit/
├── js/                 # AutoX.js scraper scripts
│   ├── ah.js           # Ahu Medical Exam scraper script
│   └── ykb.js          # Yikao Bang scraper script
├── src/                # Python post-processing source code
├── data/
│   ├── raw/            # Store raw JSON files scraped
│   └── output/          # Store processed output files
├── config.yaml         # Project configuration file
├── pyproject.toml      # Python project dependency configuration
└── Makefile            # Shortcuts for common commands
```

## Quick Start

For first-time users, it is recommended to read this document in the following order:

1. **Detailed Guide for Users with No Programming Experience**: The most detailed step-by-step instructions, suitable for users with no programming knowledge.
2. **Part One: Using the Scraper Script (AutoX.js)**: Understand the basic principles and operation of the scraper.
3. **Part Two: Post-processing with Python**: Understand the data processing methods and commands.
4. **Advanced Command-line Usage**: [COMMAND.md](COMMAND.md)

## Usage Workflow Overview

The entire usage process is divided into two main steps:

1. **Data Scraping**: Use AutoX.js scripts on an Android device to automatically extract questions and save them as JSON files.
2. **Data Processing**: Use Python programs to read JSON files, deduplicate and convert formats, and finally export files in the desired format.

Next, the specific operation methods for each step will be introduced in detail.

## Part One: Using the Scraper Script (AutoX.js)

This section is used to automatically extract question data from medical exam Apps. You need to install the AutoX.js app on an Android device and configure the relevant scripts.

### Preparation

1. **Install AutoX.js**:
   - Download and install the AutoX.js app on your Android device (search and download on Coolapk, GitHub, etc.).
   - Ensure that the Android device has granted AutoX.js accessibility services and floating window permissions.

2. **Prepare the Target App**:
   - Install the medical exam App you want to scrape (e.g., Ahu Medical Exam, Yikao Bang) on your Android device.
   - Ensure the App already contains the question content you need to scrape.

3. **Transfer Script Files**:
   - Transfer the corresponding script file (`ah.js` or `ykb.js`) from the project's `js/` directory to your Android device.
   - Import the script file in the AutoX.js app.

### Operation Steps

1. **Configure the Script**:
   - Open the AutoX.js app and edit the imported script.
   - Confirm that the `OUTPUT_DIR` variable in the script points to the path where you want to save JSON files (default is `/sdcard/tests/`).
   - Adjust other configuration items as needed (such as `SKIP_MODES`, etc.).

2. **Run the Script**:
   - Enable the script's accessibility service in AutoX.js.
   - Launch the target medical exam App and navigate to the question list you want to scrape.
   - Return to the AutoX.js app and run the script.
   - The script will automatically browse through questions, extract content, and save it as JSON files to the specified directory.

3. **Get the Data**:
   - After the script finishes running, go to the path specified by `OUTPUT_DIR` to view the generated JSON files.
   - Transfer these JSON files to your computer and store them in the project's `data/raw/` directory for subsequent processing.

### Using VSCode for Remote Development (Recommended)

For easier editing and debugging of scripts, you can use VSCode with the AutoX.js plugin for remote development.

#### Preparation

1. **Install VSCode**: Install Visual Studio Code on your computer.
2. **Install AutoX.js Plugin**: Search for and install the "Auto.js-Autox.js-VSCodeExt" plugin in the VSCode Extensions marketplace.
3. **Connect Device**: Ensure your Android device is connected to the computer via USB cable and USB debugging is enabled.

#### Operation Steps

1. **Connect Device**:
   - Open VSCode and find the Auto.js plugin icon in the sidebar.
   - Click the plugin icon and carefully read the plugin usage instructions.
   - Ensure the AutoX.js app is running on your phone.

2. **Edit Scripts**:
   - Open the project folder in VSCode.
   - Directly edit the script files (`ah.js` or `ykb.js`) in the `js/` directory.

3. **Sync and Run**:
   - After editing, use the plugin's sync feature to push the script to the AutoX.js app on the phone.
   - You can run or stop the script directly in VSCode without operating on the phone.

4. **Debug**:
   - You can use VSCode's breakpoint debugging feature to debug scripts.
   - View real-time log output to facilitate troubleshooting.

### Scraper Risk Warnings

When using this tool for data scraping, please be sure to understand and accept the following risks:

#### Legal Compliance Risks
- **Copyright Infringement**: The scraped question content may be copyrighted; unauthorized copying and distribution may constitute infringement.
- **Laws and Regulations**: Please ensure your scraping activities comply with local laws and regulations, and avoid violating laws such as the Copyright Law and Cybersecurity Law.
- **Usage Purpose**: This tool is for personal learning and research only. Please do not use it for commercial purposes or illegal activities.

#### Account Security Risks
- **Account Banning**: The target App may detect abnormal access behavior, leading to your account being restricted or banned.
- **Account Tracking**: Frequent or large-scale scraping may be recorded, increasing the risk of your account being identified and tracked.

#### Technical Risks
- **Data Errors**: Data loss, errors, or incompleteness may occur during the scraping process.
- **Device Failure**: Running scripts for extended periods may cause device overheating, lag, or abnormal restarts.
- **Compatibility Issues**: Updates to the target App may cause scripts to fail, requiring timely adjustments.

### Notes

- Do not manually operate the phone during scraping to avoid interfering with the script.
- Ensure a stable network connection to avoid missing data due to loading issues.
- Some Apps may have anti-scraping mechanisms; if you encounter issues, try reducing the scraping speed or pausing and restarting.
- Please reasonably control scraping frequency to avoid putting excessive pressure on servers.
- It is recommended to regularly back up scraped data to prevent accidental loss.

## Part Two: Post-processing with Python

This section is used to process the scraped JSON files through deduplication, format conversion, etc., and export them into convenient file formats.

### Preparation

1. **Install Python**:
   - Ensure your computer has Python 3.10 or higher installed.
   - You can check by entering `python --version` or `python3 --version` in the command line.

2. **Install Project Dependencies**:
   - Open a command-line tool (Windows users can use CMD or PowerShell, Mac/Linux users can use Terminal).
   - Navigate to the project root directory (the directory containing `pyproject.toml`).
   - Run the following command to install the required Python libraries:
     ```bash
     pip install -e .
     ```
     If you are using Python 3, you may need to use `pip3`:
     ```bash
     pip3 install -e .
     ```

### Operation Steps

1. **Prepare Raw Data**:
   - Ensure you have placed the scraped JSON files into the `data/raw/` directory.

2. **Run the Processing Program**:
   - In the command line, navigate to the project root directory.
   - Use the `med-exam` command to execute different processing tasks.

   **View Data Information**:
   ```bash
   med-exam info
   ```
   This command displays information such as the number of JSON files in the `data/raw/` directory and the distribution of question types.

   **Export to Different Formats**:
   - Export to Excel file:
     ```bash
     med-exam export -f xlsx
     ```
   - Export to CSV file:
     ```bash
     med-exam export -f csv
     ```
   - Export to Word document:
     ```bash
     med-exam export -f docx
     ```
   - Export to PDF file:
     ```bash
     med-exam export -f pdf
     ```
   - Export to database file:
     ```bash
     med-exam export -f db
     ```

   **Export Multiple Formats Simultaneously**:
   ```bash
   med-exam export -f xlsx -f csv -f docx
   ```

   **Export Questions Meeting Specific Conditions**:
   - Export only difficult questions with an accuracy rate below 50%:
     ```bash
     med-exam export -f xlsx --max-rate 50
     ```
   - Export only A1 type questions:
     ```bash
     med-exam export -f xlsx --mode A1
     ```

3. **View Results**:
   - After processing is complete, the exported files will be saved in the `data/output/` directory.
   - You can open these files to view and use them.

### Configuration Description

- The project's processing behavior can be configured via the `config.yaml` file, for example, modifying input/output directories, adjusting deduplication strategies, etc.
- If you are not familiar with the YAML format, please modify this file with caution to avoid affecting normal program operation.

## Part Three: Quiz Practice

This project includes a built-in local web quiz application supporting three learning modes to help you efficiently review medical exam questions.

### Starting the Quiz Application

First, you need to build or prepare a question bank file in `.mqb` format:

```bash
# Build question bank (from JSON files)
med-exam build -i data/raw -o data/output/questions

# Start the quiz application
med-exam quiz --bank data/output/questions.mqb
```

After starting, it will automatically open `http://127.0.0.1:5174` in your browser.

### Command Parameters

```bash
med-exam quiz --bank <question-bank-path> [options]

Options:
  --password    Question bank password (if the bank is encrypted)
  --port        Local port (default 5174)
  --host        Listen address (default 127.0.0.1)
  --no-browser  Do not automatically open browser
```

### Three Learning Modes

#### 1. Practice Mode

- **Features**: Immediate feedback, learn as you go
- **Interaction**: Shows correct/incorrect status and detailed explanations immediately after selecting an answer
- **Applicable Scenarios**: Daily study and knowledge consolidation
- **Keyboard Shortcuts**: Number keys 1-5 to select answers, arrow keys ← → to switch questions

#### 2. Exam Mode

- **Features**: Timed, simulating a formal exam environment
- **Functions**:
  - Supports custom exam duration (60/90/120/150 minutes)
  - Question mini-map navigation for quick jumping to any question
  - Supports marking feature for easy review of difficult questions
  - Shows results and explanations uniformly after submission
- **Applicable Scenarios**: Simulating real exam environments to check learning outcomes

#### 3. Memorization Mode

- **Features**: Card-flip style learning to reinforce memory
- **Interaction**: See the question and think first, click to reveal the answer, then self-assess mastery level
- **Functions**:
  - Supports self-assessment mechanism of "Mastered" and "Practice Again"
  - Intelligently repeats un mastered questions
  - Swipe left/right for self-assessment (mobile devices)
- **Applicable Scenarios**: Pre-exam sprint and intensive memory reinforcement for key questions

### Feature Highlights

- **Multi-device Adaptation**: Responsive design supporting desktop and mobile devices
- **Theme Switching**: Supports dark/light themes to protect eyesight
- **Question Filtering**: Filter practice by chapter and question type
- **Progress Statistics**: Real-time display of answering progress and accuracy rate
- **Touch Optimization**: Gesture operations like swipe to change questions on mobile devices
- **Keyboard Shortcuts**: Keyboard shortcut operations on desktop

### Keyboard Shortcuts

| Key | Function |
|------|------|
| `1`–`5` / `A`–`E` | Select corresponding option |
| `←` / `→` | Previous question / Next question |
| `M` / `Space` | Mark / Unmark current question |
| `E` | Expand / Jump to explanation area |
| `Enter` | Next question |
| `Esc` | Close popup |

## Download Pre-compiled Version (No Python Environment Required)

Download the latest version from [GitHub Releases](https://github.com/zyh001/med-exam-kit/releases) and double-click to use:

| Platform | Filename |
|------|--------|
| Windows | `med-exam-kit-vX.X.X-py-windows.exe` |
| macOS | `med-exam-kit-vX.X.X-py-macos` |
| Linux | `med-exam-kit-vX.X.X-py-linux` |

```bash
# macOS / Linux first use: Grant execution permission
chmod +x med-exam-kit-*-macos

# macOS also needs to bypass Gatekeeper: System Settings → Privacy & Security → "Open Anyway"

# Usage is identical to the pip-installed version, replace med-exam with the downloaded filename
./med-exam-kit-v1.6.1-py-linux quiz -b 内科学.mqb
```

---

## Configuration File

Create `config.yaml` in the working directory. After that, you only need `med-exam quiz -b xxx.mqb` without entering parameters each time:

```yaml
# config.yaml

ai:
  provider:  deepseek       # openai / deepseek / qwen / kimi / ollama
  model:     ""             # Leave empty to use provider's default model
  api_key:   "sk-xxx"       # API key
  base_url:  ""             # Custom API endpoint (leave empty for official)

asr:
  api_key:   "sk-xxx"       # Alibaba Cloud DashScope, for AI Q&A voice input
  model:     ""             # Leave empty to use qwen3-asr-flash

# S3 image storage (optional, compatible with MinIO / RustFS)
# Required for uploading images to question stems/explanations in the question bank editor
s3_endpoint:   "http://localhost:9000"
s3_bucket:     "med-images"
s3_access_key: "minioadmin"
s3_secret_key: "minioadmin"
```

---

## Detailed Quiz Application Instructions

### Starting Methods

```bash
# Single question bank
med-exam quiz -b 内科学.mqb

# Load multiple question banks simultaneously
med-exam quiz -b 内科.mqb -b 外科.mqb

# Open on local network (accessible from mobile phones too)
med-exam quiz -b 内科.mqb --host 0.0.0.0

# Server deployment (do not auto-open browser)
med-exam quiz -b 内科.mqb --host 0.0.0.0 --port 8080 --no-browser

# With AI Q&A
med-exam quiz -b 内科.mqb --ai-provider deepseek --ai-key sk-xxx

# With voice input (Alibaba Cloud DashScope)
med-exam quiz -b 内科.mqb --asr-key sk-xxx

# Custom access code
med-exam quiz -b 内科.mqb --pin 12345678
```

### Keyboard Shortcuts

| Key | Function |
|------|------|
| `1`–`5` / `A`–`E` | Select corresponding option |
| `←` / `→` | Previous question / Next question |
| `M` / `Space` | Mark / Unmark current question |
| `E` | Expand / Jump to explanation area |
| `Enter` | Next question |
| `Esc` | Close popup |

### AI Q&A

After configuring `ai.api_key`, an "AI Explain" button appears at the bottom-right corner of each question. It supports multi-round follow-up questions, and flowcharts in AI replies are automatically rendered.

### Voice Input

After configuring `asr.api_key`, long-pressing the send button in the AI Q&A input field triggers voice recognition, transcribing speech to the input field in real time.

### Question Bank Editor

```bash
med-exam edit --bank 内科.mqb
```

Edit questions, explanations, and answers directly in a browser, supporting batch text replacement, AI completion, and image uploads to S3 (enabled after configuration).

### PWA Installation

- **Android / Chrome**: "Install to Home Screen" icon on the right side of the address bar
- **iPhone / Safari**: "Share" at the bottom → "Add to Home Screen"

---

## All Commands Quick Reference

```bash
med-exam quiz      -b FILE [...]   # Quiz web application
med-exam edit      --bank FILE     # Question bank editor
med-exam build     -i DIR -o PATH  # Build .mqb question bank from JSON
med-exam export    -b FILE -f FMT  # Export to xlsx/docx/pdf/csv/json/db
med-exam enrich    -b FILE         # AI batch completion of missing answers/explanations
med-exam generate  ...              # Randomly generate and export Word exam papers
med-exam info      -b FILE         # View question bank statistics
med-exam inspect   -b FILE         # Browse question bank content question by question
med-exam migrate   --bank FILE     # Migrate old MQB1 format to MQB2

# Add --help to any command to view detailed parameters
med-exam quiz --help
```

---

## Detailed Guide for Users with No Programming Experience

Don't worry if you are not familiar with programming! Here is a more detailed step-by-step guide to help you complete the entire process smoothly.

### Step One: Preparation

1. **Download Project Files**:
   - Visit the project's code repository (if on GitHub or similar platforms) and download the entire project compressed package to your computer.
   - Extract the compressed package to an easy-to-find folder, such as the Desktop.

2. **Install Necessary Software**:
   - **Python**: Visit the [Python Official Website](https://www.python.org/downloads/) to download and install the latest version of Python. Make sure to check "Add Python to PATH" during installation.
   - **AutoX.js**: On your Android phone, visit [AutoX.js GitHub page](https://github.com/aiselp/AutoX) to download and install the AutoX.js app.

3. **Prepare the Medical Exam App**:
   - Install the App you want to scrape questions from on your Android phone (e.g., Ahu Medical Exam, Yikao Bang).
   - Ensure the App contains the question content you need (you may need to purchase or unlock the relevant courses).

### Step Two: Use Phone to Scrape Questions

1. **Transfer Scripts**:
   - Send the `ah.js` (for Ahu Medical Exam) or `ykb.js` (for Yikao Bang) file from the `js` directory of the extracted project folder to your phone (via WeChat, QQ, email, etc.).
   - Open the AutoX.js app on your phone, tap the "Scripts" or similar tab, then tap the "+" or "Import" button, and select the `.js` file you just sent to your phone.

2. **Set Permissions**:
   - In your phone's system settings, find "App Management" or "Permission Management", and grant AutoX.js "Accessibility Service" and "Floating Window" permissions.

3. **Run the Scraper**:
   - Open the target medical exam App and navigate to the question list you want to scrape.
   - Switch to the AutoX.js app, tap the script you just imported, and then tap "Run".
   - The phone will automatically start flipping through pages and extracting content. Do not manually operate the phone until the script finishes.

4. **Get JSON Files**:
   - After the script finishes, multiple `.json` files will be generated in the `/sdcard/tests/` directory on the phone.
   - Use your phone's built-in file manager or other file management apps to find these files and send them to your computer (via WeChat, QQ, email, or USB cable).
   - Copy all these JSON files to the `data/raw/` directory in the project folder you previously extracted on your computer.

### Step Three: Process Data on Computer

1. **Open Command Line**:
   - **Windows Users**: Hold Shift while right-clicking in an empty area of the project folder, and select "Open PowerShell window here" or "Open command window here".
   - **Mac/Linux Users**: Open the Terminal application and use the `cd` command to navigate to the project folder path.

2. **Install Dependencies**:
   - In the opened command line window, enter the following command and press Enter to execute:
     ```bash
     pip install -e .
     ```
     If it prompts that `pip` cannot be found, try using `pip3`:
     ```bash
     pip3 install -e .
     ```

3. **Execute Export Command**:
   - In the command line, enter the export command you need, for example, to export an Excel file:
     ```bash
     med-exam export -f xlsx
     ```
   - Press Enter to execute and wait for processing to complete.

4. **View Results**:
   - After processing is complete, go to the `data/output/` directory in the project folder. You will see the generated Excel or other format files.
   - Double-click to open the files and view the medical exam questions you scraped and processed.

### Frequently Asked Questions

- **What if the command line cannot find the `med-exam` command?**
  - Make sure you have executed the `pip install -e .` command in the project root directory.
  - Make sure you are executing the export command in the same command-line window and have not changed directories.

- **What if the Python version is too low?**
  - Please upgrade to Python 3.10 or higher.

- **What if JSON files cannot be recognized?**
  - Check if there are `.json` files in the `data/raw/` directory.
  - Confirm that the JSON file content is complete and not corrupted.

- **What if the exported file is empty?**
  - Check if the JSON files in the `data/raw/` directory contain valid data.

### Acknowledgments

 * This script was inspired by [https://github.com/wjixiang/ykb_copyer](https://github.com/wjixiang/ykb_copyer)
