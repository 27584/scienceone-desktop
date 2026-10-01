/*
 * Injected into the platform's page before window.onload.
 *
 * Two jobs:
 *   1. a floating control panel, isolated in a shadow root so the host page's
 *      styles cannot leak in and ours cannot leak out
 *   2. a DOM automation engine that fills the MCP form by matching the visible
 *      Chinese labels, which survive UI refactors far better than class names
 */
(function () {
  if (window.__panshiDesktop) return;
  window.__panshiDesktop = true;

  var HOST_ID = 'panshi-desktop-panel';

  // -------------------------------------------------------------------------
  // bridge to the native side
  // -------------------------------------------------------------------------

  function call(action, payload) {
    return new Promise(function (resolve, reject) {
      if (typeof window.panshiNative !== 'function') {
        reject(new Error('native bridge not available'));
        return;
      }
      Promise.resolve(window.panshiNative(JSON.stringify({ action: action, payload: payload || {} })))
        .then(function (raw) {
          try {
            resolve(JSON.parse(raw));
          } catch (e) {
            reject(new Error('bad response: ' + String(raw).slice(0, 200)));
          }
        })
        .catch(reject);
    });
  }
  window.__panshiCall = call;

  /**
   * Tell the native side what the injection is doing. Without this, a page the
   * script failed to reach looks identical to one it reached successfully.
   */
  function report(msg, data) {
    try {
      call('report', { msg: msg, data: data === undefined ? null : data });
    } catch (e) {
      /* the native side may not be listening yet */
    }
  }

  // -------------------------------------------------------------------------
  // DOM automation
  // -------------------------------------------------------------------------

  // Labels as the platform actually renders them, read off the live
  // "创建 MCP" dialog: 名称* / 描述* / URL* / 鉴权方式* / 学科*.
  var FIELD_LABELS = {
    name: ['名称'],
    description: ['描述'],
    url: ['URL'],
    auth: ['鉴权方式'],
    subject: ['学科'],
    headers: ['请求头', 'Headers'],
    document: ['说明文档', '上传文档'],
  };

  function visible(el) {
    if (!el) return false;
    var r = el.getBoundingClientRect();
    if (r.width === 0 && r.height === 0) return false;
    var s = getComputedStyle(el);
    return s.visibility !== 'hidden' && s.display !== 'none' && s.opacity !== '0';
  }

  /**
   * Every element whose own text matches.
   *
   * Both leaf text nodes and elements whose children are only inline markup are
   * considered: the platform renders "名称*" as <label>名称<span>*</span></label>,
   * so requiring a leaf would miss every field label on the form.
   */
  function textNodes(text, root) {
    var out = [];
    var scope = root || document;
    var all = scope.querySelectorAll('label,span,div,p,td,th,dt,legend,h1,h2,h3,h4,button,a');
    for (var i = 0; i < all.length; i++) {
      var el = all[i];
      var t = (el.textContent || '').trim();
      if (!t || t.indexOf(text) === -1) continue;
      // Skip wrappers that merely contain the real label further down.
      if (t.length > text.length + 12) continue;
      if (!visible(el)) continue;
      out.push(el);
    }
    return out;
  }

  /**
   * Find the control that belongs to a visible label.
   *
   * Walks up from the label until an input appears, which is how both plain
   * HTML forms and component-library forms are laid out.
   */
  /**
   * Find a form field by label, preferring the dialog and the closest match.
   *
   * Naive text matching picks the wrong element: "名称" also appears in the page
   * search box, and "URL" inside other copy. Two rules fix that — scope to the
   * open dialog when there is one, and prefer the shortest label text that
   * contains the keyword (a real field label is terse; a wrapper is not).
   *
   * `kind` selects what counts as the field: a text control for typing into, or
   * a picker wrapper for clicking open. The two must not be confused — a picker's
   * input carries the chosen option text as its value.
   */
  function findFieldByLabel(keyword, kind, root) {
    var scopes = [];
    var dialog = findFormScope();
    if (dialog !== document) scopes.push(dialog);
    scopes.push(root || document);

    for (var s = 0; s < scopes.length; s++) {
      var scope = scopes[s];
      var best = null;
      var els = scope.querySelectorAll('label,span,div,p,td,th,dt,legend');
      for (var i = 0; i < els.length; i++) {
        var el = els[i];
        var t = (el.textContent || '').trim();
        if (!t || t.indexOf(keyword) === -1) continue;
        if (!visible(el)) continue;
        // The platform renders "名称*" as <label>名称<span>*</span></label>, so a
        // leaf-only rule misses every field. A length bound does the real work
        // instead: a label is terse, a wrapper spans the whole form.
        if (t.length > keyword.length + 4) continue;
        var control = kind === 'picker' ? pickerNear(el) : controlNear(el);
        if (!control) continue;
        if (!best || t.length < best.len) best = { el: control, len: t.length, label: t };
      }
      if (best) return best;
    }
    return null;
  }

  /**
   * The picker wrapper belonging to a label.
   *
   * The clickable element is the span.w-select, not the input inside it: clicking
   * the input does nothing.
   */
  function pickerNear(labelEl) {
    var sel = '[class*=w-select]:not([class*=w-select-view-input]), [role=combobox]';

    function usable(el) {
      return el && visible(el);
    }

    var own = labelEl.querySelector(sel);
    if (usable(own)) return own;

    var cur = labelEl;
    for (var depth = 0; depth < 5 && cur; depth++) {
      var parent = cur.parentElement;
      if (!parent) break;
      var found = parent.querySelector(sel);
      if (usable(found)) return found;
      var next = cur.nextElementSibling;
      if (next) {
        var sib = next.matches(sel) ? next : next.querySelector(sel);
        if (usable(sib)) return sib;
      }
      cur = parent;
    }
    return null;
  }

  /**
   * The control belonging to a label: inside it, or a sibling within a few levels.
   *
   * Picker inputs are skipped. A w-select's input holds the *chosen option text*
   * as its value, so matching one would both mis-target the field and, when
   * written to, overwrite the picker's selection — "自定义请求头" was being
   * clobbered by the header value for exactly this reason.
   */
  function controlNear(labelEl) {
    var sel = 'textarea, input:not([type=hidden]), select, [contenteditable=true]';
    var picker = '[class*=w-select], [class*=picker], [role=combobox]';

    function usable(el) {
      if (!el || !visible(el)) return false;
      if (el.closest(picker)) return false;
      return true;
    }

    var own = labelEl.querySelector(sel);
    if (usable(own)) return own;

    var cur = labelEl;
    for (var depth = 0; depth < 5 && cur; depth++) {
      var parent = cur.parentElement;
      if (!parent) break;
      var found = parent.querySelector(sel);
      if (usable(found)) return found;
      // Also look at the following sibling, which is how many layouts place the input.
      var next = cur.nextElementSibling;
      if (next) {
        var sib = next.matches(sel) ? next : next.querySelector(sel);
        if (usable(sib)) return sib;
      }
      cur = parent;
    }
    return null;
  }

  /**
   * Fall back to the placeholder text.
   *
   * The labels are rendered as siblings of the inputs in some layouts, so the
   * ancestor walk finds nothing. Placeholders are stable and describe the field
   * unambiguously — "仅允许字母、数字、下划线" can only be the name field.
   */
  function fieldByPlaceholder(hints, root) {
    var scope = root || document;
    var els = scope.querySelectorAll('input,textarea');
    for (var i = 0; i < els.length; i++) {
      var el = els[i];
      if (!visible(el)) continue;
      var ph = el.getAttribute('placeholder') || '';
      for (var j = 0; j < hints.length; j++) {
        if (ph.indexOf(hints[j]) !== -1) return el;
      }
    }
    return null;
  }

  var PLACEHOLDER_HINTS = {
    name: ['仅允许字母', '名称'],
    description: ['用途', '描述'],
    url: ['MCP 服务器的访问地址', 'example.com/mcp', 'https://'],
    headers: ['KEY=value', '请求头'],
  };

  /** Label match first, placeholder second. `kind` is 'text' or 'picker'. */
  function findField(key, kind, root) {
    var labels = FIELD_LABELS[key] || [];
    for (var i = 0; i < labels.length; i++) {
      var hit = findFieldByLabel(labels[i], kind, root);
      if (hit) return { el: hit.el, how: 'label "' + hit.label + '"' };
    }
    if (kind === 'picker') return null; // placeholders only help text fields
    var ph = fieldByPlaceholder(PLACEHOLDER_HINTS[key] || [], root);
    if (ph) return { el: ph, how: 'placeholder "' + (ph.getAttribute('placeholder') || '') + '"' };
    return null;
  }

  /**
   * Walk up to the element that actually handles the click.
   *
   * A card's title is a leaf <div>, but the handler sits on an ancestor. Anything
   * styled with a pointer cursor counts, which is how list rows and cards behave
   * in every UI kit.
   */
  function clickableAncestor(el) {
    var cur = el;
    for (var i = 0; i < 6 && cur && cur !== document.body; i++) {
      if (cur.tagName === 'BUTTON' || cur.tagName === 'A') return cur;
      if (cur.getAttribute && (cur.getAttribute('role') === 'button' || cur.getAttribute('role') === 'option')) return cur;
      if (getComputedStyle(cur).cursor === 'pointer') return cur;
      cur = cur.parentElement;
    }
    return el;
  }

  /** Find a clickable element by its visible text, exact match first. */
  function findClickable(text, root) {
    var scope = root || document;
    var els = scope.querySelectorAll('button,a,div,span,li,p,h1,h2,h3,h4,strong');
    var loose = null;
    for (var i = 0; i < els.length; i++) {
      var el = els[i];
      if (!visible(el)) continue;
      var t = (el.textContent || '').trim();
      if (t === text) return clickableAncestor(el);
      if (!loose && t.length <= 12 && t.indexOf(text) !== -1) loose = el;
    }
    return loose ? clickableAncestor(loose) : null;
  }

  /** Just the text control for a label, for callers that do not care how. */
  function fieldByLabel(keyword, kind) {
    var hit = findFieldByLabel(keyword, kind || 'text');
    return hit ? hit.el : null;
  }

  function fieldByAnyLabel(labels, kind, root) {
    for (var i = 0; i < labels.length; i++) {
      var hit = findFieldByLabel(labels[i], kind || 'text', root);
      if (hit) return { el: hit.el, label: hit.label };
    }
    return null;
  }

  /**
   * Find a clickable element by its visible text.
   *
   * Two passes: an exact match first, then a looser contains-match. Without the
   * ordering, "创建" hits the "我的创建" tab instead of the 创建 button.
   */
  function buttonByText(texts, root) {
    var scope = root || document;
    var els = scope.querySelectorAll('button,a,div[role=button],span[role=button],li');
    var loose = null;
    for (var i = 0; i < els.length; i++) {
      var el = els[i];
      if (!visible(el)) continue;
      var t = (el.textContent || '').trim();
      for (var j = 0; j < texts.length; j++) {
        if (t === texts[j]) return el;
        if (!loose && t.length <= 12 && t.indexOf(texts[j]) !== -1) loose = el;
      }
    }
    return loose;
  }

  /**
   * Open a component-library picker and click the option that matches.
   *
   * The platform's dropdowns are a span.w-select wrapping an input
   * (w-select-view-input), not a <select>, so assigning .value is invisible to
   * the framework — the value only moves through the UI. Clicking the input
   * itself does nothing either; the wrapper has to be clicked to expand the
   * list, whose items are div.option inside div.options.
   */
  /**
   * Open a component-library picker and click the option that matches.
   *
   * The platform's dropdowns are a span.w-select wrapping an input, not a
   * <select>, so assigning .value is invisible to the framework — the value
   * only moves through the UI.
   *
   * The options carry no class at all (they are bare <strong>标题</strong> plus a
   * <span>描述</span>), so they cannot be selected by class. Instead the DOM is
   * diffed across the click: whatever becomes visible is the dropdown, and the
   * matched <strong> is the click target.
   */
  async function pickOption(control, matcher) {
    var before = visibleTextSet();
    var wrapper = control.closest('[class*=w-select]') || control.parentElement || control;
    wrapper.click();
    await sleep(900);

    // Whatever appeared is the dropdown, but the page also reveals unrelated
    // text (the sidebar's "ScienceHub" among it), so the search is narrowed to
    // the container that gained the most new leaves — that is the option list.
    var list = largestFreshContainer(before);
    if (!list) {
      wrapper.click();
      await sleep(250);
      return null;
    }

    var leaves = list.querySelectorAll('strong, div, li, span, a');
    for (var i = 0; i < leaves.length; i++) {
      var el = leaves[i];
      if (!visible(el)) continue;
      if (el.querySelector('strong, div, li, a')) continue; // leaves only
      var t = (el.textContent || '').trim();
      if (!t || t.length > 24) continue;
      if (matcher(t)) {
        // Click the row, not the bold title: the handler is on the wrapper.
        (el.closest('div,li') || el).click();
        await sleep(500);
        return t;
      }
    }

    wrapper.click();
    await sleep(250);
    return null;
  }

  /**
   * The element that gained the most newly-visible leaf text since the snapshot.
   * That is the popup list, as opposed to unrelated text revealed elsewhere.
   */
  function largestFreshContainer(before) {
    var best = null;
    var bestCount = 0;
    var all = document.querySelectorAll('div,ul,ol,section');
    for (var i = 0; i < all.length; i++) {
      var el = all[i];
      var r = el.getBoundingClientRect();
      if (r.width === 0 || r.height === 0) continue;
      var count = 0;
      var leaves = el.querySelectorAll('strong, div, li, span, a');
      for (var j = 0; j < leaves.length; j++) {
        var leaf = leaves[j];
        if (!visible(leaf) || leaf.children.length > 0) continue;
        var t = (leaf.textContent || '').trim();
        if (!t || t.length > 24) continue;
        var lr = leaf.getBoundingClientRect();
        if (!before[leaf.tagName + '|' + t + '|' + Math.round(lr.top)]) count++;
      }
      if (count > bestCount) {
        bestCount = count;
        best = el;
      }
    }
    return bestCount >= 2 ? best : null;
  }

  /** A set of visible leaf texts, used to diff the DOM across a click. */
  function visibleTextSet() {
    var set = {};
    var all = document.querySelectorAll('*');
    for (var i = 0; i < all.length; i++) {
      var el = all[i];
      var r = el.getBoundingClientRect();
      if (r.width === 0 || r.height === 0) continue;
      if (el.children.length > 0) continue;
      var t = (el.textContent || '').trim();
      if (!t || t.length > 24) continue;
      set[el.tagName + '|' + t + '|' + Math.round(r.top)] = 1;
    }
    return set;
  }

  /** The options a picker is offering right now, for diagnostics. */
  function listOptions() {
    var out = [];
    var all = document.querySelectorAll('strong, div.option, div.option-text, [role=option]');
    for (var i = 0; i < all.length; i++) {
      if (!visible(all[i])) continue;
      var t = (all[i].textContent || '').trim();
      if (t && t.length <= 24 && !all[i].querySelector('strong, div')) out.push(t);
    }
    return out;
  }

  /**
   * Set a value the way a framework will notice.
   *
   * Assigning .value directly bypasses Vue's and React's change tracking, so the
   * native setter is used and the events the frameworks listen for are fired.
   */
  function setValue(el, value) {
    var proto =
      el instanceof HTMLTextAreaElement
        ? HTMLTextAreaElement.prototype
        : el instanceof HTMLSelectElement
          ? HTMLSelectElement.prototype
          : HTMLInputElement.prototype;
    var desc = Object.getOwnPropertyDescriptor(proto, 'value');
    el.focus();
    if (desc && desc.set) desc.set.call(el, value);
    else el.value = value;
    ['input', 'change', 'blur'].forEach(function (name) {
      el.dispatchEvent(new Event(name, { bubbles: true }));
    });
  }

  /** Attach an in-memory file to a file input, the only way to satisfy one programmatically. */
  function attachFile(input, name, content, mime) {
    var file = new File([content], name, { type: mime || 'text/markdown' });
    var dt = new DataTransfer();
    dt.items.add(file);
    input.files = dt.files;
    input.dispatchEvent(new Event('change', { bubbles: true }));
  }

  function findFileInput(root) {
    var scope = root || document;
    var inputs = scope.querySelectorAll('input[type=file]');
    for (var i = 0; i < inputs.length; i++) {
      if (visible(inputs[i]) || inputs[i].offsetParent !== null) return inputs[i];
    }
    return inputs.length ? inputs[0] : null;
  }

  function sleep(ms) {
    return new Promise(function (r) {
      setTimeout(r, ms);
    });
  }

  /** Poll until fn() returns something truthy, or give up. */
  async function waitFor(fn, timeoutMs, intervalMs) {
    var deadline = Date.now() + (timeoutMs || 8000);
    var step = intervalMs || 250;
    for (;;) {
      var v = fn();
      if (v) return v;
      if (Date.now() > deadline) return null;
      await sleep(step);
    }
  }

  /**
   * Inventory the page so a human can see what the automation has to work with.
   * This is the escape hatch when the labels change.
   */
  function probe() {
    var controls = [];
    var els = document.querySelectorAll('input,textarea,select,[contenteditable=true]');
    for (var i = 0; i < els.length; i++) {
      var el = els[i];
      if (!visible(el)) continue;
      controls.push({
        tag: el.tagName.toLowerCase(),
        type: el.getAttribute('type') || '',
        placeholder: el.getAttribute('placeholder') || '',
        name: el.getAttribute('name') || '',
        id: el.id || '',
        className: String(el.className || '').slice(0, 120),
        nearText: nearestLabelText(el),
      });
    }
    var buttons = [];
    var bs = document.querySelectorAll('button,a[role=button],div[role=button]');
    for (var j = 0; j < bs.length; j++) {
      if (!visible(bs[j])) continue;
      var t = (bs[j].textContent || '').trim();
      if (t && t.length <= 20) buttons.push(t);
    }
    return {
      url: location.href,
      title: document.title,
      controls: controls,
      buttons: buttons.slice(0, 60),
      fileInputs: document.querySelectorAll('input[type=file]').length,
    };
  }

  function nearestLabelText(el) {
    var cur = el;
    for (var i = 0; i < 5 && cur; i++) {
      var t = (cur.parentElement ? cur.parentElement.textContent : '') || '';
      t = t.trim();
      if (t && t.length < 60) return t;
      cur = cur.parentElement;
    }
    return '';
  }

  // -------------------------------------------------------------------------
  // form filling
  // -------------------------------------------------------------------------

  async function fillForm(cfg, root) {
    var report = [];
    var scope = root || document;

    function note(field, ok, detail) {
      report.push({ field: field, ok: ok, detail: detail || '' });
    }

    function fill(labelKey, value) {
      if (!value) return;
      var hit = findField(labelKey, 'text', scope);
      if (!hit) {
        note(labelKey, false, 'no control found for ' + (FIELD_LABELS[labelKey] || []).join(' / '));
        return;
      }
      setValue(hit.el, value);
      note(labelKey, true, 'matched ' + hit.how);
    }

    fill('name', cfg.name);
    fill('description', cfg.description);
    fill('url', cfg.mcpUrl);

    // 鉴权方式 is a component-library picker: it has to be clicked open and have
    // an option clicked, not assigned.
    var authHit = findField('auth', 'picker', scope);
    if (authHit) {
      var picked = await pickOption(authHit.el, function (t) {
        return /自定义|请求头|header/i.test(t);
      });
      if (picked) {
        note('auth', true, 'picked "' + picked + '"');
      } else {
        note('auth', false, 'offered: ' + (listOptions().join(' / ') || 'the list did not open'));
      }
    } else {
      note('auth', false, 'control not found');
    }

    // 学科 is required. 信息科学 is the requested value; any other real subject
    // is used only as a fallback so the field is never left blank.
    var subjHit = findField('subject', 'picker', scope);
    if (subjHit) {
      var subj = await pickOption(subjHit.el, function (t) {
        return t.indexOf('信息科学') !== -1;
      });
      if (!subj) {
        subj = await pickOption(subjHit.el, function (t) {
          return /数学|物理|化学|生物|计算机|工程|材料|医学|地球|环境/.test(t);
        });
      }
      note('subject', !!subj, subj ? 'picked "' + subj + '"' : 'the list did not open');
    } else {
      note('subject', false, 'control not found');
    }

    // 请求头 only appears once a custom-header auth mode is chosen, and it is a
    // list that starts empty: a row has to be added before anything is typed.
    if (cfg.headers) {
      var hdr = await fillHeaders(scope, cfg.headers);
      note('headers', hdr.ok, hdr.detail);
    }

    if (cfg.documentContent) {
      var fi = findFileInput(scope);
      if (fi) {
        attachFile(fi, cfg.documentName || 'local-dev-mcp.md', cfg.documentContent);
        note('document', true, 'attached ' + (cfg.documentName || 'local-dev-mcp.md'));
      } else {
        note('document', false, 'no file input on this page');
      }
    }

    return report;
  }

  // -------------------------------------------------------------------------
  // navigation
  // -------------------------------------------------------------------------

  const MCP_LIST_URL = 'https://ucas.scienceone.cn/lit/#/mcp';
  const CHAT_URL = 'https://ucas.scienceone.cn/lit/#/assistant';

  /** Wait until the SPA has rendered something matching a predicate. */
  async function waitForPage(predicate, timeoutMs) {
    var deadline = Date.now() + (timeoutMs || 15000);
    for (;;) {
      if (predicate()) return true;
      if (Date.now() > deadline) return false;
      await sleep(400);
    }
  }

  /**
   * Make sure the MCP management page is open.
   *
   * The form only exists on that route, so filling it has to start by going
   * there — the panel is reachable from any page and the user may well be
   * sitting in the chat.
   */
  async function ensureMcpPage() {
    if (location.hash.indexOf('#/mcp') === 0) return true;
    location.hash = '#/mcp';
    return await waitForPage(function () {
      return location.hash.indexOf('#/mcp') === 0 &&
        !!document.querySelector('button,div[role=button]');
    }, 15000);
  }

  /** Switch to the 我的创建 tab, where an existing entry lives. */
  async function gotoMyTab() {
    var tab = findClickable('我的创建');
    if (!tab) return false;
    tab.click();
    await sleep(1500);
    return true;
  }

  /** The header row's two inputs: the name box and the value box. */
  function headerBoxes(scope) {
    var out = [];
    var els = (scope || findFormScope()).querySelectorAll('input');
    for (var i = 0; i < els.length; i++) {
      var el = els[i];
      if (!visible(el)) continue;
      if (el.closest('[class*=w-select], [class*=picker]')) continue;
      var ph = el.getAttribute('placeholder') || '';
      // Both boxes live under the 自定义请求头 field: "请输入请求头" and "请求头值".
      if (ph.indexOf('请求头') === -1) continue;
      out.push({
        el: el,
        isValue: ph.indexOf('值') !== -1,
        y: Math.round(el.getBoundingClientRect().top),
        x: Math.round(el.getBoundingClientRect().left),
      });
    }
    return out;
  }

  /** Read back the header rows as name/value pairs. */
  function headerPairs(scope) {
    var rows = {};
    headerBoxes(scope).forEach(function (b) {
      rows[b.y] = rows[b.y] || {};
      rows[b.y][b.isValue ? 'value' : 'name'] = b.el.value;
    });
    return Object.keys(rows)
      .sort(function (a, b) { return a - b; })
      .map(function (k) {
        return { name: rows[k].name || '', value: rows[k].value || '' };
      });
  }

  /**
   * Fill the header list.
   *
   * Choosing 自定义请求头 already renders exactly one row, laid out as a pair of
   * inputs: the name on the left ("请输入请求头") and the value on the right
   * ("请求头值"). Clicking "+ 添加标头" therefore adds a row that is not wanted —
   * the earlier version did that and left a stray empty header behind.
   *
   * So: use the row that is already there, and only add one if the list is empty.
   */
  async function fillHeaders(scope, value) {
    var parts = headerBoxes(scope);
    if (!parts.length) {
      // Nothing rendered yet: ask for a row, then look again.
      var add = buttonByText(['添加标头', '+ 添加标头', '添加'], scope);
      if (add) {
        add.click();
        await sleep(700);
        parts = headerBoxes(scope);
      }
    }
    if (!parts.length) {
      return { ok: false, detail: 'no header row on the form' };
    }

    // "KEY=value" is the form the caller supplies; fall back to a bare name.
    var eq = value.indexOf('=');
    var name = eq === -1 ? value : value.slice(0, eq);
    var val = eq === -1 ? '' : value.slice(eq + 1);

    var nameBox = parts.filter(function (p) { return !p.isValue; })[0];
    var valueBox = parts.filter(function (p) { return p.isValue; })[0];

    if (nameBox) setValue(nameBox.el, name);
    if (valueBox) setValue(valueBox.el, val);

    var filled = (nameBox ? 1 : 0) + (valueBox ? 1 : 0);
    return {
      ok: filled === 2,
      detail: 'row = ' + JSON.stringify(name) + ' / ' + JSON.stringify(val) +
        (filled === 2 ? '' : ' (only ' + filled + ' of 2 boxes found)'),
    };
  }

  // -------------------------------------------------------------------------
  // control panel
  // -------------------------------------------------------------------------

  var panelState = { open: true, status: null, busy: false, log: [], reported: false };

  function el(tag, style, text) {
    var n = document.createElement(tag);
    if (style) n.setAttribute('style', style);
    if (text != null) n.textContent = text;
    return n;
  }

  var CSS = [
    ':host{all:initial}',
    '*{box-sizing:border-box;font-family:-apple-system,"Segoe UI","Microsoft YaHei",sans-serif}',
    '.wrap{position:fixed;top:16px;right:16px;z-index:2147483647}',
    '.tab{display:flex;align-items:center;gap:8px;padding:9px 14px;border-radius:22px;',
    'background:rgba(18,20,28,.92);color:#e8eaf0;font-size:13px;cursor:pointer;',
    'border:1px solid rgba(255,255,255,.12);box-shadow:0 6px 24px rgba(0,0,0,.35);backdrop-filter:blur(10px)}',
    '.tab:hover{background:rgba(28,32,44,.96)}',
    '.dot{width:8px;height:8px;border-radius:50%;background:#6b7280;flex:none}',
    '.dot.ok{background:#22c55e;box-shadow:0 0 8px rgba(34,197,94,.7)}',
    '.dot.warn{background:#f59e0b;box-shadow:0 0 8px rgba(245,158,11,.7)}',
    '.dot.bad{background:#ef4444}',
    '.panel{margin-top:10px;width:380px;max-height:74vh;overflow:auto;border-radius:14px;',
    'background:rgba(18,20,28,.96);color:#e8eaf0;font-size:13px;',
    'border:1px solid rgba(255,255,255,.12);box-shadow:0 16px 48px rgba(0,0,0,.5);backdrop-filter:blur(14px)}',
    '.hd{padding:12px 14px;border-bottom:1px solid rgba(255,255,255,.08);display:flex;',
    'align-items:center;justify-content:space-between}',
    '.hd b{font-size:13px;font-weight:600;letter-spacing:.3px}',
    '.x{cursor:pointer;opacity:.5;padding:2px 6px;border-radius:6px}',
    '.x:hover{opacity:1;background:rgba(255,255,255,.08)}',
    '.sec{padding:12px 14px;border-bottom:1px solid rgba(255,255,255,.06)}',
    '.row{display:flex;align-items:flex-start;gap:8px;margin-bottom:9px}',
    '.row:last-child{margin-bottom:0}',
    '.k{width:74px;flex:none;opacity:.55;font-size:12px;padding-top:1px}',
    '.v{flex:1;word-break:break-all;line-height:1.5;font-size:12px}',
    'code{font-family:ui-monospace,Consolas,monospace;font-size:11.5px;',
    'background:rgba(255,255,255,.07);padding:1px 5px;border-radius:4px}',
    '.acts{display:flex;flex-wrap:wrap;gap:8px;padding:12px 14px}',
    'button{border:1px solid rgba(255,255,255,.14);background:rgba(255,255,255,.06);color:#e8eaf0;',
    'padding:7px 12px;border-radius:8px;font-size:12px;cursor:pointer;transition:.12s}',
    'button:hover:not(:disabled){background:rgba(255,255,255,.13)}',
    'button:disabled{opacity:.4;cursor:not-allowed}',
    'button.primary{background:#2563eb;border-color:#2563eb}',
    'button.primary:hover:not(:disabled){background:#1d4ed8}',
    '.log{max-height:150px;overflow:auto;font-family:ui-monospace,Consolas,monospace;font-size:11px;',
    'line-height:1.6;opacity:.8;white-space:pre-wrap;word-break:break-all}',
    '.note{font-size:11.5px;opacity:.6;padding:0 14px 12px;line-height:1.6}',
    '.bad{color:#fca5a5}',
    '.ok{color:#86efac}',
  ].join('');

  function mount() {
    if (document.getElementById(HOST_ID)) return true;
    if (!document.body) return false;

    var host = document.createElement('div');
    host.id = HOST_ID;
    document.body.appendChild(host);
    var root = host.attachShadow({ mode: 'open' });

    var style = document.createElement('style');
    style.textContent = CSS;
    root.appendChild(style);

    var wrap = el('div', null);
    wrap.className = 'wrap';
    root.appendChild(wrap);

    var tab = el('div', null);
    tab.className = 'tab';
    var dot = el('span', null);
    dot.className = 'dot';
    var label = el('span', null, '磐石桥接');
    tab.appendChild(dot);
    tab.appendChild(label);
    wrap.appendChild(tab);

    var panel = el('div', null);
    panel.className = 'panel';
    panel.style.display = 'block';
    wrap.appendChild(panel);

    tab.addEventListener('click', function () {
      panelState.open = !panelState.open;
      panel.style.display = panelState.open ? 'block' : 'none';
      if (panelState.open) render();
    });

    // render() reads panelState.status, so the setter has to record it first —
    // passing the snapshot straight to render() would leave the panel blank.
    panelState.render = function (status) {
      panelState.status = status;
      render();
    };
    panelState.root = root;
    panelState.panelEl = panel;
    panelState.dotEl = dot;

    render();
    report('panel mounted', { href: location.href, title: document.title });
    return true;
  }

  function render() {
    var panel = panelState.panelEl;
    if (!panel) return;
    var s = panelState.status || {};
    panel.textContent = '';

    var hd = el('div', null);
    hd.className = 'hd';
    hd.appendChild(el('b', null, '磐石桌面桥接'));
    var x = el('span', null, '✕');
    x.className = 'x';
    x.addEventListener('click', function () {
      panelState.open = false;
      panel.style.display = 'none';
    });
    hd.appendChild(x);
    panel.appendChild(hd);

    var sec = el('div', null);
    sec.className = 'sec';

    var stateClass = 'dot';
    if (s.mcpRunning && s.tunnelUrl) stateClass += ' ok';
    else if (s.mcpRunning) stateClass += ' warn';
    else stateClass += ' bad';
    panelState.dotEl.className = stateClass;

    function row(k, v) {
      var r = el('div', null);
      r.className = 'row';
      var kk = el('div', null, k);
      kk.className = 'k';
      r.appendChild(kk);
      var vv = el('div', null);
      vv.className = 'v';
      if (typeof v === 'string') vv.textContent = v;
      else vv.appendChild(v);
      r.appendChild(vv);
      sec.appendChild(r);
    }

    function code(t) {
      var c = document.createElement('code');
      c.textContent = t;
      return c;
    }

    row('MCP 服务', s.mcpRunning ? '运行中 · ' + s.mcpAddr + ' · ' + s.toolCount + ' 个工具' : '未运行');
    row('公网隧道', s.tunnelUrl ? code(s.tunnelUrl) : s.tunnelState === 'starting' ? '正在建立…' : '未连接');
    row('工作目录', s.workspace || '—');
    row('令牌', s.token ? code(s.token.slice(0, 12) + '…') : '未设置');

    panel.appendChild(sec);

    var acts = el('div', null);
    acts.className = 'acts';

    function act(text, cls, fn, disabled) {
      var b = el('button', null, text);
      if (cls) b.className = cls;
      if (disabled || panelState.busy) b.disabled = true;
      b.addEventListener('click', function () {
        fn().catch(function (e) {
          pushLog('失败: ' + e.message);
        });
      });
      acts.appendChild(b);
    }

    act('一键填入磐石表单', 'primary', doFill, !s.mcpUrl);
    act('探测页面结构', null, doProbe);
    act('复制配置', null, copyConfig);
    act('刷新状态', null, refresh);
    panel.appendChild(acts);

    if (panelState.log.length) {
      var lw = el('div', null);
      lw.className = 'sec';
      var lg = el('div', null, panelState.log.slice(-14).join('\n'));
      lg.className = 'log';
      lw.appendChild(lg);
      panel.appendChild(lw);
    }

    var note = el('div', null, '自动化填完后请自行核对再提交，不会代你点击提交。');
    note.className = 'note';
    panel.appendChild(note);
  }

  function pushLog(msg) {
    var t = new Date().toTimeString().slice(0, 8);
    panelState.log.push('[' + t + '] ' + msg);
    if (panelState.log.length > 200) panelState.log.shift();
    report('log: ' + msg);
    if (panelState.open) render();
  }
  window.__panshiLog = pushLog;

  async function refresh() {
    try {
      var s = await call('status');
      panelState.render(s);
      if (!panelState.reported) {
        panelState.reported = true;
        report('status ok', { mcp: !!s.mcpRunning, tunnel: !!s.tunnelUrl, tools: s.toolCount || 0 });
      }
      return s;
    } catch (e) {
      pushLog('状态获取失败: ' + e.message);
    }
  }

  async function copyConfig() {
    var s = panelState.status || {};
    var text =
      'MCP 地址: ' + (s.mcpUrl || '') + '\n令牌: ' + (s.token || '') + '\n请求头: X-MCP-Token=' + (s.token || '');
    try {
      await navigator.clipboard.writeText(text);
      pushLog('已复制到剪贴板');
    } catch (e) {
      pushLog('复制失败: ' + e.message);
    }
  }

  // -------------------------------------------------------------------------
  // actions driven from the panel
  // -------------------------------------------------------------------------

  function findFormScope() {
    // Prefer the innermost dialog or drawer, which is where the form lives when
    // it opens as an overlay.
    var dialogs = document.querySelectorAll(
      '[role=dialog],[class*=dialog],[class*=modal],[class*=drawer],[class*=Dialog],[class*=Modal]'
    );
    for (var i = dialogs.length - 1; i >= 0; i--) {
      if (visible(dialogs[i]) && dialogs[i].querySelector('input,textarea')) return dialogs[i];
    }
    return document;
  }

  /** Open the create dialog, or null if it will not appear. */
  async function openCreateDialog() {
    var scope = findFormScope();
    if (scope !== document) return scope;

    var add = buttonByText(['创建', '添加MCP', '添加 MCP', '新增'], null);
    if (!add) return null;
    add.click();
    await waitFor(function () {
      var s = findFormScope();
      return s !== document ? s : null;
    }, 6000);
    var opened = findFormScope();
    return opened === document ? null : opened;
  }

  /** Tool names visible on the page, read off card titles. */
  function listToolNames() {
    var out = [];
    var cards = document.querySelectorAll('div[class*=card], li[class*=card], div[class*=item]');
    for (var i = 0; i < cards.length; i++) {
      var c = cards[i];
      if (!visible(c)) continue;
      var leaves = c.querySelectorAll('div,span,h3,h4,strong');
      for (var j = 0; j < leaves.length; j++) {
        var el = leaves[j];
        if (el.children.length > 0) continue;
        var t = (el.textContent || '').trim();
        if (/^[A-Za-z][A-Za-z0-9_-]{2,40}$/.test(t)) {
          out.push(t);
          break;
        }
      }
    }
    return out;
  }

  /** The name of whichever tool's detail page is open. */
  function currentToolName() {
    var heads = document.querySelectorAll('h1,h2,h3,[class*=title],[class*=name]');
    for (var i = 0; i < heads.length; i++) {
      var el = heads[i];
      if (!visible(el)) continue;
      var r = el.getBoundingClientRect();
      if (r.left < 380) continue; // skip the left navigation column
      if (r.top < 60) continue;
      var t = (el.textContent || '').trim();
      if (/^[A-Za-z][A-Za-z0-9_-]{2,40}$/.test(t)) return t;
    }
    return null;
  }

  /**
   * Find an entry that is already mine.
   *
   * The platform rejects a second MCP with a name it already has, and the public
   * tunnel address changes on every launch, so an existing entry has to be edited
   * rather than re-created.
   */
  async function findExisting(name) {
    await gotoMyTab();
    if (listToolNames().indexOf(name) === -1) return null;

    var card = findClickable(name);
    if (!card) return null;
    card.click();
    await sleep(2500);

    var shown = currentToolName();
    if (shown && shown !== name) return null; // landed on the wrong card
    return { name: name, page: location.href };
  }

  /** Put a new URL into an already-open edit form. */
  async function updateUrl(scope, cfg) {
    var report = [];
    var field = findField('url', 'text', scope);
    if (!field) return [{ field: 'url', ok: false, detail: 'no URL field on the edit page' }];

    var current = String(field.el.value || '').trim();
    if (current === cfg.mcpUrl) {
      return [{ field: 'url', ok: true, detail: 'already current' }];
    }
    setValue(field.el, cfg.mcpUrl);
    report.push({
      field: 'url',
      ok: true,
      detail: 'replaced ' + current.slice(0, 44) + ' -> ' + cfg.mcpUrl.slice(0, 44),
    });

    // The header carries the token, so refresh it alongside the address.
    var boxes = headerBoxes(scope);
    if (cfg.headers && boxes.length) {
      var nameBox = boxes.filter(function (b) { return !b.isValue; })[0];
      var valueBox = boxes.filter(function (b) { return b.isValue; })[0];
      var eq = cfg.headers.indexOf('=');
      if (nameBox) setValue(nameBox.el, eq === -1 ? cfg.headers : cfg.headers.slice(0, eq));
      if (valueBox) setValue(valueBox.el, eq === -1 ? '' : cfg.headers.slice(eq + 1));
      report.push({ field: 'headers', ok: !!valueBox, detail: 'refreshed' });
    }
    return report;
  }

  /**
   * Save whichever form is open.
   *
   * The button is not always called "确认": an edit form says "确认更新", and a
   * create form says "确认". Matching on the bare word alone silently missed the
   * update case, leaving the dialog open while the log claimed success — so the
   * longer, more specific labels are tried first.
   *
   * Success is judged by whether the form element itself left the document.
   * Counting dialogs page-wide does not work: the platform keeps other elements
   * whose class contains "dialog", so the count never drops.
   */
  async function saveForm(scope) {
    var labels = ['确认更新', '确认修改', '保存修改', '确认创建', '确认提交', '保存', '提交', '确认', '确定'];
    var btn = null;
    for (var i = 0; i < labels.length && !btn; i++) {
      btn = findClickable(labels[i], scope);
    }
    if (!btn) return { ok: false, detail: '未找到保存按钮' };

    var label = (btn.textContent || '').trim();
    btn.click();
    await sleep(3000);

    // The form we just submitted should be gone; a still-attached form means a
    // validation error kept it open.
    var stillOpen = scope && scope !== document && document.contains(scope) &&
      scope.getBoundingClientRect().width > 0;
    if (stillOpen) {
      var errs = [];
      var nodes = scope.querySelectorAll('[class*=error],[class*=Error],[class*=danger]');
      for (var j = 0; j < nodes.length; j++) {
        var t = (nodes[j].textContent || '').trim();
        if (t && t.length < 60) errs.push(t);
      }
      return {
        ok: false,
        detail: '点了「' + label + '」但表单仍开着' + (errs.length ? '：' + errs.slice(0, 3).join(' / ') : ''),
      };
    }
    return { ok: true, detail: '已点「' + label + '」，表单已提交并关闭' };
  }

  async function doFill() {
    panelState.busy = true;
    render();
    pushLog('开始填入…');
    try {
      var cfg = await call('fillConfig');
      if (cfg.error) throw new Error(cfg.error);

      // The panel is reachable from any page, so the flow starts by navigating
      // to the MCP management page rather than assuming the user is already there.
      pushLog('进入 MCP 工具页…');
      if (!(await ensureMcpPage())) {
        pushLog('导航到 MCP 页失败，请在页面上手动打开后重试');
        return;
      }
      await sleep(2000);

      // Re-use an existing entry: a duplicate name is rejected, and the tunnel
      // address differs every launch, so the old one has to be updated.
      var existing = await findExisting(cfg.name);
      if (existing) {
        pushLog('发现已创建的「' + cfg.name + '」，改走更新');
        var editScope = findFormScope();
        if (editScope === document) {
          var editBtn = findClickable('编辑') || findClickable('修改');
          if (editBtn) {
            editBtn.click();
            await sleep(2500);
            editScope = findFormScope();
          }
        }
        if (editScope === document) {
          pushLog('打开了详情页但没有编辑入口，请手动点「编辑」后再试');
          return;
        }
        var upd = await updateUrl(editScope, cfg);
        upd.forEach(function (r) {
          pushLog((r.ok ? '✓ ' : '✗ ') + r.field + ' — ' + r.detail);
        });
        var saved = await saveForm(editScope);
        pushLog((saved.ok ? '✓ ' : '✗ ') + 'save — ' + saved.detail);
        if (!saved.ok) return;
      } else {
        var scope = await openCreateDialog();
        if (!scope) {
          pushLog('未找到「创建」按钮，请手动打开创建弹窗后再点一次');
          return;
        }

        var result = await fillForm(cfg, scope);
        result.forEach(function (r) {
          pushLog((r.ok ? '✓ ' : '✗ ') + r.field + (r.detail ? ' — ' + r.detail : ''));
        });
        var failed = result.filter(function (r) {
          return !r.ok;
        }).length;
        if (failed) {
          pushLog('有 ' + failed + ' 个字段没填上，请核对后再点确认');
          return;
        }
        var created = await saveForm(scope);
        pushLog((created.ok ? '✓ ' : '✗ ') + 'save — ' + created.detail);
        if (!created.ok) return;
      }

      // Finally, switch the server on in the chat. Creating it is not enough:
      // an MCP that is not enabled is invisible to the model.
      pushLog('回到对话页启用 ' + cfg.name + '…');
      var en = await enableInChat(cfg);
      pushLog((en.ok ? '✓ ' : '✗ ') + 'enable — ' + en.detail + '（' + en.steps.join(' → ') + '）');
      pushLog(en.ok ? '全部完成' : '请手动在对话页的工具菜单里启用');
    } finally {
      panelState.busy = false;
      render();
    }
  }

  /**
   * Enable our MCP in the chat composer.
   *
   * The chat page keeps a tool menu behind the "/" affordance next to the input
   * box: its MCP row opens a panel headed 已启用的 MCP that lists every server
   * with a toggle. A freshly created server is not enabled by default, so this
   * has to be switched on or the model can never call it.
   */
  async function enableInChat(cfg) {
    var steps = [];

    // 1. go to the chat page
    if (location.hash.indexOf('#/assistant') !== 0) {
      location.hash = '#/assistant';
      await waitForPage(function () {
        return !!document.querySelector('textarea');
      }, 15000);
    }
    steps.push('在对话页');

    // 2. open the tool menu: the button sits to the right of the attach button
    var ta = document.querySelector('textarea.w-textarea') || document.querySelector('textarea');
    if (!ta) {
      return { ok: false, steps: steps, detail: '找不到输入框' };
    }
    var trigger = findToolMenuButton(ta);
    if (!trigger) {
      return { ok: false, steps: steps, detail: '找不到工具菜单按钮' };
    }
    trigger.click();
    await sleep(1200);

    // 3. open the MCP row
    var mcpRow = findClickable('MCP');
    if (!mcpRow) {
      return { ok: false, steps: steps, detail: '工具菜单里没有 MCP 项' };
    }
    mcpRow.click();
    await sleep(1200);
    steps.push('打开 MCP 面板');

    // 4. find our entry and turn it on
    var row = findMcpRow(cfg.name);
    if (!row) {
      // The panel may need a scroll before the entry is present.
      await sleep(1200);
      row = findMcpRow(cfg.name);
    }
    if (!row) {
      return { ok: false, steps: steps, detail: '列表里没有 ' + cfg.name };
    }
    if (row.enabled) {
      return { ok: true, steps: steps, detail: cfg.name + ' 已经是启用状态' };
    }
    (row.toggle || row.el).click();
    await sleep(1200);

    var after = findMcpRow(cfg.name);
    return {
      ok: !!(after && after.enabled),
      steps: steps,
      detail: after && after.enabled ? cfg.name + ' 已启用' : '点了开关但状态未变',
    };
  }

  /** The "/" tool button beside the composer. */
  function findToolMenuButton(textarea) {
    var r = textarea.getBoundingClientRect();
    var els = document.querySelectorAll('button,div,span');
    var best = null;
    for (var i = 0; i < els.length; i++) {
      var el = els[i];
      var b = el.getBoundingClientRect();
      if (b.width === 0 || b.height === 0) continue;
      // Same row as the composer, to its left, above the send button.
      if (b.top < r.top + r.height * 0.4 || b.top > r.bottom + 40) continue;
      if (b.left < r.left - 40 || b.left > r.left + r.width * 0.5) continue;
      var cls = String(el.className || '');
      if (!/btn|icon|tool|menu/i.test(cls)) continue;
      if (!best || b.left > best.left) best = { el: el, left: b.left };
    }
    return best ? best.el : null;
  }

  /**
   * Locate a server's row in the 已启用的 MCP panel, with its toggle state.
   *
   * The switch is a component-library control rather than a checkbox, so the
   * state is read from its class list ("checked"/"active"/"on") and the click
   * target is the switch element itself.
   */
  function findMcpRow(name) {
    var els = document.querySelectorAll('div,li,section');
    var row = null;
    for (var i = 0; i < els.length; i++) {
      var el = els[i];
      if (!visible(el)) continue;
      var own = '';
      for (var k = 0; k < el.childNodes.length; k++) {
        if (el.childNodes[k].nodeType === 3) own += el.childNodes[k].textContent;
      }
      var t = (el.textContent || '').trim();
      if (t.indexOf(name) === -1) continue;
      if (t.length > 60) continue; // too wide to be the row
      row = el;
    }
    if (!row) return null;

    var sw = row.querySelector('[class*=switch],[class*=Switch],[class*=toggle],[class*=Toggle],input[type=checkbox],[role=switch]');
    var enabled = false;
    if (sw) {
      var cls = String(sw.className || '');
      if (/checked|active|on\b|open/i.test(cls)) enabled = true;
      if (sw.checked === true) enabled = true;
      if (sw.getAttribute && sw.getAttribute('aria-checked') === 'true') enabled = true;
    }
    return { el: row, toggle: sw, enabled: enabled };
  }

  async function doProbe() {
    var info = probe();
    pushLog('发现 ' + info.controls.length + ' 个输入控件、' + info.buttons.length + ' 个按钮');
    try {
      var res = await call('saveProbe', info);
      pushLog(res.path ? '已保存到 ' + res.path : '已提交');
    } catch (e) {
      pushLog('保存失败: ' + e.message);
    }
    console.log('[panshi-desktop] probe', info);
    return info;
  }

  // exposed for the console, so the automation can be driven by hand
  window.__panshiDesktopAPI = {
    probe: probe,
    fill: doFill,
    fillForm: fillForm,
    findFormScope: findFormScope,
    fieldByLabel: fieldByLabel,
    setValue: setValue,
    refresh: refresh,
    state: panelState,
    headerPairs: headerPairs,
    findExisting: findExisting,
    listToolNames: listToolNames,
    currentToolName: currentToolName,
    updateUrl: updateUrl,
    findClickable: findClickable,
  };

  // -------------------------------------------------------------------------
  // boot
  // -------------------------------------------------------------------------

  function boot() {
    var tries = 0;
    (function attempt() {
      if (mount() || ++tries > 40) return;
      setTimeout(attempt, 250);
    })();
    refresh();
    setInterval(refresh, 5000);

    // The platform is a single-page app: keep the panel alive across route changes.
    window.addEventListener('hashchange', function () {
      setTimeout(function () {
        if (!document.getElementById(HOST_ID)) {
          var t = 0;
          (function again() {
            if (mount() || ++t > 20) return;
            setTimeout(again, 250);
          })();
        }
      }, 300);
    });
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', boot);
  } else {
    boot();
  }
})();
