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
}

const reload = () => load(1);
$('#more').addEventListener('click', () => load(state.page + 1));

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
  if (e.target.closest('.queue')) return;
  fileInput.click();
});
drop.addEventListener('keydown', (e) => {
  if (e.key === 'Enter' || e.key === ' ') {
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

async function uploadAll(files) {
  files = files.filter((f) => f.type.startsWith('image/'));
  if (!files.length) return;
  const queue = $('#queue');
  const rows = files.map((f) => {
    const st = el('span', { class: 'state', text: '等待中' });
    const li = el('li', {}, el('span', { class: 'name', text: f.name || '粘贴的图片' }), st);
    queue.append(li);
    return { f, li, st };
  });
  const copyKind = localStorage.getItem('copyDefault') || '';
  const results = [];
  for (const r of rows) {
    r.st.textContent = '上传中…';
    const fd = new FormData();
    fd.append('file', r.f, r.f.name || 'paste.png');
    try {
      const data = await api('POST', '/api/v1/upload', fd);
      r.st.textContent = data.duplicate ? '已存在' : '完成';
      r.st.classList.add('done');
      if (copyKind) results.push(data.links[copyKind]);
    } catch (err) {
      r.st.textContent = err.message;
      r.st.classList.add('err');
    }
  }
  if (results.length) copy(results.join('\n'));
  reload();
  setTimeout(() => rows.forEach((r) => {
    if (r.st.classList.contains('err')) return;
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
  $('#detail').showModal();
}

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
  try { await loadTokens(); } catch (err) { toast(err.message); }
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
