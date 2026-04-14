// ── Session Token（由服务端启动时生成，嵌入此页面）──────────────────────
// 所有 /api/* 请求必须通过 apiFetch() 发出，它会自动附加此 Token。
// 外部脚本/页面无法获知 Token，因此无法伪造 API 请求（含写操作）。

let info = {};
let state = { page:1, pages:1, total:0 };
let currentQi = null;
let currentSi = 0;
let currentQData = null;
let deleteTarget = null;
let deleteTargetType = null;
let formDirty = false;
let searchTimer = null;

// Theme
function applyTheme(t) {
  document.documentElement.dataset.theme = t;
  localStorage.setItem('editor-theme', t);
  document.querySelectorAll('.theme-opt').forEach(el => el.classList.toggle('active', el.dataset.t === t));
}
function toggleThemeMenu() { document.getElementById('themeMenu').classList.toggle('open'); }
function closeThemeMenu()  { document.getElementById('themeMenu').classList.remove('open'); }
document.querySelectorAll('.theme-opt').forEach(el => { el.onclick = () => { applyTheme(el.dataset.t); closeThemeMenu(); }; });
applyTheme(localStorage.getItem('editor-theme') || 'dark');

// Init
async function init() {
  info = await apiFetch('/api/info').then(r => r.json());
  document.getElementById('bankName').textContent = info.bank_path?.split(/[\\/]/).pop() || '';
  updateTopStats();
  populateFilters();
  loadList();
}

function populateFilters() {
  // Fix: use explicit index check, not forEach index parameter
  const groups = [
    { ids: ['filterMode','rMode','newQMode'], items: info.modes  },
    { ids: ['filterUnit','rUnit','newQUnit'], items: info.units  },
  ];
  groups.forEach(({ ids, items }) => {
    ids.forEach(id => {
      const sel = document.getElementById(id);
      if (!sel) return;
      const existing = [...sel.options].map(o => o.value);
      (items||[]).forEach(v => {
        if (!existing.includes(v)) { const o = document.createElement('option'); o.value = o.textContent = v; sel.appendChild(o); }
      });
    });
  });
}

function updateTopStats() {
  document.getElementById('topStats').textContent = `${info.total_q||0} 大题 · ${info.total_sq||0} 小题`;
  document.getElementById('dirtyDot').classList.toggle('show', !!(info.dirty));
}

function openMobileEditor()  { if (window.innerWidth <= 768) document.body.classList.add('editor-open'); }
function closeMobileEditor() { document.body.classList.remove('editor-open'); }

// List
async function loadList() {
  const params = new URLSearchParams({
    page:50, per_page:50,
    q:    document.getElementById('searchInput').value,
    fp:   document.getElementById('fpInput').value,
    mode: document.getElementById('filterMode').value,
    unit: document.getElementById('filterUnit').value,
    has_ai:  document.getElementById('filterAI').checked    ? '1':'',
    missing: document.getElementById('filterMissing').checked ? '1':'',
  });
  params.set('page', state.page);
  const data = await apiFetch('/api/questions?'+params).then(r => r.json());
  state.page  = data.page;
  state.pages = data.pages || 1;
  state.total = data.total;

  document.getElementById('result-count').textContent =
    `共 ${data.total} 小题` + (data.total !== data.items.length ? `，当前页 ${data.items.length} 条`:'');

  const list = document.getElementById('qlist');
  list.innerHTML = '';
  data.items.forEach(row => {
    const div = document.createElement('div');
    div.className = 'q-item' + (row.qi===currentQi && row.si===currentSi ? ' active':'');
    const miss = !row.answer || !row.discuss;
    div.innerHTML = `
      <div class="q-item-header">
        <span class="q-idx">Q${row.qi+1}${row.sub_total>1?`-${row.si+1}`:''}</span>
        <div class="q-meta"><span class="q-mode">${esc(row.mode)}</span><span class="q-fp">${(row.fingerprint||'').slice(0,8)}</span></div>
      </div>
      <div class="q-text">${esc(row.text||row.stem||'（无题文）')}</div>
      <div class="q-badges">
        ${row.has_ai?'<span class="badge ai">AI</span>':''}
        ${miss?'<span class="badge miss">缺内容</span>':''}
      </div>`;
    div.onclick = () => selectQuestion(row.qi, row.si);
    list.appendChild(div);
  });

  document.getElementById('btnPrev').disabled = state.page <= 1;
  document.getElementById('btnNext').disabled = state.page >= state.pages;
  document.getElementById('pageInfo').textContent = `${state.page} / ${state.pages}`;
}

function debounceSearch() { clearTimeout(searchTimer); searchTimer = setTimeout(() => { state.page=1; loadList(); }, 300); }
function prevPage() { if (state.page > 1)           { state.page--; loadList(); } }
function nextPage() { if (state.page < state.pages) { state.page++; loadList(); } }
function jumpPage(v) { const p=parseInt(v); if (!isNaN(p)&&p>=1&&p<=state.pages) { state.page=p; loadList(); } }

// Dirty tracking
function markFormDirty() { formDirty = true; }
function markFormClean() { formDirty = false; }
function confirmLeave()  { if (!formDirty) return true; return confirm('当前编辑内容未保存，确定离开？'); }

// Select & render
async function selectQuestion(qi, si) {
  if (!confirmLeave()) return;
  const data = await apiFetch(`/api/question/${qi}`).then(r => r.json());
  currentQi = qi; currentSi = si ?? 0; currentQData = data;
  renderEditor(data, currentSi);
  openMobileEditor();
  loadList();
}

function stripOptionPrefix(opt) { return (opt||'').replace(/^[A-Z]\.\s*/,''); }

function renderEditor(data, activeSi=0) {
  markFormClean();
  const ed = document.getElementById('editor');
  ed.innerHTML = '';

  const back = document.createElement('div');
  back.id = 'mobileBack'; back.textContent = '← 返回列表'; back.onclick = closeMobileEditor;
  ed.appendChild(back);

  // Nav
  const navDiv = document.createElement('div');
  navDiv.className = 'section';
  navDiv.innerHTML = `<div class="section-body" style="padding:10px 14px">
    <div class="editor-nav">
      <button class="nav-btn" id="btnNavPrev" onclick="navigateQ(-1)">← 上一题</button>
      <span class="nav-center">第 ${data.qi+1} 题 · ${data.sub_questions?.length||1} 个子题</span>
      <button class="nav-btn" id="btnNavNext" onclick="navigateQ(1)">下一题 →</button>
    </div>
  </div>`;
  ed.appendChild(navDiv);
  updateNavButtons();

  // Meta
  const isMultiSub  = data.sub_questions.length > 1;
  const hasStem     = !!(data.stem||'').trim();
  const hasSharedOpts = Array.isArray(data.shared_options) && data.shared_options.length > 0;
  // 共享题干：多子题 OR 已有内容时才显示
  const stemHtml = (isMultiSub || hasStem) ? `
    <div class="field-group" style="margin-top:10px"><span class="field-label">共享题干
      ${''/* 图片上传按钮由 JS 在渲染后插入，避免 esc() 转义 onclick */}
    </span>
      <textarea class="field-input" id="f_stem" oninput="markFormDirty()" placeholder="（输入共享题干）">${esc(data.stem||'')}</textarea>
    </div>` : `<input type="hidden" id="f_stem" value="">`;

  const metaSection = mkSection('元数据', `
    <div class="meta-grid">
      <div class="field-group"><span class="field-label">题型</span>
        <input class="field-input" id="f_mode" value="${esc(data.mode)}" oninput="markFormDirty()"></div>
      <div class="field-group"><span class="field-label">章节</span>
        <input class="field-input" id="f_unit" value="${esc(data.unit)}" oninput="markFormDirty()"></div>
      <div class="field-group"><span class="field-label">分类</span>
        <input class="field-input" id="f_cls" value="${esc(data.cls)}" oninput="markFormDirty()"></div>
    </div>
    ${stemHtml}`);
  const fp0 = data.sub_questions[0]?.fingerprint||'';
  if (fp0) {
    const fpEl = document.createElement('span');
    fpEl.className='sec-fp'; fpEl.title='点击复制：'+fp0;
    fpEl.textContent = fp0.slice(0,12)+'…';
    fpEl.onclick = () => navigator.clipboard?.writeText(fp0).then(()=>toast('已复制指纹'));
    metaSection.querySelector('.section-header').appendChild(fpEl);
  }
  ed.appendChild(metaSection);

  // B型共享选项（shared_options 非空时显示独立编辑区块）
  if (hasSharedOpts || data.mode === 'B1') {
    const soHtml = (data.shared_options||[]).map((o,i) => `
      <div class="option-row" data-soi="${i}">
        <span class="drag-handle">⠿</span>
        <span class="option-label">${String.fromCharCode(65+i)}</span>
        <input class="option-input so-input" value="${esc(stripOptionPrefix(o))}" data-soi="${i}" oninput="markFormDirty()">
        <button class="btn-icon" onclick="removeSharedOpt(${i})" title="删除">✕</button>
      </div>`).join('');
    const soSection = mkSection('B型共享选项', `
      <div style="font-size:11px;color:var(--muted);margin-bottom:10px">该组所有子题共用以下选项，子题的"选项"区无需重复填写</div>
      <div class="options-list" id="sharedOptsList">${soHtml}</div>
      <button class="btn btn-ghost" style="margin-top:10px;font-size:12px" onclick="addSharedOpt()">＋ 添加共享选项</button>`);
    soSection.querySelector('.section-header').innerHTML += `
      <span style="font-size:10px;color:var(--ai-color);background:var(--ai-bg);padding:2px 8px;border-radius:5px">B型题</span>`;
    ed.appendChild(soSection);
  }

  // 子题标签：只在多子题时显示
  if (isMultiSub) {
    const tabsDiv = document.createElement('div');
    tabsDiv.className = 'section';
    let tabsHtml = data.sub_questions.map((sq,i) =>
      `<div class="sub-tab ${i===activeSi?'active':''}" onclick="selectSub(${data.qi},${i})">子题 ${i+1}${!sq.answer?' ❓':''}</div>`
    ).join('');
    tabsHtml += `<button class="sub-tab" style="border-style:dashed" onclick="addSubQuestion()">＋ 添加子题</button>`;
    tabsDiv.innerHTML = `<div class="section-body"><div class="sub-tabs">${tabsHtml}</div></div>`;
    ed.appendChild(tabsDiv);
  }

  const sq = data.sub_questions[activeSi];
  currentSi = activeSi;

  ed.appendChild(mkSection('题目文字',
    `<textarea class="field-input tall" id="f_text" oninput="markFormDirty()">${esc(sq.text)}</textarea>`));

  const optHtml = (sq.options||[]).map((o,i) => buildOptRow(o,i)).join('');
  ed.appendChild(mkSection('选项', `
    <div class="options-list" id="optionsList">${optHtml}</div>
    <button class="btn btn-ghost" style="margin-top:10px;font-size:12px" onclick="addOption()">＋ 添加选项</button>`));
  initDragSort();

  const aiAnsBadge = sq.ai_answer  ? `<span class="ai-badge">AI: ${esc(sq.ai_answer)}</span>`:'';
  const aiDisBadge = sq.ai_discuss ? `<span class="ai-badge">AI</span>`:'';
  ed.appendChild(mkSection('答案与解析', `
    <div class="answer-row">
      <div class="field-group"><span class="field-label">答案 ${aiAnsBadge}</span>
        <input class="field-input" id="f_answer" value="${esc(sq.answer)}"
               style="${sq.answer_source==='ai'?'border-color:var(--ai-color)':''}"
               oninput="markFormDirty();updatePreview()"></div>
      <div class="field-group"><span class="field-label">考点</span>
        <input class="field-input" id="f_point" value="${esc(sq.point)}" oninput="markFormDirty()"></div>
      <div class="field-group"><span class="field-label">正确率（%）
        <span style="font-weight:400;font-size:10px;color:var(--muted)">${sq.rate!=null?sq.rate:'无'}</span></span>
        <input class="field-input" id="f_rate" type="number" min="0" max="100" step="0.1"
               value="${esc(sq.rate!=null?String(sq.rate).replace('%',''):'')}"
               placeholder="0–100" oninput="markFormDirty()"></div>
    </div>
    <div class="field-group" style="margin-top:12px"><span class="field-label">解析 ${aiDisBadge}</span>
      <textarea class="field-input tall" id="f_discuss"
        style="${sq.discuss_source==='ai'?'border-color:var(--ai-color)':''}"
        oninput="markFormDirty()">${esc(sq.discuss)}</textarea></div>
    ${sq.ai_discuss?`
    <div class="field-group" style="margin-top:10px">
      <span class="field-label" style="color:var(--ai-color)">AI 解析原文（只读参考）</span>
      <textarea class="field-input tall" readonly style="color:var(--ai-color);opacity:.7;cursor:default">${esc(sq.ai_discuss)}</textarea>
    </div>`:''}
  `));

  const actDiv = document.createElement('div');
  actDiv.className = 'section';
  const canDelSub = data.sub_questions.length > 1;
  actDiv.innerHTML = `<div class="section-body">
    <div class="action-bar">
      <button class="btn btn-primary" onclick="saveSubQuestion()">💾 保存此题</button>
      <button class="btn btn-ghost"   onclick="togglePreview()" id="btnPreview">👁 预览</button>
      ${!isMultiSub ? `<button class="btn btn-ghost" onclick="addSubQuestion()">＋ 添加子题</button>` : ''}
      ${canDelSub ? `<button class="btn btn-warn" onclick="openDeleteSubQuestion(${data.qi},${activeSi})">删除此子题</button>` : ''}
      <button class="btn btn-danger"  onclick="openDelete(${data.qi},'question')">删除整道题</button>
    </div>
    <div class="preview-pane" id="previewPane"></div>
  </div>`;
  ed.appendChild(actDiv);

  // 挂载图片上传按钮（仅在 S3 已配置时）
  if (info.s3_enabled) {
    _mountImgUploadBtn('f_stem',    '题干');
    _mountImgUploadBtn('f_text',    '题目文字');
    _mountImgUploadBtn('f_discuss', '解析');
  }
}

function buildOptRow(o, i) {
  return `<div class="option-row" draggable="true" data-oi="${i}">
    <span class="drag-handle" title="拖拽排序">⠿</span>
    <span class="option-label">${String.fromCharCode(65+i)}</span>
    <input class="option-input" value="${esc(stripOptionPrefix(o))}" data-oi="${i}" oninput="markFormDirty();updatePreview()">
    <button class="btn-icon" onclick="removeOption(${i})" title="删除">✕</button>
  </div>`;
}

function mkSection(title, bodyHtml) {
  const div = document.createElement('div');
  div.className = 'section';
  div.innerHTML = `<div class="section-header"><span class="sec-title">${title}</span></div><div class="section-body">${bodyHtml}</div>`;
  return div;
}

// Navigation
function updateNavButtons() {
  document.getElementById('btnNavPrev')?.toggleAttribute('disabled', currentQi <= 0);
  document.getElementById('btnNavNext')?.toggleAttribute('disabled', currentQi >= (info.total_q-1));
}
async function navigateQ(dir) {
  if (!confirmLeave()) return;
  const next = currentQi + dir;
  if (next < 0 || next >= info.total_q) return;
  const data = await apiFetch(`/api/question/${next}`).then(r => r.json());
  currentQi = next; currentSi = 0; currentQData = data;
  renderEditor(data, 0);
}

// Sub-tab switch (uses cache)
function selectSub(qi, si) {
  if (!confirmLeave()) return;
  if (currentQData && currentQData.qi === qi) { currentSi=si; renderEditor(currentQData, si); }
  else selectQuestion(qi, si);
}

// Options
function getOptions() { return [...document.querySelectorAll('#optionsList .option-input')].map(i=>i.value.trim()); }
function addOption() {
  const list = document.getElementById('optionsList');
  const i = list.querySelectorAll('.option-row').length;
  const row = document.createElement('div');
  row.className='option-row'; row.draggable=true; row.dataset.oi=i;
  row.innerHTML=`<span class="drag-handle">⠿</span><span class="option-label">${String.fromCharCode(65+i)}</span>
    <input class="option-input" value="" data-oi="${i}" oninput="markFormDirty();updatePreview()">
    <button class="btn-icon" onclick="removeOption(${i})" title="删除">✕</button>`;
  list.appendChild(row); reindexOptions(); initDragSort(); markFormDirty();
}
function removeOption(idx) {
  const rows = document.getElementById('optionsList').querySelectorAll('.option-row');
  rows[idx]?.remove(); reindexOptions(); markFormDirty(); updatePreview();
}
function reindexOptions() {
  document.getElementById('optionsList').querySelectorAll('.option-row').forEach((row,i)=>{
    row.dataset.oi=i;
    row.querySelector('.option-label').textContent=String.fromCharCode(65+i);
    row.querySelector('.option-input').dataset.oi=i;
    const btn=row.querySelector('.btn-icon'); if(btn) btn.setAttribute('onclick',`removeOption(${i})`);
  });
}

// Shared options (B-type)
function getSharedOpts() {
  return [...document.querySelectorAll('#sharedOptsList .so-input')].map(i=>i.value.trim());
}
function addSharedOpt() {
  const list = document.getElementById('sharedOptsList');
  if (!list) return;
  const i = list.querySelectorAll('.option-row').length;
  const row = document.createElement('div');
  row.className='option-row'; row.dataset.soi=i;
  row.innerHTML=`<span class="drag-handle">⠿</span>
    <span class="option-label">${String.fromCharCode(65+i)}</span>
    <input class="option-input so-input" value="" data-soi="${i}" oninput="markFormDirty()">
    <button class="btn-icon" onclick="removeSharedOpt(${i})" title="删除">✕</button>`;
  list.appendChild(row); reindexSharedOpts(); markFormDirty();
}
function removeSharedOpt(idx) {
  const rows = document.getElementById('sharedOptsList')?.querySelectorAll('.option-row');
  rows?.[idx]?.remove(); reindexSharedOpts(); markFormDirty();
}
function reindexSharedOpts() {
  document.getElementById('sharedOptsList')?.querySelectorAll('.option-row').forEach((row,i)=>{
    row.dataset.soi=i;
    row.querySelector('.option-label').textContent=String.fromCharCode(65+i);
    row.querySelector('.so-input').dataset.soi=i;
    const btn=row.querySelector('.btn-icon'); if(btn) btn.setAttribute('onclick',`removeSharedOpt(${i})`);
  });
}

// Drag-to-reorder
function initDragSort() {
  const list = document.getElementById('optionsList');
  if (!list) return;
  let dragged = null;
  list.querySelectorAll('.option-row').forEach(row => {
    row.addEventListener('dragstart', e => { dragged=row; row.classList.add('dragging'); e.dataTransfer.effectAllowed='move'; });
    row.addEventListener('dragend',   () => {
      row.classList.remove('dragging');
      list.querySelectorAll('.option-row').forEach(r=>r.classList.remove('drag-over'));
      dragged=null; reindexOptions(); markFormDirty(); updatePreview();
    });
    row.addEventListener('dragover',  e => { e.preventDefault(); list.querySelectorAll('.option-row').forEach(r=>r.classList.remove('drag-over')); if(row!==dragged)row.classList.add('drag-over'); });
    row.addEventListener('drop',      e => { e.preventDefault(); if(dragged&&row!==dragged){ const all=[...list.querySelectorAll('.option-row')]; if(all.indexOf(dragged)<all.indexOf(row)) list.insertBefore(dragged,row.nextSibling); else list.insertBefore(dragged,row); }});
  });
}

// Save
async function saveSubQuestion() {
  const rateRaw = document.getElementById('f_rate')?.value?.trim();
  // 收集 B型共享选项
  const soInputs = document.querySelectorAll('#sharedOptsList .so-input');
  const sharedOptions = soInputs.length > 0
    ? [...soInputs].map(i => i.value.trim())
    : undefined;
  const payload = {
    mode:    document.getElementById('f_mode')?.value??'',
    unit:    document.getElementById('f_unit')?.value??'',
    cls:     document.getElementById('f_cls')?.value??'',
    stem:    document.getElementById('f_stem')?.value??'',
    text:    document.getElementById('f_text').value,
    answer:  document.getElementById('f_answer').value.trim().toUpperCase(),
    discuss: document.getElementById('f_discuss').value,
    point:   document.getElementById('f_point').value,
    rate:    rateRaw!==''?parseFloat(rateRaw):null,
    options: getOptions(),
  };
  if (sharedOptions !== undefined) payload.shared_options = sharedOptions;
  const res = await apiFetch(`/api/subquestion/${currentQi}/${currentSi}`,{
    method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(payload)
  }).then(r=>r.json());
  if (res.ok) {
    info.dirty=true; markFormClean(); updateTopStats();
    if (currentQData&&currentQData.qi===currentQi) {
      const sq=currentQData.sub_questions[currentSi];
      Object.assign(sq,{answer:payload.answer,discuss:payload.discuss,text:payload.text,point:payload.point,rate:payload.rate,options:payload.options});
      currentQData.mode=payload.mode; currentQData.unit=payload.unit; currentQData.stem=payload.stem;
    }
    loadList(); toast('✓ 已保存');
  } else toast('保存失败：'+(res.error||''),true);
}

// New question
function openNewQuestion() { populateFilters(); document.getElementById('newQModal').style.display='flex'; }
function closeNewQuestion(){ document.getElementById('newQModal').style.display='none'; }
async function confirmNewQuestion() {
  const mode=document.getElementById('newQMode').value;
  const unit=document.getElementById('newQUnit').value;
  const res=await apiFetch('/api/question',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({mode,unit})}).then(r=>r.json());
  closeNewQuestion();
  if(res.ok){info.total_q++;info.dirty=true;updateTopStats();loadList();selectQuestion(res.qi,0);toast('✓ 新题已创建');}
  else toast('创建失败：'+(res.error||''),true);
}

// Add sub-question
async function addSubQuestion() {
  if (!confirmLeave()) return;
  const res=await apiFetch(`/api/question/${currentQi}/subquestion`,{method:'POST',headers:{'Content-Type':'application/json'},body:'{}'}).then(r=>r.json());
  if(res.ok){
    info.dirty=true;updateTopStats();
    const data=await apiFetch(`/api/question/${currentQi}`).then(r=>r.json());
    currentQData=data; renderEditor(data,res.si); toast('✓ 子题已添加');
  } else toast('添加失败：'+(res.error||''),true);
}

// Delete
function openDelete(qi,type='question'){
  deleteTarget=qi; deleteTargetType=type;
  document.getElementById('deleteModalDesc').textContent='删除后不可撤销（保存前可关闭页面放弃更改）。确定要删除这道大题（含全部子题）吗？';
  document.getElementById('deleteModal').style.display='flex';
}
function openDeleteSubQuestion(qi,si){
  deleteTarget={qi,si}; deleteTargetType='subquestion';
  document.getElementById('deleteModalDesc').textContent=`确定删除子题 ${si+1}？删除后不可撤销。`;
  document.getElementById('deleteModal').style.display='flex';
}
function closeDelete(){ document.getElementById('deleteModal').style.display='none'; deleteTarget=null; }
async function confirmDelete(){
  if(deleteTarget===null)return;
  if(deleteTargetType==='subquestion'){
    const{qi,si}=deleteTarget;
    const res=await apiFetch(`/api/subquestion/${qi}/${si}`,{method:'DELETE'}).then(r=>r.json());
    closeDelete();
    if(res.ok){info.dirty=true;updateTopStats();const data=await apiFetch(`/api/question/${qi}`).then(r=>r.json());currentQData=data;renderEditor(data,Math.min(si,res.sub_total-1));loadList();toast('子题已删除');}
    else toast(res.error||'删除失败',true);
  } else {
    const res=await apiFetch(`/api/question/${deleteTarget}`,{method:'DELETE'}).then(r=>r.json());
    closeDelete();
    if(res.ok){currentQi=null;currentQData=null;info.dirty=true;info.total_q=res.total;markFormClean();updateTopStats();loadList();closeMobileEditor();
      document.getElementById('editor').innerHTML='<div id="mobileBack" onclick="closeMobileEditor()">← 返回列表</div><div class="empty"><span class="empty-icon">✎</span>从左侧选择一道题目开始编辑</div>';
      toast(`已删除，剩余 ${res.total} 大题`);
    } else toast('删除失败',true);
  }
}

// Preview
function togglePreview(){
  const pane=document.getElementById('previewPane'); if(!pane)return;
  pane.classList.toggle('show');
  document.getElementById('btnPreview').textContent=pane.classList.contains('show')?'👁 隐藏预览':'👁 预览';
  if(pane.classList.contains('show'))updatePreview();
}
function updatePreview(){
  const pane=document.getElementById('previewPane'); if(!pane||!pane.classList.contains('show'))return;
  const text=document.getElementById('f_text')?.value||'';
  const stem=document.getElementById('f_stem')?.value||'';
  const answer=(document.getElementById('f_answer')?.value||'').toUpperCase();
  const discuss=document.getElementById('f_discuss')?.value||'';
  // B型：优先用共享选项；否则用子题自己的选项
  const soInputs = document.querySelectorAll('#sharedOptsList .so-input');
  const opts = soInputs.length > 0 ? [...soInputs].map(i=>i.value.trim()) : getOptions();
  const ansSet=new Set(answer.split(''));
  const optsHtml=opts.map((o,i)=>{
    const lbl=String.fromCharCode(65+i); const cor=ansSet.has(lbl);
    return `<div class="preview-opt ${cor?'correct':'normal'}"><span class="preview-opt-lbl">${lbl}</span><span>${esc(o)}</span></div>`;
  }).join('');
  pane.innerHTML=`<div class="preview-title">题目预览</div>
    ${stem?`<div class="preview-stem">${escWithImg(stem)}</div>`:''}
    <div class="preview-q">${escWithImg(text)}</div>
    ${optsHtml}
    ${answer?`<div class="preview-ans">✓ 答案：${answer}</div>`:''}
    ${discuss?`<div class="preview-discuss">📝 ${escWithImg(discuss.slice(0,300))}${discuss.length>300?'…':''}</div>`:''}`;
}

// Replace
function openReplace(){
  document.getElementById('replaceModal').style.display='flex';
  document.getElementById('replacePreviewBox').innerHTML='';
  document.getElementById('replacePreviewBox').classList.remove('show');
  document.getElementById('replacePreviewStatus').textContent='';
  document.getElementById('btnReplaceExec').disabled=true;
}
function closeReplace(){ document.getElementById('replaceModal').style.display='none'; }
async function doReplacePreview(){
  const find=document.getElementById('rFind').value;
  const fields=[...document.querySelectorAll('.checkbox-group input:checked')].map(c=>c.value);
  const mode=document.getElementById('rMode').value;
  const unit=document.getElementById('rUnit').value;
  if(!find){toast('查找文本不能为空',true);return;}
  if(!fields.length){toast('请至少选择一个字段',true);return;}
  document.getElementById('replacePreviewStatus').textContent='查询中…';
  const res=await apiFetch('/api/replace/preview',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({find,fields,mode,unit,limit:30})}).then(r=>r.json());
  const box=document.getElementById('replacePreviewBox');
  if(!res.hits?.length){box.innerHTML='';box.classList.remove('show');document.getElementById('replacePreviewStatus').textContent='未找到匹配内容';document.getElementById('btnReplaceExec').disabled=true;return;}
  box.classList.add('show');
  box.innerHTML=res.hits.map(h=>`<div class="rp-row"><div class="rp-meta">Q${h.qi+1}-${h.si+1} · ${esc(h.mode)} · ${esc(h.unit)} · 字段:${h.field}</div><div class="rp-ctx">…${esc(h.before)}<span class="rp-match">${esc(h.match)}</span>${esc(h.after)}…</div></div>`).join('')
    +(res.truncated?`<div class="rp-truncated">仅显示前 30 条，实际可能更多</div>`:'');
  document.getElementById('replacePreviewStatus').textContent=`共命中 ${res.total} 处`;
  document.getElementById('btnReplaceExec').disabled=false;
}
async function doReplace(){
  const find=document.getElementById('rFind').value;
  const repl=document.getElementById('rReplace').value;
  const fields=[...document.querySelectorAll('.checkbox-group input:checked')].map(c=>c.value);
  const mode=document.getElementById('rMode').value;
  const unit=document.getElementById('rUnit').value;
  if(!find){toast('查找文本不能为空',true);return;}
  if(!fields.length){toast('请至少选择一个作用字段',true);return;}
  const res=await apiFetch('/api/replace',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({find,replace:repl,fields,mode,unit})}).then(r=>r.json());
  if(res.ok){info.dirty=true;updateTopStats();closeReplace();loadList();toast(`替换完成：共 ${res.replaced} 处`);}
  else toast('替换失败: '+(res.error||''),true);
}

// Stats
function closeStats(){ document.getElementById('statsModal').style.display='none'; }
async function openStats(){
  document.getElementById('statsModal').style.display='flex';
  document.getElementById('statsContent').innerHTML='<div style="text-align:center;color:var(--muted);padding:24px">加载中…</div>';
  const d=await apiFetch('/api/stats').then(r=>r.json());
  const tsq=d.total_sq||1;
  const mp=a=>Math.round(a/tsq*100);
  document.getElementById('statsContent').innerHTML=`
    <div class="stats-grid" style="margin-bottom:16px">
      <div class="stat-card"><div class="stat-num" style="color:var(--accent)">${d.total_q}</div><div class="stat-label">大题总数</div></div>
      <div class="stat-card"><div class="stat-num" style="color:var(--accent)">${d.total_sq}</div><div class="stat-label">小题总数</div></div>
      <div class="stat-card ${d.has_ai>0?'warn':''}"><div class="stat-num" style="color:var(--ai-color)">${d.has_ai}</div><div class="stat-label">含 AI 内容</div></div>
      <div class="stat-card ${d.missing_answer>0?'danger':'success'}"><div class="stat-num">${d.missing_answer}</div><div class="stat-label">缺答案</div>
        <div class="stat-bar-wrap"><div class="stat-bar" style="width:${mp(d.missing_answer)}%;background:var(--danger)"></div></div></div>
      <div class="stat-card ${d.missing_discuss>0?'danger':'success'}"><div class="stat-num">${d.missing_discuss}</div><div class="stat-label">缺解析</div>
        <div class="stat-bar-wrap"><div class="stat-bar" style="width:${mp(d.missing_discuss)}%;background:var(--danger)"></div></div></div>
      <div class="stat-card ${d.missing_both>0?'danger':'success'}"><div class="stat-num">${d.missing_both}</div><div class="stat-label">两者均缺</div>
        <div class="stat-bar-wrap"><div class="stat-bar" style="width:${mp(d.missing_both)}%;background:var(--danger)"></div></div></div>
    </div>
    <div style="margin-bottom:16px"><div class="stats-section-title">难度分布（按正确率）</div>
      <div class="diff-row">
        <span class="diff-chip easy">简单 ${d.difficulty.easy}</span>
        <span class="diff-chip medium">中等 ${d.difficulty.medium}</span>
        <span class="diff-chip hard">较难 ${d.difficulty.hard}</span>
        <span class="diff-chip extreme">极难 ${d.difficulty.extreme}</span>
        <span class="diff-chip unknown">无数据 ${d.difficulty.unknown}</span>
      </div></div>
    <div style="margin-bottom:16px"><div class="stats-section-title">题型分布</div>
      ${Object.entries(d.mode_sq).map(([k,v])=>{const p=Math.round(v/tsq*100);return`<div class="stats-list-row"><span class="stats-list-name">${esc(k)}</span><div class="stats-list-bar-wrap"><div class="stats-list-bar" style="width:${p}%"></div></div><span class="stats-list-val">${v}</span></div>`;}).join('')}
    </div>
    <div><div class="stats-section-title">章节分布（前 20）</div>
      ${Object.entries(d.unit_sq).map(([k,v])=>{const p=Math.round(v/tsq*100);return`<div class="stats-list-row"><span class="stats-list-name">${esc(k)}</span><div class="stats-list-bar-wrap"><div class="stats-list-bar" style="width:${p}%"></div></div><span class="stats-list-val">${v}</span></div>`;}).join('')}
    </div>`;
}

// Save
async function doSave(){
  const res=await apiFetch('/api/save',{method:'POST',headers:{'Content-Type':'application/json'},body:'{}'}).then(r=>r.json());
  if(res.ok){info.dirty=false;updateTopStats();toast('✓ 已保存至 '+res.path.split(/[\\\\/]/).pop());}
  else toast('保存失败: '+(res.error||''),true);
}

function esc(s){ return (s||'').replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;').replace(/'/g,'&#39;'); }

/** 转义文本供预览，但安全还原本站 /api/img/local/ 图片标签（防 XSS）*/
function escWithImg(s) {
  if (!s) return '';
  // esc() 先把所有 HTML 实体化；再把符合白名单路径的 img 标签还原为真实 <img>
  var escaped = esc(s);
  return escaped.replace(
    /&lt;img src=&quot;(\/api\/img\/local\/[a-zA-Z0-9\-_.]{10,80})&quot;(?:[^>]*)?&gt;/g,
    function(_, src) {
      return '<img src="' + src + '" alt="图片" style="max-width:100%;height:auto;border-radius:6px;margin:6px 0;display:block">';
    }
  );
}


// Keyboard shortcuts
document.addEventListener('keydown', e => {
  if ((e.ctrlKey||e.metaKey)&&e.key==='s') { e.preventDefault(); if(formDirty)saveSubQuestion(); else if(info.dirty)doSave(); }
  if (e.key==='Escape') { document.querySelectorAll('.modal-backdrop').forEach(el=>{ if(el.style.display!=='none')el.style.display='none'; }); closeThemeMenu(); }
  if (e.altKey&&e.key==='ArrowLeft')  navigateQ(-1);
  if (e.altKey&&e.key==='ArrowRight') navigateQ(1);
});

window.addEventListener('beforeunload', e => { if(info.dirty||formDirty){ e.preventDefault(); e.returnValue=''; } });

// ── 图片上传（仅 S3 配置时可用）──────────────────────────────────────

/**
 * 在 id=fieldId 的 textarea 旁插入「📷 插入图片」按钮。
 * 点击后弹出文件选择，上传成功后在光标处插入 <img> 标签。
 */
function _mountImgUploadBtn(fieldId, label) {
  const ta = document.getElementById(fieldId);
  if (!ta) return;

  // 找到包裹该 textarea 的 .field-group，在 .field-label 里追加按钮
  const group = ta.closest('.field-group');
  if (!group) return;
  const lbl = group.querySelector('.field-label');
  if (!lbl) return;

  // 避免重复挂载
  if (lbl.querySelector('.img-upload-btn')) return;

  const btn = document.createElement('button');
  btn.className = 'img-upload-btn';
  btn.type = 'button';
  btn.title = '上传图片到 S3 并在光标处插入';
  btn.innerHTML = '📷';

  // 隐藏 file input
  const inp = document.createElement('input');
  inp.type = 'file';
  inp.accept = 'image/*';
  inp.style.display = 'none';
  inp.addEventListener('change', () => _doImgUpload(inp, ta));
  document.body.appendChild(inp);

  btn.addEventListener('click', (e) => {
    e.preventDefault();
    inp.value = ''; // 允许重复选同一文件
    inp.click();
  });

  lbl.appendChild(btn);
}

/** 执行上传并在 textarea 光标处插入 <img> 标签 */
async function _doImgUpload(inp, ta) {
  const file = inp.files[0];
  if (!file) return;

  if (file.size > 10 * 1024 * 1024) {
    toast('图片不能超过 10 MB', true);
    return;
  }
  if (!file.type.startsWith('image/')) {
    toast('只支持图片格式', true);
    return;
  }

  // 找到对应按钮，显示 loading 状态
  const btn = ta.closest('.field-group')?.querySelector('.img-upload-btn');
  const origText = btn ? btn.innerHTML : '';
  if (btn) { btn.innerHTML = '⏳'; btn.disabled = true; }

  try {
    const fd = new FormData();
    fd.append('file', file);

    const res = await apiFetch('/api/img/upload', { method: 'POST', body: fd });
    if (!res.ok) {
      const err = await res.json().catch(() => ({}));
      throw new Error(err.error || `HTTP ${res.status}`);
    }
    const { url } = await res.json();

    // 在光标处插入 <img> 标签
    const tag = `<img src="${url}" alt="图片">`;
    const start = ta.selectionStart ?? ta.value.length;
    const end   = ta.selectionEnd   ?? ta.value.length;
    ta.value = ta.value.slice(0, start) + tag + ta.value.slice(end);
    ta.selectionStart = ta.selectionEnd = start + tag.length;
    ta.focus();
    markFormDirty();
    updatePreview();
    toast('✓ 图片已上传并插入');
  } catch (e) {
    toast('上传失败：' + e.message, true);
  } finally {
    if (btn) { btn.innerHTML = origText; btn.disabled = false; }
  }
}

init();
