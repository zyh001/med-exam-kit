#!/usr/bin/env node
/**
 * 医海医考 app-service.js 混淆字符串解码器
 *
 * 用法:
 *   node decode_obfuscation.js /path/to/app-service.js 0x652 0x1ea1 0x25ba
 *   node decode_obfuscation.js /path/to/app-service.js --scan  # 扫描所有hex引用
 *
 * 原理:
 *   app-service.js 使用 a1_0x43dc(hexValue) 查表替换所有有意义的字符串.
 *   本脚本从JS中提取查表函数和字符串数组, eval执行后可解码任意值.
 */

const fs = require('fs');
const path = require('path');

if (process.argv.length < 3) {
    console.log('Usage: node decode_obfuscation.js <app-service.js> [hex values | --scan]');
    process.exit(1);
}

const jsPath = process.argv[2];
const data = fs.readFileSync(jsPath, 'utf8');

// 提取字符串数组函数和解码函数
const arrStart = data.indexOf("function a1_0x1557(){");
let depth = 0, arrEnd = arrStart;
for (let i = arrStart; i < arrStart + 500000; i++) {
    if (data[i] === '{') depth++;
    if (data[i] === '}') { depth--; if (depth === 0) { arrEnd = i + 1; break; } }
}

const decStart = data.indexOf("function a1_0x43dc(");
depth = 0;
let decEnd = decStart;
for (let i = decStart; i < decStart + 2000; i++) {
    if (data[i] === '{') depth++;
    if (data[i] === '}') { depth--; if (depth === 0) { decEnd = i + 1; break; } }
}

// 加载 shuffle 初始化代码
const shuffleCode = data.substring(0, data.indexOf("function a1_0x1557()"));

eval(data.substring(arrStart, arrEnd) + "\n" + data.substring(decStart, decEnd) + "\n" + shuffleCode);

const decode = a1_0x43dc;

if (process.argv[3] === '--scan') {
    // 扫描模式: 找出所有使用的hex值并解码
    const hexRefs = new Set();
    const regex = /0x([a-f0-9]{2,4})(?=[\)\],}])/g;
    let match;
    while ((match = regex.exec(data)) !== null) {
        hexRefs.add(parseInt(match[1], 16));
    }

    console.log(`Found ${hexRefs.size} unique hex references\n`);
    const results = [];
    for (const hex of hexRefs) {
        try {
            const val = decode(hex);
            if (val && val.length < 50) {
                results.push({ hex: '0x' + hex.toString(16), value: val });
            }
        } catch(e) {}
    }
    results.sort((a, b) => a.value.localeCompare(b.value));
    for (const r of results) {
        console.log(`${r.hex.padEnd(8)} → ${r.value}`);
    }
} else {
    // 单个解码模式
    for (let i = 3; i < process.argv.length; i++) {
        const hex = parseInt(process.argv[i], 16);
        try {
            console.log(`${process.argv[i]} → "${decode(hex)}"`);
        } catch(e) {
            console.log(`${process.argv[i]} → [error: ${e.message}]`);
        }
    }
}
