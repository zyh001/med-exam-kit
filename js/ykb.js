// ==================== 配置区 ====================
var APP_NAME = "医考帮";
var PKG_NAME = "com.yikaobang.yixue";
var OUTPUT_DIR = "/sdcard/tests/";
var PERIODIC_RETURN_INTERVAL = 250;   // 每 N 题返回列表页释放内存（建议 200~350）
var record = null;
var lastNumb = "";
var lastUnit = "";
var currentChapter = "";

// ==================== 核心过滤函数 ====================

/**
 * 在当前页面查找指定 ID 的元素
 * 仅过滤 X 轴：排除左右相邻页面的元素
 * Y 轴不限制：屏幕下方未显示的考点、解析等也能获取到
 */
function findOnScreen(idStr) {
    var els = id(idStr).find();
    var result = [];
    for (var i = 0; i < els.length; i++) {
        var b = els[i].bounds();
        if (b.left >= 0 && b.left < b.right && b.right <= device.width) {
            result.push(els[i]);
        }
    }
    return result;
}

/**
 * 获取屏幕内 Tab 的 Y 坐标范围
 */
function getTabYRange() {
    var tabs = findOnScreen("com.yikaobang.yixue:id/tv_column_name");
    if (tabs.length === 0) return null;
    var top = tabs[0].bounds().top;
    var bottom = tabs[0].bounds().bottom;
    for (var i = 1; i < tabs.length; i++) {
        var b = tabs[i].bounds();
        if (b.top < top) top = b.top;
        if (b.bottom > bottom) bottom = b.bottom;
    }
    return { top: top, bottom: bottom };
}

// ==================== 滑动操作 ====================

function swipeNextSub() {
    swipe(1000, 2200, 200, 2200, 150);
    sleep(300);
}

function swipeNextMain() {
    swipe(1000, 600, 200, 600, 250);
    sleep(300);
}

// ==================== 工具函数 ====================

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

function validateMultiSub(test) {
    if (test == null) return false;
    if (!test.cls || !test.numb || !test.mode || !test.test) return false;
    if (!test.sub_questions || test.sub_questions.length === 0) return false;
    for (var i = 0; i < test.sub_questions.length; i++) {
        var sq = test.sub_questions[i];
        if (!sq.sub_numb || !sq.sub_test) return false;
        if (!sq.option || sq.option.length === 0) return false;
        if (!sq.answer) return false;
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

// ==================== 全局元素提取（屏幕坐标过滤） ====================

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
    var els = findOnScreen("com.yikaobang.yixue:id/questiondetails_tv_title");
    for (var i = 0; i < els.length; i++) {
        var text = els[i].text();
        if (text != null && text.trim() !== "") return text;
    }
    return null;
}

function get_numb() {
    var els = findOnScreen("com.yikaobang.yixue:id/pagenumtv");
    if (els.length > 0) {
        return els[0].text().replace(/\s/g, "");
    }
    return null;
}

function get_mode() {
    var els = findOnScreen("com.yikaobang.yixue:id/typeStr");
    if (els.length > 0) {
        return els[0].text();
    }
    return null;
}

function isMultiSubQuestion() {
    var tabs = findOnScreen("com.yikaobang.yixue:id/tv_column_name");
    return tabs.length > 0;
}

function getSubTabs() {
    var tabs = findOnScreen("com.yikaobang.yixue:id/tv_column_name");
    var result = [];
    for (var i = 0; i < tabs.length; i++) {
        var b = tabs[i].bounds();
        result.push({
            text: tabs[i].text(),
            x: (b.left + b.right) / 2,
            y: (b.top + b.bottom) / 2
        });
    }
    result.sort(function(a, b) { return a.x - b.x; });
    return result;
}

function updateChapter() {
    var title = id("com.yikaobang.yixue:id/txt_actionbar_title").findOne(500);
    if (title != null && title.text() !== "") {
        currentChapter = title.text();
    } else {
        currentChapter = get_unit() || currentChapter;
    }
}

/**
 * 获取共享题干（Tab 上方的 titletv）
 */
function getSharedStem() {
    var tabRange = getTabYRange();
    var dividerY = tabRange ? tabRange.top : 977;

    var els = findOnScreen("com.yikaobang.yixue:id/titletv");
    for (var i = 0; i < els.length; i++) {
        var b = els[i].bounds();
        if (b.top < dividerY && els[i].text()) {
            return els[i].text();
        }
    }
    return null;
}

/**
 * 获取子题题干（Tab 下方的 titletv）
 */
function getSubQuestionStem() {
    var tabRange = getTabYRange();
    var dividerY = tabRange ? tabRange.bottom : 1107;

    var els = findOnScreen("com.yikaobang.yixue:id/titletv");
    for (var i = 0; i < els.length; i++) {
        var b = els[i].bounds();
        if (b.top > dividerY && els[i].text()) {
            return els[i].text();
        }
    }
    return null;
}

/**
 * 获取选项（屏幕内，按 Y 排序）
 */
function get_option() {
    var els = findOnScreen("com.yikaobang.yixue:id/QuestionOptions_item_tv_content");
    var items = [];
    for (var i = 0; i < els.length; i++) {
        if (els[i].text()) {
            items.push({ text: els[i].text(), y: els[i].bounds().centerY() });
        }
    }
    items.sort(function(a, b) { return a.y - b.y; });
    var options = [];
    for (var i = 0; i < items.length; i++) {
        options.push(items[i].text);
    }
    return options;
}

function get_answer() {
    var els = findOnScreen("com.yikaobang.yixue:id/questiondetails_tv_Answer");
    for (var i = 0; i < els.length; i++) {
        var text = els[i].text();
        if (text != null && text.trim() !== "") {
            text = text.replace("答案：", "").trim();
            var m = text.match(/正确答案\s*([A-Z]+)/);
            if (m) return m[1];
            return text;
        }
    }
    return null;
}

function get_point() {
    var els = findOnScreen("com.yikaobang.yixue:id/questiondetails_tv_content_ques1");
    for (var i = 0; i < els.length; i++) {
        var text = els[i].text();
        if (text != null && text.trim() !== "") return text;
    }
    return "";
}

function get_discuss() {
    var els = findOnScreen("com.yikaobang.yixue:id/questiondetails_tv_contents");
    for (var i = 0; i < els.length; i++) {
        var text = els[i].text();
        if (text != null && text.trim() !== "") return text;
    }
    return "";
}

function get_accuracy() {
    var els = findOnScreen("com.yikaobang.yixue:id/questiondetails_tv_statistics");
    for (var i = 0; i < els.length; i++) {
        var text = els[i].text();
        if (text) {
            var m = text.match(/正确率(\d+\.?\d*)%/);
            if (m) return m[1] + "%";
        }
    }
    return "";
}

/**
 * A1/A2 单题：titletv 只有一个（无 Tab），直接取
 */
function getSingleStem() {
    var els = findOnScreen("com.yikaobang.yixue:id/titletv");
    if (els.length > 0) {
        // 按 Y 排序，取第一个
        els.sort(function(a, b) { return a.bounds().top - b.bounds().top; });
        return els[0].text();
    }
    return null;
}

// ==================== A1/A2 拉取 ====================

function fetchA1A2() {
    console.log("========== 拉取 A1/A2 ==========");

    var test = {
        name: getFormattedTimestamp(),
        pkg: PKG_NAME,
        cls: get_cls(),
        numb: get_numb(),
        unit: get_unit(),
        mode: get_mode(),
        test: getSingleStem(),
        option: get_option(),
        answer: get_answer(),
        rate: get_accuracy(),
        point: get_point(),
        discuss: get_discuss()
    };

    console.log("  [" + test.mode + "] " + test.numb);
    console.log("  题目: " + (test.test || "").substring(0, 60) + "...");
    console.log("  选项数: " + (test.option ? test.option.length : 0));
    console.log("  答案: " + test.answer);

    return test;
}

// ==================== A3/A4 & 案例分析 拉取 ====================

function fetchMultiSub() {
    console.log("========== 拉取 A3/A4 或 案例分析 ==========");

    var cls = get_cls();
    var numb = get_numb();
    var unit = get_unit();
    var mode = get_mode();
    var stem = getSharedStem() || "";

    console.log("  [" + mode + "] " + numb);
    console.log("  共享题干: " + stem.substring(0, 60) + "...");

    var tabList = getSubTabs();
    var tabCount = tabList.length;
    console.log("  子题数: " + tabCount);

    if (tabCount === 0) {
        console.log("  ✗ 未找到子题 Tab，回退到 A1/A2 模式");
        return fetchA1A2();
    }

    var subQuestions = [];

    for (var t = 0; t < tabCount; t++) {
        var tabName = (t < tabList.length) ? tabList[t].text : ("第" + (t + 1) + "问");
        console.log("  --- " + tabName + " (" + (t + 1) + "/" + tabCount + ") ---");

        if (t > 0) {
            swipeNextSub();
        }

        sleep(200);

        var subTest = getSubQuestionStem() || "";
        var options = get_option();
        var answer = get_answer();
        var point = get_point();
        var discuss = get_discuss();
        var accuracy = get_accuracy();

        console.log("    子题: " + subTest.substring(0, 50) + "...");
        console.log("    选项数: " + options.length + " | 答案: " + answer + " | 正确率: " + accuracy);

        subQuestions.push({
            sub_numb: tabName,
            sub_test: subTest,
            option: options,
            answer: answer,
            rate: accuracy,
            point: point,
            discuss: discuss
        });
    }

    var test = {
        name: getFormattedTimestamp(),
        pkg: PKG_NAME,
        cls: cls,
        numb: numb,
        unit: unit,
        mode: mode,
        test: stem,
        sub_questions: subQuestions
    };

    console.log("  子题采集完成: " + subQuestions.length + "/" + tabCount);
    return test;
}

// ==================== 统一入口 ====================

function fetchQuestion() {
    var mode = get_mode();
    var isMulti = isMultiSubQuestion();
    console.log("  检测题型: " + mode + " | 多子题: " + isMulti);

    if (isMulti) {
        return fetchMultiSub();
    } else {
        return fetchA1A2();
    }
}

function validateQuestion(test) {
    if (test == null) return false;
    if (test.sub_questions !== undefined) {
        return validateMultiSub(test);
    }
    return !hasNullValue(test);
}

// ==================== 保存 ====================

function savejson(test) {
    var name = OUTPUT_DIR + test.cls + "/" + (test.unit || "") + "/" + test.name + ".json";
    record = test;
    lastNumb = test.numb;
    lastUnit = test.unit || "";
    var jsonData = JSON.stringify(test, null, 2);
    files.createWithDirs(name);
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

/**
 * 获取题目列表的实际可视区域边界
 * @returns {object|null} {top: number, bottom: number} 或 null
 */
function getVisibleBounds() {
    var gridView = id("com.yikaobang.yixue:id/questionList_GridView").findOne(1000);
    if (!gridView) {
        console.log("  警告：未找到 questionList_GridView");
        return null;
    }

    var bounds = gridView.bounds();
    return {
        top: bounds.top,
        bottom: bounds.bottom
    };
}

/**
 * 获取可视区内的题号范围
 * @param {Array} items 题号元素列表
 * @returns {object} {min: number, max: number}
 */
function getVisibleQuestionRange(items) {
    var minNum = Infinity;
    var maxNum = 0;
    var visibleBounds = getVisibleBounds();

    for (var i = 0; i < items.length; i++) {
        var num = parseInt(items[i].text());
        if (isNaN(num)) continue;

        // 检查题号是否在实际可视区内
        if (visibleBounds) {
            var bounds = items[i].bounds();

            // 判断是否不在可视区内：贴边、超出边界、或者 top >= bottom（异常元素，不在可视区的元素）
            if (bounds.top <= visibleBounds.top ||
                bounds.bottom >= visibleBounds.bottom ||
                bounds.top >= bounds.bottom) {
                continue;  // 不在可视区内，跳过
            }
        }

        if (num < minNum) minNum = num;
        if (num > maxNum) maxNum = num;
    }

    return {
        min: minNum === Infinity ? 0 : minNum,
        max: maxNum
    };
}

/**
 * 尝试点击题号并验证是否成功进入题目页
 * @param {string} targetNum 目标题号
 * @param {Object} element 元素对象
 * @returns {boolean} true 表示成功进入题目页
 */
function clickQuestionAndVerify(targetNum, element) {
    var parent = element.parent();
    var grandParent = parent ? parent.parent() : null;

    console.log("  找到题号 " + targetNum + "，尝试点击策略");

    // 策略1: 尝试点击 grandParent
    if (grandParent && grandParent.clickable()) {
        console.log("  策略1: 尝试点击 grandParent");
        grandParent.click();
        sleep(3000);
        if (!id("com.yikaobang.yixue:id/questionList_item_tv").exists()) {
            console.log("  策略1 成功");
            return true;
        }
    }

    // 策略2: 尝试点击 parent
    if (parent && parent.clickable()) {
        console.log("  策略2: 尝试点击 parent");
        parent.click();
        sleep(3000);
        if (!id("com.yikaobang.yixue:id/questionList_item_tv").exists()) {
            console.log("  策略2 成功");
            return true;
        }
    }

    // 策略3: 尝试点击 X/Y 坐标
    var bounds = element.bounds();
    var x = bounds.centerX();
    var y = bounds.centerY();
    console.log("  策略3: 尝试点击坐标 (" + x + ", " + y + ")");
    click(x, y);
    sleep(3000);
    if (!id("com.yikaobang.yixue:id/questionList_item_tv").exists()) {
        console.log("  策略3 成功");
        return true;
    }

    // 策略4: 尝试点击 id("img_conunite")
    var imgConunite = id("com.yikaobang.yixue:id/img_conunite").findOne(1000);
    if (imgConunite) {
        console.log("  策略4: 尝试点击 id('img_conunite')");
        imgConunite.click();
        sleep(3000);
        if (!id("com.yikaobang.yixue:id/questionList_item_tv").exists()) {
            console.log("  策略4 成功");
            return true;
        }
    }

    console.log("  所有点击策略均失败");
    return false;
}

/**
 * 检查题号是否在安全可见范围内
 * @param {Object} bounds 题号边界
 * @returns {boolean} true 表示在安全范围内
 */
function isInSafeVisibleArea(bounds) {
    var visibleBounds = getVisibleBounds();
    if (!visibleBounds) return false;

    var safeMargin = 1;
    return bounds.top >= visibleBounds.top + safeMargin &&
           bounds.bottom <= visibleBounds.bottom - safeMargin;
}

/**
 * 根据题号位置判断滚动方向
 * @param {Object} bounds 题号边界
 * @returns {string} "forward" 或 "backward"
 *          - 靠近上边界：返回 "backward"（手指向下滑动）
 *          - 靠近下边界：返回 "forward"（手指向上滑动）
 */
function determineScrollDirectionByPosition(bounds) {
    var visibleBounds = getVisibleBounds();
    if (!visibleBounds) return "forward";

    var visibleCenter = (visibleBounds.top + visibleBounds.bottom) / 2;
    var itemCenter = (bounds.top + bounds.bottom) / 2;

    // 题号中心在可视区上半部分，手指向下滑动让它往中间移
    if (itemCenter < visibleCenter) {
        return "backward";
    }
    // 题号中心在可视区下半部分，手指向上滑动让它往中间移
    return "forward";
}

/**
 * 根据目标题号和可视区范围判断滚动方向
 * @param {number} targetNum 目标题号
 * @param {object} range 可视区范围 {min, max}
 * @returns {string} "backward" 或 "forward"
 *          - "backward": 手指向下滑动，显示更小的题号
 *          - "forward": 手指向上滑动，显示更大的题号
 */
function determineScrollDirection(targetNum, range) {
    console.log("  当前可视区题号范围: [" + range.min + ", " + range.max + "]");

    if (targetNum < range.min) {
        console.log("  题号 " + targetNum + " < 最小题号 " + range.min + "，需要更小的题号，scrollBackward（手指向下滑动）");
        return "backward";
    }
    if (targetNum > range.max) {
        console.log("  题号 " + targetNum + " > 最大题号 " + range.max + "，需要更大的题号，scrollForward（手指向上滑动）");
        return "forward";
    }
    console.log("  题号 " + targetNum + " 在可视区范围内但未找到，默认 scrollForward");
    return "forward";
}

/**
 * 在列表中查找并点击目标题号
 * @param {string} targetNum 目标题号（如 "1" 或 "123/456"）
 * @returns {object} {success: boolean, needScroll: boolean, scrollDirection: string}
 */
function findAndClickQuestion(targetNum) {
    var items = id("com.yikaobang.yixue:id/questionList_item_tv").find();
    var targetNumInt = parseInt(targetNum.split("/")[0]);

    // 查找目标题号
    for (var i = 0; i < items.length; i++) {
        if (items[i].text() !== targetNum) continue;

        var element = items[i];
        var bounds = element.bounds();

        // 检查位置是否安全（内部会获取实时可视区域）
        if (!isInSafeVisibleArea(bounds)) {
            var direction = determineScrollDirectionByPosition(bounds);
            console.log("  题号 " + targetNum + " 贴近边界，" + direction + " 让其移到中间");
            return {success: false, needScroll: true, scrollDirection: direction};
        }

        // 尝试点击
        if (clickQuestionAndVerify(targetNum, element)) {
            return {success: true, needScroll: false, scrollDirection: null};
        }

        // 点击失败，根据位置判断滚动方向
        var direction = determineScrollDirectionByPosition(bounds);
        console.log("  点击失败，" + direction + " 继续尝试");
        return {success: false, needScroll: true, scrollDirection: direction};
    }

    // 未找到题号，判断滚动方向
    var range = getVisibleQuestionRange(items);
    var scrollDirection = determineScrollDirection(targetNumInt, range);
    return {success: false, needScroll: true, scrollDirection: scrollDirection};
}

/**
 * 检查可视区是否卡住未变化
 * @param {object} currentRange 当前范围
 * @param {object} lastRange 上次范围
 * @param {object} state 状态对象 {stuckCount}
 * @returns {boolean} true 表示已卡住
 */
function isScrollStuck(currentRange, lastRange, state) {
    if (currentRange.min === lastRange.min && currentRange.max === lastRange.max) {
        state.stuckCount++;
        return state.stuckCount >= 3;
    }
    state.stuckCount = 0;
    return false;
}

/**
 * 执行滚动操作
 * @param {string} direction 滚动方向
 *          - "backward": 手指向下滑动，显示更小的题号
 *          - "forward": 手指向上滑动，显示更大的题号
 * @returns {boolean} true 表示滚动成功，false 表示失败
 */
function performScroll(direction) {
    var gridView = id("com.yikaobang.yixue:id/questionList_GridView").findOne(1000);
    if (!gridView) {
        console.log("  未找到 GridView，无法滚动");
        return false;
    }

    var bounds = gridView.bounds();
    var centerX = (bounds.left + bounds.right) / 2;
    var startY, endY;

    if (direction === "backward") {
        // 手指从上往下滑（startY < endY），显示更小的题号
        startY = bounds.top + 100;
        endY = bounds.bottom - 100;
    } else {
        // 手指从下往上滑（startY > endY），显示更大的题号
        startY = bounds.bottom - 100;
        endY = bounds.top + 100;
    }

    swipe(centerX, startY, centerX, endY, 1000);
    sleep(1500);
    return true;
}

/**
 * 滚动查找并点击目标题号
 * @param {string} targetNum 目标题号
 * @returns {boolean} true 表示成功
 */
function scrollToQuestion(targetNum) {
    var scrollCount = 0;
    var lastRange = {min: 0, max: 0};
    var state = {stuckCount: 0};

    while (true) {
        var result = findAndClickQuestion(targetNum);

        if (result.success) {
            console.log("========== 恢复完成，继续爬取 ==========");
            return true;
        }

        if (!result.needScroll) return false;

        var items = id("com.yikaobang.yixue:id/questionList_item_tv").find();
        var currentRange = getVisibleQuestionRange(items);

        // 检查是否卡住
        if (isScrollStuck(currentRange, lastRange, state)) {
            console.log("  失败，可视区未变化（" + currentRange.min + "-" + currentRange.max + "），未找到题号 " + targetNum);
            console.log("  总共滚动: " + scrollCount + " 次");
            return false;
        }

        lastRange = currentRange;
        scrollCount++;

        if (!performScroll(result.scrollDirection)) {
            return false;
        }
    }
}

/**
 * 返回到题目列表页
 * @returns {boolean} true 表示成功返回
 */
function returnToQuestionList() {
    var backBtn = id("com.yikaobang.yixue:id/include_btn_left").findOne(3000);
    if (!backBtn) {
        console.log("  未找到返回按钮");
        return false;
    }

    console.log("  点击返回按钮");
    backBtn.click();
    sleep(2000);

    // 等待进入题目列表页
    var waitCount = 0;
    while (!id("com.yikaobang.yixue:id/questionList_item_tv").exists() && waitCount < 10) {
        sleep(1000);
        waitCount++;
    }

    return id("com.yikaobang.yixue:id/questionList_item_tv").exists();
}

/**
 * 处理周期性返回：每 400 题返回列表页，释放内存后恢复
 * @param {number} savedCount 当前已保存的题目数量
 * @returns {boolean} true 表示成功恢复并继续，false 表示失败
 */
function handlePeriodicReturn(savedCount) {
    console.log("========== 执行周期性返回 ==========");
    console.log("当前已保存: " + savedCount + " 题");

    // 步骤1: 返回到题目列表页
    if (!returnToQuestionList()) {
        return false;
    }

    // 步骤2: 等待释放内存
    console.log("  等待 8 秒释放内存...");
    sleep(8000);
    closeAd();

    // 步骤3: 恢复到上次的题号
    if (!lastNumb || lastNumb === "") {
        console.log("  没有记录上次题号，无法恢复");
        return false;
    }

    var targetNum = lastNumb.split("/")[0];
    console.log("  恢复到题号: " + targetNum);

    return scrollToQuestion(targetNum);
}

// ==================== 翻页与等待 ====================

function waitForPage(timeout) {
    timeout = timeout || 5000;
    var start = Date.now();
    while (Date.now() - start < timeout) {
        var els = findOnScreen("com.yikaobang.yixue:id/titletv");
        if (els.length > 0) {
            sleep(200);
            return true;
        }
        sleep(200);
    }
    return false;
}

/**
 * 等待元素消失
 * @param {String} elementId 元素的 id（例如："com.yikaobang.yixue:id/openrel"）
 * @param {Number} maxWait 最大等待次数
 * @param {Function} waitFn 可选的等待函数，默认为 sleep(1000)，也可以传入滚动等操作
 * @return {Boolean} 是否成功（元素已消失）
 */
function waitForElementDisappear(elementId, maxWait, waitFn) {
    // 从 elementId 中提取元素名称（取最后一个 / 后面的部分）
    var parts = elementId.split("/");
    var elementName = parts[parts.length - 1];

    var waitCount = 0;
    while (id(elementId).exists() && waitCount < maxWait) {
        console.log("  等待 " + elementName + " 消失... (" + (waitCount + 1) + "/" + maxWait + ")");
        if (waitFn) {
            waitFn();
        } else {
            sleep(1000);
        }
        waitCount++;
    }

    if (!id(elementId).exists()) {
        console.log("  " + elementName + " 已消失");
        return true;
    } else {
        console.log("  警告：等待超时，" + elementName + " 仍然存在");
        return false;
    }
}

/**
 * 处理章节切换
 * 场景1: 最后一题滑动后弹出"已经是最后一题，是否跳转下一章？"
 * 场景2: 跳转后进入题目列表页，需要选背题模式并点击第1题
 */
function handleNextChapter() {
    // 场景1: 弹窗 "跳转下一章"
    var jumpBtn = id("com.yikaobang.yixue:id/tv_next").findOne(500);
    if (jumpBtn != null) {
        updateChapter();
        console.log("★ 当前章节结束: " + currentChapter + "，跳转下一章 ★");
        jumpBtn.click();
        sleep(2000);

        // 等待 centerPopupContainer 消失，表示已跳转
        waitForElementDisappear("com.yikaobang.yixue:id/centerPopupContainer", 10);

        // 根据 openrel 是否存在采取不同的等待策略
        if (!id("com.yikaobang.yixue:id/openrel").exists()) {
            // openrel 不存在，说明当前章节题目较少，等待 10 秒让新章节题目加载
            console.log("  openrel 不存在，等待新章节题目加载...");
            sleep(10000);
            console.log("  等待完成");
        } else {
            // openrel 存在，等待其消失（新章节加载完会自动回滚到顶部，openrel 会消失）
            waitForElementDisappear("com.yikaobang.yixue:id/openrel", 10);
        }

        return enterFromQuestionList();
    }

    // 场景2: 已经在题目列表页（比如从其他地方进入）
    if (id("com.yikaobang.yixue:id/questionList_item_tv").exists()) {
        console.log("★ 检测到题目列表页 ★");
        return enterFromQuestionList();
    }

    return false;
}

/**
 * 从题目列表页进入：选背题模式 → 点第1题
 */
function enterFromQuestionList() {
    // 如果 openrel 存在，向上滚动直到 openrel 消失
    waitForElementDisappear("com.yikaobang.yixue:id/openrel", 50, function() {
        swipe(700, 1800, 700, 2200, 1000);  // 向上滚动
        sleep(1000);
    });

    // 点击"背题模式"
    var labels = id("com.yikaobang.yixue:id/labeltext").find();
    for (var i = 0; i < labels.length; i++) {
        if (labels[i].text() === "全部") {
            console.log("  点击全部");
            labels[i].click();
            sleep(1000);
        }

        if (labels[i].text() === "背题模式") {
            console.log("  点击背题模式");
            labels[i].click();
            sleep(1000);
            break;
        }
    }

    // 等待题目列表加载完成，任意题号出现即可
    var maxWait = 10;  // 最多等待 10 秒
    for (var w = 0; w < maxWait; w++) {
        var items = id("com.yikaobang.yixue:id/questionList_item_tv").find();
        if (items.length > 0) {
            console.log("  题目列表已加载完成，共 " + items.length + " 个题号");
            break;
        }
        console.log("  等待题目列表加载... (" + (w + 1) + "/" + maxWait + ")");
        sleep(1000);
    }

    // 查找并点击题号"1"，使用统一的滚动查找逻辑
    if (scrollToQuestion("1")) {
        lastNumb = "";
        lastUnit = "";
        updateChapter();
        console.log("★ 已进入新章节: " + currentChapter + " ★");
        return true;
    }

    console.log("  进入新章节失败，未找到题号'1'");
    return false;
}

// ==================== 打印缺失字段 ====================

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

// ==================== 主循环 ====================

function main() {
    console.log("========== 脚本启动 ==========");
    console.log("设备: " + device.width + "x" + device.height);
    setScreenMetrics(1200, 2670)  //设置屏幕分辨率

    sleep(3000);
    closeAd();
    sleep(1000);

    var failCount = 0;
    var maxFail = 5;
    var savedCount = 0;
    var stuckCount = 0;

    for (var i = 0; i < 10000; i++) {
        updateChapter();
        console.log("\n---------- 第 " + (i + 1) + " 轮 | " + currentChapter + " | 已保存: " + savedCount + " ----------");

        closeAd();

        if (id("com.yikaobang.yixue:id/questionList_item_tv").exists()) {
            console.log("检测到题目列表页，尝试进入");
            if (enterFromQuestionList()) {
                continue;
            } else {
                console.log("进入失败，重置");
                reset();
                continue;
            }
        }

        if (handleNextChapter()) continue;

        if (!waitForPage(5000)) {
            console.log("页面未加载");
            closeAd();
            if (handleNextChapter()) continue;
            if (!waitForPage(3000)) {
                failCount++;
                console.log("连续失败: " + failCount + "/" + maxFail);
                if (failCount >= maxFail) {
                    reset();
                    failCount = 0;
                }
                swipeNextMain();
                continue;
            }
        }

        var currentNumb = get_numb();
        var currentUnit = get_unit();

        if (currentUnit != null && lastUnit !== "" && currentUnit !== lastUnit) {
            console.log("★ 章节切换: " + lastUnit + " → " + currentUnit);
            lastNumb = "";
        }

        if (currentNumb != null && currentNumb === lastNumb
            && (currentUnit || "") === lastUnit) {
            stuckCount++;
            console.log("题号未变(" + currentNumb + ") 卡住: " + stuckCount);

            if (stuckCount >= 3) {
                swipe(1050, 600, 100, 600, 150);
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
                swipeNextMain();
            }
            continue;
        }
        stuckCount = 0;

        var json = null;
        try {
            json = fetchQuestion();
        } catch (e) {
            console.log("拉取异常: " + e);
            failCount++;
            swipeNextMain();
            continue;
        }

        if (!validateQuestion(json)) {
            console.log("  ✗ 数据不完整，重试...");
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

            // 每 100 题执行一次返回恢复
            if (savedCount % PERIODIC_RETURN_INTERVAL === 0) {
                if (!handlePeriodicReturn(savedCount)) {
                    console.log("周期性返回失败，退出脚本");
                    break;
                }
            }
        }

        swipeNextMain();
        //sleep(300);
        handleNextChapter();
    }

    console.log("\n========== 脚本结束 ==========");
    console.log("共保存: " + savedCount);
}

// ==================== 入口 ====================
files.createWithDirs(OUTPUT_DIR + "placeholder");
files.remove(OUTPUT_DIR + "placeholder");

console.log("启动: " + APP_NAME);
app.launchApp(APP_NAME);

sleep(5000);
closeAd();
sleep(1000);
closeAd();

main();
