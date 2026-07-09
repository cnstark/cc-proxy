const app = document.getElementById('app');
let pendingRestart = false;

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

async function saveConfig(cfg) {
  const {ok,data} = await api('/api/config',{method:'PUT',body:JSON.stringify(cfg)});
  if (ok) { renderConfig(); }
  else alert('保存失败: '+(data.error||''));
}
function addUpstream(cfg){ const name=prompt('name'); if(!name)return; const url=prompt('url'); const ak=prompt('apikey'); const ms=prompt('models 逗号分隔').split(','); cfg.upstreams.push({name,url,apikey:ak,models:ms,timeout:60000000000}); saveConfig(cfg); }
function editUpstream(cfg,u){ const url=prompt('url',u.url); if(url!=null)u.url=url; const ak=prompt('apikey（留空保留）',''); if(ak)u.apikey=ak; saveConfig(cfg); }
async function delUpstream(cfg,name){ if(!confirm('删除 '+name))return; cfg.upstreams=cfg.upstreams.filter(u=>u.name!==name); saveConfig(cfg); }
function addProject(cfg){ const name=prompt('name'); if(!name)return; const key=prompt('private key（可先用生成按钮）'); const lvl=prompt('log_level','off'); cfg.projects.push({name,log_level:lvl,model_map:{}}); cfg.server.private_keys[key]=name; pendingRestart=true; render(); saveConfig(cfg); }
function editProject(cfg,p){ const lvl=prompt('log_level',p.log_level); if(lvl)p.log_level=lvl; pendingRestart=true; render(); saveConfig(cfg); }
async function delProject(cfg,name){ if(!confirm('删除 '+name))return; cfg.projects=cfg.projects.filter(p=>p.name!==name); for(const[k,v]of Object.entries(cfg.server.private_keys))if(v===name)delete cfg.server.private_keys[k]; saveConfig(cfg); }
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
