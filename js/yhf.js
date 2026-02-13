"auto";
setScreenMetrics(1200, 2670);

var PKG_NAME = "com.yihafo.medexam";
var OUTPUT_DIR = "/sdcard/yihafo/";
var SWIPE_DELAY = 1500;

var record = null;
var lastNumb = "";
var sameCount = 0;
var savedCount = 0;

files.ensureDir(OUTPUT_DIR);
requestScreenCapture();
sleep(2000);

// ==================== 工具函数 ====================

function getFormattedTimestamp() {
    var now = new Date();
    var pad2 = function(n) { return String(n).padStart(2, "0"); };
    var pad3 = function(n) { return String(n).padStart(3, "0"); };
    return now.getFullYear() + "-" + pad2(now.getMonth() + 1) + "-" + pad2(now.getDate())
        + "-" + pad2(now.getHours()) + "-" + pad2(now.getMinutes())
        + "-" + pad2(now.getSeconds()) + "-" + pad3(now.getMilliseconds());
}

function hasNullValue(obj) {
    if (obj === null || obj === undefined) return true;
    if (typeof obj === "string") return false;
    if (typeof obj === "number") return false;
    if (Array.isArray(obj)) {
        if (obj.length === 0) return true;
        return obj.some(hasNullValue);
    }
    if (typeof obj === "object") {
        var keys = Object.keys(obj);
        for (var i = 0; i < keys.length; i++) {
            if (hasNullValue(obj[keys[i]])) return true;
        }
        return false;
    }
    return false;
}

function validateQuestion(json) {
    if (json == null) return false;
    if (!json.cls || !json.numb || !json.mode || !json.test) return false;
    if (json.sub_questions !== undefined) {
        if (!json.sub_questions || json.sub_questions.length === 0) return false;
        for (var i = 0; i < json.sub_questions.length; i++) {
            var sq = json.sub_questions[i];
            if (!sq.sub_numb || !sq.sub_test) return false;
            if (!sq.option || sq.option.length === 0) return false;
            if (!sq.answer) return false;
        }
        return true;
    }
    return !hasNullValue(json);
}

function printMissing(json) {
    if (json == null) { console.log("    json = null"); return; }
    var keys = Object.keys(json);
    for (var k = 0; k < keys.length; k++) {
        var val = json[keys[k]];
        if (val === null || val === undefined) console.log("    缺失: " + keys[k]);
        else if (Array.isArray(val) && val.length === 0) console.log("    空数组: " + keys[k]);
    }
    if (json.sub_questions) {
        for (var s = 0; s < json.sub_questions.length; s++) {
            var sq = json.sub_questions[s];
            var sqKeys = Object.keys(sq);
            for (var k = 0; k < sqKeys.length; k++) {
                var val = sq[sqKeys[k]];
                if (val === null || val === undefined) console.log("    子题[" + s + "] 缺失: " + sqKeys[k]);
                else if (Array.isArray(val) && val.length === 0) console.log("    子题[" + s + "] 空数组: " + sqKeys[k]);
            }
        }
    }
}

// ==================== 屏幕过滤 ====================

function onScreen(el) {
    var b = el.bounds();
    return b.left >= 0 && b.right <= 1200 && b.top >= 0 && b.bottom <= 2670;
}

function findVis(sel) {
    var all = sel.find();
    var r = [];
    for (var i = 0; i < all.length; i++) {
        if (onScreen(all[i])) r.push(all[i]);
    }
    return r;
}

// ==================== 通用提取 ====================

function getSection() {
    var els = findVis(textMatches(/^第.+节.+/));
    return els.length ? els[0].text().trim() : "";
}

function getNumber() {
    var els = findVis(textMatches(/^\d+\/\d+$/));
    return els.length ? els[0].text().trim() : "";
}

function getType() {
    var els = findVis(textMatches(/^\[.+题.*\]/));
    return els.length ? els[0].text().trim() : "";
}

function getStem() {
    var els = findVis(textMatches(/^\d+[\.\．].+/));
    return els.length ? els[0].text().trim() : "";
}

function getAnalysis() {
    var els = findVis(textContains("解析"));
    for (var i = 0; i < els.length; i++) {
        var t = els[i].text().trim();
        if (t.length > 10) return t;
    }
    return "";
}

function getExamPoint() {
    var labels = findVis(text("本题考点"));
    if (!labels.length) return "";
    var lb = labels[0].bounds();
    var all = className("android.widget.TextView").find();
    for (var i = 0; i < all.length; i++) {
        var t = all[i].text();
        var b = all[i].bounds();
        if (t && t !== "本题考点" && b.left > lb.right - 30
            && Math.abs(b.centerY() - lb.centerY()) < 50) {
            return t.trim();
        }
    }
    return "";
}

// ==================== 普通题提取 ====================

function getRegularOptions() {
    var letters = ["A", "B", "C", "D", "E"];
    var opts = [];
    for (var i = 0; i < letters.length; i++) {
        var els = className("android.widget.TextView").text(letters[i]).find();
        for (var j = 0; j < els.length; j++) {
            var b = els[j].bounds();
            if (b.left < 200 && b.top > 600 && b.bottom < 1700) {
                var p = els[j].parent();
                var optText = "";
                if (p) {
                    for (var k = 0; k < p.childCount(); k++) {
                        var c = p.child(k);
                        if (c && c.text() && c.text().trim() !== letters[i]) {
                            optText = c.text().trim();
                            break;
                        }
                    }
                }
                opts.push({ letter: letters[i], text: optText, cx: b.centerX(), cy: b.centerY() });
                break;
            }
        }
    }
    opts.sort(function(a, b) { return a.cy - b.cy; });
    return opts;
}

function getRegularAnswer(opts) {
    if (!opts.length) return "";
    var img = captureScreen();
    var ans = "";
    for (var i = 0; i < opts.length; i++) {
        var color = images.pixel(img, opts[i].cx, opts[i].cy - 35);
        if (colors.green(color) > 150 && colors.red(color) < 100) {
            ans = opts[i].letter;
            break;
        }
    }
    img.recycle();
    return ans;
}

// ==================== 共用选项题提取 ====================

function getSharedOptions() {
    var els = textStartsWith("A.").find();
    if (!els.length) els = textStartsWith("A．").find();
    for (var i = 0; i < els.length; i++) {
        var t = els[i].text();
        if (t.indexOf("B") > 0 && t.indexOf("\n") > 0) {
            var lines = t.split("\n");
            var opts = [];
            for (var j = 0; j < lines.length; j++) {
                var m = lines[j].match(/^([A-E])[\.\．](.+)/);
                if (m) opts.push({ letter: m[1], text: m[2].trim() });
            }
            return opts;
        }
    }
    return [];
}

function getSharedAnswer() {
    var letters = ["A", "B", "C", "D", "E"];
    var img = captureScreen();
    var ans = "";
    for (var i = 0; i < letters.length; i++) {
        var els = className("android.view.View").text(letters[i]).find();
        for (var j = 0; j < els.length; j++) {
            var b = els[j].bounds();
            if (b.right - b.left > 100 && b.bottom - b.top > 80) {
                var color = images.pixel(img, b.centerX(), b.top + 15);
                if (colors.green(color) > 150 && colors.red(color) < 100) {
                    ans = letters[i];
                }
                break;
            }
        }
    }
    img.recycle();
    return ans;
}

function getSubCount(type) {
    var m = type.match(/(\d+)-(\d+)题共用/);
    if (m) return parseInt(m[2]) - parseInt(m[1]) + 1;
    return 1;
}

// ==================== 拉取逻辑 ====================

function fetchRegular() {
    console.log("========== 拉取 普通题 ==========");
    var opts = getRegularOptions();
    var answer = getRegularAnswer(opts);

    var optionTexts = [];
    for (var i = 0; i < opts.length; i++) {
        optionTexts.push(opts[i].text);
    }

    var test = {
        name: getFormattedTimestamp(),
        pkg: PKG_NAME,
        cls: getSection(),
        numb: getNumber(),
        unit: "",
        mode: getType(),
        test: getStem(),
        option: optionTexts,
        answer: answer,
        rate: "",
        point: getExamPoint(),
        discuss: getAnalysis()
    };

    console.log("  [" + test.mode + "] " + test.numb);
    console.log("  题目: " + (test.test || "").substring(0, 50) + "...");
    console.log("  选项数: " + test.option.length + " | 答案: " + test.answer);
    return test;
}

function fetchSharedOption() {
    var type = getType();
    var subCount = getSubCount(type);
    var sharedOpts = getSharedOptions();
    var cls = getSection();
    var startNumb = getNumber();

    console.log("========== 拉取 共用选项题 ==========");
    console.log("  [" + type + "] " + startNumb + " | 子题数: " + subCount);

    // 构建共用选项文本和数组
    var sharedText = "";
    var optionTexts = [];
    for (var i = 0; i < sharedOpts.length; i++) {
        sharedText += sharedOpts[i].letter + "." + sharedOpts[i].text;
        if (i < sharedOpts.length - 1) sharedText += "\n";
        optionTexts.push(sharedOpts[i].text);
    }

    var subQuestions = [];

    for (var t = 0; t < subCount; t++) {
        if (t > 0) {
            swipe(900, 1300, 300, 1300, 300);
            sleep(SWIPE_DELAY);
            sleep(300);
        }

        var subStem = getStem();
        var answer = getSharedAnswer();
        var point = getExamPoint();
        var discuss = getAnalysis();

        console.log("  --- 子题 " + (t + 1) + "/" + subCount + " ---");
        console.log("    题目: " + (subStem || "").substring(0, 50));
        console.log("    答案: " + answer);

        subQuestions.push({
            sub_numb: String(t + 1),
            sub_test: subStem || "",
            option: optionTexts,
            answer: answer || "",
            rate: "",
            point: point || "",
            discuss: discuss || ""
        });
    }

    // 更新 lastNumb 为最后一个子题的题号
    lastNumb = getNumber();

    var test = {
        name: getFormattedTimestamp(),
        pkg: PKG_NAME,
        cls: cls,
        numb: startNumb,
        unit: "",
        mode: type,
        test: sharedText,
        sub_questions: subQuestions
    };

    console.log("  子题采集完成: " + subQuestions.length + "/" + subCount);
    return test;
}

// ==================== 保存 ====================

function saveJson(test) {
    var name = OUTPUT_DIR + test.name + ".json";
    record = test;
    lastNumb = test.sub_questions ? getNumber() : test.numb;
    var jsonData = JSON.stringify(test, null, 2);
    files.create(name);
    files.write(name, jsonData);
    console.log("  ✔ 保存成功 " + test.numb);
    savedCount++;
    return jsonData;
}

// ==================== 主循环 ====================

function main() {
    console.log("========== 脚本启动 ==========");
    console.log("设备: " + device.width + "x" + device.height);
    toast("3秒后开始，请确保在背题模式");
    sleep(3000);

    var failCount = 0;
    var maxFail = 5;

    for (var i = 0; i < 10000; i++) {
        console.log("\n---------- 第 " + (i + 1) + " 轮 | 已保存: " + savedCount + " ----------");
        sleep(800);

        var numb = getNumber();
        if (!numb) {
            console.log("  未检测到题号，等待...");
            sleep(1000);
            failCount++;
            if (failCount >= maxFail) {
                console.log("  连续失败 " + maxFail + " 次，结束");
                break;
            }
            continue;
        }

        if (numb === lastNumb) {
            sameCount++;
            console.log("  题号未变(" + numb + ") 卡住: " + sameCount);
            if (sameCount > 3) {
                console.log("  已到最后一题，结束");
                break;
            }
            swipe(900, 1300, 300, 1300, 300);
            sleep(SWIPE_DELAY);
            continue;
        }
        sameCount = 0;
        failCount = 0;

        // 检测题型
        var type = getType();
        var isShared = type.indexOf("共用选项") >= 0;

        var json = null;
        try {
            if (isShared) {
                json = fetchSharedOption();
            } else {
                json = fetchRegular();
            }
        } catch (e) {
            console.log("  拉取异常: " + e);
            failCount++;
            swipe(900, 1300, 300, 1300, 300);
            sleep(SWIPE_DELAY);
            continue;
        }

        // 验证
        if (!validateQuestion(json)) {
            console.log("  ✗ 数据不完整，重试...");
            printMissing(json);
            sleep(2000);

            try {
                if (isShared) {
                    json = fetchSharedOption();
                } else {
                    json = fetchRegular();
                }
            } catch (e) {
                console.log("  重试异常: " + e);
            }

            if (validateQuestion(json)) {
                saveJson(json);
            } else {
                console.log("  ✗ 重试仍不完整，跳过");
                printMissing(json);
                lastNumb = numb;
            }
        } else {
            saveJson(json);
        }

        // 翻到下一题
        swipe(900, 1300, 300, 1300, 300);
        sleep(SWIPE_DELAY);
    }

    console.log("\n========== 脚本结束 ==========");
    console.log("共保存: " + savedCount);
    toast("爬取完成！共保存 " + savedCount + " 题");
}

// ==================== 入口 ====================
main();
