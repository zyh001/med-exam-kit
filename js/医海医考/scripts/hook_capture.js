/*
 * 医海医考 Frida Hook — 完整配对数据捕获
 * 
 * 用法:
 *   frida -U -f uni.UNI7EE4208 -l hook_capture.js | tee capture.txt
 *   (等APP加载后操作, 然后用 analyze_capture.py 分析)
 *
 * 功能:
 *   1. SSL Pinning 绕过
 *   2. 捕获完整请求头 (AppVerify + Token, 不截断)
 *   3. 捕获完整响应体 (encryptData + encryptSign)
 *   4. 自动计算嵌入IV位置
 *   5. 抓取 WXStreamModule.fetch 的 JS 层参数
 */

console.log("[hook] 医海医考配对数据捕获\n");

// SSL Pinning 绕过
Java.perform(function(){
    try {
        Java.use("com.android.org.conscrypt.TrustManagerImpl")
            .verifyChain.implementation = function(){ return arguments[0]; };
    } catch(e){}
});

setTimeout(function(){
Java.perform(function(){

    // ═══ 1. Hook getOKRequest — 完整请求头 ═══
    try {
        var Adapter = Java.use("io.dcloud.feature.weex.adapter.DCWXHttpAdapter");
        Adapter.getOKRequest.implementation = function(builder, wxReq, listener){
            var okReq = this.getOKRequest(builder, wxReq, listener);
            try {
                var url = okReq.url().toString();
                if (url.indexOf("yhykwch") !== -1) {
                    var h = okReq.headers();
                    console.log("\n╔══════════════════════════════════════");
                    console.log("║ [REQ] " + okReq.method() + " " + url.split(".com")[1]);
                    for (var i = 0; i < h.size(); i++) {
                        var name = h.name(i);
                        if (name === "AppVerify" || name === "Token") {
                            console.log("║ [" + name + "] " + h.value(i));
                        }
                    }
                    console.log("╚══════════════════════════════════════");
                }
            } catch(e){}
            return okReq;
        };
        console.log("[✓] getOKRequest hook");
    } catch(e){ console.log("[!] " + e.message); }

    // ═══ 2. Hook Response — 完整响应体 ═══
    try {
        var Adapter2 = Java.use("io.dcloud.feature.weex.adapter.DCWXHttpAdapter");
        Adapter2.readInputStreamAsBytes.implementation = function(is, l){
            var r = this.readInputStreamAsBytes(is, l);
            if (r) {
                try {
                    var s = Java.use("java.lang.String").$new(r, "UTF-8").toString();
                    if (s.indexOf('"encryptData"') !== -1) {
                        var json = JSON.parse(s);
                        if (json.encryptData && json.encryptSign) {
                            var ivPos = (json.encryptData.length - 24) % 128;
                            var embIV = json.encryptData.substr(ivPos, 24);
                            console.log("\n┌──────────────────────────────────────");
                            console.log("│ [RESP] code=" + json.code);
                            console.log("│ [ENC-DATA] " + json.encryptData);
                            console.log("│ [ENC-SIGN] " + json.encryptSign);
                            console.log("│ [IV-POS] " + ivPos + "  [EMB-IV] " + embIV);
                            console.log("└──────────────────────────────────────");
                        }
                    } else if (s.indexOf('"code"') !== -1 && s.length < 500) {
                        console.log("[RESP-PLAIN] " + s);
                    }
                } catch(e){}
            }
            return r;
        };
        console.log("[✓] Response hook");
    } catch(e){}

    // ═══ 3. Hook WXStreamModule.fetch — JS层参数 ═══
    try {
        var Stream = Java.use("com.taobao.weex.http.WXStreamModule");
        Stream.fetch.overload(
            "com.alibaba.fastjson.JSONObject",
            "com.taobao.weex.bridge.JSCallback",
            "com.taobao.weex.bridge.JSCallback"
        ).implementation = function(opts, respCb, progressCb){
            try {
                var url = opts.getString("url");
                if (url && url.indexOf("yhykwch") !== -1) {
                    var keys = opts.keySet().toArray();
                    var info = {};
                    for (var i = 0; i < keys.length; i++) {
                        var k = keys[i].toString();
                        var v = opts.get(k);
                        if (v !== null) {
                            var vs = v.toString();
                            info[k] = vs.length < 500 ? vs : vs.substring(0,100) + "...";
                        }
                    }
                    console.log("[FETCH-OPTS] " + JSON.stringify(info));
                }
            } catch(e){}
            this.fetch(opts, respCb, progressCb);
        };
        console.log("[✓] WXStreamModule.fetch hook");
    } catch(e){}

    console.log("\n══ 就绪! 操作APP, 日志保存: frida ... | tee capture.txt ══\n");
});
}, 12000);
