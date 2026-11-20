'use strict';

const app = document.querySelector('#app');
const drawer = document.querySelector('#drawer');
const openSections = document.querySelector('#open-sections');
const desktop = document.querySelector('#sections');
const notice = document.querySelector('#notice');
const admin = location.pathname.startsWith('/admin');
const theme = document.querySelector('#theme');

const escapeHTML = value => String(value ?? '').replace(/[&<>"']/g, char => ({
  '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
})[char]);

const labels = {
  items: 'Позиции', sections: 'Разделы', tags: 'Теги', places: 'Места хранения', homepage: 'Главная', import: 'Заполнение из файла', connection: 'Подключение', appearance: 'Оформление',
};
const colorLabels = {
  bg: 'Фон страниц', surface: 'Фон карточек и окон', header: 'Фон шапки',
  ink: 'Основной текст', muted: 'Второстепенный текст', line: 'Границы',
  accent: 'Основные кнопки и выбранные элементы', on: 'Текст на основных кнопках',
  soft: 'Фон тегов и заглушек', link: 'Ссылки', focus: 'Рамка фокуса',
  button: 'Фон обычных кнопок', 'button-ink': 'Текст обычных кнопок',
  danger: 'Ошибки и удаление', shadow: 'Тени', backdrop: 'Затемнение за окном',
  qr: 'Подложка QR-кода',
};
const fontOptions = { system: 'Системный', serif: 'Georgia', mono: 'Моноширинный', custom: 'Свой шрифт' };
const defaultMenuTitle = 'Домашнее меню';
const defaultPhotoNote = 'Реальная упаковка и вид могут отличаться от фото.';
const menuTitle = () => savedAppearance.title || defaultMenuTitle;
const photoNote = () => savedAppearance.photo_note ?? defaultPhotoNote;
let savedAppearance = {};
let appliedAppearance;
let customFont;
let customFontFile = '';
let data;
let requestVersion = 0;
let noticeTimer;
let touch;
let swipeAnimation;
let menuView = localStorage.getItem('homemenu-view') === 'list' ? 'list' : 'grid';
const pending = new Set();
const cartKey = 'homemenu-cart';
const cartLink = document.querySelector('#cart-link');
const cartBadge = document.querySelector('#cart-count');
let cart = readCart();
let checkingOut = false;

function readCart() {
  try {
    const stored = JSON.parse(localStorage.getItem(cartKey));
    return {
      items: new Map((stored?.items || []).filter(([id, quantity]) =>
        Number.isSafeInteger(id) && id > 0 && Number.isSafeInteger(quantity) && quantity > 0)),
      token: typeof stored?.token === 'string' ? stored.token : '',
    };
  } catch {
    return { items: new Map(), token: '' };
  }
}

function updateCartBadge() {
  const count = [...cart.items.values()].reduce((sum, quantity) => sum + quantity, 0);
  cartBadge.textContent = count > 99 ? '99+' : count;
  cartBadge.hidden = count === 0;
  cartLink.setAttribute('aria-label', count ? `Корзина, ${count} шт.` : 'Корзина');
}

function saveCart() {
  localStorage.setItem(cartKey, JSON.stringify({ items: [...cart.items], token: cart.token }));
  updateCartBadge();
}

function isCart() {
  return new URLSearchParams(location.search).get('cart') === '1';
}

updateCartBadge();
if (!admin) {
  cartLink.setAttribute('data-nav', '');
  document.querySelector('.brand').setAttribute('data-nav', '');
}
if (admin) {
  const mode = document.querySelector('#mode-link');
  mode.href = '/';
  mode.textContent = menuTitle();
}

openSections.hidden = admin;
desktop.hidden = admin;

function currentTheme() {
  return document.documentElement.dataset.theme ||
    (matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light');
}

const storedTheme = localStorage.getItem('homemenu-theme');
if (storedTheme) document.documentElement.dataset.theme = storedTheme;
theme.addEventListener('click', () => {
  const next = currentTheme() === 'dark' ? 'light' : 'dark';
  document.documentElement.dataset.theme = next;
  localStorage.setItem('homemenu-theme', next);
  const picker = app.querySelector('#appearance-palette');
  if (picker) {
    picker.value = next;
    showPalette(picker.form, next);
  }
});
openSections.addEventListener('click', () => drawer.showModal());
document.querySelector('#close-sections').addEventListener('click', () => drawer.close());
drawer.addEventListener('click', event => {
  if (event.target === drawer && event.clientX > drawer.getBoundingClientRect().right) drawer.close();
});

function announce(message) {
  clearTimeout(noticeTimer);
  notice.hidden = false;
  notice.textContent = message;
  noticeTimer = setTimeout(() => { notice.hidden = true; }, 4200);
}

async function request(path, options) {
  const response = await fetch('/api/' + path, options);
  if (!response.ok) {
    const body = await response.json().catch(() => ({}));
    const error = new Error(body.error || 'Не удалось выполнить запрос.');
    error.status = response.status;
    throw error;
  }
  return response.json();
}

async function refresh(shouldRender = () => true) {
  const version = ++requestVersion;
  const next = await request('catalog');
  if (version !== requestVersion) return;
  data = next;
  applyAppearance();
  app.removeAttribute('aria-busy');
  if (shouldRender()) render();
}

function state() {
  const params = new URLSearchParams(location.search);
  const detail = data.items.find(item => item.id === Number(params.get('item')));
  const section = data.sections.find(entry => entry.id === Number(params.get('section')));
  return { section, detail, itemID: Number(params.get('item')), selected: new Set(params.getAll('tag').map(Number)) };
}

function visitorURL(section, tags = [], item) {
  const params = new URLSearchParams();
  if (section) params.set('section', section);
  for (const tag of tags) params.append('tag', tag);
  if (item) params.set('item', item);
  return params.size || params.toString() ? '/?' + params : '/';
}

const itemTags = item => item.tags ? item.tags.split(',').map(Number) : [];
const inSection = section => data.items.filter(item => item.section === section?.id && item.quantity > 0);
const visitorItems = section => section ? inSection(section) : data.sections.flatMap(inSection);
const matches = (item, selected) => !selected.size || itemTags(item).some(tag => selected.has(tag));
const photoURL = item => '/photos/' + encodeURIComponent(item.photo);

function thumbnail(item) {
  return item.photo
    ? `<img class="thumb" src="${photoURL(item)}" alt="" loading="lazy">`
    : `<span class="thumb" aria-hidden="true">${escapeHTML(Array.from(item.name)[0]?.toUpperCase())}</span>`;
}

function basket(item, text = false) {
  const full = (cart.items.get(item.id) || 0) >= item.quantity;
  return `<button class="${text ? 'take-action' : 'icon'}" type="button" data-cart-add="${item.id}" aria-label="В корзину: ${escapeHTML(item.name)}" title="В корзину"${full || cart.token ? ' disabled' : ''}>${text ? 'В корзину' : '🛒'}</button>`;
}

function takeButton(item, primary = false) {
  return `<button class="${primary ? 'primary' : 'take-action'}" type="button" data-take="${item.id}" aria-label="Взять одну штуку: ${escapeHTML(item.name)}" title="Взять"${!item.quantity || pending.has(item.id) ? ' disabled' : ''}>Взять</button>`;
}

function menuRow(item, href, subtitle, actions) {
  const label = `${thumbnail(item)}<span class="row-name"><strong>${escapeHTML(item.name)}</strong><small>${subtitle}</small></span>`;
  const content = href ? `<a class="row-link" data-nav href="${href}">${label}</a>` :
    `<div class="row-link">${label}</div>`;
  return `<article class="row">${content}<div class="row-actions">${actions}</div></article>`;
}

function renderCart() {
  const rows = [...cart.items].map(([id, quantity]) => {
    const item = data.items.find(entry => entry.id === id);
    const available = item?.quantity || 0;
    const subtitle = `${quantity} шт. · доступно ${available}`;
    const controls = `<button class="icon" data-cart-minus="${id}" aria-label="Уменьшить количество"${cart.token ? ' disabled' : ''}>−</button>
      <button class="icon" data-cart-add="${id}" aria-label="Увеличить количество"${quantity >= available || cart.token ? ' disabled' : ''}>+</button>`;
    return menuRow(item || { name: 'Позиция удалена' }, item ? `/?cart=1&item=${id}` : '', subtitle, controls);
  });
  const canTake = [...cart.items].every(([id, quantity]) =>
    data.items.some(item => item.id === id && item.quantity >= quantity));
  app.innerHTML = `<h1>Корзина</h1>${rows.length
    ? `<div class="rows cart-rows">${rows.join('')}</div>
      ${cart.token && !checkingOut ? '<p class="muted">Повторите «Взять всё», чтобы подтвердить результат списания.</p>' :
        canTake || checkingOut ? '' : '<p class="danger">Остатки изменились. Уменьшите количество недоступных позиций.</p>'}
      <button class="primary" data-checkout${(!canTake && !cart.token) || checkingOut ? ' disabled' : ''}>${checkingOut ? 'Списание…' : 'Взять всё'}</button>`
    : '<p class="muted">В корзине пока ничего нет.</p>'}
    <div class="cart-return"><a class="button" data-nav href="/">К меню</a></div>`;
}

function changeCart(id, increase, button) {
  if (cart.token) return;
  const quantity = cart.items.get(id) || 0;
  const available = data.items.find(item => item.id === id)?.quantity || 0;
  if (increase && quantity >= available) return;
  const next = increase ? quantity + 1 : Math.min(quantity - 1, available);
  if (next > 0) cart.items.set(id, next);
  else cart.items.delete(id);
  cart.token = '';
  saveCart();
  if (isCart() && !state().itemID) {
    renderCart();
    app.querySelector(`[data-cart-${increase ? 'add' : 'minus'}="${id}"]`)?.focus({ preventScroll: true });
  } else {
    for (const button of app.querySelectorAll(`[data-cart-add="${id}"]`)) button.disabled = next >= available;
    showActionCloud(button.getBoundingClientRect(), 'Добавлено в корзину');
  }
}

async function checkout(button) {
  if (checkingOut || !cart.items.size) return;
  const bounds = button.getBoundingClientRect();
  if (!cart.token) {
    cart.token = Array.from(crypto.getRandomValues(new Uint8Array(16)), byte =>
      byte.toString(16).padStart(2, '0')).join('');
    saveCart();
  }
  const items = [...cart.items].map(([id, quantity]) => ({ id, quantity }));
  checkingOut = true;
  renderCart();
  try {
    await request('cart/take', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ token: cart.token, items }),
    });
    for (const { id, quantity } of items) {
      const item = data.items.find(entry => entry.id === id);
      if (item) item.quantity = Math.max(0, item.quantity - quantity);
    }
    cart.items.clear();
    cart.token = '';
    saveCart();
    showActionCloud(bounds, 'Взято');
  } catch (error) {
    if (error.status === 400 || error.status === 409) {
      cart.token = '';
      saveCart();
    }
    announce(error.message);
  } finally {
    checkingOut = false;
  }
  render();
  await refresh();
}

function neighbors(section, selected, detail) {
  const items = visitorItems(section).filter(item => matches(item, selected));
  const sectionOrder = new Map(data.sections.map((entry, index) => [entry.id, index]));
  // A sold-out detail can stay open after the last unit is taken.
  const precedes = item => item.section === detail.section
    ? item.position < detail.position || (item.position === detail.position && item.id < detail.id)
    : sectionOrder.get(item.section) < sectionOrder.get(detail.section);
  const previous = items.filter(precedes).pop();
  const next = items.find(item => item.id !== detail.id && !precedes(item));
  return { previous, next };
}

function detailArrow(item, direction, section, selected) {
  const label = direction === 'previous' ? 'Предыдущая позиция' : 'Следующая позиция';
  const arrow = direction === 'previous' ? '←' : '→';
  return item
    ? `<a class="button icon detail-arrow ${direction}" data-nav data-step href="${visitorURL(section?.id, selected, item.id)}" aria-label="${label}" title="${escapeHTML(item.name)}">${arrow}</a>`
    : `<button class="icon detail-arrow ${direction}" aria-label="${label}" disabled>${arrow}</button>`;
}

function renderMenuItems(items, section, selected) {
  if (!items.length) return '<p class="muted">Доступных позиций нет.</p>';
  if (menuView === 'list') {
    return `<div class="rows">${items.map(item => menuRow(item,
      visitorURL(section?.id, selected, item.id), `${item.quantity} шт.`,
      basket(item) + takeButton(item))).join('')}</div>`;
  }
  return `<div class="menu-grid">${items.map(item =>
    `<article class="menu-card">
      <a class="card-link" data-nav href="${visitorURL(section?.id, selected, item.id)}">
        ${thumbnail(item)}<span class="row-name"><strong>${escapeHTML(item.name)}</strong><small>${item.quantity} шт.</small></span>
      </a>
      <div class="card-stock row-actions">${basket(item, true)}${takeButton(item)}</div>
    </article>`).join('')}</div>`;
}

function renderHomeCover() {
  const { photo, caption } = data.homepage[0] || {};
  if (!photo && !caption) return '';
  return `<figure class="homepage-cover">
    ${photo ? `<img src="${photoURL({ photo })}" alt="">` : ''}
    ${caption ? `<figcaption>${escapeHTML(caption)}</figcaption>` : ''}
  </figure>`;
}

function renderHomeSections(items, selected) {
  if (!items.length) return renderMenuItems(items, undefined, selected);
  return data.sections.map(section => {
    const group = items.filter(item => item.section === section.id);
    if (!group.length) return '';
    return `<section class="home-section">
      <h2><a data-nav href="${visitorURL(section.id)}">${escapeHTML(section.name)}</a></h2>
      ${renderMenuItems(group, undefined, selected)}
    </section>`;
  }).join('');
}

function renderVisitor() {
  swipeAnimation?.();
  const { section, selected, detail, itemID } = state();
  const sectionLinks = data.sections.map(entry =>
    `<a class="button" data-nav href="${visitorURL(entry.id)}"${entry === section ? ' aria-current="page"' : ''}>${escapeHTML(entry.name)}</a>`).join('');
  const homeLink = `<a class="button" data-nav href="/"${!section && !isCart() ? ' aria-current="page"' : ''}>Главная</a>`;
  desktop.innerHTML = homeLink + sectionLinks;
  document.querySelector('#drawer-sections').innerHTML = homeLink + sectionLinks;
  if (isCart() && !itemID) {
    renderCart();
    return;
  }
  if (itemID) {
    const back = `<a class="button icon" data-nav href="${isCart() ? '/?cart=1' : visitorURL(section?.id, selected)}" aria-label="Вернуться к списку">←</a>`;
    if (!detail) {
      app.innerHTML = `<section class="detail"><div class="detail-heading">${back}<h1>Позиция не найдена</h1></div></section>`;
      return;
    }
    const place = data.places.find(entry => entry.id === detail.place);
    const tags = data.tags.filter(tag => itemTags(detail).includes(tag.id));
    const adjacent = neighbors(section, selected, detail);
    const alignment = detail.description_align === 'left' ? 'left' : 'center';
    app.innerHTML = `<article class="detail">
      ${isCart() ? '' : detailArrow(adjacent.previous, 'previous', section, selected)}
      ${isCart() ? '' : detailArrow(adjacent.next, 'next', section, selected)}
      <div class="detail-content">
        <div class="detail-heading">${back}<h1 id="detail-title">${escapeHTML(detail.name)}</h1></div>
        ${detail.quantity === 0 ? '<p class="ended danger">закончилась</p>' : ''}
        ${place ? `<p class="muted detail-place">${escapeHTML(place.name)}</p>` : ''}
        ${detail.photo ? `<img class="detail-photo" src="${photoURL(detail)}" alt="${escapeHTML(detail.name)}">` : ''}
        ${tags.length ? `<div class="tags">${tags.map(tag => `<span class="tag">${escapeHTML(tag.name)}</span>`).join('')}</div>` : ''}
        ${detail.description ? `<p class="detail-description align-${alignment}">${escapeHTML(detail.description)}</p>` : ''}
      </div>
      <div class="stock"><strong>${detail.quantity} шт.</strong>
      ${basket(detail)}${takeButton(detail, true)}</div>
    </article>`;
    return;
  }
  const available = visitorItems(section);
  const tagIDs = new Set(available.flatMap(itemTags));
  const tagButtons = data.tags.filter(tag => tagIDs.has(tag.id)).map(tag =>
    `<button class="tag" data-tag="${tag.id}" aria-pressed="${selected.has(tag.id)}">${escapeHTML(tag.name)}</button>`).join('');
  const items = available.filter(item => matches(item, selected));
  app.innerHTML = `${section ? '' : renderHomeCover()}<h1 class="menu-title">${escapeHTML(section?.name || menuTitle())}</h1>
    ${photoNote() ? `<p class="menu-photo-note">${escapeHTML(photoNote())}</p>` : ''}
    ${tagButtons || selected.size ? `<div class="tags" aria-label="Фильтр по тегам">${tagButtons}${selected.size ? '<button data-clear>Сбросить</button>' : ''}</div>` : ''}
    <div class="actions view-switch" role="group" aria-label="Вид меню">
      <button class="icon" data-view="grid" aria-label="Карточки" title="Карточки" aria-pressed="${menuView === 'grid'}"><span class="view-icon grid-icon" aria-hidden="true"></span></button>
      <button class="icon" data-view="list" aria-label="Список" title="Список" aria-pressed="${menuView === 'list'}"><span class="view-icon list-icon" aria-hidden="true"></span></button>
    </div>
    ${section ? renderMenuItems(items, section, selected) : renderHomeSections(items, selected)}`;
}

function adminState() {
  const params = new URLSearchParams(location.search);
  const requested = params.get('tab');
  const table = Object.hasOwn(labels, requested) ? requested : 'items';
  return { table, edit: params.get('edit') };
}

function adminURL(table, edit) {
  return '/admin?tab=' + table + (edit ? '&edit=' + edit : '');
}

function option(entry, selected) {
  return `<option value="${entry.id}"${selected === entry.id ? ' selected' : ''}>${escapeHTML(entry.name)}</option>`;
}

function nextPosition(entries) {
  return Math.max(0, ...entries.map(entry => entry.position)) + 10;
}

function adminForm(table, entry) {
  const section = entry?.section || data.sections[0]?.id;
  const position = entry?.position ?? (table === 'items'
    ? nextPosition(data.items.filter(item => item.section === section)) : nextPosition(data.sections));
  const ordered = table === 'sections' || table === 'items';
  const camera = '<input class="photo-input" id="camera" type="file" accept="image/*" capture="environment"><label class="button" for="camera">Снять фото</label>';
  return `<form class="admin-form">
    <label>Название<input name="name" maxlength="160" required value="${escapeHTML(entry?.name)}"></label>
    ${table === 'items' ? `
      <label>Раздел<select name="section" required>${data.sections.map(value => option(value, section)).join('')}</select></label>
      ${entry ? `<input type="hidden" name="original_quantity" value="${entry.quantity}">` : ''}
      <label>Количество, шт.<input name="quantity" type="number" min="0" max="2147483647" step="1" required value="${entry?.quantity ?? 0}"></label>` : ''}
    ${ordered ? `<label>Порядок (от меньшего к большему)<input name="position" type="number" step="1" required value="${position}"></label>` : ''}
    ${table === 'items' ? `
      <label>Место хранения<select name="place"><option value="">Не указано</option>${data.places.map(value => option(value, entry?.place)).join('')}</select></label>
      ${data.tags.length ? `<fieldset><legend>Теги</legend>${data.tags.map(tag =>
        `<label class="check"><input type="checkbox" name="tag" value="${tag.id}"${entry && itemTags(entry).includes(tag.id) ? ' checked' : ''}>${escapeHTML(tag.name)}</label>`).join('')}</fieldset>` : ''}
      <label>Выравнивание описания<select name="description_align">
        <option value="center">По центру</option>
        <option value="left"${entry?.description_align === 'left' ? ' selected' : ''}>По левому краю</option>
      </select></label>
      <label>Описание<textarea name="description" rows="4" class="align-${entry?.description_align === 'left' ? 'left' : 'center'}">${escapeHTML(entry?.description)}</textarea></label>
      <fieldset><legend>Фото</legend>
        ${entry?.photo ? `<img class="existing-photo" src="${photoURL(entry)}" alt="Текущее фото"><label class="check"><input type="checkbox" name="remove_photo" value="1">Удалить фото</label>` : ''}
        <div class="actions"><input class="photo-input" id="gallery" type="file" accept="image/*"><label class="button" for="gallery">Выбрать фото</label>${camera}</div>
        <p id="photo-selection" class="muted" hidden></p>
      </fieldset>` : ''}
    <div class="actions"><button class="primary" type="submit">${entry ? 'Сохранить' : 'Добавить'}</button>
    <a class="button" data-nav href="${adminURL(table)}">Отмена</a></div>
  </form>`;
}

function renderImportForm() {
  const example = {
    sections: [{ name: 'Еда', position: 10 }],
    tags: [{ name: 'Домашнее' }, { name: 'Сладкое' }],
    places: [{ name: 'Холодильник' }],
    items: [{
      name: 'Сырники',
      section: 'Еда',
      quantity: 6,
      position: 10,
      place: 'Холодильник',
      description: 'Творожные сырники к завтраку.',
      description_align: 'center',
      tags: ['Домашнее', 'Сладкое'],
      photo_base64: 'R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7',
    }],
  };
  return `<h2>Заполнение из файла</h2>
    <form class="admin-form" id="import-form">
      <label>Файл JSON<input name="file" type="file" accept=".json,application/json" required></label>
      <div class="actions">
        <button class="primary" type="submit">Загрузить</button>
        <button type="button" data-import-paste aria-haspopup="dialog">Вставить</button>
      </div>
      <p class="import-result" role="status" hidden></p>
      <details>
        <summary>Формат JSON и пример</summary>
        <p>Сохраните файл в UTF-8. Максимальный размер — 25 МиБ. Включите только нужные массивы.</p>
        <ul>
          <li><code>name</code> обязательно в каждой записи. Названия сравниваются без учёта регистра и крайних пробелов. Совпадение обновляет запись в её справочнике. Название позиции ищется по всему меню.</li>
          <li>Для новой позиции укажите <code>section</code> и целое <code>quantity</code> от нуля. Раздел, теги и место указывайте по названию. Они должны быть в файле или уже в админпанели.</li>
          <li><code>position</code> задаёт порядок от меньшего к большему. Новые записи без порядка добавляются в конец.</li>
          <li>Пропущенные поля сохраняют прежние значения. Чтобы убрать теги, передайте <code>[]</code>. Чтобы очистить описание, место или фото, передайте <code>""</code>.</li>
          <li><code>description_align</code> задаёт выравнивание описания. <code>center</code> — по центру, <code>left</code> — по левому краю.</li>
          <li><code>photo_base64</code> — содержимое JPEG, PNG, WebP или GIF в Base64 без префикса. В примере используется однопиксельный GIF.</li>
        </ul>
        <pre><code>${escapeHTML(JSON.stringify(example, null, 2))}</code></pre>
      </details>
    </form>
    <dialog class="description-dialog import-dialog" id="import-paste-dialog" aria-labelledby="import-paste-title">
      <h2 id="import-paste-title">Вставить JSON</h2>
      <form id="import-paste-form">
        <label for="import-paste-json">Текст JSON</label>
        <textarea id="import-paste-json" name="json" rows="10" spellcheck="false" required autofocus></textarea>
        <p class="import-result" role="status" hidden></p>
        <div class="actions">
          <button class="primary" type="submit">Импортировать</button>
          <button type="submit" formmethod="dialog" formnovalidate>Отмена</button>
        </div>
      </form>
    </dialog>`;
}

async function submitImport(form, button) {
  const dialog = form.closest('dialog');
  const body = dialog ? new Blob([form.elements.json.value]) : form.elements.file.files[0];
  const result = form.querySelector('.import-result');
  button.disabled = true;
  result.hidden = false;
  result.classList.remove('danger');
  result.textContent = 'Загрузка…';
  try {
    if (body.size > 25 * 1024 * 1024) throw new Error('Максимальный размер JSON — 25 МиБ.');
    const counts = await request('import', { method: 'POST', body, headers: { 'Content-Type': 'application/json' } });
    await refresh(() => !form.isConnected).catch(() => {});
    const message = `Добавлено — ${counts.created}. Обновлено — ${counts.updated}.`;
    form.reset();
    if (!form.isConnected) {
      announce(message);
      return;
    }
    const summary = dialog ? app.querySelector('#import-form .import-result') : result;
    summary.classList.remove('danger');
    summary.hidden = false;
    summary.textContent = message;
    if (dialog) {
      result.hidden = true;
      dialog.close();
    }
  } catch (error) {
    result.classList.add('danger');
    result.textContent = error.message;
  } finally {
    button.disabled = false;
  }
}

async function loadConnection(panel) {
  const select = panel.querySelector('select');
  const status = panel.querySelector('[role=status]');
  const figure = panel.querySelector('figure');
  const section = select.value;
  const isCurrent = () => panel.isConnected && select.value === section;
  status.textContent = 'Загрузка…';
  status.hidden = false;
  figure.hidden = true;
  try {
    const connection = await request('connection' + (section ? '?section=' + encodeURIComponent(section) : ''));
    if (!isCurrent()) return;
    figure.querySelector('img').src = 'data:image/png;base64,' + connection.png;
    const link = figure.querySelector('a');
    link.href = connection.url;
    link.textContent = connection.url;
    status.hidden = true;
    figure.hidden = false;
  } catch (error) {
    if (isCurrent()) status.textContent = error.message;
  }
}

function renderHomepageForm() {
  const { caption = '', photo } = data.homepage[0] || {};
  return `<h2>Главная</h2>
    <form class="admin-form" id="homepage-form">
      ${photo ? `<img class="existing-photo" src="${photoURL({ photo })}" alt="Текущее изображение главной">
        <label class="check"><input type="checkbox" name="remove_photo" value="1">Удалить изображение</label>` : ''}
      <label>Изображение<input name="photo" type="file" accept="image/*"></label>
      <label>Подпись<textarea name="caption" maxlength="1000" rows="3">${escapeHTML(caption)}</textarea></label>
      <div class="actions"><button class="primary" type="submit">Сохранить</button></div>
    </form>`;
}

function stockControls(item) {
  const busy = pending.has(item.id);
  return `<div class="admin-stock-actions" role="group" aria-label="Остаток: ${escapeHTML(item.name)}">
    <button type="button" class="icon" data-stock="take" data-stock-item="${item.id}"
      aria-label="Убрать одну штуку" title="Убрать одну штуку"${busy || item.quantity === 0 ? ' disabled' : ''}>−</button>
    <button type="button" class="icon" data-stock="restock" data-stock-item="${item.id}"
      aria-label="Добавить одну штуку" title="Добавить одну штуку"${busy || item.quantity >= 2147483647 ? ' disabled' : ''}>+</button>
  </div>`;
}

async function changeStock(button) {
  const id = Number(button.dataset.stockItem);
  const action = button.dataset.stock;
  if (pending.has(id)) return;
  const rowControls = button.parentElement.querySelectorAll('button');
  const previousStates = Array.from(rowControls, control => control.disabled);
  const page = location.href;
  pending.add(id);
  rowControls.forEach(control => { control.disabled = true; });
  try {
    await request(`items/${id}/${action}`, { method: 'POST' });
  } catch (error) {
    announce(error.message);
  } finally {
    pending.delete(id);
    rowControls.forEach((control, index) => { control.disabled = previousStates[index]; });
  }
  await refresh(() => location.href === page);
  if (location.href === page) {
    app.querySelector(`[data-stock-item="${id}"][data-stock="${action}"]`)?.focus({ preventScroll: true });
  }
}

function applyAppearance() {
  const raw = data.appearance?.[0]?.config;
  if (raw === appliedAppearance) return;
  appliedAppearance = raw;
  savedAppearance = raw ? JSON.parse(raw) : {};
  const title = menuTitle();
  document.title = title;
  document.querySelector('.brand').textContent = title;
  if (admin) document.querySelector('#mode-link').textContent = title;
  const style = document.documentElement.style;
  for (const themeName of ['light', 'dark']) {
    for (const role of Object.keys(colorLabels)) {
      const property = `--configured-${themeName}-${role}`;
      const value = savedAppearance[themeName]?.[role];
      if (value) style.setProperty(property, value);
      else style.removeProperty(property);
    }
  }

  const fontFile = savedAppearance.font === 'custom' ? savedAppearance.font_file : '';
  if (fontFile !== customFontFile) {
    if (customFont) document.fonts.delete(customFont);
    customFont = fontFile ? new FontFace('HomeMenu Upload', `url("/fonts/${encodeURIComponent(fontFile)}")`) : null;
    if (customFont) document.fonts.add(customFont);
    customFontFile = fontFile;
  }

  const family = {
    serif: 'Georgia, "Times New Roman", serif',
    mono: '"Courier New", ui-monospace, monospace',
    custom: '"HomeMenu Upload", system-ui, sans-serif',
  }[savedAppearance.font];
  if (family) {
    style.setProperty('--app-font', family);
    style.setProperty('--app-heading', family);
  } else {
    style.removeProperty('--app-font');
    style.removeProperty('--app-heading');
  }
}

function showPalette(form, themeName) {
  for (const panel of form.querySelectorAll('[data-palette]')) {
    panel.hidden = panel.dataset.palette !== themeName;
  }
}

function renderAppearanceForm() {
  const defaults = getComputedStyle(document.documentElement);
  const selectedTheme = currentTheme();
  const fields = ['light', 'dark'].map(themeName =>
    `<fieldset data-palette="${themeName}"${selectedTheme !== themeName ? ' hidden' : ''}><legend>Цвета</legend>
      <div class="appearance-colors">${Object.entries(colorLabels).map(([role, label]) => {
        const defaultColor = defaults.getPropertyValue(`--default-${themeName}-${role}`).trim();
        const value = savedAppearance[themeName]?.[role] || defaultColor;
        return `<label>${label}<input type="color" name="${themeName}-${role}" value="${escapeHTML(value)}"></label>`;
      }).join('')}</div>
    </fieldset>`).join('');

  return `<h2>Оформление</h2>
    <form class="admin-form appearance-form" id="appearance-form">
      <label>Название меню
        <input name="title" required maxlength="100" value="${escapeHTML(menuTitle())}">
      </label>
      <label>Подпись о фотографиях
        <textarea name="photo_note" rows="2" maxlength="300" aria-describedby="photo-note-hint">${escapeHTML(photoNote())}</textarea>
      </label>
      <p id="photo-note-hint" class="muted appearance-hint">Оставьте пустой, чтобы скрыть подпись.</p>
      <label>Палитра
        <select id="appearance-palette">
          <option value="light">Светлая тема</option>
          <option value="dark"${selectedTheme === 'dark' ? ' selected' : ''}>Тёмная тема</option>
        </select>
      </label>
      ${fields}
      <label>Шрифт
        <select name="font">${Object.entries(fontOptions).map(([id, label]) =>
          `<option value="${id}"${(savedAppearance.font || 'system') === id ? ' selected' : ''}>${label}</option>`).join('')}
        </select>
      </label>
      <label>Загрузить свой шрифт
        <input type="file" name="font_file" accept=".woff2,.woff,.ttf,.otf" aria-describedby="font-formats">
      </label>
      <p id="font-formats" class="muted current-font">WOFF2, WOFF, TTF или OTF. До 8 МиБ.</p>
      ${savedAppearance.font_file ? `<p class="current-font">${escapeHTML(savedAppearance.font_name)}</p>
        <label class="check"><input type="checkbox" name="remove_font" value="1">Удалить загруженный шрифт</label>` : ''}
      <div class="actions"><button class="primary" type="submit">Сохранить</button></div>
    </form>`;
}

async function saveAppearance(form, button) {
  const page = location.href;
  const body = new FormData();
  const palette = themeName => Object.fromEntries(Object.keys(colorLabels).map(role =>
    [role, form.elements[`${themeName}-${role}`].value]));
  body.set('appearance', JSON.stringify({
    light: palette('light'), dark: palette('dark'), font: form.elements.font.value,
    title: form.elements.title.value, photo_note: form.elements.photo_note.value,
  }));
  if (form.elements.font_file.files.length) body.set('font_file', form.elements.font_file.files[0]);
  if (form.elements.remove_font?.checked) body.set('remove_font', '1');
  const selectedPalette = form.querySelector('#appearance-palette').value;

  button.disabled = true;
  try {
    await request('appearance', { method: 'POST', body });
    await refresh(() => location.href === page);
    const currentPicker = app.querySelector('#appearance-palette');
    if (currentPicker) {
      currentPicker.value = selectedPalette;
      showPalette(currentPicker.form, selectedPalette);
    }
    announce('Оформление сохранено.');
  } catch (error) {
    announce(error.message);
  } finally {
    button.disabled = false;
  }
}

function renderAdmin() {
  const { table, edit } = adminState();
  const nav = Object.entries(labels).map(([key, label]) =>
    `<a class="button" data-nav href="${adminURL(key)}"${key === table ? ' aria-current="page"' : ''}>${label}</a>`).join('');
  const header = `<h1>Админпанель</h1><nav class="actions admin-nav" aria-label="Справочники">${nav}</nav>`;
  if (table === 'appearance') {
    app.innerHTML = header + renderAppearanceForm();
    return;
  }
  if (table === 'homepage') {
    app.innerHTML = header + renderHomepageForm();
    return;
  }
  if (table === 'connection') {
    app.innerHTML = header + `<section id="connection">
      <h2>Подключение</h2>
      <p>Отсканируйте QR-код камерой телефона в той же локальной сети.</p>
      <label class="connection-target">Страница для QR-кода
        <select id="connection-section">
          <option value="">Главная</option>
          ${data.sections.map(section => option(section)).join('')}
        </select>
      </label>
      <p role="status">Загрузка…</p>
      <figure hidden>
        <img width="256" height="256" alt="QR-код для открытия домашнего меню">
        <figcaption><a></a></figcaption>
      </figure>
    </section>`;
    loadConnection(app.querySelector('#connection'));
    return;
  }
  if (table === 'import') {
    app.innerHTML = header + renderImportForm();
    return;
  }
  const entry = data[table].find(value => value.id === Number(edit));
  const entries = table === 'items'
    ? data.sections.flatMap(section => data.items.filter(item => item.section === section.id))
    : data[table];
  app.innerHTML = `${header}
    <h2>${labels[table]}</h2>${edit
      ? ((edit === 'new' || entry) ? adminForm(table, entry) : '<p>Запись не найдена.</p>')
      : `${table === 'items' && !data.sections.length ? '<p>Добавьте раздел, чтобы создать позицию.</p>' :
        `<a class="button primary" data-nav href="${adminURL(table, 'new')}">Добавить</a>`}
        <div>${entries.map(value => {
          const parent = data.sections.find(section => section.id === value.section);
          const summary = parent ? `${parent.name} · ${value.quantity} шт. · порядок ${value.position}` :
            (table === 'sections' ? `Порядок ${value.position}` : '');
          return `<article class="admin-row"><div><strong>${escapeHTML(value.name)}</strong>
            ${summary ? `<div class="muted">${escapeHTML(summary)}</div>` : ''}</div>
            <div class="actions"><a class="button" data-nav href="${adminURL(table, value.id)}">Изменить</a>
            <button class="danger" data-delete="${value.id}">Удалить</button>
            ${table === 'items' ? stockControls(value) : ''}</div></article>`;
        }).join('')}</div>`}`;
}

function render() {
  if (!data) return;
  if (admin) renderAdmin();
  else renderVisitor();
}

function scrollToDetail() {
  const heading = app.querySelector('.detail-heading');
  if (!heading) return false;
  heading.scrollIntoView({ block: 'start' });
  return true;
}

function navigate(url, scroll = true, replace = false) {
  if (replace) history.replaceState(null, '', url);
  else history.pushState(null, '', url);
  render();
  if (scroll && !scrollToDetail()) window.scrollTo(0, 0);
}

function animateDetailSwipe(detail, href, direction) {
  const content = detail.querySelector('.detail-content');
  if (matchMedia('(prefers-reduced-motion: reduce)').matches || !content?.animate) {
    navigate(href, true, true);
    return;
  }

  swipeAnimation?.();
  const bounds = content.getBoundingClientRect();
  const frame = document.createElement('div');
  frame.className = 'detail-swipe-frame';
  frame.inert = true;
  frame.setAttribute('aria-hidden', 'true');
  const outgoing = content.cloneNode(true);
  outgoing.querySelectorAll('[id]').forEach(element => element.removeAttribute('id'));
  Object.assign(outgoing.style, {
    position: 'absolute',
    top: bounds.top + 'px',
    left: bounds.left + 'px',
    width: bounds.width + 'px',
  });
  frame.append(outgoing);

  navigate(href, true, true);
  const incoming = app.querySelector('.detail-content');
  if (!incoming) return;
  document.body.append(frame);

  const offset = Math.round(bounds.width * .32) * (direction === 'next' ? 1 : -1);
  const options = { duration: 260, easing: 'cubic-bezier(.22, .72, .24, 1)' };
  const exit = outgoing.animate([
    { transform: 'translateX(0)', opacity: 1 },
    { transform: `translateX(${-offset}px)`, opacity: 0 },
  ], options);
  const enter = incoming.animate([
    { transform: `translateX(${offset}px)`, opacity: 0 },
    { transform: 'translateX(0)', opacity: 1 },
  ], options);

  const cleanUp = () => {
    frame.remove();
    exit.cancel();
    enter.cancel();
    swipeAnimation = undefined;
  };
  swipeAnimation = cleanUp;
  enter.finished.then(cleanUp, () => {});
}

function showActionCloud(bounds, message) {
  const status = document.querySelector('#action-status');
  status.textContent = '';
  requestAnimationFrame(() => { status.textContent = message; });
  const cloud = document.createElement('span');
  cloud.className = 'taken-cloud';
  cloud.setAttribute('aria-hidden', 'true');
  cloud.textContent = message;
  document.body.append(cloud);
  const width = cloud.offsetWidth;
  cloud.style.left = Math.max(8, Math.min(innerWidth - width - 8, bounds.left + (bounds.width - width) / 2)) + 'px';
  cloud.style.top = Math.max(8, Math.min(innerHeight - 42, bounds.top)) + 'px';
  cloud.addEventListener('animationend', () => cloud.remove(), { once: true });
}

async function take(id, button) {
  if (pending.has(id)) return;
  const bounds = button.getBoundingClientRect();
  button.disabled = true;
  pending.add(id);
  try {
    await request('items/' + id + '/take', { method: 'POST' });
    const item = data.items.find(entry => entry.id === id);
    if (item && item.quantity > 0) item.quantity--;
    showActionCloud(bounds, 'Взято');
  } catch (error) {
    announce(error.message);
  } finally {
    pending.delete(id);
    button.disabled = false;
  }
  render();
  await refresh();
}

document.addEventListener('click', async event => {
  const link = event.target.closest('a[data-nav]');
  if (link && !event.ctrlKey && !event.metaKey && !event.shiftKey && !event.altKey) {
    event.preventDefault();
    if (drawer.open) drawer.close();
    navigate(link.href, true, link.hasAttribute('data-step'));
    return;
  }
  const button = event.target.closest('button');
  if (!button || !data) return;
  try {
    if (button.hasAttribute('data-import-paste')) {
      app.querySelector('#import-paste-dialog').showModal();
    } else if (button.dataset.view) {
      menuView = button.dataset.view;
      localStorage.setItem('homemenu-view', menuView);
      renderVisitor();
      app.querySelector(`[data-view="${menuView}"]`).focus({ preventScroll: true });
    } else if (button.dataset.cartAdd) {
      changeCart(Number(button.dataset.cartAdd), true, button);
    } else if (button.dataset.cartMinus) {
      changeCart(Number(button.dataset.cartMinus), false, button);
    } else if (button.hasAttribute('data-checkout')) {
      await checkout(button);
    } else if (button.dataset.take) {
      await take(Number(button.dataset.take), button);
    } else if (button.hasAttribute('data-tag') || button.hasAttribute('data-clear')) {
      const { section, selected } = state();
      if (button.hasAttribute('data-clear')) selected.clear();
      else {
        const id = Number(button.dataset.tag);
        if (selected.has(id)) selected.delete(id);
        else selected.add(id);
      }
      navigate(visitorURL(section?.id, selected), false);
      app.querySelector(`[data-tag="${button.dataset.tag}"]`)?.focus();
    } else if (button.dataset.stock) {
      await changeStock(button);
    } else if (button.dataset.delete && confirm('Удалить запись?')) {
      button.disabled = true;
      try {
        await request(adminState().table + '/' + button.dataset.delete, { method: 'DELETE' });
        await refresh();
        announce('Запись удалена.');
      } finally {
        button.disabled = false;
      }
    }
  } catch (error) {
    announce(error.message);
  }
});

app.addEventListener('change', event => {
  const input = event.target;
  if (input.id === 'appearance-palette') {
    showPalette(input.form, input.value);
    return;
  }
  if (input.name === 'font_file' && input.files.length) {
    input.form.elements.font.value = 'custom';
    if (input.form.elements.remove_font) input.form.elements.remove_font.checked = false;
    return;
  }
  if (input.name === 'remove_font' && input.checked && input.form.elements.font.value === 'custom') {
    input.form.elements.font.value = 'system';
    return;
  }
  if (input.id === 'connection-section') {
    loadConnection(input.closest('#connection'));
    return;
  }
  if (input.name === 'description_align') {
    input.form.elements.description.className = 'align-' + input.value;
  }
  if (input.id === 'camera' || input.id === 'gallery') {
    if (!input.files.length) return;
    document.querySelector(input.id === 'camera' ? '#gallery' : '#camera').value = '';
    const selection = document.querySelector('#photo-selection');
    selection.textContent = input.files[0].name;
    selection.hidden = false;
  }
  if (input.name === 'section' && adminState().edit === 'new') {
    input.form.elements.position.value = nextPosition(data.items.filter(item => item.section === Number(input.value)));
  }
});

async function preparePhoto(file) {
  if (/^image\/(jpeg|png|webp|gif)$/.test(file.type)) return file;
  const bitmap = await createImageBitmap(file).catch(() => {
    throw new Error('Не удалось открыть фото. Выберите JPEG, PNG, WebP или GIF.');
  });
  const ratio = Math.min(1, 2000 / Math.max(bitmap.width, bitmap.height));
  const canvas = document.createElement('canvas');
  canvas.width = Math.max(1, Math.round(bitmap.width * ratio));
  canvas.height = Math.max(1, Math.round(bitmap.height * ratio));
  canvas.getContext('2d').drawImage(bitmap, 0, 0, canvas.width, canvas.height);
  bitmap.close();
  return new Promise((resolve, reject) => canvas.toBlob(
    blob => blob ? resolve(blob) : reject(new Error('Не удалось подготовить фото.')), 'image/jpeg', 0.88,
  ));
}

app.addEventListener('submit', async event => {
  if (event.target.method === 'dialog' || event.submitter?.formMethod === 'dialog') return;
  event.preventDefault();
  const { table, edit } = adminState();
  const form = event.target;
  const button = form.querySelector('button[type=submit]');
  if (button.disabled) return;
  if (form.id === 'appearance-form') {
    await saveAppearance(form, button);
    return;
  }
  if (form.id === 'import-form' || form.id === 'import-paste-form') {
    await submitImport(form, button);
    return;
  }
  const body = new FormData(form);
  const photo = form.querySelector('#camera')?.files[0] || form.querySelector('#gallery')?.files[0] ||
    form.querySelector('[type=file][name=photo]')?.files[0];
  button.disabled = true;
  try {
    if (photo) {
      const prepared = await preparePhoto(photo);
      body.set('photo', prepared, prepared === photo ? photo.name : 'photo.jpg');
    }
    await request(table + (edit && edit !== 'new' ? '/' + edit : ''), { method: 'POST', body });
    const sameForm = () => form.isConnected;
    await refresh(() => !sameForm());
    if (sameForm()) navigate(adminURL(table), true, true);
    announce('Изменения сохранены.');
  } catch (error) {
    announce(error.message);
  } finally {
    button.disabled = false;
  }
});

app.addEventListener('touchstart', event => {
  const detail = event.target.closest('.detail');
  const point = event.touches[0];
  touch = !admin && data && event.touches.length === 1 && detail &&
    !event.target.closest('a,button') ? { detail, x: point.clientX, y: point.clientY, horizontal: false } : null;
}, { passive: true });

app.addEventListener('touchmove', event => {
  if (!touch) return;
  if (event.touches.length !== 1) {
    touch = null;
    return;
  }
  const point = event.touches[0];
  const dx = Math.abs(point.clientX - touch.x);
  const dy = Math.abs(point.clientY - touch.y);
  if (!touch.horizontal && dy > 12 && dy >= dx) {
    touch = null;
    return;
  }
  if (dx > 12 && dx > dy) touch.horizontal = true;
  if (touch.horizontal && event.cancelable) event.preventDefault();
}, { passive: false });

app.addEventListener('touchend', event => {
  if (!touch) return;
  const start = touch;
  touch = null;
  if (!start.detail.isConnected || drawer.open) return;
  const point = event.changedTouches[0];
  const dx = point.clientX - start.x;
  if (Math.abs(dx) < 45 || Math.abs(dx) < Math.abs(point.clientY - start.y) * 1.25) return;
  const direction = dx < 0 ? 'next' : 'previous';
  const link = start.detail.querySelector(`a.detail-arrow.${direction}`);
  if (link) animateDetailSwipe(start.detail, link.href, direction);
}, { passive: true });

app.addEventListener('touchcancel', () => { touch = null; }, { passive: true });

window.addEventListener('storage', event => {
  if (event.key !== cartKey || checkingOut) return;
  cart = readCart();
  updateCartBadge();
  if (!admin) render();
});

window.addEventListener('popstate', () => {
  render();
  if (app.querySelector('.detail-heading')) requestAnimationFrame(scrollToDetail);
});
if (!admin) {
  window.addEventListener('focus', () => refresh().catch(() => {}));
}
refresh().then(scrollToDetail).catch(error => {
  app.removeAttribute('aria-busy');
  app.textContent = error.message;
});
