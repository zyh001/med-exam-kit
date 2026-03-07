// ==================== 配置区 ====================
var APP_NAME = "阿虎医考";
var PKG_NAME = "com.ahuxueshu";
var OUTPUT_DIR = "/sdcard/tests/";
var SKIP_MODES = [];  // ★ 清空：A1/A2、A3/A4、B1 均已适配
var ENABLE_IMAGE_CAPTURE = false;  // ★ 新增：是否捕获题目中的图片（Base64 编码）
var record;
var lastNumb = "";

// ==================== 工具函数 ====================

/**
 * 检查数据是否包含 null 值
 * @param {Object} obj - 要检查的对象
 * @param {boolean} skipImageFields - 是否跳过图片字段（用于数据完整性检查）
 */
function hasNullValue(obj, skipImageFields) {
    if (skipImageFields === undefined) skipImageFields = false;
    
    if (obj === null || obj === undefined) {
        return true;
    }
    if (Array.isArray(obj)) {
        if (obj.length === 0) return true;
        for (var i = 0; i < obj.length; i++) {
            if (hasNullValue(obj[i], skipImageFields)) return true;
        }
        return false;
    }
    if (typeof obj === 'object' && obj !== null) {
        var keys = Object.keys(obj);
        for (var i = 0; i < keys.length; i++) {
            var key = keys[i];
            // 如果跳过图片字段，跳过所有包含 image 的键
            if (skipImageFields && key.toLowerCase().indexOf('image') !== -1) {
                continue;
            }
            var val = obj[key];
            if (val === null || val === undefined) {
                return true;
            }
            if (typeof val === 'object' && val !== null) {
                if (hasNullValue(val, skipImageFields)) return true;
            }
        }
    }
    return false;
}

function isVisible(element) {
    if (element == null) return false;
    let bounds = element.bounds();
    // 只检查 right > 0，允许 left 为负数（左滑时左侧页面的元素）
    return bounds.right > 0 && bounds.left < device.width;
}

/**
 * 判断元素是否在当前页面内（宽松判断，用于滚动容器内的元素）
 * 只排除左右页的元素，不要求在屏幕可视区域内
 * 允许 left 为负数或 right 超出屏幕（滚动时）
 */
function isCurrentPage(element) {
    if (element == null) return false;
    let bounds = element.bounds();
    // 使用宽松判断：只要不是完全在左侧或右侧页面外即可
    // device.width 通常是 1200，第二页的 left 会是 1200 或更大
    return bounds.left < device.width && bounds.right > 0;
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
 * 按 bottom 值范围筛选（用于 A3/A4，元素 top 被裁剪，bottom 有效）
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
 * 按 top 值范围筛选（用于 B1，元素 bottom 被裁剪，top 有效）
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

// ==================== 图片处理工具函数 ====================

/**
 * 检测文本中是否包含图片占位符
 */
function hasImagePlaceholder(text) {
    if (text == null) return false;
    // 检查是否包含 U+FFFD 替换字符（￼）
    return text.indexOf('\uFFFD') !== -1;
}

/**
 * 对指定元素的区域进行截图并转换为 Base64
 * 返回：data:image/png;base64,xxxxx 格式的字符串
 */
function captureElementToBase64(element) {
    if (element == null) return null;
    try {
        // 获取元素边界
        var bounds = element.bounds();
        var left = bounds.left;
        var top = bounds.top;
        var right = bounds.right;
        var bottom = bounds.bottom;
        
        // 确保坐标在屏幕范围内
        if (left < 0) left = 0;
        if (top < 0) top = 0;
        if (right > device.width) right = device.width;
        if (bottom > device.height) bottom = device.height;
        
        var width = right - left;
        var height = bottom - top;
        
        if (width <= 0 || height <= 0) {
            console.log("  元素尺寸无效：" + width + "x" + height);
            return null;
        }
        
        // 截图并裁剪
        var screenImg = captureScreen();
        if (screenImg == null) {
            console.log("  截图失败");
            return null;
        }
        
        var clippedImg = images.clip(screenImg, left, top, width, height);
        if (clippedImg == null) {
            console.log("  裁剪失败");
            return null;
        }
        
        // 转换为 Base64
        var base64 = images.toBase64(clippedImg);
        if (base64 == null) {
            console.log("  Base64 转换失败");
            return null;
        }
        
        return "data:image/png;base64," + base64;
    } catch (e) {
        console.log("  截图异常：" + e);
        return null;
    }
}

/**
 * 对指定坐标区域进行截图并转换为 Base64
 */
function captureRegionToBase64(left, top, right, bottom) {
    try {
        if (!requestScreenCapture()) {
            return null;
        }
        
        // 确保坐标在屏幕范围内
        if (left < 0) left = 0;
        if (top < 0) top = 0;
        if (right > device.width) right = device.width;
        if (bottom > device.height) bottom = device.height;
        
        var width = right - left;
        var height = bottom - top;
        
        if (width <= 0 || height <= 0) return null;
        
        var screenImg = captureScreen();
        if (screenImg == null) return null;
        
        var clippedImg = images.clip(screenImg, left, top, width, height);
        if (clippedImg == null) return null;
        
        var base64 = images.toBase64(clippedImg);
        return "data:image/png;base64," + base64;
    } catch (e) {
        console.log("  区域截图异常：" + e);
        return null;
    }
}

// ==================== 通用数据提取 ====================

function get_cls() {
    let el = id("com.ahuxueshu:id/tk_title_sub_tv").findOne(5000);
    if (el == null) {
        console.log("  ✗ 未找到课程名，尝试重置");
        reset();
        return get_cls();
    }
    return el.text();
}

function get_unit() {
    let el = id("com.ahuxueshu:id/section_name").findOne(500);
    if (el != null) {
        return el.text();
    }
    return null;
}

function get_numb() {
    let el = id("com.ahuxueshu:id/section_position").findOne(3000);
    if (el != null) {
        return el.text().replace(/\s/g, "");
    }
    return null;
}

/**
 * 检测当前题型
 * 优先级：B1(question_type_b) → A3/A4(test_tv) → A1/A2(question_name_ax)
 */
function getCurrentMode() {
    try {
        // 1. B1 型题
        var b1Elements = id("com.ahuxueshu:id/question_type_b").find();
        for (var i = 0; i < b1Elements.length; i++) {
            if (isVisible(b1Elements[i])) {
                var txt = b1Elements[i].text();
                if (txt != null && txt.trim() !== "") {
                    var match = txt.match(/\[(.+?)\]/);
                    if (match) return match[1];
                    return "B1 型题";
                }
            }
        }

        // 2. A3/A4 型题
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

        // 3. A1/A2 型题
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
        console.log("获取题型异常：" + e);
    }
    return null;
}

// ==================== A1/A2 型题 数据提取 ====================

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
            return text.replace("正确答案：", "").replace(/\n/g, "").trim();
        }
    }
    return null;
}

function get_discuss() {
    let elements = id("com.ahuxueshu:id/official_analysis_htv").find();
    for (let i = 0; i < elements.length; i++) {
        let el = elements[i];
        if (isVisible(el)) {
            return el.text();
        }
    }
    return "";
}

function get_rate() {
    let elements = id("com.ahuxueshu:id/rate_of_all_tv").find();
    for (let i = 0; i < elements.length; i++) {
        let el = elements[i];
        if (isVisible(el)) {
            return el.text().split("\n")[0].trim();
        }
    }
    return "";
}

function get_error_prone() {
    let elements = id("com.ahuxueshu:id/error_prone_tv").find();
    for (let i = 0; i < elements.length; i++) {
        let el = elements[i];
        if (isVisible(el)) {
            return el.text().split("\n")[0].trim();
        }
    }
    return "";
}

/**
 * 拉取 A1/A2 型题数据（支持图片捕获）
 */
function fetch() {
    console.log("========== 拉取 A1/A2 ==========");
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

    // ★★★ 图片捕获逻辑 ★★★
    if (ENABLE_IMAGE_CAPTURE) {
        // 检查题目是否包含图片
        if (hasImagePlaceholder(test.test)) {
            console.log("  题目包含图片，正在捕获...");
            let questionEl = id("com.ahuxueshu:id/question_name_ax").findOne(2000);
            if (questionEl != null) {
                test.test_image_base64 = captureElementToBase64(questionEl);
                if (test.test_image_base64 != null) {
                    console.log("  题目图片捕获成功");
                }
            }
        }

        // 检查选项是否包含图片
        let contentEls = id("com.ahuxueshu:id/option_content").find();
        let optionImages = [];
        for (let i = 0; i < contentEls.length; i++) {
            if (isVisible(contentEls[i])) {
                let optText = contentEls[i].text();
                if (hasImagePlaceholder(optText)) {
                    console.log("  选项 " + (i + 1) + " 包含图片，正在捕获...");
                    let imgBase64 = captureElementToBase64(contentEls[i]);
                    optionImages.push(imgBase64);
                } else {
                    optionImages.push(null);
                }
            }
        }
        if (optionImages.length > 0) {
            test.option_images = optionImages;
            console.log("  选项图片捕获：" + optionImages.filter(function(x) { return x != null; }).length + " 个");
        }

        // 检查解析是否包含图片
        if (hasImagePlaceholder(test.discuss)) {
            console.log("  解析包含图片，正在捕获...");
            let discussEl = id("com.ahuxueshu:id/official_analysis_htv").findOne(2000);
            if (discussEl != null) {
                test.discuss_image_base64 = captureElementToBase64(discussEl);
                if (test.discuss_image_base64 != null) {
                    console.log("  解析图片捕获成功");
                }
            }
        }
    }

    console.log("  [" + test.mode + "] " + test.numb);
    console.log("  题目：" + (test.test || "").substring(0, 60) + "...");
    console.log("  选项数：" + (test.option ? test.option.length : 0));
    console.log("  答案：" + test.answer);

    return test;
}

// ==================== A3/A4 型题 数据提取 ====================

/**
 * 拉取 A3/A4 型题数据
 *
 * 结构：共用题干 (test_tv) + 多个子题 (question_name_atf)
 * 分组依据：元素 bounds().top 值（按垂直顺序排列）
 *
 * 输出格式:
 * { stem: "共用题干", sub_questions: [{ test, option:[], answer, rate, error_prone, discuss }] }
 */
function fetchA3A4() {
    console.log("========== 拉取 A3/A4 或 案例分析 ==========");
    var timestamp = getFormattedTimestamp();

    // 1. 题干和题型
    var stemText = "";
    var mode = "A3/A4 型题";
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
            break;
        }
    }

    // 2. 子题锚点 (question_name_atf) - 使用 top 坐标排序
    var subQAnchors = [];
    var atfElements = id("com.ahuxueshu:id/question_name_atf").find();
    for (var i = 0; i < atfElements.length; i++) {
        if (isCurrentPage(atfElements[i])) {
            var b = atfElements[i].bounds();
            subQAnchors.push({ text: atfElements[i].text(), top: b.top });
        }
    }
    subQAnchors.sort(function(a, b) { return a.top - b.top; });

    // 3. 所有选项对 (option + option_content) - 使用 top 坐标
    var allOptionPairs = [];
    var letterEls = id("com.ahuxueshu:id/option").find();
    var contentEls = id("com.ahuxueshu:id/option_content").find();
    var visLetters = [];
    var visContents = [];

    for (var i = 0; i < letterEls.length; i++) {
        if (isCurrentPage(letterEls[i])) {
            var b = letterEls[i].bounds();
            visLetters.push({ text: letterEls[i].text(), top: b.top });
        }
    }
    for (var i = 0; i < contentEls.length; i++) {
        if (isCurrentPage(contentEls[i])) {
            var b = contentEls[i].bounds();
            visContents.push({ text: contentEls[i].text(), top: b.top });
        }
    }

    visLetters.sort(function(a, b) { return a.top - b.top; });
    visContents.sort(function(a, b) { return a.top - b.top; });

    for (var i = 0; i < Math.min(visLetters.length, visContents.length); i++) {
        allOptionPairs.push({
            text: visLetters[i].text + "." + visContents[i].text,
            top: visLetters[i].top
        });
    }

    // 4. 所有答案 - 使用 top 坐标
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

    // 5. 所有正确率 - 使用 top 坐标
    var allRates = [];
    var rateEls = id("com.ahuxueshu:id/rate_of_all_tv").find();
    for (var i = 0; i < rateEls.length; i++) {
        if (isCurrentPage(rateEls[i])) {
            var b = rateEls[i].bounds();
            allRates.push({ text: rateEls[i].text().split("\n")[0].trim(), top: b.top });
        }
    }
    allRates.sort(function(a, b) { return a.top - b.top; });

    // 6. 所有易错选项 - 使用 top 坐标
    var allErrors = [];
    var errEls = id("com.ahuxueshu:id/error_prone_tv").find();
    for (var i = 0; i < errEls.length; i++) {
        if (isCurrentPage(errEls[i])) {
            var b = errEls[i].bounds();
            allErrors.push({ text: errEls[i].text().split("\n")[0].trim(), top: b.top });
        }
    }
    allErrors.sort(function(a, b) { return a.top - b.top; });

    // 7. 所有解析 (official_analysis_htv_include — A3/A4 专用) - 使用 top 坐标
    var allAnalysis = [];
    var anaEls = id("com.ahuxueshu:id/official_analysis_htv_include").find();
    for (var i = 0; i < anaEls.length; i++) {
        if (isCurrentPage(anaEls[i])) {
            var b = anaEls[i].bounds();
            allAnalysis.push({ text: anaEls[i].text(), top: b.top });
        }
    }
    allAnalysis.sort(function(a, b) { return a.top - b.top; });

    // 8. 按 top 坐标分组
    var subQuestions = [];
    for (var q = 0; q < subQAnchors.length; q++) {
        var lo = subQAnchors[q].top;
        var hi = (q + 1 < subQAnchors.length) ? subQAnchors[q + 1].top : Infinity;

        var opts = [];
        var rangeOpts = filterByTop(allOptionPairs, lo, hi);
        for (var j = 0; j < rangeOpts.length; j++) opts.push(rangeOpts[j].text);

        var answer = "";
        var ra = filterByTop(allAnswers, lo, hi);
        if (ra.length > 0) answer = ra[0].text;

        var rate = "";
        var rr = filterByTop(allRates, lo, hi);
        if (rr.length > 0) rate = rr[0].text;

        var errorProne = "";
        var re = filterByTop(allErrors, lo, hi);
        if (re.length > 0) errorProne = re[0].text;

        var discuss = "";
        var rd = filterByTop(allAnalysis, lo, hi);
        if (rd.length > 0) discuss = rd[0].text;

        var qText = subQAnchors[q].text.replace(/^\(\d+\)\s*/, "").trim();

        subQuestions.push({
            test: qText,
            option: opts,
            answer: answer,
            rate: rate,
            error_prone: errorProne,
            discuss: discuss
        });
    }

    var result = {
        name: timestamp,
        pkg: PKG_NAME,
        cls: get_cls(),
        numb: get_numb(),
        unit: get_unit(),
        mode: mode,
        stem: stemText,
        sub_questions: subQuestions
    };

    // ★★★ 图片捕获逻辑 (A3/A4) ★★★
    if (ENABLE_IMAGE_CAPTURE) {
        // 检查共享题干是否包含图片
        if (hasImagePlaceholder(stemText)) {
            console.log("  共享题干包含图片，正在捕获...");
            let stemEl = id("com.ahuxueshu:id/test_tv").findOne(2000);
            if (stemEl != null) {
                result.stem_image_base64 = captureElementToBase64(stemEl);
                if (result.stem_image_base64 != null) {
                    console.log("  共享题干图片捕获成功");
                }
            }
        }

        // 检查每个子题
        for (var q = 0; q < subQuestions.length; q++) {
            var subQ = subQuestions[q];
            if (hasImagePlaceholder(subQ.test)) {
                console.log("  子题 " + (q + 1) + " 包含图片，正在捕获...");
                // 找到对应的子题元素
                if (q < subQAnchors.length) {
                    let qEl = id("com.ahuxueshu:id/question_name_atf").find()[q];
                    if (qEl != null) {
                        subQ.test_image_base64 = captureElementToBase64(qEl);
                    }
                }
            }
            // 检查子题选项
            if (subQ.option && subQ.option.length > 0) {
                let optImages = [];
                // 获取该子题范围内的选项元素
                var lo = subQAnchors[q].top;
                var hi = (q + 1 < subQAnchors.length) ? subQAnchors[q + 1].top : Infinity;
                var rangeOpts = filterByTop(allOptionPairs, lo, hi);
                
                for (var oi = 0; oi < subQ.option.length; oi++) {
                    if (hasImagePlaceholder(subQ.option[oi])) {
                        console.log("  子题 " + (q + 1) + " 选项 " + (oi + 1) + " 包含图片，正在捕获...");
                        // 找到对应的选项元素进行截图
                        if (oi < rangeOpts.length) {
                            // 通过遍历所有 option_content 元素找到对应的
                            let contentEls = id("com.ahuxueshu:id/option_content").find();
                            let foundEl = null;
                            let matchCount = 0;
                            for (var ci = 0; ci < contentEls.length; ci++) {
                                if (isCurrentPage(contentEls[ci])) {
                                    if (matchCount === oi) {
                                        foundEl = contentEls[ci];
                                        break;
                                    }
                                    matchCount++;
                                }
                            }
                            if (foundEl != null) {
                                optImages.push(captureElementToBase64(foundEl));
                            } else {
                                optImages.push(null);
                            }
                        } else {
                            optImages.push(null);
                        }
                    } else {
                        optImages.push(null);
                    }
                }
                if (optImages.some(function(x) { return x != null; })) {
                    subQ.option_images = optImages;
                    console.log("  子题 " + (q + 1) + " 选项图片捕获：" + optImages.filter(function(x) { return x != null; }).length + " 个");
                }
            }
        }
        console.log("  A3/A4 图片捕获完成");
    }

    console.log("  [" + mode + "] " + result.numb);
    console.log("  共享题干：" + stemText.substring(0, 60) + "...");
    console.log("  子题数：" + subQuestions.length);

    return result;
}

// ==================== B1 型题 数据提取 ====================

/**
 * 拉取 B1 型题数据
 *
 * 结构：共用选项 (share_option + share_option_content) + 多个子题 (item_question_name)
 * 子题只有字母按钮 (option_btn)，答案从共用选项中选择
 * 整体解析在最后 (official_analysis_htv)
 *
 * 分组依据：元素 bounds().top 值（B1 的 bottom 被裁剪，top 有效）
 *
 * 输出格式:
 * {
 *   shared_options: ["A.根尖周病变...", "B.根尖周病变..."],
 *   sub_questions: [{ test, answer, rate, error_prone }],
 *   discuss: "第 1 题：... 第 2 题：..."
 * }
 */
function fetchB1() {
    console.log("========== 拉取 B1 ==========");
    var timestamp = getFormattedTimestamp();

    // -------- 1. 题型 --------
    var mode = "B1 型题";
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
        sharedOptions.push(optText);
    }

    // -------- 3. 子题锚点 (item_question_name — B1 专用) --------
    var subQAnchors = [];
    var itemQEls = id("com.ahuxueshu:id/item_question_name").find();
    for (var i = 0; i < itemQEls.length; i++) {
        if (isCurrentPage(itemQEls[i])) {
            var b = itemQEls[i].bounds();
            subQAnchors.push({ text: itemQEls[i].text(), top: b.top });
        }
    }
    subQAnchors.sort(function(a, b) { return a.top - b.top; });

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

    // -------- 7. 整体解析 (official_analysis_htv — B1 所有子题合并) --------
    var discuss = "";
    var anaEls = id("com.ahuxueshu:id/official_analysis_htv").find();
    for (var i = 0; i < anaEls.length; i++) {
        if (isCurrentPage(anaEls[i])) {
            discuss = anaEls[i].text();
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

        subQuestions.push({
            test: qText,
            answer: answer,
            rate: rate,
            error_prone: errorProne
        });
    }

    var result = {
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

    // ★★★ 图片捕获逻辑 (B1) ★★★
    if (ENABLE_IMAGE_CAPTURE) {
        // 检查共用选项是否包含图片并实际捕获
        let sharedOptImages = [];
        let shareContentEls = id("com.ahuxueshu:id/share_option_content").find();
        
        for (var i = 0; i < sharedOptions.length; i++) {
            if (hasImagePlaceholder(sharedOptions[i])) {
                console.log("  共用选项 " + (i + 1) + " 包含图片，正在捕获...");
                // 找到对应的选项元素进行截图
                if (i < shareContentEls.length && isCurrentPage(shareContentEls[i])) {
                    sharedOptImages.push(captureElementToBase64(shareContentEls[i]));
                } else {
                    sharedOptImages.push(null);
                }
            } else {
                sharedOptImages.push(null);
            }
        }
        if (sharedOptImages.some(function(x) { return x != null; })) {
            result.shared_options_images = sharedOptImages;
            console.log("  共用选项图片捕获：" + sharedOptImages.filter(function(x) { return x != null; }).length + " 个");
        }

        // 检查每个子题
        for (var q = 0; q < subQuestions.length; q++) {
            var subQ = subQuestions[q];
            if (hasImagePlaceholder(subQ.test)) {
                console.log("  子题 " + (q + 1) + " 包含图片，正在捕获...");
                if (q < subQAnchors.length) {
                    let qEl = id("com.ahuxueshu:id/item_question_name").find()[q];
                    if (qEl != null) {
                        subQ.test_image_base64 = captureElementToBase64(qEl);
                    }
                }
            }
        }

        // 检查解析是否包含图片
        if (hasImagePlaceholder(discuss)) {
            console.log("  解析包含图片，正在捕获...");
            let discussEl = id("com.ahuxueshu:id/official_analysis_htv").findOne(2000);
            if (discussEl != null) {
                result.discuss_image_base64 = captureElementToBase64(discussEl);
                if (result.discuss_image_base64 != null) {
                    console.log("  解析图片捕获成功");
                }
            }
        }
        console.log("  B1 图片捕获完成");
    }

    console.log("  [B1 型题] " + result.numb);
    console.log("  共用选项数：" + sharedOptions.length);
    console.log("  子题数：" + subQuestions.length);

    return result;
}

// ==================== 核心逻辑函数 ====================

function savejson(test) {
    var name = OUTPUT_DIR + test.cls + "/" + (test.unit || "") + "/" + test.name + ".json";
    record = test;
    lastNumb = test.numb;
    var jsonData = JSON.stringify(test, null, 2);
    files.createWithDirs(name);
    files.write(name, jsonData);
    console.log("  ✔ 保存成功 " + test.numb);
    return jsonData;
}

/**
 * 处理请求频繁弹窗
 * 流程：请求频繁弹窗 → 点击确定 → 返回题目列表 → 点击继续做题 → 做题记录弹窗 → 点击继续
 */
function handleRateLimit() {
    if (id("com.ahuxueshu:id/redo_content_tv").exists()) {
        console.log("  检测到请求频繁弹窗，点击确定...");
        id("com.ahuxueshu:id/redo_cancel_tv").findOne(1000).click();
        sleep(1500);
        // 点击确定后返回题目列表，需要点击"继续做题"按钮
        handleContinueFromList();
        return true;
    }
    return false;
}

/**
 * 处理题目列表页面的"继续做题"按钮
 */
function handleContinueFromList() {
    // 检测是否到达题目列表页面（通过章节列表元素判断）
    if (id("com.ahuxueshu:id/subject_of_question").exists() || id("com.ahuxueshu:id/doing").exists()) {
        console.log("  已到达题目列表，寻找继续做题按钮...");
        // 查找"继续做题"按钮（ID: doing）
        var doingBtn = id("com.ahuxueshu:id/doing").findOne(2000);
        if (doingBtn != null) {
            console.log("  找到继续做题按钮，点击...");
            doingBtn.click();
            sleep(1500);
            // 点击继续做题后会出现做题记录弹窗，需要处理
            handleContinueDialog();
            return true;
        }
    }
    return false;
}

/**
 * 处理做题记录弹窗（继续做题确认）
 * 弹窗内容：您上一次做到了第 X 题，请问是否继续？
 * 按钮：重新开始 | 继续
 */
function handleContinueDialog() {
    if (text("继续").exists() && text("重新开始").exists()) {
        console.log("  检测到做题记录弹窗，点击继续...");
        text("继续").findOne(1000).click();
        sleep(1500);
        return true;
    }
    return false;
}

function closeAd() {
    if (id("close").exists()) {
        id("close").findOne(500).click();
        sleep(500);
        return true;
    }
    if (id("iv_close").exists()) {
        id("iv_close").findOne(500).click();
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
    // 处理请求频繁弹窗（会触发完整的恢复流程）
    if (handleRateLimit()) return true;
    // 处理做题记录弹窗（直接在题目页面时）
    if (handleContinueDialog()) return true;
    return false;
}

function forceStop() {
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
        console.log("恢复到：" + record.cls + " > " + (record.unit || "") + " > " + record.numb);
        sleep(2000);
        closeAd();
        try { sim_click(record.cls); sleep(2000); closeAd(); } catch (e) { console.log("  ✗ 导航课程失败：" + e); }
        try { sim_click(record.unit); sleep(2000); closeAd(); } catch (e) { console.log("  ✗ 导航章节失败：" + e); }
        try {
            let currentNum = record.numb.split("/")[0];
            console.log("恢复到题号：" + currentNum);
            sleep(3000);
            sim_click(currentNum);
            sleep(3000);
        } catch (e) { console.log("  ✗ 导航题号失败：" + e); }
    }
    console.log("========== 重置完成 ==========");
}

function checkMode() {
    let beitiTab = text("背题").findOne(500);
    if (beitiTab != null) {
        if (!id("com.ahuxueshu:id/right_answer_tv").exists()) {
            console.log("  切换到背题模式");
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
 * 支持：A1/A2(question_name_ax) | A3/A4(test_tv) | B1(question_type_b)
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
 * 返回："A1" | "A3A4" | "B1" | "SKIP" | "UNKNOWN"
 */
function classifyMode(mode) {
    if (mode == null) return "UNKNOWN";
    if (shouldSkip(mode)) return "SKIP";
    if (mode.indexOf("B1") !== -1) return "B1";
    if (mode.indexOf("A3") !== -1 || mode.indexOf("A4") !== -1) return "A3A4";
    if (mode.indexOf("案例分析") !== -1) return "A3A4";  // 案例分析题按 A3/A4 处理
    return "A1";  // A1/A2 及其他未知类型都走 A1 逻辑
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
    console.log("设备宽度：" + device.width + " 高度：" + device.height);
    console.log("图片捕获功能：" + (ENABLE_IMAGE_CAPTURE ? "启用" : "禁用"));
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
        console.log("\n---------- 第 " + (i + 1) + " 轮 | 已保存：" + savedCount
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
                console.log("连续失败：" + failCount + "/" + maxFail);
                if (failCount >= maxFail) {
                    console.log("连续失败过多，重置 App");
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
            console.log("★★★ 章节切换：" + lastUnit + " → " + currentUnit + " ★★★");
            lastNumb = "";
            checkMode();
            sleep(500);
        }

        // 5. 去重：同一章节内题号相同则跳过
        if (currentNumb != null && currentNumb === lastNumb && currentUnit === lastUnit) {
            stuckCount++;
            console.log("题号未变化 (" + currentNumb + ")，卡住次数：" + stuckCount);

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
        console.log("当前题型：" + currentMode + " → 类别：" + typeClass);

        // 7. 跳过未适配的题型
        if (typeClass === "SKIP") {
            console.log("⏭ 跳过 [" + currentMode + "]: " + currentNumb);
            stats["SKIP"]++;
            lastNumb = currentNumb;
            lastUnit = currentUnit || "";
            swipeNext();
            continue;
        }

        // 8. 未知题型处理
        if (typeClass === "UNKNOWN") {
            console.log("⚠ 未识别题型，尝试按 A1 处理");
            typeClass = "A1";
        }

        // 9. ★★★ 根据题型拉取数据 ★★★
        var json = null;
        try {
            json = fetchByType(typeClass);
        } catch (e) {
            console.log("拉取数据异常：" + e);
            failCount++;
            swipeNext();
            continue;
        }

        // 10. 校验并保存
        // 使用 skipImageFields=true 跳过图片字段检查（图片捕获失败不影响保存）
        if (json == null || hasNullValue(json, true)) {
            console.log("数据不完整，打印缺失字段:");
            printMissingFields(json);

            // 等待后重试一次
            console.log("等待 2 秒后重试...");
            sleep(2000);
            try {
                json = fetchByType(typeClass);
            } catch (e) {
                console.log("重试拉取异常：" + e);
            }

            // 重试时也跳过图片字段检查
            if (json != null && !hasNullValue(json, true)) {
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
                    console.log("连续失败过多，重置 App");
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
    console.log("共保存题目：" + savedCount);
    console.log("  A1/A2: " + stats["A1"]);
    console.log("  A3/A4: " + stats["A3A4"]);
    console.log("  B1:    " + stats["B1"]);
    console.log("  跳过：  " + stats["SKIP"]);
}

// ==================== 入口 ====================
files.createWithDirs(OUTPUT_DIR + "placeholder");
files.remove(OUTPUT_DIR + "placeholder");

console.log("启动 App: " + APP_NAME);
app.launchApp(APP_NAME);

// ★★★ 请求截图权限（只有在启用图片捕获时才请求）★★★
if (ENABLE_IMAGE_CAPTURE) {
    console.log("请求截图权限...");
    if (!requestScreenCapture()) {
        console.log("截图权限请求失败，请手动授予权限后重试");
        toast("请授予截图权限");
        sleep(3000);
    } else {
        console.log("截图权限已获得");
    }
} else {
    console.log("图片捕获功能已禁用，跳过截图权限请求");
}

sleep(5000);
closeAd();
sleep(1000);
closeAd();

main();
