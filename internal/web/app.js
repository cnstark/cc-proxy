const app = document.getElementById('app');
let pendingRestart = false;

// project log_level 选项。含 info：validate.go 接受 off/meta/info/debug，
// 且 NewSnapshot 将 meta→info 映射后回写，GET 可能返回 info，故需列出以防数据丢失。
const LOG_LEVELS = [
  {value:'off', label:'off'},
  {value:'meta', label:'meta'},
  {value:'info', label:'info'},
  {value:'debug', label:'debug'},
];

async function api(path, opts={}) {
  const r = await fetch(path, {headers:{'content-type':'application/json'}, ...opts});
  if (r.status === 401) { location.hash='#login'; throw new Error('未登录'); }
  const text = await r.text();
  let data; try { data = text ? JSON.parse(text) : {}; } catch { data = {error:text}; }
  if (!r.ok && !data.error) data.error = 'HTTP '+r.status;
  return {ok:r.ok, status:r.status, data};
}

function el(tag, props={}, children=[]) {
  const e = document.createElement(tag);
  for (const [k,v] of Object.entries(props)) {
    if (k === 'class') e.className = v;
    else if (k.startsWith('on')) e.addEventListener(k.slice(2).toLowerCase(), v);
    else e.setAttribute(k, v);
  }
  for (const c of [].concat(children)) {
    if (c == null) continue;
    e.appendChild(typeof c === 'string' ? document.createTextNode(c) : c);
  }
  return e;
}

async function render() {
  const hash = location.hash.slice(1) || 'config';
  app.innerHTML = '';
  if (logES) { logES.close(); logES = null; }
  if (hash === 'login') return renderLogin();
  // HttpOnly cookie is invisible to JS; probe the authed endpoint instead.
  // (api() already redirects to #login on 401, so a failed probe routes there.)
  if (hash !== 'login') {
    const {ok} = await api('/api/status');
    if (!ok) return; // api() already set location.hash='#login'
  }
  const main = el('div', {class:'main'});
  if (pendingRestart) main.appendChild(el('div',{class:'banner'},'部分更改需重启 ccp-proxy 生效（ccp proxy stop && ccp proxy start）'));
  const layout = el('div', {class:'layout'}, [
    el('div', {class:'nav'}, [
      el('a', {href:'#config'}, '配置'),
      el('a', {href:'#usage'}, '用量'),
      el('a', {href:'#logs'}, '请求日志'),
      el('a', {href:'#', onclick:async()=>{await api('/api/logout',{method:'POST'}); pendingRestart=false; location.hash='#login'; render();}}, '退出'),
    ]),
    main,
  ]);
  app.appendChild(layout);
  main.appendChild(el('div', {id:'page'}));
  if (hash === 'config') renderConfig();
  else if (hash === 'usage') renderUsage();
  else if (hash === 'logs') renderLogs();
  document.querySelectorAll('.nav a').forEach(a => {
    if (a.getAttribute('href') === '#'+hash) a.classList.add('active');
  });
}

function renderLogin() {
  app.innerHTML = '';
  app.appendChild(el('div', {class:'card login'}, [
    el('h2', {}, 'cc-proxy 后台'),
    el('input', {type:'password', id:'pw', placeholder:'密码'}),
    el('button', {onclick:async()=>{
      const {ok} = await api('/api/login',{method:'POST',body:JSON.stringify({password:pw.value})});
      if (ok) { location.hash='#config'; render(); }
      else alert('密码错误');
    }}, '登录'),
  ]));
}

async function renderConfig() {
  const page = document.getElementById('page');
  page.innerHTML = '加载中...';
  const {data} = await api('/api/config');
  page.innerHTML = '';
  const cfg = data || {};
  const ups = cfg.upstreams || [];
  page.appendChild(el('div', {class:'card'}, [
    el('h3', {}, '上游池'),
    el('button', {onclick:()=>addUpstream(cfg)}, '添加上游'),
    el('table', {}, [
      el('tr',{},[el('th',{},'name'),el('th',{},'url'),el('th',{},'models'),el('th',{},'apikey'),el('th',{},'')]),
      ...ups.map(u => el('tr',{},[
        el('td',{},u.name), el('td',{},u.url), el('td',{},(u.models||[]).join(', ')),
        el('td',{},el('code',{},u.apikey||'')),
        el('td',{},[el('button',{class:'ghost',onclick:()=>editUpstream(cfg,u)},'编辑'),
                    el('button',{class:'danger',onclick:()=>delUpstream(cfg,u.name)},'删除')]),
      ])),
    ]),
  ]));
  const projs = cfg.projects || [];
  page.appendChild(el('div', {class:'card'}, [
    el('h3', {}, '项目'),
    el('button', {onclick:()=>addProject(cfg)}, '添加项目'),
    el('table', {}, [
      el('tr',{},[el('th',{},'name'),el('th',{},'log_level'),el('th',{},'映射数'),el('th',{},'direct_access'),el('th',{},'')]),
      ...projs.map(p => el('tr',{},[
        el('td',{},el('a',{href:'#logs'},p.name)),
        el('td',{},p.log_level||'off'),
        el('td',{},String(Object.keys(p.model_map||{}).length)),
        el('td',{},String(p.allow_direct_access||false)),
        el('td',{},[el('button',{class:'ghost',onclick:()=>editProject(cfg,p)},'编辑'),
                    el('button',{class:'danger',onclick:()=>delProject(cfg,p.name)},'删除')]),
      ])),
    ]),
  ]));
  page.appendChild(renderMappings(cfg));
}

function renderMappings(cfg) {
  const projSel = el('select', {id:'mapproj'});
  (cfg.projects||[]).forEach(p => projSel.appendChild(el('option',{value:p.name},p.name)));
  const box = el('div', {id:'mapbox'});
  function drawMap() {
    box.innerHTML='';
    const p = (cfg.projects||[]).find(x=>x.name===projSel.value);
    if (!p) return;
    for (const [model, targets] of Object.entries(p.model_map||{})) {
      box.appendChild(el('div',{},[
        el('code',{},model), ' → ',
        el('span',{class:'muted'}, targets.join(', ')),
        el('button',{class:'danger',onclick:()=>delMapping(cfg,p.name,model)},'删除'),
      ]));
    }
    box.appendChild(el('button',{onclick:()=>addMapping(cfg,projSel.value)},'添加映射'));
  }
  projSel.onchange = drawMap;
  drawMap();
  return el('div', {class:'card'}, [el('h3',{},'模型映射'), projSel, box]);
}

// saveConfig 整份 PUT 配置。成功不自动重渲染（由调用方决定 renderConfig/render）；
// 失败返回 {error} 供弹窗内展示，替换原 alert('保存失败')。
async function saveConfig(cfg) {
  const {ok, data} = await api('/api/config',{method:'PUT',body:JSON.stringify(cfg)});
  if (ok) return {ok:true};
  return {ok:false, error: data.error || '保存失败'};
}
let currentModal = null;

// openModal 弹窗工厂。
// opts: {title, fields:[{key,label,type,...opts}], onSubmit, submitLabel}
// - fields 各项的 type 决定渲染方式（见 renderField）；getValue 收集提交值。
// - onSubmit(values) 可为 async；返回 undefined=成功关闭弹窗，返回 {error}=失败，弹窗内展示不关闭。
// - 基础校验（required 的 text/password 非空、list 至少一项）由本函数在调 onSubmit 前统一做；
//   业务校验（引用存在性等）由后端 config.Validate 兜底（422），前端不重复实现。
// - 取消：Esc / 点 overlay 遮罩 / 取消按钮 → 直接关闭，无副作用（未 PUT）。
// - 同一时刻仅一个弹窗：已有弹窗时再调直接忽略。
function openModal(opts) {
  if (currentModal) return;
  const overlay = el('div', {class:'overlay'});
  const card = el('div', {class:'modal'});
  card.appendChild(el('h3', {}, opts.title));
  const errBox = el('div', {class:'modal-error'});
  const collectors = {};
  for (const f of opts.fields) {
    const {node, getValue} = renderField(f);
    collectors[f.key] = getValue;
    card.appendChild(node);
  }
  card.appendChild(errBox);
  const submitBtn = el('button', {}, opts.submitLabel || '保存');
  card.appendChild(el('div', {class:'modal-actions'}, [
    el('button', {class:'ghost', onclick: close}, '取消'),
    submitBtn,
  ]));
  submitBtn.onclick = submit;
  overlay.appendChild(card);
  overlay.addEventListener('click', e => { if (e.target === overlay) close(); });
  function onKey(e) { if (e.key === 'Escape') close(); }
  document.addEventListener('keydown', onKey);
  function close() {
    overlay.remove();
    currentModal = null;
    document.removeEventListener('keydown', onKey);
  }
  function showError(msg) { errBox.textContent = msg; }
  async function submit() {
    if (submitBtn.disabled) return;
    const values = {};
    for (const k in collectors) values[k] = collectors[k]();
    for (const f of opts.fields) {
      if (!f.required) continue;
      const v = values[f.key];
      if (f.type === 'list' && (!Array.isArray(v) || v.length === 0)) return showError((f.label || f.key) + '不能为空');
      if ((f.type === 'text' || f.type === 'password') && !String(v).trim()) return showError((f.label || f.key) + '不能为空');
    }
    submitBtn.disabled = true;
    let res;
    try {
      res = await opts.onSubmit(values);
    } catch (e) {
      submitBtn.disabled = false;
      return showError(e && e.message ? e.message : '操作失败');
    }
    if (res && res.error) {
      submitBtn.disabled = false;
      return showError(res.error);
    }
    close();
  }
  document.body.appendChild(overlay);
  currentModal = overlay;
}

// confirmModal 删除确认弹窗。onConfirm 返回 undefined=关闭 / {error}=弹窗内展示。
function confirmModal(title, onConfirm) {
  openModal({
    title,
    fields: [],
    submitLabel: '确认删除',
    onSubmit: onConfirm,
  });
}

// renderField 按声明渲染单个字段，返回 {node, getValue}。
// 支持类型：text / password / checkbox / select / list / secret / ordered-list。
function renderField(f) {
  const wrap = el('div', {class:'field'}, [el('label', {}, f.label)]);

  if (f.type === 'text' || f.type === 'password') {
    const inp = el('input', {type: f.type, value: f.value || '', placeholder: f.placeholder || ''});
    if (f.readonly) inp.setAttribute('readonly', '');
    const row = el('div', {class:'field-row'}, [inp]);
    (f.actions || []).forEach(a => row.appendChild(el('button', {class:'ghost', onclick: () => a.onClick(v => { inp.value = v; })}, a.label)));
    wrap.appendChild(row);
    return {node: wrap, getValue: () => inp.value};
  }

  if (f.type === 'checkbox') {
    const inp = el('input', {type:'checkbox'});
    inp.checked = !!f.value;
    wrap.appendChild(inp);
    return {node: wrap, getValue: () => inp.checked};
  }

  if (f.type === 'select') {
    const sel = el('select', {});
    for (const o of f.options) sel.appendChild(el('option', {value:o.value}, o.label));
    // 防止数据丢失：当前值不在 options 中时追加（如历史配置含 info）
    if (f.value && !f.options.some(o => o.value === f.value)) {
      sel.appendChild(el('option', {value:f.value}, f.value));
    }
    sel.value = f.value || (f.options[0] && f.options[0].value) || '';
    let hintEl = null;
    function refreshHint() {
      const txt = f.hintFor ? f.hintFor(sel.value) : (f.hint || null);
      if (txt) {
        if (!hintEl) { hintEl = el('div', {class:'field-hint'}); wrap.appendChild(hintEl); }
        hintEl.textContent = txt;
      } else if (hintEl) { hintEl.remove(); hintEl = null; }
    }
    sel.addEventListener('change', refreshHint);
    wrap.appendChild(sel);
    refreshHint();
    return {node: wrap, getValue: () => sel.value};
  }

  if (f.type === 'list') return renderListField(f, wrap);
  if (f.type === 'secret') return renderSecretField(f, wrap);
  if (f.type === 'ordered-list') return renderOrderedListField(f, wrap);

  return {node: wrap, getValue: () => undefined};
}

// renderListField 可增删多行文本（如 models 数组）。getValue 返回去空、trim 后的 string[]。
function renderListField(f, wrap) {
  const items = Array.isArray(f.value) ? f.value.slice() : [];
  const rows = el('div', {});
  function draw() {
    rows.innerHTML = '';
    items.forEach((val, i) => {
      const inp = el('input', {type:'text', value: val});
      inp.addEventListener('input', () => { items[i] = inp.value; });
      rows.appendChild(el('div', {class:'field-row'}, [
        inp,
        el('button', {class:'ghost', onclick: () => { items.splice(i, 1); draw(); }}, '删除'),
      ]));
    });
  }
  draw();
  wrap.appendChild(rows);
  wrap.appendChild(el('button', {class:'ghost', onclick: () => { items.push(''); draw(); }}, '添加'));
  return {node: wrap, getValue: () => items.map(s => s.trim()).filter(Boolean)};
}

// renderSecretField 脱敏 apikey 字段：默认只读显示脱敏占位 +「重置」按钮；
// 点「重置」切换为 password 明文输入框。getValue 返回 {reset, value}：
//   未重置 → {reset:false, value:''}（onSubmit 不改 u.apikey，保留占位→后端保留原值）；
//   重置   → {reset:true, value: 明文}（onSubmit 设 u.apikey=value）。
function renderSecretField(f, wrap) {
  let reset = false;
  const dispBox = el('div', {class:'field-row'});
  const inputBox = el('div', {class:'field-row', style:'display:none'});
  const maskedText = el('code', {}, f.value || '(未设置)');
  dispBox.appendChild(maskedText);
  dispBox.appendChild(el('button', {class:'ghost', onclick: () => {
    reset = true;
    dispBox.style.display = 'none';
    inputBox.style.display = '';
  }}, '重置'));
  const inp = el('input', {type:'password', placeholder:'输入新 apikey'});
  inputBox.appendChild(inp);
  wrap.appendChild(dispBox);
  wrap.appendChild(inputBox);
  return {node: wrap, getValue: () => ({reset, value: reset ? inp.value : ''})};
}

// renderOrderedListField 有序列表（mapping targets）。每项 = upstream 下拉 + model 下拉 + 上下移 + 删除。
// f.upstreams 取自 cfg.upstreams（每项含 name/models）。f.value 是现有 upstream/model 串数组。
// getValue 返回 string[]，每项为组装的 upstream/model 串，行序=主备顺序。
// 双下拉利用现有配置数据做前端即时约束（杜绝手敲格式错误），与后端 config.Validate 形成双重保险。
function renderOrderedListField(f, wrap) {
  const ups = f.upstreams || [];
  // 反解析现有 upstream/model 串
  const items = (Array.isArray(f.value) ? f.value : []).map(s => {
    const idx = s.indexOf('/');
    if (idx <= 0 || idx === s.length - 1) return {upstream:'', model:''};
    return {upstream: s.slice(0, idx), model: s.slice(idx + 1)};
  });
  const rows = el('div', {});
  function modelsOf(upName) {
    const u = ups.find(x => x.name === upName);
    return u ? (u.models || []) : [];
  }
  function draw() {
    rows.innerHTML = '';
    items.forEach((it, i) => {
      const upSel = el('select', {});
      ups.forEach(u => upSel.appendChild(el('option', {value:u.name}, u.name)));
      upSel.value = it.upstream || (ups[0] && ups[0].name) || '';
      it.upstream = upSel.value;
      const modelSel = el('select', {});
      function fillModels() {
        modelSel.innerHTML = '';
        const ms = modelsOf(upSel.value);
        ms.forEach(m => modelSel.appendChild(el('option', {value:m}, m)));
        if (ms.includes(it.model)) modelSel.value = it.model;
        else if (ms[0]) { it.model = ms[0]; modelSel.value = it.model; }
        else it.model = '';
      }
      fillModels();
      upSel.addEventListener('change', () => { it.upstream = upSel.value; fillModels(); });
      modelSel.addEventListener('change', () => { it.model = modelSel.value; });
      rows.appendChild(el('div', {class:'field-row'}, [
        upSel, modelSel,
        el('button', {class:'ghost', onclick: () => { if (i > 0) { [items[i-1], items[i]] = [items[i], items[i-1]]; draw(); } }}, '上移'),
        el('button', {class:'ghost', onclick: () => { if (i < items.length - 1) { [items[i+1], items[i]] = [items[i], items[i+1]]; draw(); } }}, '下移'),
        el('button', {class:'danger', onclick: () => { items.splice(i, 1); draw(); }}, '删除'),
      ]));
    });
  }
  draw();
  wrap.appendChild(rows);
  wrap.appendChild(el('button', {class:'ghost', onclick: () => {
    const firstUp = (ups[0] && ups[0].name) || '';
    const firstModel = modelsOf(firstUp)[0] || '';
    items.push({upstream: firstUp, model: firstModel});
    draw();
  }}, '添加项'));
  return {node: wrap, getValue: () => items.filter(it => it.upstream && it.model).map(it => it.upstream + '/' + it.model)};
}

function addUpstream(cfg) {
  openModal({
    title: '添加上游',
    fields: [
      {key:'name', label:'name', type:'text', required:true},
      {key:'url', label:'url', type:'text', required:true, placeholder:'https://api.example.com'},
      {key:'apikey', label:'apikey', type:'password', required:true},
      {key:'models', label:'models（每行一个）', type:'list', required:true, value:[]},
      {key:'timeout', label:'timeout（秒）', type:'text', value:'60', placeholder:'60'},
    ],
    onSubmit: async (v) => {
      cfg.upstreams.push({
        name: v.name.trim(),
        url: v.url.trim(),
        apikey: v.apikey,
        models: v.models,
        timeout: (Number(v.timeout) || 60) * 1e9,
      });
      const r = await saveConfig(cfg);
      if (!r.ok) return {error: r.error};
      renderConfig();
    },
  });
}

function editUpstream(cfg, u) {
  openModal({
    title: '编辑上游: ' + u.name,
    fields: [
      {key:'name', label:'name', type:'text', readonly:true, value:u.name},
      {key:'url', label:'url', type:'text', value:u.url},
      {key:'apikey', label:'apikey', type:'secret', value:u.apikey},
      {key:'models', label:'models（每行一个）', type:'list', value: u.models || []},
      {key:'timeout', label:'timeout（秒）', type:'text', value: String((u.timeout || 6e10) / 1e9)},
    ],
    onSubmit: async (v) => {
      u.url = v.url.trim();
      if (v.apikey.reset) u.apikey = v.apikey.value;  // 未重置则不改（保留脱敏占位→后端保留原值）
      u.models = v.models;
      u.timeout = (Number(v.timeout) || 60) * 1e9;
      const r = await saveConfig(cfg);
      if (!r.ok) return {error: r.error};
      renderConfig();
    },
  });
}

function delUpstream(cfg, name) {
  confirmModal('删除上游「' + name + '」?', async () => {
    cfg.upstreams = cfg.upstreams.filter(u => u.name !== name);
    const r = await saveConfig(cfg);
    if (!r.ok) return {error: r.error};
    renderConfig();
  });
}
function addProject(cfg) {
  openModal({
    title: '添加项目',
    fields: [
      {key:'name', label:'name', type:'text', required:true},
      {key:'key', label:'private key', type:'password', required:true, placeholder:'sk-cp-...（可点生成）', actions:[
        {label:'生成', onClick: async (set) => {
          const {ok, data} = await api('/api/keys/gen', {method:'POST'});
          if (ok) set(data.key);
        }},
      ]},
      {key:'log_level', label:'log_level', type:'select', options:LOG_LEVELS, value:'off', hintFor: lvl => lvl === 'debug' ? '会记录完整请求/响应体到请求日志库' : null},
      {key:'allow_direct_access', label:'允许直连（allow_direct_access）', type:'checkbox', value:false},
    ],
    onSubmit: async (v) => {
      cfg.projects.push({name: v.name.trim(), log_level: v.log_level, model_map:{}});
      cfg.server.private_keys[v.key] = v.name.trim();
      pendingRestart = true;
      const r = await saveConfig(cfg);
      if (!r.ok) return {error: r.error};
      render();
    },
  });
}

function editProject(cfg, p) {
  openModal({
    title: '编辑项目: ' + p.name,
    fields: [
      {key:'name', label:'name', type:'text', readonly:true, value:p.name},
      {key:'log_level', label:'log_level', type:'select', options:LOG_LEVELS, value: p.log_level || 'off', hintFor: lvl => lvl === 'debug' ? '会记录完整请求/响应体到请求日志库' : null},
      {key:'allow_direct_access', label:'允许直连（allow_direct_access）', type:'checkbox', value: p.allow_direct_access || false},
    ],
    onSubmit: async (v) => {
      if (p.log_level !== v.log_level) pendingRestart = true;
      p.log_level = v.log_level;
      p.allow_direct_access = v.allow_direct_access;
      const r = await saveConfig(cfg);
      if (!r.ok) return {error: r.error};
      render();
    },
  });
}

function delProject(cfg, name) {
  confirmModal('删除项目「' + name + '」?', async () => {
    cfg.projects = cfg.projects.filter(p => p.name !== name);
    for (const [k, v] of Object.entries(cfg.server.private_keys)) if (v === name) delete cfg.server.private_keys[k];
    const r = await saveConfig(cfg);
    if (!r.ok) return {error: r.error};
    renderConfig();
  });
}
function addMapping(cfg,proj){ const m=prompt('请求模型名'); if(!m)return; const t=prompt('upstream/model（逗号分隔多个为主备）').split(','); const p=cfg.projects.find(x=>x.name===proj); p.model_map[m]=t; saveConfig(cfg); }
function delMapping(cfg,proj,m){ const p=cfg.projects.find(x=>x.name===proj); delete p.model_map[m]; saveConfig(cfg); }

let usageChart = null;
async function renderUsage() {
  const page = document.getElementById('page');
  page.innerHTML = '';
  const sinceSel = el('select',{},['1d','7d','30d'].map(s=>el('option',{value:s},s)));
  sinceSel.value='7d';
  const draw = async ()=>{
    const {data} = await api('/api/stats?since='+sinceSel.value);
    const rows = (data&&data.rows)||[];
    const byDate = {};
    rows.forEach(r=>{ byDate[r.Date]=(byDate[r.Date]||0)+(r.Total||0); });
    const dates = Object.keys(byDate).sort();
    const totals = dates.map(d=>byDate[d]);
    const canvas = el('canvas',{id:'uc',width:600,height:200});
    page.innerHTML='';
    page.appendChild(el('div',{class:'card'},[el('h3',{},'用量趋势'),sinceSel,canvas]));
    if (usageChart) usageChart.destroy();
    usageChart = new Chart(canvas.getContext('2d'),{type:'line',data:{labels:dates,datasets:[{label:'total tokens',data:totals,borderColor:'#4c6ef5'}]},options:{responsive:true}});
    page.appendChild(el('div',{class:'card'},[
      el('h3',{},'明细'),
      el('table',{},[el('tr',{},['Project','Model','Date','Input','Output','Total'].map(h=>el('th',{},h))),
        ...rows.map(r=>el('tr',{},[el('td',{},r.Project),el('td',{},r.Model),el('td',{},r.Date),el('td',{},String(r.Input)),el('td',{},String(r.Output)),el('td',{},String(r.Total))]))]),
    ]));
  };
  sinceSel.onchange=draw; draw();
}

let logES = null;
async function renderLogs() {
  const page = document.getElementById('page');
  page.innerHTML='加载中...';
  const {data:cfg} = await api('/api/config');
  const projSel = el('select',{},[el('option',{value:''},'全部'),...(cfg.projects||[]).map(p=>el('option',{value:p.name},p.name))]);
  let live = false;
  const list = el('div',{});
  async function loadHistory(){
    const {data} = await api('/api/logs?project='+projSel.value);
    list.innerHTML='';
    (data.rows||[]).forEach(r=>list.appendChild(renderLogRow(r)));
  }
  function startLive(){
    if (logES) logES.close();
    logES = new EventSource('/api/logs/stream?project='+projSel.value);
    logES.onmessage = ev => { list.prepend(renderLogRow(JSON.parse(ev.data))); };
  }
  const liveBtn = el('button',{class:'ghost',onclick:()=>{
    live=!live;
    if(live){liveBtn.textContent='停止实时';startLive();}
    else{liveBtn.textContent='实时';if(logES)logES.close();}
  }},'实时');
  projSel.onchange=()=>{ if(live)startLive(); else loadHistory(); };
  page.innerHTML='';
  page.appendChild(el('div',{class:'card'},[el('h3',{},'请求日志'),projSel,liveBtn,list]));
  loadHistory();
}
function renderLogRow(r){
  return el('div',{},[
    el('div',{onclick:function(){const d=this.nextElementSibling;d.style.display=d.style.display==='none'?'block':'none';}},
      `${new Date(r.TS).toLocaleString()} [${r.Project}] ${r.Method||''} ${r.Path||''} → ${r.Upstream||'-'} ${r.Status||0} ${r.DurationMs||0}ms`),
    el('div',{class:'row-detail',style:'display:none'},[
      el('div',{},'model: '+(r.Model||'')+' / real: '+(r.RealModel||'')),
      r.Error?el('div',{},'error: '+r.Error):null,
      r.RequestBody?el('pre',{},'请求体:\n'+r.RequestBody):null,
      r.ResponseBody?el('pre',{},'响应体:\n'+r.ResponseBody):null,
    ]),
  ]);
}

window.addEventListener('hashchange', render);
render();
