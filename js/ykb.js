// ==================== 配置区 ====================
var APP_NAME = "医考帮";
var PKG_NAME = "com.yikaobang.yixue";
var record = null;
var lastNumb = "";
var lastUnit = "";

// ==================== 工具函数 ====================

/**
 * 在屏幕中下部向下滚动（加载子题的答案/考点/解析）
 */
function scrollContentDown() {
    var x = Math.floor(device.width / 2);
    var startY = Math.floor(device.height * 0.82);
    var endY = Math.floor(device.height * 0.42);
    swipe(x, startY, x, endY, 400);
    sleep(400);
}

/**
 * 在屏幕中下部向上滚动（回到 Tab 区域）
 */
function scrollContentUp() {
    var x = Math.floor(device.width / 2);
    var startY = Math.floor(device.height * 0.42);
    var endY = Math.floor(device.height * 0.82);
    swipe(x, startY, x, endY, 400);
    sleep(400);
}

/**
 * 滚动加载子题全部内容，然后滚回顶部
 */
function scrollToLoadContent() {
    // 向下滚 2 次，确保答案/考点/解析加载
    scrollContentDown();
    scrollContentDown();
    // 滚回顶部，确保 Tab 可点击
    scrollContentUp();
    scrollContentUp();
    scrollContentUp(); // 多滚一次确保回到顶部
    sleep(300);
}

function hasNullValue(obj) {
    if (obj === null || obj === undefined) return true;
    if (typeof obj === 'string') return false;
    if (typeof obj === 'number') return false;
    if (Array.isArray(obj)) {
        if (obj.length === 0) return true;
        return obj.some(hasNullValue);
    }
    if (typeof obj === 'object') {
        return Object.values(obj).some(hasNullValue);
    }
    return false;
}

/**
 * A3/A4 和案例分析的校验（允许 point/discuss 为空字符串）
 */
function validateMultiSub(test) {
    if (test == null) return false;
    if (!test.cls || !test.numb || !test.mode || !test.test) return false;
    if (!test.sub_questions || test.sub_questions.length === 0) return false;

    for (var i = 0; i < test.sub_questions.length; i++) {
        var sq = test.sub_questions[i];
        if (!sq.sub_numb) return false;
        if (!sq.sub_test) return false;
        if (!sq.option || sq.option.length === 0) return false;
        if (!sq.answer) return false;
        // point 和 discuss 允许为空字符串
    }
    return true;
}

function getFormattedTimestamp() {
    var now = new Date();
    var y = now.getFullYear();
    var mo = String(now.getMonth() + 1).padStart(2, '0');
    var d = String(now.getDate()).padStart(2, '0');
    var h = String(now.getHours()).padStart(2, '0');
    var mi = String(now.getMinutes()).padStart(2, '0');
    var s = String(now.getSeconds()).padStart(2, '0');
    var ms = String(now.getMilliseconds()).padStart(3, '0');
    return y + "-" + mo + "-" + d + "-" + h + "-" + mi + "-" + s + "-" + ms;
}

// ==================== 核心：获取当前可见页面的容器 ====================

function getVisibleFrame() {
    var qlistviews = id("com.yikaobang.yixue:id/qlistview").find();
    if (qlistviews.length === 0) return null;

    var candidates = [];
    for (var i = 0; i < qlistviews.length; i++) {
        var frame = qlistviews[i].parent();
        if (frame == null) continue;

        var bounds = frame.bounds();
        var centerX = (bounds.left + bounds.right) / 2;
        candidates.push({
            element: frame,
            centerX: centerX,
            dist: Math.abs(centerX - device.width / 2)
        });
    }

    if (candidates.length === 0) return null;
    candidates.sort(function(a, b) { return a.dist - b.dist; });

    var best = candidates[0];
    if (best.dist > device.width) return null;
    return best.element;
}

// ==================== 全局元素提取（不在 frame 内） ====================

function get_cls() {
    var el = id("com.yikaobang.yixue:id/include_title_center").findOne(5000);
    if (el == null) {
        console.log("未找到课程名，尝试重置");
        reset();
        return get_cls();
    }
    return el.text();
}


function get_unit() {
    var els = id("com.yikaobang.yixue:id/questiondetails_tv_title").find();
    for (var i = 0; i < els.length; i++) {
        var b = els[i].bounds();
        var cx = (b.left + b.right) / 2;
        var h = b.bottom - b.top;
        // 过滤掉高度为0的幽灵元素，且在屏幕内
        if (cx >= 0 && cx < device.width && h > 10) {
            var text = els[i].text();
            if (text != null && text.trim() !== "") return text;
        }
    }
    return null;
}

function get_numb() {
    var els = id("com.yikaobang.yixue:id/pagenumtv").find();
    for (var i = 0; i < els.length; i++) {
        var b = els[i].bounds();
        var cx = (b.left + b.right) / 2;
        if (cx >= 0 && cx < device.width) {
            return els[i].text().replace(/\s/g, "");
        }
    }
    return null;
}

// ==================== frame 内元素提取 ====================

function get_mode() {
    var els = id("com.yikaobang.yixue:id/typeStr").find();
    for (var i = 0; i < els.length; i++) {
        var b = els[i].bounds();
        var cx = (b.left + b.right) / 2;
        if (cx >= 0 && cx < device.width) {
            return els[i].text();
        }
    }
    return null;
}

/**
 * 获取 frame 内所有 titletv，按 Y 排序返回
 * A1/A2: 只有 1 个（题干）
 * A3/A4 & 案例分析: 有 2 个（第1个=共享题干，第2个=子题题干）
 */
function get_all_titletv(frame) {
    var elements = frame.find(id("com.yikaobang.yixue:id/titletv"));
    var items = [];
    for (var i = 0; i < elements.length; i++) {
        var el = elements[i];
        if (el != null && el.text() != null) {
            items.push({
                text: el.text(),
                y: el.bounds().centerY()
            });
        }
    }
    items.sort(function(a, b) { return a.y - b.y; });
    return items;
}

function get_option(frame) {
    var elements = frame.find(id("com.yikaobang.yixue:id/QuestionOptions_item_tv_content"));
    var items = [];
    for (var i = 0; i < elements.length; i++) {
        var el = elements[i];
        if (el != null && el.text() != null) {
            items.push({ text: el.text(), y: el.bounds().centerY() });
        }
    }
    items.sort(function(a, b) { return a.y - b.y; });
    var options = [];
    for (var i = 0; i < items.length; i++) {
        options.push(items[i].text);
    }
    return options;
}

function get_answer(frame) {
    var elements = frame.find(id("com.yikaobang.yixue:id/questiondetails_tv_Answer"));
    for (var i = 0; i < elements.length; i++) {
        var text = elements[i].text();
        if (text != null && text.trim() !== "") {
            return text.replace("答案：", "").trim();
        }
    }
    return null;
}

function get_point(frame) {
    var elements = frame.find(id("com.yikaobang.yixue:id/questiondetails_tv_content_ques1"));
    for (var i = 0; i < elements.length; i++) {
        var text = elements[i].text();
        if (text != null && text.trim() !== "") return text;
    }
    return "";
}

function get_discuss(frame) {
    var elements = frame.find(id("com.yikaobang.yixue:id/questiondetails_tv_contents"));
    for (var i = 0; i < elements.length; i++) {
        var text = elements[i].text();
        if (text != null && text.trim() !== "") return text;
    }
    return "";
}

function get_accuracy(frame) {
    var els = frame.find(id("com.yikaobang.yixue:id/questiondetails_tv_statistics"));
    for (var i = 0; i < els.length; i++) {
        var text = els[i].text();
        if (text) {
            // 取第一个"正确率xx.x%"（全部考生的），忽略后面本人的
            var m = text.match(/正确率(\d+\.?\d*)%/);
            if (m) return m[1] + "%";
        }
    }
    return "";
}

// ==================== 题型检测 ====================

/**
 * 判断当前 frame 是否为多子题类型（A3/A4 或 案例分析）
 * 依据：frame 内存在 tv_column_name（"第1问"、"第2问"...）
 */
function isMultiSubQuestion() {
    var tabs = id("com.yikaobang.yixue:id/tv_column_name").find();
    for (var i = 0; i < tabs.length; i++) {
        var b = tabs[i].bounds();
        // 只要有一个 tab 完全在屏幕内就算多子题
        if (b.left >= 0 && b.right <= device.width && (b.bottom - b.top) > 10) {
            return true;
        }
    }
    return false;
}

/**
 * 获取子题 Tab 列表，按 X 坐标排序
 * 返回: [{text: "第1问", x: 118}, {text: "第2问", x: 350}, ...]
 */
function getSubTabs() {
    var tabs = id("com.yikaobang.yixue:id/tv_column_name").find();
    var result = [];
    for (var i = 0; i < tabs.length; i++) {
        var b = tabs[i].bounds();
        var cx = (b.left + b.right) / 2;
        // 只取屏幕内的 tab
        if (cx >= 0 && cx < device.width && (b.bottom - b.top) > 10) {
            result.push({
                text: tabs[i].text(),
                element: tabs[i],
                x: cx,
                y: (b.top + b.bottom) / 2
            });
        }
    }
    // 按 x 坐标排序（第1问在左，第2问在右...）
    result.sort(function(a, b) { return a.x - b.x; });
    return result;
}

/**
 * 获取 A3/A4 的共享题干（在 Tab 上方的 titletv）
 */
function getSharedStem() {
    var els = id("com.yikaobang.yixue:id/titletv").find();
    for (var i = 0; i < els.length; i++) {
        var b = els[i].bounds();
        var cx = (b.left + b.right) / 2;
        // 共享题干在屏幕内，且 y 位置在 Tab 上方（大约 y < 1050）
        if (cx >= 0 && cx < device.width && b.bottom < 1050) {
            return els[i].text();
        }
    }
    return null;
}

// ==================== A1/A2 拉取 ====================

function fetchA1A2() {
    var frame = getVisibleFrame();
    var numb = get_numb();
    var unit = get_unit();

    if (frame == null) {
        console.log("未找到可见 frame");
        return null;
    }

    console.log("========== 拉取 A1/A2 ==========");
    var timestamp = getFormattedTimestamp();

    var titles = get_all_titletv(frame);
    var testText = titles.length > 0 ? titles[0].text : null;
    var accuracy = get_accuracy(frame);

    var test = {
        name: timestamp,
        pkg: PKG_NAME,
        cls: get_cls(),
        numb: get_numb(),
        unit: get_unit(),
        mode: get_mode(),
        test: testText,
        option: get_option(frame),
        answer: get_answer(frame),
        rate: accuracy,
        point: get_point(frame),
        discuss: get_discuss(frame)
    };

    console.log("  [" + test.mode + "] " + test.numb);
    console.log("  题目: " + (test.test || "").substring(0, 60) + "...");
    console.log("  选项数: " + (test.option ? test.option.length : 0));
    console.log("  答案: " + test.answer);

    return test;
}

// ==================== A3/A4 & 案例分析 拉取 ====================

/**
 * 拉取多子题类型（A3/A4 型题 & 案例分析题）
 *
 * 流程：
 *   1. 提取共享题干（frame 内第 1 个 titletv）
 *   2. 获取所有子题 Tab（"第1问"、"第2问"...）
 *   3. 逐个点击 Tab，提取每个子题的：
 *      - 子题题干（frame 内第 2 个 titletv）
 *      - 选项、答案、考点、解析
 *   4. 组装为 sub_questions 数组
 */
function fetchMultiSub() {
    var frame = getVisibleFrame();
    var numb = get_numb();
    var unit = get_unit();

    if (frame == null) {
        console.log("未找到可见 frame");
        return null;
    }

    console.log("========== 拉取 A3/A4 或 案例分析 ==========");
    var timestamp = getFormattedTimestamp();

    // 1. 全局信息
    var cls = get_cls();
    var numb = get_numb();
    var unit = get_unit();
    var mode = get_mode();

    // 2. 共享题干（第 1 个 titletv）
    var stem = getSharedStem() || "";

    console.log("  [" + mode + "] " + numb);
    console.log("  共享题干: " + stem.substring(0, 60) + "...");

    // 3. 获取子题 Tab
    var tabList = getSubTabs();
    console.log("  子题数: " + tabList.length);

    if (tabList.length === 0) {
        console.log("  ✗ 未找到子题 Tab，回退到 A1/A2 模式");
        return fetchA1A2();
    }

    // 4. 逐个点击 Tab，提取子题数据
    var subQuestions = [];

    for (var t = 0; t < tabList.length; t++) {
        console.log("  --- 切换到: " + tabList[t].text + " ---");

        // 点击 Tab
        clickTab(tabList[t]);
        sleep(800);

        // 滚动加载全部内容，再滚回顶部
        scrollToLoadContent();

        // 重新获取 frame（滚动 + Tab 切换后 UI 树可能刷新）
        frame = getVisibleFrame();
        if (frame == null) {
            console.log("  ✗ 切换后 frame 丢失，跳过 " + tabList[t].text);
            continue;
        }

        // 重新获取 Tab 列表（防止引用失效）
        tabList = getSubTabs();

        // 提取子题题干（第 2 个 titletv）
        var subTitles = get_all_titletv(frame);
        var subTest = "";
        if (subTitles.length >= 2) {
            subTest = subTitles[1].text;
        } else if (subTitles.length === 1) {
            // 可能只有子题题干没有共享题干（极端情况）
            subTest = subTitles[0].text;
        }

        // 提取选项、答案、考点、解析
        var options = get_option(frame);
        var answer = get_answer(frame);
        var point = get_point(frame);
        var discuss = get_discuss(frame);

        console.log("    子题: " + subTest.substring(0, 50) + "...");
        console.log("    选项数: " + options.length);
        console.log("    答案: " + answer);

        subQuestions.push({
            sub_numb: tabList[t].text,
            sub_test: subTest,
            option: options,
            answer: answer,
            point: point,
            rate: get_accuracy(frame),
            discuss: discuss
        });
    }

    // 5. 组装最终 JSON
    var test = {
        name: timestamp,
        pkg: PKG_NAME,
        cls: cls,
        numb: numb,
        unit: unit,
        mode: mode,
        test: stem,
        sub_questions: subQuestions
    };

    console.log("  子题采集完成: " + subQuestions.length + "/" + tabList.length);
    return test;
}

/**
 * 点击子题 Tab
 * 优先用 element.click()，失败则用坐标点击
 */
function clickTab(tab) {
    try {
        // 先尝试 element.click()
        if (tab.element != null) {
            tab.element.click();
            return;
        }
    } catch (e) {
        console.log("    Tab element.click 失败: " + e);
    }

    // 回退：坐标点击
    if (tab.x && tab.y) {
        click(tab.x, tab.y);
    }
}

// ==================== 统一拉取入口 ====================

/**
 * 自动检测题型并拉取
 * - A1/A2 型题 → fetchA1A2()
 * - A3/A4 型题 / 案例分析 → fetchMultiSub()
 */
function fetchQuestion() {
    var frame = getVisibleFrame();
    if (frame == null) {
        console.log("未找到可见 frame");
        return null;
    }

    var mode = get_mode(frame);
    var isMulti = isMultiSubQuestion(frame);

    console.log("  检测题型: " + mode + " | 多子题: " + isMulti);

    if (isMulti) {
        return fetchMultiSub();
    } else {
        return fetchA1A2();
    }
}

// ==================== 统一校验 ====================

/**
 * 根据题目结构选择校验方式
 */
function validateQuestion(test) {
    if (test == null) return false;

    // 多子题结构
    if (test.sub_questions !== undefined) {
        return validateMultiSub(test);
    }

    // A1/A2 结构
    return !hasNullValue(test);
}

// ==================== 保存 ====================

function savejson(test) {
    var name = "/sdcard/tests/" + test.name + ".json";
    record = test;
    lastNumb = test.numb;
    lastUnit = test.unit || "";
    var jsonData = JSON.stringify(test, null, 2);
    files.create(name);
    files.write(name, jsonData);
    console.log("  ✔ 保存成功 " + test.numb);
    return jsonData;
}

// ==================== 广告 / 强制停止 / 重置 ====================

function closeAd() {
    if (id("close").exists()) {
        console.log("  关闭广告");
        id("close").findOne(500).click();
        sleep(500);
        return true;
    }
    if (text("关闭").exists()) {
        text("关闭").findOne(500).click();
        sleep(500);
        return true;
    }
    if (text("跳过").exists()) {
        text("跳过").findOne(500).click();
        sleep(500);
        return true;
    }
    return false;
}

function forceStop() {
    console.log("强制停止 App");
    app.openAppSetting(PKG_NAME);
    sleep(1000);
    var stopBtn = text("强行停止").findOne(3000)
        || text("结束运行").findOne(2000)
        || text("强制停止").findOne(2000);
    if (stopBtn != null) {
        stopBtn.click();
        sleep(1000);
        var confirmBtn = text("确定").findOne(3000) || text("确认").findOne(2000);
        if (confirmBtn != null) confirmBtn.click();
    }
    sleep(3000);
}

function sim_click(targetText) {
    var target = textContains(targetText).findOne(250);
    while (target == null) {
        swipe(700, 2000, 700, 1800, 500);
        sleep(250);
        target = textContains(targetText).findOne(250);
    }
    var targetBounds = target.parent().bounds();

    while (targetBounds.centerY() > device.height - 200) {
        swipe(700, 2000, 700, 1600, 1000);
        targetBounds = textContains(targetText).findOne().parent().bounds();
        sleep(500);
    }

    sleep(1000);
    click(targetBounds.centerX() / 2, targetBounds.centerY());
}

function reset() {
    console.log("========== 开始重置 ==========");
    closeAd();
    forceStop();

    app.launchApp(APP_NAME);

    var waitCount = 0;
    while (!(id("close").exists() || text("错题").exists()) && waitCount < 30) {
        sleep(1000);
        waitCount++;
        if (text("点击刷新页面").exists()) {
            text("点击刷新页面").findOne().click();
        }
    }

    sleep(3000);
    closeAd();
    sleep(1000);

    try {
        text("大三期末").findOne(3000).parent().parent().click();
        console.log("点击横栏项目");
    } catch (e) {
        console.log("点击横栏失败: " + e);
    }

    sleep(5000);
    closeAd();

    if (record != null) {
        console.log("恢复到: " + record.cls + " > " + (record.unit || "") + " > " + record.numb);

        try { sim_click(record.cls); sleep(1000); closeAd(); }
        catch (e) { console.log("导航课程失败: " + e); }

        try { sim_click(record.unit); sleep(1000); closeAd(); }
        catch (e) { console.log("导航章节失败: " + e); }

        try {
            var currentNum = record.numb.split("/")[0];
            console.log("恢复到题号: " + currentNum);
            sleep(3000);
            sim_click(currentNum);
            sleep(3000);
        } catch (e) { console.log("导航题号失败: " + e); }
    }

    console.log("========== 重置完成 ==========");
}

// ==================== 翻页与等待 ====================

function swipeNext() {
    swipe(1000, 1000, 200, 1000, 200);
    sleep(600);
}

function waitForPage(timeout) {
    timeout = timeout || 5000;
    var start = Date.now();
    while (Date.now() - start < timeout) {
        var frame = getVisibleFrame();
        if (frame != null) {
            var titles = frame.find(id("com.yikaobang.yixue:id/titletv"));
            if (titles.length > 0) {
                sleep(300);
                return true;
            }
        }
        sleep(300);
    }
    return false;
}

function handleNextChapter() {
    if (text("进入下一章").exists()) {
        console.log("★ 检测到章节切换 ★");
        text("进入下一章").findOne().click();
        sleep(3000);

        var firstQ = text("1").findOne(5000);
        if (firstQ != null) {
            firstQ.parent().click();
            sleep(1000);
        }

        var newUnit = get_unit();
        if (newUnit != null && newUnit === lastUnit) {
            console.log("!!!章节脱节!!! 重置");
            reset();
        } else {
            lastNumb = "";
        }
        return true;
    }
    return false;
}

// ==================== 主循环 ====================

function main() {
    console.log("========== 脚本启动 ==========");
    console.log("设备: " + device.width + "x" + device.height);
    console.log("支持题型: A1/A2, A3/A4, 案例分析");

    sleep(3000);
    closeAd();
    sleep(1000);

    var failCount = 0;
    var maxFail = 5;
    var savedCount = 0;
    var stuckCount = 0;

    for (var i = 0; i < 10000; i++) {
        console.log("\n---------- 第 " + (i + 1) + " 轮 | 已保存: " + savedCount + " ----------");

        // 1. 广告
        closeAd();

        // 2. 背题模式异常
        if (text("背题模式").exists()) {
            console.log("检测到背题模式异常，重置");
            reset();
            continue;
        }

        // 3. 章节切换
        if (handleNextChapter()) {
            continue;
        }

        // 4. 等待页面
        if (!waitForPage(5000)) {
            console.log("页面未加载");
            sleep(2000);
            closeAd();
            if (!waitForPage(5000)) {
                failCount++;
                console.log("连续失败: " + failCount + "/" + maxFail);
                if (failCount >= maxFail) {
                    reset();
                    failCount = 0;
                }
                swipeNext();
                continue;
            }
        }

        // 5. 获取题号 + 章节用于去重
        var frame = getVisibleFrame();
        var currentNumb = get_numb(frame);
        var currentUnit = get_unit(frame);

        // 章节变化
        if (currentUnit != null && lastUnit !== "" && currentUnit !== lastUnit) {
            console.log("★ 章节切换: " + lastUnit + " → " + currentUnit);
            lastNumb = "";
        }

        // 去重
        if (currentNumb != null && currentNumb === lastNumb
            && (currentUnit || "") === lastUnit) {
            stuckCount++;
            console.log("题号未变(" + currentNumb + ") 卡住: " + stuckCount);

            if (stuckCount >= 3) {
                swipe(1050, 1000, 100, 1000, 150);
                sleep(1500);

                if (handleNextChapter()) {
                    stuckCount = 0;
                    continue;
                }

                var rn = get_numb();
                var ru = get_unit();
                if (rn === currentNumb && (ru || "") === (currentUnit || "")) {
                    console.log("确认到达末尾，脚本结束");
                    break;
                } else {
                    stuckCount = 0;
                }
            } else {
                swipeNext();
            }
            continue;
        }
        stuckCount = 0;

        // 6. 拉取数据（自动检测题型）
        var json = null;
        try {
            json = fetchQuestion();
        } catch (e) {
            console.log("拉取异常: " + e);
            failCount++;
            swipeNext();
            continue;
        }

        // 7. 校验并保存
        if (!validateQuestion(json)) {
            console.log("  ✗ 数据不完整，重试...");

            // 打印缺失信息
            printMissing(json);

            sleep(2000);
            try {
                json = fetchQuestion();
            } catch (e) {
                console.log("重试异常: " + e);
            }

            if (validateQuestion(json)) {
                savejson(json);
                savedCount++;
                failCount = 0;
            } else {
                console.log("  ✗ 重试仍不完整，跳过");
                failCount++;
                if (currentNumb != null) {
                    lastNumb = currentNumb;
                    lastUnit = currentUnit || "";
                }
                if (failCount >= maxFail) {
                    reset();
                    failCount = 0;
                    continue;
                }
            }
        } else {
            savejson(json);
            savedCount++;
            failCount = 0;
        }

        // 8. 翻页
        swipeNext();
        sleep(300);

        // 翻页后检查章节切换
        handleNextChapter();
    }

    console.log("\n========== 脚本结束 ==========");
    console.log("共保存: " + savedCount);
}

/**
 * 打印缺失字段信息（调试用）
 */
function printMissing(json) {
    if (json == null) {
        console.log("    json = null");
        return;
    }

    var keys = Object.keys(json);
    for (var k = 0; k < keys.length; k++) {
        var key = keys[k];
        var val = json[key];
        if (val === null || val === undefined) {
            console.log("    缺失: " + key);
        } else if (Array.isArray(val) && val.length === 0) {
            console.log("    空数组: " + key);
        }
    }

    // 检查子题
    if (json.sub_questions) {
        for (var s = 0; s < json.sub_questions.length; s++) {
            var sq = json.sub_questions[s];
            var sqKeys = Object.keys(sq);
            for (var k = 0; k < sqKeys.length; k++) {
                var key = sqKeys[k];
                var val = sq[key];
                if (val === null || val === undefined) {
                    console.log("    子题[" + s + "] 缺失: " + key);
                } else if (Array.isArray(val) && val.length === 0) {
                    console.log("    子题[" + s + "] 空数组: " + key);
                }
            }
        }
    }
}

// ==================== 入口 ====================
files.createWithDirs("/sdcard/tests/placeholder");
files.remove("/sdcard/tests/placeholder");

console.log("启动: " + APP_NAME);
app.launchApp(APP_NAME);

sleep(5000);
closeAd();
sleep(1000);
closeAd();

main();
