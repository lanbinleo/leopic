'use strict';

const $ = (sel) => document.querySelector(sel);
const $$ = (sel) => [...document.querySelectorAll(sel)];

const state = {
  page: 0, last: 1, keyword: '', order: 'newest', loading: false,
  current: null,
  items: new Map(),      // key -> item（已加载的）
  selected: new Set(),   // 选中的 key
  anchor: null,          // Shift 连选的起点
};

function el(tag, props = {}, ...children) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (k === 'class') node.className = v;
    else if (k === 'text') node.textContent = v;
    else if (k.startsWith('on')) node.addEventListener(k.slice(2), v);
    else node.setAttribute(k, v);
  }
  for (const c of children) if (c) node.append(c);
  return node;
}

let toastTimer;
function toast(msg) {
  const t = $('#toast');
  t.textContent = msg;
  t.classList.add('show');
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => t.classList.remove('show'), 1800);
}

async function api(method, url, body) {
  const opt = { method, headers: { Accept: 'application/json' }, credentials: 'same-origin' };
  if (body instanceof FormData) opt.body = body;
  else if (body) {
    opt.headers['Content-Type'] = 'application/json';
    opt.body = JSON.stringify(body);
  }
  const res = await fetch(url, opt);
  if (res.status === 401) {
    showLogin();
    throw new Error('未登录');
  }
  const json = await res.json().catch(() => ({ status: false, message: `HTTP ${res.status}` }));
  if (!json.status) throw new Error(json.message || '请求失败');
  return json.data;
}

async function copy(text, label) {
  try {
    await navigator.clipboard.writeText(text);
    toast(`已复制${label ? ' ' + label : ''}`);
  } catch {
    toast('复制失败，请手动复制');
  }
}

function fmtSize(kb) {
  return kb >= 1024 ? `${(kb / 1024).toFixed(2)} MB` : `${kb.toFixed(1)} KB`;
}

const displayName = (item) => item.origin_name || item.name;
const isTyping = (t) => t.matches?.('input, textarea, select') || t.isContentEditable;

// 可复制的链接种类，详情、右键菜单、批量复制共用。
const KINDS = [
  ['url', '原图 URL'],
  ['markdown', '原图 Markdown'],
  ['webp', 'WebP URL'],
  ['webp_markdown', 'WebP Markdown'],
  ['html', 'HTML'],
];

function linkOf(item, kind) {
  if (kind === 'html') return `<img src="${item.links.url}" alt="${displayName(item).replace(/"/g, '&quot;')}">`;
  return item.links[kind];
}

// 图片加载完淡入；已缓存的图片也要处理。
function fadeIn(img) {
  const done = () => img.classList.add('loaded');
  img.addEventListener('load', done, { once: true });
  img.addEventListener('error', done, { once: true });
  if (img.complete) done();
}

// 带退场动画关闭 dialog。
function closeDialog(d) {
  if (!d.open || d.classList.contains('closing')) return;
  d.classList.add('closing');
  setTimeout(() => {
    d.classList.remove('closing');
    d.close();
  }, 190);
}

// ---------- 主题 ----------

const THEMES = [
  { key: 'system', icon: '◐', label: '跟随系统' },
  { key: 'light', icon: '☀', label: '浅色' },
  { key: 'dark', icon: '☾', label: '深色' },
];

function applyTheme(key) {
  const root = document.documentElement;
  if (key === 'system') {
    delete root.dataset.theme;
    localStorage.removeItem('theme');
  } else {
    root.dataset.theme = key;
    localStorage.setItem('theme', key);
  }
  const t = THEMES.find((x) => x.key === key);
  const btn = $('#btn-theme');
  btn.textContent = t.icon;
  btn.title = `主题：${t.label}（点击切换）`;
  btn.setAttribute('aria-label', btn.title);
}

$('#btn-theme').addEventListener('click', () => {
  const cur = document.documentElement.dataset.theme || 'system';
  const next = THEMES[(THEMES.findIndex((x) => x.key === cur) + 1) % THEMES.length];
  applyTheme(next.key);
  toast(`主题：${next.label}`);
});
applyTheme(document.documentElement.dataset.theme || 'system');

// ---------- 视图 ----------

function applyView(view) {
  const list = view === 'list';
  $('#grid').classList.toggle('list', list);
  $('#list-head').hidden = !list;
  const btn = $('#btn-view');
  btn.textContent = list ? '▦' : '☰';
  btn.title = list ? '切换到网格视图' : '切换到列表视图';
  btn.setAttribute('aria-label', btn.title);
  localStorage.setItem('view', view);
}
$('#btn-view').addEventListener('click', () => applyView($('#grid').classList.contains('list') ? 'grid' : 'list'));
applyView(localStorage.getItem('view') || 'grid');

// 列表表头排序：再次点击同一列切换升降序。
const SORTS = { date: ['newest', 'earliest'], size: ['utmost', 'least'] };
function renderSort() {
  $$('#list-head [data-sort]').forEach((b) => {
    const [desc, asc] = SORTS[b.dataset.sort];
    const mark = state.order === desc ? ' ↓' : state.order === asc ? ' ↑' : '';
    b.textContent = b.dataset.label + mark;
    b.setAttribute('aria-sort', mark ? (mark === ' ↓' ? 'descending' : 'ascending') : 'none');
  });
}
$$('#list-head [data-sort]').forEach((b) => b.addEventListener('click', () => {
  const [desc, asc] = SORTS[b.dataset.sort];
  state.order = state.order === desc ? asc : desc;
  renderSort();
  reload();
}));
renderSort();

// ---------- 登录 ----------

function showLogin() {
  $('#app').hidden = true;
  $('#login').hidden = false;
  $('#password').focus();
}

function showApp() {
  $('#login').hidden = true;
  $('#app').hidden = false;
  reload();
}

$('#login-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  $('#login-error').textContent = '';
  try {
    await api('POST', '/login', { password: $('#password').value });
    $('#password').value = '';
    showApp();
  } catch (err) {
    $('#login-error').textContent = err.message;
    const card = $('#login-form');
    card.classList.remove('shake');
    void card.offsetWidth; // 重启动画
    card.classList.add('shake');
  }
});

$('#btn-logout').addEventListener('click', async () => {
  await fetch('/logout', { method: 'POST', credentials: 'same-origin' });
  showLogin();
});

// ---------- 列表 ----------

function card(item, i) {
  const name = displayName(item);
  const img = el('img', { src: item.links.webp, alt: name, loading: 'lazy', decoding: 'async' });
  fadeIn(img);
  const btn = el('button', { type: 'button', class: 'card' },
    el('div', { class: 'thumb' }, img),
    el('div', { class: 'cap' },
      el('div', { class: 'name', text: name, title: name }),
      el('small', { class: 'sub', text: `${item.date.slice(0, 10)} · ${fmtSize(item.size)}` })),
    el('span', { class: 'col dim', text: `${item.width}×${item.height}` }),
    el('span', { class: 'col size', text: fmtSize(item.size) }),
    el('span', { class: 'col date', text: item.date.slice(0, 16) }),
    el('span', { class: 'col key', text: item.key }));
  const check = el('button', { type: 'button', class: 'check', 'aria-label': `选择 ${name}`, 'aria-pressed': 'false', tabindex: '-1' });
  const li = el('li', { 'data-key': item.key }, btn, check);
  li.style.setProperty('--i', Math.min(i, 20));
  if (state.selected.has(item.key)) markSelected(li, true);
  return li;
}

async function load(page) {
  if (state.loading) return;
  state.loading = true;
  try {
    const q = new URLSearchParams({ page, per_page: 40, order: state.order });
    if (state.keyword) q.set('keyword', state.keyword);
    const data = await api('GET', `/api/v1/images?${q}`);
    if (page === 1) {
      $('#grid').replaceChildren();
      state.items.clear();
      clearSelection();
    }
    data.data.forEach((item, i) => {
      state.items.set(item.key, item);
      $('#grid').append(card(item, i));
    });
    state.page = data.current_page;
    state.last = data.last_page;
    $('#more').hidden = state.page >= state.last;
    $('#empty').hidden = data.total > 0;
    $('#count').textContent = `${data.total} 张`;
  } catch (err) {
    if (err.message !== '未登录') toast(err.message);
  } finally {
    state.loading = false;
  }
  // 一页填不满屏幕时哨兵一直可见，IO 不会再触发，这里补一次。
  const r = $('#sentinel').getBoundingClientRect();
  if (state.page < state.last && r.top < innerHeight + 800) loadMore();
}

const reload = () => load(1);
const loadMore = () => { if (state.page < state.last) load(state.page + 1); };
$('#more').addEventListener('click', loadMore);

// 无限滚动：哨兵进入视口前 800px 就拉下一页；按钮保留作兜底（键盘 / 不支持 IO 时）。
if ('IntersectionObserver' in window) {
  new IntersectionObserver((entries) => {
    if (entries.some((e) => e.isIntersecting) && !$('#app').hidden && !state.loading) loadMore();
  }, { rootMargin: '800px 0px' }).observe($('#sentinel'));
}

let searchTimer;
$('#search').addEventListener('input', (e) => {
  clearTimeout(searchTimer);
  searchTimer = setTimeout(() => {
    state.keyword = e.target.value.trim();
    reload();
  }, 250);
});

// 从界面移除已删除的图，带依次收缩动画。
function removeCards(keys) {
  keys.forEach((k, i) => {
    state.items.delete(k);
    state.selected.delete(k);
    const li = document.querySelector(`#grid li[data-key="${CSS.escape(k)}"]`);
    if (!li) return;
    li.style.animationDelay = `${Math.min(i, 20) * 20}ms`;
    li.classList.add('leaving');
    setTimeout(() => li.remove(), 240 + Math.min(i, 20) * 20);
  });
  const n = Math.max(0, parseInt($('#count').textContent, 10) - keys.length);
  $('#count').textContent = `${n} 张`;
  if (n === 0) $('#empty').hidden = false;
  renderBar();
}

async function deleteKeys(keys) {
  if (!keys.length) return false;
  const msg = keys.length === 1
    ? `删除「${displayName(state.items.get(keys[0]))}」？`
    : `删除选中的 ${keys.length} 张图片？`;
  if (!confirm(`${msg}\n原图与 WebP 文件都会被删除，引用它们的页面将失效。`)) return false;
  try {
    await api('POST', '/api/admin/images/delete', { keys });
  } catch (err) {
    toast(err.message);
    return false;
  }
  removeCards(keys);
  toast(keys.length === 1 ? '已删除' : `已删除 ${keys.length} 张`);
  return true;
}

function copyKeys(keys, kind) {
  const label = KINDS.find(([k]) => k === kind)[1];
  const text = keys.map((k) => linkOf(state.items.get(k), kind)).join('\n');
  copy(text, keys.length > 1 ? `${keys.length} 条${label}` : label);
}

// 通过隐藏表单提交到 iframe：浏览器直接流式下载，不占页面内存。
function download(keys, variant) {
  if (!keys.length) return;
  const form = $('#dl-form');
  form.replaceChildren(el('input', { type: 'hidden', name: 'variant', value: variant }),
    ...keys.map((k) => el('input', { type: 'hidden', name: 'key', value: k })));
  form.submit();
  toast(keys.length > 1 ? `正在打包 ${keys.length} 张…` : '开始下载');
}

// ---------- 选择 ----------

const cards = () => $$('#grid li[data-key]');

function markSelected(li, on) {
  li.classList.toggle('selected', on);
  li.querySelector('.check').setAttribute('aria-pressed', String(on));
}

function setSelected(key, on) {
  if (on) state.selected.add(key);
  else state.selected.delete(key);
  const li = document.querySelector(`#grid li[data-key="${CSS.escape(key)}"]`);
  if (li) markSelected(li, on);
}

function clearSelection() {
  for (const k of [...state.selected]) setSelected(k, false);
  state.anchor = null;
  renderBar();
}

function selectRange(toKey, additive) {
  const list = cards().map((li) => li.dataset.key);
  const a = list.indexOf(state.anchor ?? toKey);
  const b = list.indexOf(toKey);
  if (a < 0 || b < 0) return;
  if (!additive) for (const k of [...state.selected]) setSelected(k, false);
  const [lo, hi] = a < b ? [a, b] : [b, a];
  for (let i = lo; i <= hi; i++) setSelected(list[i], true);
}

function selectedKeys() {
  // 按界面顺序
  return cards().map((li) => li.dataset.key).filter((k) => state.selected.has(k));
}

function renderBar() {
  const n = state.selected.size;
  $('#bar').classList.toggle('show', n > 0);
  $('#bar').setAttribute('aria-hidden', String(n === 0));
  $('#bar-count').textContent = `已选 ${n} 张`;
  document.body.classList.toggle('selecting', n > 0);
}

$('#grid').addEventListener('click', (e) => {
  const li = e.target.closest('li[data-key]');
  if (!li || suppressClick) return;
  const key = li.dataset.key;
  const onCheck = e.target.closest('.check');
  if (e.shiftKey) {
    selectRange(key, e.ctrlKey || e.metaKey);
  } else if (e.ctrlKey || e.metaKey || onCheck || state.selected.size) {
    setSelected(key, !state.selected.has(key));
    state.anchor = key;
  } else {
    openDetail(state.items.get(key));
    return;
  }
  renderBar();
});

// 空白处按住拖动框选；按住 Ctrl 在已选基础上追加。
const marquee = $('#marquee');
let drag = null;
let suppressClick = false;

document.addEventListener('pointerdown', (e) => {
  if (e.button !== 0 || $('#app').hidden || e.pointerType === 'touch') return;
  if (!e.target.closest('main') || e.target.closest('li[data-key], button, input, a, #list-head')) return;
  drag = {
    x: e.clientX, y: e.clientY, sx: scrollX, sy: scrollY, active: false,
    base: e.ctrlKey || e.metaKey ? new Set(state.selected) : new Set(),
  };
});

document.addEventListener('pointermove', (e) => {
  if (!drag) return;
  // 记录文档坐标，滚动后框选范围仍正确。
  const x0 = drag.x + drag.sx, y0 = drag.y + drag.sy;
  const x1 = e.clientX + scrollX, y1 = e.clientY + scrollY;
  if (!drag.active) {
    if (Math.abs(x1 - x0) + Math.abs(y1 - y0) < 6) return;
    drag.active = true;
    marquee.hidden = false;
    document.body.classList.add('dragging');
  }
  drag.last = e;
  const L = Math.min(x0, x1), T = Math.min(y0, y1), R = Math.max(x0, x1), B = Math.max(y0, y1);
  Object.assign(marquee.style, { left: `${L - scrollX}px`, top: `${T - scrollY}px`, width: `${R - L}px`, height: `${B - T}px` });
  for (const li of cards()) {
    const r = li.getBoundingClientRect();
    const hit = r.right + scrollX > L && r.left + scrollX < R && r.bottom + scrollY > T && r.top + scrollY < B;
    setSelected(li.dataset.key, hit || drag.base.has(li.dataset.key));
  }
  renderBar();
  autoScroll(e.clientY);
});

let scrollTimer = null;
function autoScroll(y) {
  clearInterval(scrollTimer);
  const edge = 40;
  const dy = y < edge ? -12 : y > innerHeight - edge ? 12 : 0;
  if (!dy) return;
  scrollTimer = setInterval(() => {
    scrollBy(0, dy);
    if (drag?.last) document.dispatchEvent(new PointerEvent('pointermove', drag.last));
  }, 16);
}

function endDrag() {
  clearInterval(scrollTimer);
  if (drag?.active) {
    marquee.hidden = true;
    document.body.classList.remove('dragging');
    suppressClick = true;
    setTimeout(() => { suppressClick = false; }, 0);
  }
  drag = null;
}
document.addEventListener('pointerup', endDrag);
document.addEventListener('pointercancel', endDrag);

// 快捷键：Ctrl+A 全选已加载，Esc 取消，Delete 删除选中。
document.addEventListener('keydown', (e) => {
  if ($('#app').hidden || isTyping(e.target) || $$('dialog[open]').length || !ctx.hidden) return;
  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'a') {
    e.preventDefault();
    cards().forEach((li) => setSelected(li.dataset.key, true));
    renderBar();
  } else if (e.key === 'Escape' && state.selected.size) {
    clearSelection();
  } else if (e.key === 'Delete' && state.selected.size) {
    deleteKeys(selectedKeys());
  }
});

// ---------- 批量操作栏 ----------

$('#bar-copy').addEventListener('click', (e) => {
  const r = e.currentTarget.getBoundingClientRect();
  openMenu(KINDS.slice(0, 4).map(([kind, label]) => [`复制${label}`, () => copyKeys(selectedKeys(), kind)]), r.left, r.bottom + 6, e.currentTarget);
});
$('#bar-dl').addEventListener('click', () => download(selectedKeys(), 'original'));
$('#bar-dl-webp').addEventListener('click', () => download(selectedKeys(), 'webp'));
$('#bar-delete').addEventListener('click', () => deleteKeys(selectedKeys()));
$('#bar-clear').addEventListener('click', clearSelection);

// ---------- 右键菜单 ----------

const ctx = $('#ctx');
let ctxOwner = null;

function closeMenu(refocus) {
  if (ctx.hidden) return;
  ctx.hidden = true;
  if (ctxOwner) {
    ctxOwner.classList.remove('active');
    if (refocus) ctxOwner.focus();
  }
  ctxOwner = null;
}

// entries: [label, fn, extraClass?] 或 null（分隔线）
function openMenu(entries, x, y, owner) {
  closeMenu();
  ctxOwner = owner;
  owner?.classList.add('active');
  ctx.replaceChildren(...entries.map((e) => {
    if (!e) return el('li', { role: 'separator' });
    const [text, fn, cls = ''] = e;
    return el('li', { role: 'none' }, el('button', {
      type: 'button', role: 'menuitem', tabindex: '-1', class: cls,
      onclick: () => { closeMenu(true); fn(); },
    }, el('span', { text })));
  }));
  ctx.hidden = false;
  // 贴着光标，越界则翻转。
  const r = ctx.getBoundingClientRect();
  const left = x + r.width > innerWidth - 8 ? Math.max(8, x - r.width) : x;
  const top = y + r.height > innerHeight - 8 ? Math.max(8, y - r.height) : y;
  ctx.style.left = `${left}px`;
  ctx.style.top = `${top}px`;
  ctx.style.transformOrigin = `${left < x ? 'right' : 'left'} ${top < y ? 'bottom' : 'top'}`;
  ctx.querySelector('[role="menuitem"]').focus();
}

function itemMenu(item) {
  return [
    ...KINDS.map(([kind, label]) => [`复制${label}`, () => copyKeys([item.key], kind)]),
    null,
    ['新标签打开原图', () => window.open(item.links.url, '_blank', 'noopener')],
    ['下载原图', () => download([item.key], 'original')],
    ['查看详情', () => openDetail(item)],
    null,
    ['删除', () => deleteKeys([item.key]), 'danger'],
  ];
}

function batchMenu() {
  const n = state.selected.size;
  return [
    ...KINDS.slice(0, 4).map(([kind, label]) => [`复制 ${n} 条${label}`, () => copyKeys(selectedKeys(), kind)]),
    null,
    [`下载 ${n} 张原图`, () => download(selectedKeys(), 'original')],
    [`下载 ${n} 张 WebP`, () => download(selectedKeys(), 'webp')],
    null,
    ['取消选择', clearSelection],
    [`删除 ${n} 张`, () => deleteKeys(selectedKeys()), 'danger'],
  ];
}

$('#grid').addEventListener('contextmenu', (e) => {
  const li = e.target.closest('li[data-key]');
  const item = li && state.items.get(li.dataset.key);
  if (!item) return;
  e.preventDefault();
  const btn = li.querySelector('.card');
  let { clientX: x, clientY: y } = e;
  if (!x && !y) { // 键盘触发（菜单键 / Shift+F10）
    const r = btn.getBoundingClientRect();
    x = r.left + 12;
    y = r.top + 12;
  }
  const batch = state.selected.has(item.key) && state.selected.size > 1;
  openMenu(batch ? batchMenu() : itemMenu(item), x, y, btn);
});

ctx.addEventListener('keydown', (e) => {
  const items = [...ctx.querySelectorAll('[role="menuitem"]')];
  const i = items.indexOf(document.activeElement);
  if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
    e.preventDefault();
    const d = e.key === 'ArrowDown' ? 1 : -1;
    items[(i + d + items.length) % items.length].focus();
  } else if (e.key === 'Home' || e.key === 'End') {
    e.preventDefault();
    items[e.key === 'Home' ? 0 : items.length - 1].focus();
  } else if (e.key === 'Escape') {
    e.preventDefault();
    e.stopPropagation();
    closeMenu(true);
  } else if (e.key === 'Tab') {
    closeMenu(true);
  }
});
ctx.addEventListener('contextmenu', (e) => e.preventDefault());
document.addEventListener('pointerdown', (e) => { if (!ctx.contains(e.target) && !e.target.closest('#bar-copy')) closeMenu(); });
window.addEventListener('scroll', () => closeMenu(), { passive: true });
window.addEventListener('resize', () => closeMenu());
window.addEventListener('blur', () => closeMenu());

// ---------- 上传 ----------

const drop = $('#drop');
const fileInput = $('#file');

drop.addEventListener('click', (e) => {
  if (e.target.closest('.queue, .nomark')) return;
  fileInput.click();
});
drop.addEventListener('keydown', (e) => {
  if (e.target === drop && (e.key === 'Enter' || e.key === ' ')) {
    e.preventDefault();
    fileInput.click();
  }
});
fileInput.addEventListener('change', () => {
  uploadAll([...fileInput.files]);
  fileInput.value = '';
});
['dragenter', 'dragover'].forEach((t) => drop.addEventListener(t, (e) => {
  e.preventDefault();
  drop.classList.add('over');
}));
['dragleave', 'drop'].forEach((t) => drop.addEventListener(t, (e) => {
  e.preventDefault();
  drop.classList.remove('over');
}));
drop.addEventListener('drop', (e) => uploadAll([...e.dataTransfer.files]));
document.addEventListener('paste', (e) => {
  if ($('#app').hidden || isTyping(e.target)) return;
  const files = [...e.clipboardData.files];
  if (files.length) uploadAll(files);
});

const UPLOAD_CONCURRENCY = 2;

async function uploadOne(r, noMark) {
  r.st.textContent = '上传中…';
  r.st.classList.remove('err');
  const fd = new FormData();
  fd.append('file', r.f, r.f.name || 'paste.png');
  if (noMark) fd.append('watermark', '0');
  try {
    r.data = await api('POST', '/api/v1/upload', fd);
    r.err = null;
    r.st.textContent = r.data.duplicate ? '已存在' : '完成';
    r.st.classList.add('done');
  } catch (err) {
    r.err = err;
    r.st.textContent = err.message;
    r.st.classList.add('err');
  }
}

// 业务错误（格式 / 大小）重试也没用，只重试网络或服务端异常。
const retryable = (err) => err && !/不支持|超出限制|请选择/.test(err.message) && err.message !== '未登录';

async function uploadAll(files) {
  files = files.filter((f) => f.type.startsWith('image/'));
  if (!files.length) return;
  const noMark = $('#no-mark').checked;
  const queue = $('#queue');
  const rows = files.map((f) => {
    const st = el('span', { class: 'state', text: '等待中' });
    const li = el('li', {}, el('span', { class: 'name', text: f.name || '粘贴的图片' }), st);
    queue.append(li);
    return { f, li, st, data: null, err: null };
  });
  // 先 2 路并发；失败的再逐张重试一遍（并发出错时回退为串行）。
  let next = 0;
  const worker = async () => {
    while (next < rows.length) await uploadOne(rows[next++], noMark);
  };
  await Promise.all(Array.from({ length: Math.min(UPLOAD_CONCURRENCY, rows.length) }, worker));
  for (const r of rows.filter((x) => retryable(x.err))) {
    r.st.textContent = '重试中…';
    await uploadOne(r, noMark);
  }
  const copyKind = localStorage.getItem('copyDefault') || '';
  const results = copyKind ? rows.filter((r) => r.data).map((r) => r.data.links[copyKind]) : [];
  if (results.length) copy(results.join('\n'));
  reload();
  setTimeout(() => rows.forEach((r) => {
    if (r.err) return;
    r.li.classList.add('leaving');
    setTimeout(() => r.li.remove(), 320);
  }), 4000);
}

// ---------- 详情 ----------

function linkRow(label, value) {
  const id = `l-${Math.random().toString(36).slice(2)}`;
  const input = el('input', { id, readonly: '', value });
  input.addEventListener('focus', () => input.select());
  return el('div', { class: 'link' },
    el('label', { for: id, text: label }),
    input,
    el('button', { class: 'ghost', type: 'button', text: '复制', onclick: () => copy(value, label) }));
}

function openDetail(item) {
  state.current = item;
  const name = displayName(item);
  $('#d-title').textContent = name;
  const img = $('#d-img');
  img.classList.remove('loaded');
  img.src = item.links.webp;
  img.alt = name;
  fadeIn(img);
  $('#d-open').href = item.links.url;
  const meta = [
    ['尺寸', `${item.width} × ${item.height}`],
    ['大小', item.webp_size > 0 ? `${fmtSize(item.size)}（WebP ${fmtSize(item.webp_size)}）` : fmtSize(item.size)],
    ['类型', item.mimetype],
    ['时间', item.date],
    ['Key', item.key],
    ['路径', item.pathname],
  ];
  $('#d-meta').replaceChildren(...meta.flatMap(([k, v]) => [el('dt', { text: k }), el('dd', { text: v })]));
  $('#d-links').replaceChildren(...KINDS.map(([kind, label]) => linkRow(label.replace('Markdown', 'MD'), linkOf(item, kind))));
  const mark = $('#d-mark');
  mark.checked = item.watermark !== false;
  mark.disabled = item.extension === 'gif';
  mark.parentElement.title = mark.disabled ? 'GIF 没有 WebP 衍生图' : '';
  $('#detail').showModal();
}

$('#d-mark').addEventListener('change', async (e) => {
  const item = state.current;
  if (!item) return;
  const on = e.target.checked;
  e.target.disabled = true;
  try {
    const data = await api('POST', `/api/admin/images/${encodeURIComponent(item.key)}/watermark`, { watermark: String(on) });
    item.watermark = data.watermark;
    item.webp_size = data.webp_size;
    // 衍生图 URL 不变，加时间戳绕开浏览器缓存刷新预览与网格。
    const bust = `${item.links.webp}${item.links.webp.includes('?') ? '&' : '?'}t=${Date.now()}`;
    $('#d-img').src = bust;
    const li = document.querySelector(`#grid li[data-key="${CSS.escape(item.key)}"] img`);
    if (li) li.src = bust;
    toast(on ? '已加水印（CDN 可能仍缓存旧图）' : '已去掉水印（CDN 可能仍缓存旧图）');
  } catch (err) {
    e.target.checked = !on;
    toast(err.message);
  } finally {
    e.target.disabled = false;
  }
});

$('#d-download').addEventListener('click', () => state.current && download([state.current.key], 'original'));
$('#d-delete').addEventListener('click', async () => {
  if (state.current && await deleteKeys([state.current.key])) closeDialog($('#detail'));
});

$$('dialog').forEach((d) => {
  d.addEventListener('click', (e) => {
    if (e.target === d || e.target.closest('[data-close]')) closeDialog(d);
  });
  d.addEventListener('cancel', (e) => {
    e.preventDefault();
    closeDialog(d);
  });
});

// ---------- 设置 ----------

$('#copy-default').value = localStorage.getItem('copyDefault') || '';
$('#copy-default').addEventListener('change', (e) => localStorage.setItem('copyDefault', e.target.value));

function fmtTime(ts) {
  return ts ? new Date(ts * 1000).toLocaleString('zh-CN', { hour12: false }) : '从未';
}

async function loadTokens() {
  const list = await api('GET', '/api/admin/tokens');
  $('#tokens').replaceChildren(...list.map((t) => el('li', {},
    el('div', {},
      el('span', { text: `#${t.id} ${t.name || '(未命名)'}` }),
      el('small', { text: `创建 ${fmtTime(t.created_at)} · 最近使用 ${fmtTime(t.last_used_at)}` })),
    el('button', {
      class: 'danger', type: 'button', text: '吊销',
      onclick: async () => {
        if (!confirm(`吊销 token #${t.id}？使用它的脚本将无法上传。`)) return;
        await api('DELETE', `/api/admin/tokens/${t.id}`);
        loadTokens();
      },
    }))));
  if (!list.length) $('#tokens').append(el('li', { class: 'muted', text: '暂无 token' }));
}

$('#btn-settings').addEventListener('click', async () => {
  $('#token-new').hidden = true;
  $('#settings').showModal();
  try {
    await Promise.all([loadTokens(), loadSettings()]);
  } catch (err) { toast(err.message); }
});

// ---------- 设置：分页签 ----------

const TABS = $$('#settings [role="tab"]');
function selectTab(tab) {
  TABS.forEach((t) => {
    const on = t === tab;
    t.setAttribute('aria-selected', String(on));
    t.tabIndex = on ? 0 : -1;
    $(`#${t.getAttribute('aria-controls')}`).hidden = !on;
  });
  if (tab.id === 'tab-mark') schedulePreview();
  if (tab.id === 'tab-advanced') pollRegen();
}
TABS.forEach((t, i) => {
  t.addEventListener('click', () => selectTab(t));
  t.addEventListener('keydown', (e) => {
    const d = { ArrowRight: 1, ArrowLeft: -1 }[e.key];
    if (!d) return;
    e.preventDefault();
    const n = TABS[(i + d + TABS.length) % TABS.length];
    n.focus();
    selectTab(n);
  });
});

// ---------- 设置：服务端参数 ----------

let srv = null; // { settings, defaults, custom_font, has_icon }

// 表单字段名形如 "watermark.opacity"，按路径读写嵌套对象。
const getPath = (o, path) => path.split('.').reduce((x, k) => x?.[k], o);
function setPath(o, path, v) {
  const ks = path.split('.');
  const last = ks.pop();
  ks.reduce((x, k) => x[k], o)[last] = v;
}

function fillForm(form, st) {
  for (const input of form.elements) {
    if (!input.name) continue;
    const v = getPath(st, input.name);
    if (input.type === 'checkbox') input.checked = !!v;
    else input.value = v ?? '';
  }
  form.querySelectorAll('output[data-for]').forEach(renderOutput);
}

function readForm(form, st) {
  const out = structuredClone(st);
  for (const input of form.elements) {
    if (!input.name) continue;
    let v = input.type === 'checkbox' ? input.checked : input.value;
    if (input.type === 'number' || input.type === 'range') v = Number(v);
    setPath(out, input.name, v);
  }
  return out;
}

function renderOutput(o) {
  const v = Number($(`#mark-form [name="${o.dataset.for}"]`).value);
  o.textContent = o.dataset.for.endsWith('opacity') ? `${Math.round(v * 100)}%` : `${v}%`;
}

function renderAssets() {
  const icon = $('#icon-img');
  icon.hidden = !srv.has_icon;
  if (srv.has_icon) icon.src = `/api/admin/assets/icon?t=${Date.now()}`;
  $('#icon-state').textContent = srv.has_icon ? '' : '未上传图标';
  $('#icon-del').hidden = !srv.has_icon;
  $('#font-state').textContent = srv.custom_font ? '使用自定义字体' : '使用内嵌字体（思源黑体常用字）';
  $('#font-del').hidden = !srv.custom_font;
}

function applyServer(data) {
  srv = data;
  fillForm($('#mark-form'), srv.settings);
  fillForm($('#adv-form'), srv.settings);
  $$('#adv-form [data-rec]').forEach((x) => {
    x.textContent = `（推荐 ${srv.defaults[x.dataset.rec].toLocaleString('zh-CN')}）`;
  });
  renderAssets();
}

async function loadSettings() {
  applyServer(await api('GET', '/api/admin/settings'));
  if (!$('#pane-mark').hidden) schedulePreview();
}

async function saveSettings(st, form) {
  const btn = form.querySelector('button[type="submit"]');
  btn.disabled = true;
  try {
    applyServer(await api('PUT', '/api/admin/settings', st));
    toast('已保存');
  } catch (err) {
    toast(err.message);
  } finally {
    btn.disabled = false;
  }
}

$('#mark-form').addEventListener('submit', (e) => {
  e.preventDefault();
  saveSettings(readForm($('#mark-form'), srv.settings), e.target);
});
$('#adv-form').addEventListener('submit', (e) => {
  e.preventDefault();
  if (!e.target.reportValidity()) return;
  saveSettings(readForm($('#adv-form'), srv.settings), e.target);
});
$('#adv-reset').addEventListener('click', () => {
  // 只把高级页的数值恢复推荐值，邮箱保持不变；需点保存才生效。
  const st = readForm($('#adv-form'), srv.settings);
  for (const k of ['webp_quality', 'webp_max_edge', 'max_size_mb', 'max_pixels']) st[k] = srv.defaults[k];
  fillForm($('#adv-form'), st);
  toast('已填入推荐值，点保存生效');
});

// 预览：参数变动后 300ms 请求一次，旧请求的结果丢弃。
let previewTimer;
let previewSeq = 0;
let previewURL = null;
function schedulePreview() {
  clearTimeout(previewTimer);
  previewTimer = setTimeout(renderPreview, 300);
}
async function renderPreview() {
  if (!srv) return;
  const seq = ++previewSeq;
  const st = readForm($('#mark-form'), srv.settings);
  try {
    const res = await fetch('/api/admin/settings/preview', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json', Accept: 'image/webp,application/json' },
      body: JSON.stringify({ settings: st, key: $('#preview-key').value.trim() }),
    });
    if (seq !== previewSeq) return;
    if (!res.headers.get('Content-Type')?.startsWith('image/')) {
      const j = await res.json().catch(() => ({}));
      throw new Error(j.message || '预览失败');
    }
    const url = URL.createObjectURL(await res.blob());
    if (previewURL) URL.revokeObjectURL(previewURL);
    previewURL = url;
    $('#mark-preview').src = url;
  } catch (err) {
    if (seq === previewSeq) toast(err.message);
  }
}
$('#mark-form').addEventListener('input', (e) => {
  if (e.target.type === 'range') renderOutput($(`#mark-form output[data-for="${e.target.name}"]`));
  schedulePreview();
});
$('#preview-key').addEventListener('input', schedulePreview);

// 图标 / 字体上传
async function uploadAsset(name, file) {
  const fd = new FormData();
  fd.append('file', file);
  try {
    applyServer(await api('POST', `/api/admin/assets/${name}`, fd));
    toast('已上传');
    schedulePreview();
  } catch (err) { toast(err.message); }
}
async function deleteAsset(name) {
  try {
    applyServer(await api('DELETE', `/api/admin/assets/${name}`));
    schedulePreview();
  } catch (err) { toast(err.message); }
}
for (const name of ['icon', 'font']) {
  const input = $(`#${name}-file`);
  $(`#${name}-pick`).addEventListener('click', () => input.click());
  input.addEventListener('change', () => {
    if (input.files[0]) uploadAsset(name, input.files[0]);
    input.value = '';
  });
  $(`#${name}-del`).addEventListener('click', () => deleteAsset(name));
}

// 全量重建
let regenTimer;
function renderRegen(j) {
  const bar = $('#regen-bar');
  $('#regen-start').disabled = j.running;
  bar.hidden = !j.running;
  if (j.running) {
    bar.max = j.total || 1;
    bar.value = j.done;
    $('#regen-state').textContent = `进行中 ${j.done} / ${j.total}`;
  } else if (j.ended) {
    $('#regen-state').textContent = j.error
      ? `出错：${j.error}`
      : `上次完成于 ${fmtTime(j.ended)}，处理 ${j.ok} 张`;
  } else {
    $('#regen-state').textContent = '';
  }
}
async function pollRegen() {
  clearTimeout(regenTimer);
  try {
    const j = await api('GET', '/api/admin/regen');
    renderRegen(j);
    if (j.running && $('#settings').open) regenTimer = setTimeout(pollRegen, 1000);
  } catch (err) { toast(err.message); }
}
$('#regen-start').addEventListener('click', async () => {
  if (!confirm('按当前设置重建全部 WebP 衍生图？图片多时需要几分钟，期间可正常使用。')) return;
  try {
    renderRegen(await api('POST', '/api/admin/regen', { all: 'true' }));
    pollRegen();
  } catch (err) { toast(err.message); }
});

$('#token-form').addEventListener('submit', async (e) => {
  e.preventDefault();
  try {
    const data = await api('POST', '/api/admin/tokens', { name: $('#token-name').value.trim() });
    $('#token-name').value = '';
    $('#token-value').textContent = data.token;
    $('#token-new').hidden = false;
    loadTokens();
  } catch (err) {
    toast(err.message);
  }
});
$('#token-copy').addEventListener('click', () => copy($('#token-value').textContent, 'token'));

// 下载出错时服务端返回 JSON，落在隐藏 iframe 里，读出来提示。
$('#dl-frame').addEventListener('load', () => {
  try {
    const text = $('#dl-frame').contentDocument?.body?.textContent || '';
    if (text.startsWith('{')) toast(JSON.parse(text).message || '下载失败');
  } catch { /* 跨域或非 JSON，忽略 */ }
});

// ---------- 启动 ----------

fetch('/api/admin/session', { headers: { Accept: 'application/json' }, credentials: 'same-origin' })
  .then((r) => (r.ok ? showApp() : showLogin()))
  .catch(showLogin);
