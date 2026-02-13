// ==================== 配置区 ====================
var APP_NAME = "阿虎医考";
var PKG_NAME = "com.ahuxueshu";
var OUTPUT_DIR = "/sdcard/tests/";
var SKIP_MODES = [];  // ★ 清空：A1/A2、A3/A4、B1 均已适配
var record;
var lastNumb = "";

// ==================== 工具函数 ====================

function hasNullValue(obj) {
    if (obj === null || obj === undefined) {
        return true;
    }
    if (Array.isArray(obj)) {
        if (obj.length === 0) return true;
        return obj.some(hasNullValue);
    }
    if (typeof obj === 'object' && obj !== null) {
        return Object.values(obj).some(hasNullValue);
    }
    return false;
}

function isVisible(element) {
    if (element == null) return false;
    let bounds = element.bounds();
    return bounds.left >= 0 && bounds.right > 0 && bounds.left < device.width;
}

/**
 * 判断元素是否在当前页面内（宽松判断，用于滚动容器内的元素）
 * 只排除左右页的元素，不要求在屏幕可视区域内
 */
function isCurrentPage(element) {
    if (element == null) return false;
    let bounds = element.bounds();
    return bounds.left >= 0 && bounds.right > 0 && bounds.left < device.width;
}

function getFormattedTimestamp() {
    const now = new Date();
    const year = now.getFullYear();
    const month = String(now.getMonth() + 1).padStart(2, '0');
    const day = String(now.getDate()).padStart(2, '0');
    const hours = String(now.getHours()).padStart(2, '0');
    const minutes = String(now.getMinutes()).padStart(2, '0');
    const seconds = String(now.getSeconds()).padStart(2, '0');
    const milliseconds = String(now.getMilliseconds()).padStart(3, '0');
    return year + "-" + month + "-" + day + "-" + hours + "-" + minutes + "-" + seconds + "-" + milliseconds;
}

function shouldSkip(mode) {
    if (mode == null) return false;
    for (var i = 0; i < SKIP_MODES.length; i++) {
        if (mode.indexOf(SKIP_MODES[i]) !== -1) {
            return true;
        }
    }
    return false;
}

/**
 * 按 bottom 值范围筛选（用于A3/A4，元素top被裁剪，bottom有效）
 */
function filterByBottom(arr, lower, upper) {
    var result = [];
    for (var i = 0; i < arr.length; i++) {
        if (arr[i].bottom >= lower && arr[i].bottom < upper) {
            result.push(arr[i]);
        }
    }
    return result;
}

/**
 * 按 top 值范围筛选（用于B1，元素bottom被裁剪，top有效）
 */
function filterByTop(arr, lower, upper) {
    var result = [];
    for (var i = 0; i < arr.length; i++) {
        if (arr[i].top >= lower && arr[i].top < upper) {
            result.push(arr[i]);
        }
    }
    return result;
}

// ==================== 通用数据提取 ====================

function get_cls() {
    let el = id("com.ahuxueshu:id/tk_title_sub_tv").findOne(5000);
    if (el == null) {
        console.log("未找到课程名，尝试重置");
        reset();
        return get_cls();
    }
    let text = el.text();
    console.log("课程: " + text);
    return text;
}

function get_unit() {
    let el = id("com.ahuxueshu:id/section_name").findOne(500);
    if (el != null) {
        console.log("章节: " + el.text());
        return el.text();
    }
    return null;
}

function get_numb() {
    let el = id("com.ahuxueshu:id/section_position").findOne(3000);
    if (el != null) {
        let text = el.text().replace(/\s/g, "");
        console.log("题号: " + text);
        return text;
    }
    return null;
}

/**
 * 检测当前题型
 * 优先级: B1(question_type_b) → A3/A4(test_tv) → A1/A2(question_name_ax)
 */
function getCurrentMode() {
    try {
        // 1. B1型题
        var b1Elements = id("com.ahuxueshu:id/question_type_b").find();
        for (var i = 0; i < b1Elements.length; i++) {
            if (isVisible(b1Elements[i])) {
                var txt = b1Elements[i].text();
                if (txt != null && txt.trim() !== "") {
                    var match = txt.match(/\[(.+?)\]/);
                    if (match) return match[1];
                    return "B1型题";
                }
            }
        }

        // 2. A3/A4型题
        var testTvElements = id("com.ahuxueshu:id/test_tv").find();
        for (var i = 0; i < testTvElements.length; i++) {
            if (isVisible(testTvElements[i])) {
                var txt = testTvElements[i].text();
                if (txt != null) {
                    var match = txt.match(/\[(.+?)\]/);
                    if (match) return match[1];
                }
            }
        }

        // 3. A1/A2型题
        var elements = id("com.ahuxueshu:id/question_name_ax").find();
        for (var i = 0; i < elements.length; i++) {
            if (isVisible(elements[i])) {
                var txt = elements[i].text();
                if (txt != null) {
                    var match = txt.match(/\[(.+?)\]/);
                    if (match) return match[1];
                }
            }
        }
    } catch (e) {
        console.log("获取题型异常: " + e);
    }
    return null;
}

// ==================== A1/A2型题 数据提取 ====================

function get_mode_and_test() {
    let elements = id("com.ahuxueshu:id/question_name_ax").find();
    for (let i = 0; i < elements.length; i++) {
        let el = elements[i];
        if (isVisible(el)) {
            let text = el.text();
            let mode = null;
            let test = text;
            let match = text.match(/\[(.+?)\]\s*/);
            if (match) {
                mode = match[1];
                test = text.replace(match[0], "").trim();
            }
            console.log("题型: " + mode);
            console.log("题目: " + test);
            return { mode: mode, test: test };
        }
    }
    return { mode: null, test: null };
}

function get_option() {
    let options = [];
    let letters = [];
    let contents = [];

    let letterElements = id("com.ahuxueshu:id/option").find();
    let contentElements = id("com.ahuxueshu:id/option_content").find();

    for (let i = 0; i < letterElements.length; i++) {
        if (isVisible(letterElements[i])) {
            letters.push({
                text: letterElements[i].text(),
                y: letterElements[i].bounds().centerY()
            });
        }
    }

    for (let i = 0; i < contentElements.length; i++) {
        if (isVisible(contentElements[i])) {
            contents.push({
                text: contentElements[i].text(),
                y: contentElements[i].bounds().centerY()
            });
        }
    }

    letters.sort(function(a, b) { return a.y - b.y; });
    contents.sort(function(a, b) { return a.y - b.y; });

    for (let i = 0; i < Math.min(letters.length, contents.length); i++) {
        let optionText = letters[i].text + "." + contents[i].text;
        console.log("选项: " + optionText);
        options.push(optionText);
    }

    return options;
}

function get_answer() {
    let elements = id("com.ahuxueshu:id/right_answer_tv").find();
    for (let i = 0; i < elements.length; i++) {
        let el = elements[i];
        if (isVisible(el)) {
            let text = el.text();
            let answer = text.replace("正确答案：", "").replace(/\n/g, "").trim();
            console.log("答案: " + answer);
            return answer;
        }
    }
    return null;
}

function get_discuss() {
    let elements = id("com.ahuxueshu:id/official_analysis_htv").find();
    for (let i = 0; i < elements.length; i++) {
        let el = elements[i];
        if (isVisible(el)) {
            let text = el.text();
            console.log("解析: " + text.substring(0, 60) + "...");
            return text;
        }
    }
    return "";
}

function get_rate() {
    let elements = id("com.ahuxueshu:id/rate_of_all_tv").find();
    for (let i = 0; i < elements.length; i++) {
        let el = elements[i];
        if (isVisible(el)) {
            let text = el.text().split("\n")[0].trim();
            console.log("正确率: " + text);
            return text;
        }
    }
    return "";
}

function get_error_prone() {
    let elements = id("com.ahuxueshu:id/error_prone_tv").find();
    for (let i = 0; i < elements.length; i++) {
        let el = elements[i];
        if (isVisible(el)) {
            let text = el.text().split("\n")[0].trim();
            console.log("易错选项: " + text);
            return text;
        }
    }
    return "";
}

/**
 * 拉取 A1/A2 型题数据
 */
function fetch() {
    console.log("========== 开始拉取 A1/A2 ==========");
    let timestamp = getFormattedTimestamp();
    let modeAndTest = get_mode_and_test();

    let test = {
        name: timestamp,
        pkg: PKG_NAME,
        cls: get_cls(),
        numb: get_numb(),
        unit: get_unit(),
        mode: modeAndTest.mode,
        test: modeAndTest.test,
        option: get_option(),
        answer: get_answer(),
        rate: get_rate(),
        error_prone: get_error_prone(),
        discuss: get_discuss()
    };
    return test;
}

// ==================== A3/A4型题 数据提取 ====================

/**
 * 拉取 A3/A4 型题数据
 *
 * 结构：共用题干(test_tv) + 多个子题(question_name_atf)
 * 分组依据：元素 bounds().bottom 值（A3/A4的top被裁剪，bottom有效）
 *
 * 输出格式:
 * { stem: "共用题干", sub_questions: [{ test, option:[], answer, rate, error_prone, discuss }] }
 */
function fetchA3A4() {
    console.log("========== 开始拉取 A3/A4 ==========");
    var timestamp = getFormattedTimestamp();

    // 1. 题干和题型
    var stemText = "";
    var mode = "A3/A4型题";
    var testTvElements = id("com.ahuxueshu:id/test_tv").find();
    for (var i = 0; i < testTvElements.length; i++) {
        if (isVisible(testTvElements[i])) {
            var text = testTvElements[i].text();
            var match = text.match(/\[(.+?)\]\s*/);
            if (match) {
                mode = match[1];
                stemText = text.replace(match[0], "").trim();
            } else {
                stemText = text.trim();
            }
            console.log("题型: " + mode);
            console.log("题干: " + stemText.substring(0, 80) + "...");
            break;
        }
    }

    // 2. 子题锚点 (question_name_atf)
    var subQAnchors = [];
    var atfElements = id("com.ahuxueshu:id/question_name_atf").find();
    for (var i = 0; i < atfElements.length; i++) {
        if (isCurrentPage(atfElements[i])) {
            var b = atfElements[i].bounds();
            subQAnchors.push({ text: atfElements[i].text(), bottom: b.bottom });
        }
    }
    subQAnchors.sort(function(a, b) { return a.bottom - b.bottom; });
    console.log("子题数量: " + subQAnchors.length);

    // 3. 所有选项对 (option + option_content)
    var allOptionPairs = [];
    var letterEls = id("com.ahuxueshu:id/option").find();
    var contentEls = id("com.ahuxueshu:id/option_content").find();
    var visLetters = [];
    var visContents = [];

    for (var i = 0; i < letterEls.length; i++) {
        if (isCurrentPage(letterEls[i])) {
            var b = letterEls[i].bounds();
            visLetters.push({ text: letterEls[i].text(), bottom: b.bottom });
        }
    }
    for (var i = 0; i < contentEls.length; i++) {
        if (isCurrentPage(contentEls[i])) {
            var b = contentEls[i].bounds();
            visContents.push({ text: contentEls[i].text(), bottom: b.bottom });
        }
    }

    visLetters.sort(function(a, b) { return a.bottom - b.bottom; });
    visContents.sort(function(a, b) { return a.bottom - b.bottom; });

    for (var i = 0; i < Math.min(visLetters.length, visContents.length); i++) {
        allOptionPairs.push({
            text: visLetters[i].text + "." + visContents[i].text,
            bottom: visLetters[i].bottom
        });
    }

    // 4. 所有答案
    var allAnswers = [];
    var ansEls = id("com.ahuxueshu:id/right_answer_tv").find();
    for (var i = 0; i < ansEls.length; i++) {
        if (isCurrentPage(ansEls[i])) {
            var b = ansEls[i].bounds();
            var t = ansEls[i].text().replace("正确答案：", "").replace(/\n/g, "").trim();
            allAnswers.push({ text: t, bottom: b.bottom });
        }
    }
    allAnswers.sort(function(a, b) { return a.bottom - b.bottom; });

    // 5. 所有正确率
    var allRates = [];
    var rateEls = id("com.ahuxueshu:id/rate_of_all_tv").find();
    for (var i = 0; i < rateEls.length; i++) {
        if (isCurrentPage(rateEls[i])) {
            var b = rateEls[i].bounds();
            allRates.push({ text: rateEls[i].text().split("\n")[0].trim(), bottom: b.bottom });
        }
    }
    allRates.sort(function(a, b) { return a.bottom - b.bottom; });

    // 6. 所有易错选项
    var allErrors = [];
    var errEls = id("com.ahuxueshu:id/error_prone_tv").find();
    for (var i = 0; i < errEls.length; i++) {
        if (isCurrentPage(errEls[i])) {
            var b = errEls[i].bounds();
            allErrors.push({ text: errEls[i].text().split("\n")[0].trim(), bottom: b.bottom });
        }
    }
    allErrors.sort(function(a, b) { return a.bottom - b.bottom; });

    // 7. 所有解析 (official_analysis_htv_include — A3/A4专用)
    var allAnalysis = [];
    var anaEls = id("com.ahuxueshu:id/official_analysis_htv_include").find();
    for (var i = 0; i < anaEls.length; i++) {
        if (isCurrentPage(anaEls[i])) {
            var b = anaEls[i].bounds();
            allAnalysis.push({ text: anaEls[i].text(), bottom: b.bottom });
        }
    }
    allAnalysis.sort(function(a, b) { return a.bottom - b.bottom; });

    // 8. 按 bottom 坐标分组
    var subQuestions = [];
    for (var q = 0; q < subQAnchors.length; q++) {
        var lo = subQAnchors[q].bottom;
        var hi = (q + 1 < subQAnchors.length) ? subQAnchors[q + 1].bottom : Infinity;

        var opts = [];
        var rangeOpts = filterByBottom(allOptionPairs, lo, hi);
        for (var j = 0; j < rangeOpts.length; j++) opts.push(rangeOpts[j].text);

        var answer = "";
        var ra = filterByBottom(allAnswers, lo, hi);
        if (ra.length > 0) answer = ra[0].text;

        var rate = "";
        var rr = filterByBottom(allRates, lo, hi);
        if (rr.length > 0) rate = rr[0].text;

        var errorProne = "";
        var re = filterByBottom(allErrors, lo, hi);
        if (re.length > 0) errorProne = re[0].text;

        var discuss = "";
        var rd = filterByBottom(allAnalysis, lo, hi);
        if (rd.length > 0) discuss = rd[0].text;

        var qText = subQAnchors[q].text.replace(/^\(\d+\)\s*/, "").trim();
        console.log("  子题" + (q + 1) + ": " + qText);
        console.log("    选项数: " + opts.length + " | 答案: " + answer + " | 正确率: " + rate);

        subQuestions.push({
            test: qText,
            option: opts,
            answer: answer,
            rate: rate,
            error_prone: errorProne,
            discuss: discuss
        });
    }

    return {
        name: timestamp,
        pkg: PKG_NAME,
        cls: get_cls(),
        numb: get_numb(),
        unit: get_unit(),
        mode: mode,
        stem: stemText,
        sub_questions: subQuestions
    };
}

// ==================== B1型题 数据提取 ====================

/**
 * 拉取 B1 型题数据
 *
 * 结构：共用选项(share_option + share_option_content) + 多个子题(item_question_name)
 * 子题只有字母按钮(option_btn)，答案从共用选项中选择
 * 整体解析在最后(official_analysis_htv)
 *
 * 分组依据：元素 bounds().top 值（B1的bottom被裁剪，top有效）
 *
 * 输出格式:
 * {
 *   shared_options: ["A.根尖周病变...", "B.根尖周病变..."],
 *   sub_questions: [{ test, answer, rate, error_prone }],
 *   discuss: "第1题: ... 第2题: ..."
 * }
 */
function fetchB1() {
    console.log("========== 开始拉取 B1 ==========");
    var timestamp = getFormattedTimestamp();

    // -------- 1. 题型 --------
    var mode = "B1型题";
    var b1TypeElements = id("com.ahuxueshu:id/question_type_b").find();
    for (var i = 0; i < b1TypeElements.length; i++) {
        if (isCurrentPage(b1TypeElements[i])) {
            var txt = b1TypeElements[i].text();
            if (txt) {
                var match = txt.match(/\[(.+?)\]/);
                if (match) mode = match[1];
            }
            break;
        }
    }
    console.log("题型: " + mode);

    // -------- 2. 共用选项 (share_option + share_option_content) --------
    var sharedOptions = [];
    var shareLetterEls = id("com.ahuxueshu:id/share_option").find();
    var shareContentEls = id("com.ahuxueshu:id/share_option_content").find();

    var visShareLetters = [];
    var visShareContents = [];

    for (var i = 0; i < shareLetterEls.length; i++) {
        if (isCurrentPage(shareLetterEls[i])) {
            var b = shareLetterEls[i].bounds();
            visShareLetters.push({ text: shareLetterEls[i].text(), top: b.top });
        }
    }
    for (var i = 0; i < shareContentEls.length; i++) {
        if (isCurrentPage(shareContentEls[i])) {
            var b = shareContentEls[i].bounds();
            visShareContents.push({ text: shareContentEls[i].text(), top: b.top });
        }
    }

    visShareLetters.sort(function(a, b) { return a.top - b.top; });
    visShareContents.sort(function(a, b) { return a.top - b.top; });

    for (var i = 0; i < Math.min(visShareLetters.length, visShareContents.length); i++) {
        var optText = visShareLetters[i].text + "." + visShareContents[i].text;
        console.log("  共用选项: " + optText.substring(0, 60));
        sharedOptions.push(optText);
    }

    // -------- 3. 子题锚点 (item_question_name — B1专用) --------
    var subQAnchors = [];
    var itemQEls = id("com.ahuxueshu:id/item_question_name").find();
    for (var i = 0; i < itemQEls.length; i++) {
        if (isCurrentPage(itemQEls[i])) {
            var b = itemQEls[i].bounds();
            subQAnchors.push({ text: itemQEls[i].text(), top: b.top });
        }
    }
    subQAnchors.sort(function(a, b) { return a.top - b.top; });
    console.log("子题数量: " + subQAnchors.length);

    // -------- 4. 所有答案 (right_answer_tv) --------
    var allAnswers = [];
    var ansEls = id("com.ahuxueshu:id/right_answer_tv").find();
    for (var i = 0; i < ansEls.length; i++) {
        if (isCurrentPage(ansEls[i])) {
            var b = ansEls[i].bounds();
            var t = ansEls[i].text().replace("正确答案：", "").replace(/\n/g, "").trim();
            allAnswers.push({ text: t, top: b.top });
        }
    }
    allAnswers.sort(function(a, b) { return a.top - b.top; });

    // -------- 5. 所有正确率 --------
    var allRates = [];
    var rateEls = id("com.ahuxueshu:id/rate_of_all_tv").find();
    for (var i = 0; i < rateEls.length; i++) {
        if (isCurrentPage(rateEls[i])) {
            var b = rateEls[i].bounds();
            allRates.push({ text: rateEls[i].text().split("\n")[0].trim(), top: b.top });
        }
    }
    allRates.sort(function(a, b) { return a.top - b.top; });

    // -------- 6. 所有易错选项 --------
    var allErrors = [];
    var errEls = id("com.ahuxueshu:id/error_prone_tv").find();
    for (var i = 0; i < errEls.length; i++) {
        if (isCurrentPage(errEls[i])) {
            var b = errEls[i].bounds();
            allErrors.push({ text: errEls[i].text().split("\n")[0].trim(), top: b.top });
        }
    }
    allErrors.sort(function(a, b) { return a.top - b.top; });

    // -------- 7. 整体解析 (official_analysis_htv — B1所有子题合并) --------
    var discuss = "";
    var anaEls = id("com.ahuxueshu:id/official_analysis_htv").find();
    for (var i = 0; i < anaEls.length; i++) {
        if (isCurrentPage(anaEls[i])) {
            discuss = anaEls[i].text();
            console.log("  解析: " + discuss.substring(0, 80) + "...");
            break;
        }
    }

    // -------- 8. 按 top 坐标分组 --------
    var subQuestions = [];
    for (var q = 0; q < subQAnchors.length; q++) {
        var lo = subQAnchors[q].top;
        var hi = (q + 1 < subQAnchors.length) ? subQAnchors[q + 1].top : Infinity;

        // 答案
        var answer = "";
        var ra = filterByTop(allAnswers, lo, hi);
        if (ra.length > 0) answer = ra[0].text;

        // 正确率
        var rate = "";
        var rr = filterByTop(allRates, lo, hi);
        if (rr.length > 0) rate = rr[0].text;

        // 易错选项
        var errorProne = "";
        var re = filterByTop(allErrors, lo, hi);
        if (re.length > 0) errorProne = re[0].text;

        // 清理子题文本
        var qText = subQAnchors[q].text.replace(/^\(\d+\)\s*/, "").trim();

        console.log("  子题" + (q + 1) + ": " + qText);
        console.log("    答案: " + answer + " | 正确率: " + rate + " | 易错: " + errorProne);

        subQuestions.push({
            test: qText,
            answer: answer,
            rate: rate,
            error_prone: errorProne
        });
    }

    return {
        name: timestamp,
        pkg: PKG_NAME,
        cls: get_cls(),
        numb: get_numb(),
        unit: get_unit(),
        mode: mode,
        shared_options: sharedOptions,
        sub_questions: subQuestions,
        discuss: discuss
    };
}

// ==================== 核心逻辑函数 ====================

function savejson(test) {
    var name = OUTPUT_DIR + test.name + ".json";
    record = test;
    lastNumb = test.numb;
    var jsonData = JSON.stringify(test, null, 2);
    files.create(name);
    files.write(name, jsonData);
    console.log("\n####保存成功#### " + test.numb + " [" + test.mode + "]\n\n");
    return jsonData;
}

function closeAd() {
    if (id("close").exists()) {
        console.log("检测到广告(id:close)，关闭");
        id("close").findOne(500).click();
        sleep(500);
        return true;
    }
    if (id("iv_close").exists()) {
        console.log("检测到广告(id:iv_close)，关闭");
        id("iv_close").findOne(500).click();
        sleep(500);
        return true;
    }
    if (text("关闭").exists()) {
        console.log("检测到广告(text:关闭)，关闭");
        text("关闭").findOne(500).click();
        sleep(500);
        return true;
    }
    if (text("跳过").exists()) {
        console.log("检测到广告(text:跳过)，关闭");
        text("跳过").findOne(500).click();
        sleep(500);
        return true;
    }
    return false;
}

function forceStop() {
    console.log("强制停止App");
    app.openAppSetting(PKG_NAME);
    sleep(1000);
    let stopBtn = text("强行停止").findOne(3000);
    if (stopBtn == null) stopBtn = text("结束运行").findOne(2000);
    if (stopBtn == null) stopBtn = text("强制停止").findOne(2000);
    if (stopBtn != null) {
        stopBtn.click();
        sleep(1000);
        let confirmBtn = text("确定").findOne(3000);
        if (confirmBtn == null) confirmBtn = text("确认").findOne(2000);
        if (confirmBtn != null) confirmBtn.click();
    }
    sleep(2000);
}

function sim_click(targetText) {
    let target = textContains(targetText).findOne(250);
    while (target == null) {
        swipe(700, 2000, 700, 1800, 500);
        sleep(250);
        target = textContains(targetText).findOne(250);
    }
    let targetBounds = target.parent().bounds();
    console.log("找到: " + targetText + " Y:" + targetBounds.centerY());
    while (targetBounds.centerY() > device.height - 200) {
        swipe(700, 2000, 700, 1600, 1000);
        targetBounds = textContains(targetText).findOne().parent().bounds();
        sleep(500);
    }
    sleep(1000);
    click(targetBounds.centerX(), targetBounds.centerY());
}

function reset() {
    console.log("========== 开始重置 ==========");
    closeAd();
    forceStop();
    app.launchApp(APP_NAME);
    let waitCount = 0;
    while (!id("com.ahuxueshu:id/tk_title_sub_tv").exists() && waitCount < 30) {
        sleep(1000);
        closeAd();
        waitCount++;
        if (text("点击重试").exists()) text("点击重试").findOne().click();
        if (text("点击刷新").exists()) text("点击刷新").findOne().click();
    }
    sleep(3000);
    closeAd();
    if (record != null) {
        console.log("尝试恢复到: " + record.cls + " > " + record.unit + " > " + record.numb);
        sleep(2000);
        closeAd();
        try { sim_click(record.cls); sleep(2000); closeAd(); } catch (e) { console.log("导航课程失败: " + e); }
        try { sim_click(record.unit); sleep(2000); closeAd(); } catch (e) { console.log("导航章节失败: " + e); }
        try {
            let currentNum = record.numb.split("/")[0];
            console.log("恢复到题号: " + currentNum);
            sleep(3000);
            sim_click(currentNum);
            sleep(3000);
        } catch (e) { console.log("导航题号失败: " + e); }
    }
    console.log("========== 重置完成 ==========");
}

function checkMode() {
    let beitiTab = text("背题").findOne(500);
    if (beitiTab != null) {
        if (!id("com.ahuxueshu:id/right_answer_tv").exists()) {
            console.log("切换到背题模式");
            beitiTab.click();
            sleep(2000);
        }
    }
}

// ==================== 主循环 ====================

function swipeNext() {
    swipe(1000, 1200, 200, 1200, 250);
    sleep(800);
}

/**
 * 等待页面加载完成
 * 支持: A1/A2(question_name_ax) | A3/A4(test_tv) | B1(question_type_b)
 */
function waitForPage(timeout) {
    timeout = timeout || 5000;
    let start = Date.now();
    while (Date.now() - start < timeout) {
        // A1/A2
        let ax = id("com.ahuxueshu:id/question_name_ax").find();
        for (let i = 0; i < ax.length; i++) {
            if (isVisible(ax[i])) { sleep(300); return true; }
        }
        // A3/A4
        let tv = id("com.ahuxueshu:id/test_tv").find();
        for (let i = 0; i < tv.length; i++) {
            if (isVisible(tv[i])) { sleep(300); return true; }
        }
        // B1
        let b1 = id("com.ahuxueshu:id/question_type_b").find();
        for (let i = 0; i < b1.length; i++) {
            if (isVisible(b1[i])) { sleep(300); return true; }
        }
        sleep(300);
    }
    return false;
}

/**
 * 判断题型类别
 * 返回: "A1" | "A3A4" | "B1" | "SKIP" | "UNKNOWN"
 */
function classifyMode(mode) {
    if (mode == null) return "UNKNOWN";
    if (shouldSkip(mode)) return "SKIP";
    if (mode.indexOf("B1") !== -1) return "B1";
    if (mode.indexOf("A3") !== -1 || mode.indexOf("A4") !== -1) return "A3A4";
    return "A1";  // A1/A2 及其他未知类型都走A1逻辑
}

/**
 * 根据题型类别调用对应的拉取函数
 */
function fetchByType(typeClass) {
    switch (typeClass) {
        case "B1":   return fetchB1();
        case "A3A4": return fetchA3A4();
        case "A1":   return fetch();
        default:     return null;
    }
}

/**
 * 打印数据缺失字段（支持所有题型的嵌套结构）
 */
function printMissingFields(json) {
    if (json == null) {
        console.log("  ✗ json = null");
        return;
    }
    for (var key in json) {
        var val = json[key];
        if (val === null || val === undefined) {
            console.log("  ✗ " + key + " = null");
        } else if (Array.isArray(val) && val.length === 0) {
            console.log("  ✗ " + key + " = [] (空数组)");
        } else if (key === "sub_questions" && Array.isArray(val)) {
            for (var sq = 0; sq < val.length; sq++) {
                var subQ = val[sq];
                for (var sk in subQ) {
                    if (subQ[sk] === null || subQ[sk] === undefined) {
                        console.log("  ✗ sub_questions[" + sq + "]." + sk + " = null");
                    } else if (Array.isArray(subQ[sk]) && subQ[sk].length === 0) {
                        console.log("  ✗ sub_questions[" + sq + "]." + sk + " = [] (空数组)");
                    }
                }
            }
        }
    }
}

function main() {
    console.log("========== 脚本启动 ==========");
    console.log("设备宽度: " + device.width + " 高度: " + device.height);
    setScreenMetrics(1200, 2670);   //设置分辨率，解决分辨率不同的问题
    sleep(3000);
    closeAd();
    checkMode();
    sleep(1000);

    var failCount = 0;
    var maxFail = 5;
    var savedCount = 0;
    var lastUnit = "";
    var stuckCount = 0;

    // ★ 统计各题型保存数量
    var stats = { "A1": 0, "A3A4": 0, "B1": 0, "SKIP": 0 };

    for (var i = 0; i < 10000; i++) {
        console.log("\n---------- 第 " + (i + 1) + " 轮 | 已保存: " + savedCount
            + " (A1:" + stats["A1"] + " A3/A4:" + stats["A3A4"] + " B1:" + stats["B1"]
            + " 跳过:" + stats["SKIP"] + ") ----------");

        // 1. 处理广告弹窗
        closeAd();

        // 2. 等待页面加载（支持所有题型）
        if (!waitForPage(5000)) {
            console.log("页面未加载，等待中...");
            sleep(2000);
            closeAd();
            if (!waitForPage(5000)) {
                failCount++;
                console.log("连续失败: " + failCount + "/" + maxFail);
                if (failCount >= maxFail) {
                    console.log("连续失败过多，重置App");
                    reset();
                    checkMode();
                    failCount = 0;
                }
                continue;
            }
        }

        // 3. 获取当前题号和章节
        var currentNumb = get_numb();
        var currentUnit = get_unit();

        // 4. 检测章节切换
        if (currentUnit != null && lastUnit !== "" && currentUnit !== lastUnit) {
            console.log("★★★ 章节切换: " + lastUnit + " → " + currentUnit + " ★★★");
            lastNumb = "";
            checkMode();
            sleep(500);
        }

        // 5. 去重：同一章节内题号相同则跳过
        if (currentNumb != null && currentNumb === lastNumb && currentUnit === lastUnit) {
            stuckCount++;
            console.log("题号未变化(" + currentNumb + ")，卡住次数: " + stuckCount);

            if (stuckCount >= 3) {
                console.log("多次卡住，检查是否已到末尾...");
                swipe(1050, 1200, 100, 1200, 200);
                sleep(1500);

                var recheckNumb = get_numb();
                var recheckUnit = get_unit();
                if (recheckNumb === currentNumb && recheckUnit === currentUnit) {
                    console.log("确认已到达全部题目末尾，脚本结束");
                    break;
                } else {
                    console.log("强力滑动后有变化，继续");
                    stuckCount = 0;
                }
            } else {
                swipeNext();
            }
            continue;
        }
        stuckCount = 0;

        // 6. ★★★ 检测题型 ★★★
        var currentMode = getCurrentMode();
        var typeClass = classifyMode(currentMode);
        console.log("当前题型: " + currentMode + " → 类别: " + typeClass);

        // 7. 跳过未适配的题型
        if (typeClass === "SKIP") {
            console.log("⏭ 跳过[" + currentMode + "]: " + currentNumb);
            stats["SKIP"]++;
            lastNumb = currentNumb;
            lastUnit = currentUnit || "";
            swipeNext();
            continue;
        }

        // 8. 未知题型处理
        if (typeClass === "UNKNOWN") {
            console.log("⚠ 未识别题型，尝试按A1处理");
            typeClass = "A1";
        }

        // 9. ★★★ 根据题型拉取数据 ★★★
        var json = null;
        try {
            json = fetchByType(typeClass);
        } catch (e) {
            console.log("拉取数据异常: " + e);
            failCount++;
            swipeNext();
            continue;
        }

        // 10. 校验并保存
        if (json == null || hasNullValue(json)) {
            console.log("数据不完整，打印缺失字段:");
            printMissingFields(json);

            // 等待后重试一次
            console.log("等待2秒后重试...");
            sleep(2000);
            try {
                json = fetchByType(typeClass);
            } catch (e) {
                console.log("重试拉取异常: " + e);
            }

            if (json != null && !hasNullValue(json)) {
                savejson(json);
                savedCount++;
                stats[typeClass]++;
                failCount = 0;
                lastUnit = json.unit || "";
            } else {
                console.log("重试后仍不完整，跳过此题");
                printMissingFields(json);
                failCount++;
                lastNumb = currentNumb;
                lastUnit = currentUnit || "";
                if (failCount >= maxFail) {
                    console.log("连续失败过多，重置App");
                    reset();
                    checkMode();
                    failCount = 0;
                    continue;
                }
            }
        } else {
            savejson(json);
            savedCount++;
            stats[typeClass]++;
            failCount = 0;
            lastUnit = json.unit || "";
        }

        // 11. 左滑到下一题
        swipeNext();
        sleep(300);
    }

    console.log("\n========== 脚本结束 ==========");
    console.log("共保存题目: " + savedCount);
    console.log("  A1/A2: " + stats["A1"]);
    console.log("  A3/A4: " + stats["A3A4"]);
    console.log("  B1:    " + stats["B1"]);
    console.log("  跳过:  " + stats["SKIP"]);
}

// ==================== 入口 ====================
files.createWithDirs(OUTPUT_DIR + "placeholder");
files.remove(OUTPUT_DIR + "placeholder");

console.log("启动App: " + APP_NAME);
app.launchApp(APP_NAME);

sleep(5000);
closeAd();
sleep(1000);
closeAd();

main();