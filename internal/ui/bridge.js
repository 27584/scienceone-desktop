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
  if (window.__scienceoneDesktop) return;
  window.__scienceoneDesktop = true;

  var HOST_ID = 'scienceone-desktop-panel';

  // -------------------------------------------------------------------------
  // bridge to the native side
  // -------------------------------------------------------------------------

  function call(action, payload) {
    return new Promise(function (resolve, reject) {
      if (typeof window.scienceoneNative !== 'function') {
        reject(new Error('native bridge not available'));
        return;
      }
      // The native side answers on the UI thread. If that thread is blocked the
      // promise never settles and the whole flow would hang silently, so every
      // call is bounded and the failure is visible in the panel log.
      var settled = false;
      var timer = setTimeout(function () {
        if (!settled) {
          settled = true;
          reject(new Error('native call timed out: ' + action));
        }
      }, 8000);

      Promise.resolve(window.scienceoneNative(JSON.stringify({ action: action, payload: payload || {} })))
        .then(function (raw) {
          if (settled) return;
          settled = true;
          clearTimeout(timer);
          try {
            resolve(JSON.parse(raw));
          } catch (e) {
            reject(new Error('bad response: ' + String(raw).slice(0, 200)));
          }
        })
        .catch(function (e) {
          if (settled) return;
          settled = true;
          clearTimeout(timer);
          reject(e);
        });
    });
  }
  window.__scienceoneCall = call;

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

  /**
   * Find a clickable element by its visible text.
   *
   * Exact match wins, and a card title is preferred over any other element with
   * the same text: the list shows both "local-dev-mcp" and "desktop" with
   * identical descriptions, so matching loosely lands on the wrong entry.
   */
  function findClickable(text, root) {
    var scope = root || document;
    var els = scope.querySelectorAll(
      '.card-title, button, a, div, span, li, p, h1, h2, h3, h4, strong'
    );
    var loose = null;
    for (var i = 0; i < els.length; i++) {
      var el = els[i];
      if (!visible(el)) continue;
      var t = (el.textContent || '').trim();
      if (t === text) {
        if (el.classList && el.classList.contains('card-title')) {
          // Click the card itself rather than the heading inside it: the card
          // owns the navigation handler, and its centre is a larger target.
          return el.closest('[class*=card], li, article') || el;
        }
        if (!loose) loose = el;
      }
    }
    if (loose) return clickableAncestor(loose);
    for (var j = 0; j < els.length; j++) {
      var e2 = els[j];
      if (!visible(e2)) continue;
      var t2 = (e2.textContent || '').trim();
      if (t2.length <= 12 && t2.indexOf(text) !== -1) return clickableAncestor(e2);
    }
    return null;
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

  /**
   * Attach the document to the upload widget.
   *
   * Two channels are tried, because a component-library uploader accepts a file
   * without necessarily keeping it on the input element:
   *   - assign to every <input type=file> and fire input/change
   *   - drop the file on the area that advertises "点击或拖拽上传"
   *
   * Success is judged by the interface, not by input.files: uploaders commonly
   * clear the input afterwards so the same file can be picked again, which makes
   * a file count read as zero even though the attachment worked.
   */
  async function attachDocument(scope, name, content) {
    var file = new File([content], name, { type: 'text/markdown' });

    function assign(input) {
      try {
        var dt = new DataTransfer();
        dt.items.add(file);
        input.files = dt.files;
        input.dispatchEvent(new Event('input', { bubbles: true }));
        input.dispatchEvent(new Event('change', { bubbles: true }));
        return true;
      } catch (e) {
        return false;
      }
    }

    function shown() {
      // The form echoes the file name once it is attached.
      var els = (scope || document).querySelectorAll('div,span,strong,p,li');
      for (var i = 0; i < els.length; i++) {
        var el = els[i];
        if (el.children.length > 0 || !visible(el)) continue;
        var t = (el.textContent || '').trim();
        if (t === name || t.indexOf(name) !== -1) return true;
      }
      return false;
    }

    var inputs = (scope || document).querySelectorAll('input[type=file]');
    for (var i = 0; i < inputs.length; i++) assign(inputs[i]);
    await sleep(1500);
    if (shown()) return { ok: true, detail: 'attached ' + name };

    var zone = findDropZone(scope);
    if (zone) {
      try {
        var dt2 = new DataTransfer();
        dt2.items.add(file);
        ['dragenter', 'dragover', 'drop'].forEach(function (type) {
          zone.dispatchEvent(new DragEvent(type, { bubbles: true, cancelable: true, dataTransfer: dt2 }));
        });
      } catch (e) {
        /* fall through to the verification below */
      }
      await sleep(1500);
      if (shown()) return { ok: true, detail: 'attached ' + name + ' (via drop)' };
    }

    return {
      ok: false,
      detail: 'file inputs=' + inputs.length + ', drop zone=' + (zone ? 'yes' : 'no') + ', name not shown',
    };
  }

  /** The upload area that advertises drag-and-drop. */
  function findDropZone(scope) {
    var els = (scope || document).querySelectorAll('div,section,label');
    for (var i = 0; i < els.length; i++) {
      var el = els[i];
      if (!visible(el)) continue;
      var t = (el.textContent || '').trim();
      if (t.indexOf('拖拽') !== -1 || t.indexOf('上传说明') !== -1) return el;
    }
    return null;
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
  // click recorder
  // -------------------------------------------------------------------------

  /**
   * Write down every click the user makes, with the element's ancestor chain.
   *
   * Some controls on this platform only appear under a real mouse hover, which
   * synthetic events cannot reproduce — so the only reliable way to learn where
   * a menu lives is to have the user open it once while every click they make is
   * being recorded. Guessing from screenshots cost hours; this costs one click.
   */
  function recordClicks() {
    if (window.__scienceoneClickLog) {
      pushLog('已经在记录了，请直接去点开那个开关');
      return 'already recording';
    }
    window.__scienceoneClickLog = [];
    document.addEventListener('click', function (e) {
      var chain = [];
      var cur = e.target;
      for (var d = 0; d < 6 && cur && cur !== document.body; d++) {
        var t = (cur.textContent || '').trim();
        chain.push(
          cur.tagName.toLowerCase() +
            '.' + String(cur.className).slice(0, 48) +
            (t && t.length <= 26 ? ' T=' + JSON.stringify(t) : '')
        );
        cur = cur.parentElement;
      }
      if (window.__scienceoneClickLog.length < 40) {
        window.__scienceoneClickLog.push(chain[0]);
      }
      // Show it in the panel as well as logging it, so the user can see that
      // the recording is live while they are clicking around.
      pushLog('点击 → ' + String(chain[0] || '').slice(0, 76));
      render();
      call('report', {
        msg: 'USER CLICK @' + Math.round(e.clientX) + ',' + Math.round(e.clientY),
        data: chain.join('\n    < '),
      });
    }, true);
    pushLog('● 开始记录点击 — 现在去点开那个 MCP 开关，点完这里会显示');
    return 'recording';
  }

  // -------------------------------------------------------------------------
  // input blocker
  // -------------------------------------------------------------------------

  var BLOCKER_ID = 'scienceone-desktop-blocker';

  /**
   * Swallow the user's input while the automation drives the page.
   *
   * Synthetic events bypass hit-testing, so a full-screen catcher stops clicks,
   * wheel and keys from the user without touching el.click() or
   * dispatchEvent() — which is exactly the split wanted: the flow keeps working
   * and a stray click cannot move the page out from under it mid-edit.
   */
  function blockInput(on) {
    var el = document.getElementById(BLOCKER_ID);
    if (!on) {
      if (el) el.remove();
      return;
    }
    if (el) return;
    el = document.createElement('div');
    el.id = BLOCKER_ID;
    el.style.cssText =
      'position:fixed;inset:0;z-index:2147483646;cursor:progress;background:transparent';
    ['mousedown', 'mouseup', 'click', 'dblclick', 'contextmenu', 'wheel',
      'keydown', 'keyup', 'keypress', 'touchstart', 'touchend'].forEach(function (t) {
      el.addEventListener(t, function (e) { e.stopPropagation(); e.preventDefault(); }, true);
    });
    document.body.appendChild(el);
  }

  // -------------------------------------------------------------------------
  // last saved address
  // -------------------------------------------------------------------------

  var LAST_SAVED_KEY = 'scienceone-desktop:lastSavedURL';

  // The edit form does not back-fill the saved address, so the page cannot be
  // asked "is this already up to date" — the field is always empty. The answer
  // therefore has to be remembered from the last time a save succeeded.
  function lastSavedURL() {
    try { return localStorage.getItem(LAST_SAVED_KEY) || ''; } catch (e) { return ''; }
  }
  function markSavedURL(url) {
    try { localStorage.setItem(LAST_SAVED_KEY, url); } catch (e) { /* not fatal */ }
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
      var doc = await attachDocument(scope, cfg.documentName || 'local-dev-mcp.md', cfg.documentContent);
      note('document', doc.ok, doc.detail);
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
   *
   * Setting location.hash is enough for the SPA router to react, but pushing
   * through history as well covers routers that only listen to popstate.
   */
  async function ensureMcpPage() {
    // "starts with #/mcp" is not enough. #/mcp/details/<id> is a detail page:
    // no list on it and no 创建 button either. Treating it as the list made the
    // flow hunt for the entry somewhere it cannot be, then fail on a missing
    // button.
    if (location.hash.indexOf('#/mcp') === 0 && location.hash.indexOf('/details/') === -1) return true;
    try {
      // Land on 我的创建 directly. Going to the default tab first and then
      // switching means the list renders twice, and it is the second render the
      // flow then sits waiting on.
      location.hash = '#/mcp?tab=my&sort=hot&order=desc';
      if (location.hash.indexOf('#/mcp') !== 0) {
        history.pushState(null, '', location.pathname + location.search + '#/mcp?tab=my&sort=hot&order=desc');
        window.dispatchEvent(new PopStateEvent('popstate'));
        window.dispatchEvent(new HashChangeEvent('hashchange'));
      }
    } catch (e) {
      return false;
    }
    return await waitForPage(function () {
      // Wait for the route AND for the page's own content to arrive; the hash
      // changes before the view is rendered.
      return location.hash.indexOf('#/mcp') === 0 && !!findClickable('我的创建');
    }, 12000);
  }

  /**
   * Switch to the 我的创建 tab, and wait until it has actually taken effect.
   *
   * Navigating by URL rather than clicking the tab: the click is a request the
   * router may or may not honour, and an earlier version accepted "the 创建
   * button exists" as success — true on every tab — so the list silently stayed
   * on 公开MCP, the existing entry was never found, and the flow went on to
   * create a duplicate name that the server rejects.
   */
  async function gotoMyTab() {
    // Already on the list with entries on it? Nothing to re-route. The common
    // case is the user sitting on the right page, and navigating away throws out
    // a rendered list only to wait for it to render again.
    if (location.hash.indexOf('tab=my') !== -1 && listToolNames().length > 0) return true;

    var before = listToolNames().join('|');
    if (location.hash.indexOf('tab=my') === -1) {
      location.hash = '#/mcp?tab=my&sort=hot&order=desc';
    }
    // Short: the hash changes immediately, and the list either follows within a
    // beat or it does not. Waiting longer only delays the conclusion.
    return await waitForPage(function () {
      return location.hash.indexOf('tab=my') !== -1 && listToolNames().join('|') !== before;
    }, 8000);
  }

  /**
   * Click an element the way a mouse would.
   *
   * mousedown/mouseup are dispatched first because card handlers sometimes
   * listen for them, but the final click must be the native .click(): a
   * synthetic MouseEvent does not trigger an anchor's default navigation, and
   * the list cards are wrapped in links. Dispatching all three broke the
   * navigation instead of improving it.
   */
  function realClick(el) {
    if (!el) return false;
    var r = el.getBoundingClientRect();
    var opts = {
      bubbles: true,
      cancelable: true,
      clientX: r.left + r.width / 2,
      clientY: r.top + r.height / 2,
      view: window,
    };
    try {
      el.dispatchEvent(new MouseEvent('mousedown', opts));
      el.dispatchEvent(new MouseEvent('mouseup', opts));
    } catch (e) {
      /* fall through to the native click */
    }
    el.click();
    return true;
  }

  /**
   * Open an entry's detail page.
   *
   * The URL must change to a *different* details page: checking only for
   * "/details/" was satisfied by the page the previous run left behind, so a
   * failed click looked like a success and the flow then edited the wrong entry.
   */
  async function openDetail(name) {
    var before = location.hash;
    var card = findClickable(name);
    if (!card) return false;
    realClick(card);
    return await waitForPage(function () {
      return location.hash.indexOf('/details/') !== -1 && location.hash !== before;
    }, 12000);
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
    var label = el('span', null, 'ScienceOne桥接');
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
    hd.appendChild(el('b', null, 'ScienceOne桌面桥接'));
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
    row('隧道后端', s.tunnelProvider === 'frp' ? 'frp · 自建服务器'
      : s.tunnelProvider === 'cloudflared' ? 'cloudflared · quick tunnel' : '—');
    row('公网隧道', s.tunnelUrl ? code(s.tunnelUrl) : s.tunnelState === 'starting' ? '正在建立…' : '未连接');
    // The platform tests the address on save, so "the address exists" and "the
    // address works" are two different facts and both are worth showing.
    row('隧道可达', s.tunnelWarm ? '已自测通过'
      : s.tunnelUrl ? '预热中…' + (s.tunnelWhy ? '（' + s.tunnelWhy + '）' : '') : '—');
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
        // Not every action is async. Assuming a Promise here meant a synchronous
        // one threw `fn(...).catch is not a function` straight into the void —
        // the button looked dead.
        try {
          var r = fn();
          if (r && typeof r.catch === 'function') {
            r.catch(function (e) {
              pushLog('失败: ' + e.message);
            });
          }
        } catch (e) {
          pushLog('失败: ' + e.message);
        }
      });
      acts.appendChild(b);
    }

    act('一键填入ScienceOne表单', 'primary', function () { return doFill(false); }, !s.mcpUrl);
    act('强制重填', null, function () { return doFill(true); }, !s.mcpUrl);
    act('探测页面结构', null, doProbe);
    act('记录我的点击', null, recordClicks);
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
  window.__scienceoneLog = pushLog;

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

  // Labels that only ever occur inside the MCP entry form itself.
  var FORM_MARKERS = /名称\s*\*|URL\s*\*|鉴权方式|说明文档/;

  function findFormScope() {
    // Prefer the innermost dialog or drawer, which is where the form lives when
    // it opens as an overlay.
    var dialogs = document.querySelectorAll(
      '[role=dialog],[class*=dialog],[class*=modal],[class*=drawer],[class*=Dialog],[class*=Modal]'
    );
    var fallback = null;
    for (var i = dialogs.length - 1; i >= 0; i--) {
      var d = dialogs[i];
      if (!visible(d) || !d.querySelector('input,textarea')) continue;
      // Only a container carrying the entry form's own field labels counts as
      // the form. Accepting any visible dialog that merely holds an <input>
      // matched persistent page chrome (search boxes) and leftover dialogs, so
      // the scope never went away and a submit that had actually succeeded was
      // reported as "form still open".
      if (FORM_MARKERS.test(d.textContent || '')) return d;
      if (!fallback) fallback = d;
    }
    return fallback || document;
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

  /**
   * Tool names visible on the page, read off the card titles.
   *
   * Scoped to the cards. A looser selector that also matched "anything with
   * 'item' in its class" picked up the sidebar's own list items, which pushed
   * the real entries out of the first twelve results — so an entry that existed
   * looked absent, and the flow went on to create a duplicate the platform then
   * rejects.
   */
  function listToolNames() {
    var out = [];
    var titles = document.querySelectorAll(
      'article [class*=card-title], [class*=resource-card] [class*=card-title], h3[class*=card-title]'
    );
    for (var i = 0; i < titles.length; i++) {
      var el = titles[i];
      if (el.children.length > 0 || !visible(el)) continue;
      var t = (el.textContent || '').trim();
      if (/^[A-Za-z][A-Za-z0-9_-]{2,40}$/.test(t) && out.indexOf(t) === -1) {
        out.push(t);
      }
    }
    return out;
  }

  /**
   * The name of whichever entry's detail page is open.
   *
   * List cards must be excluded. The SPA keeps the list mounted behind the
   * detail view, so a document-order scan found "local-dev-mcp" in the card grid
   * before it ever reached the detail heading — which made a correct navigation
   * look like it had opened the wrong entry.
   */
  function currentToolName() {
    var heads = document.querySelectorAll('h1,h2,h3,[class*=title],[class*=name]');
    for (var i = 0; i < heads.length; i++) {
      var el = heads[i];
      if (!visible(el)) continue;
      if (el.children.length > 0) continue;
      // Skip anything that belongs to the list, not the detail view.
      if (el.closest('article, [class*=cards-grid], [class*=card-title-row], [class*=resource-list]')) continue;
      var r = el.getBoundingClientRect();
      if (r.left < 300) continue; // the navigation column
      if (r.top < 50) continue; // the window chrome
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
    // Read what is already on screen before navigating anywhere.
    if (listToolNames().indexOf(name) === -1) {
      await gotoMyTab();
    }
    if (listToolNames().indexOf(name) === -1) {
      // The list re-renders after the route change, so it can be legitimately
      // empty for a moment. Wait for it to fill in rather than concluding the
      // entry is absent and re-routing a second time.
      await waitFor(function () { return listToolNames().length > 0; }, 4000);
    }
    var names = listToolNames();
    if (names.indexOf(name) === -1) {
      // Reported so a mis-navigated tab is distinguishable from an entry that
      // genuinely is not there.
      report('findExisting: not in list', {
        hash: location.hash,
        seen: names.slice(0, 12),
      });
      return null;
    }

    if (!(await openDetail(name))) {
      report('findExisting: detail did not open', { hash: location.hash });
      return null;
    }

    // The title must match. Accepting an unknown title was the bug that made the
    // flow edit the wrong entry: the list holds two servers with identical
    // descriptions, so a mis-targeted click looked like a success.
    // The title must match, but an unreadable title is not evidence of the wrong
    // entry: the card was found by name and the route did change. Rejecting on
    // "could not read it" sent the flow into the create path from a detail page
    // that has no create button, so it failed with a message about a missing
    // button. Only a *mismatch* is a rejection.
    var shown = await waitFor(function () {
      return currentToolName();
    }, 10000);
    if (shown && shown !== name) {
      report('findExisting: opened the wrong entry', { shown: shown, wanted: name });
      return null;
    }
    if (!shown) {
      report('findExisting: detail title unreadable, accepting on route change', { name: name });
    }
    return { name: name, page: location.href };
  }

  /** Put a new URL into an already-open edit form. */
  async function updateUrl(scope, cfg) {
    var report = [];
    var field = findField('url', 'text', scope);
    if (!field) return [{ field: 'url', ok: false, detail: 'no URL field on the edit page' }];

    var current = String(field.el.value || '').trim();
    if (current === cfg.mcpUrl) {
      report.push({ field: 'url', ok: true, detail: 'already current' });
    } else {
      setValue(field.el, cfg.mcpUrl);
      report.push({
        field: 'url',
        ok: true,
        detail: 'replaced ' + (current ? current.slice(0, 44) : '(empty)') + ' -> ' + cfg.mcpUrl.slice(0, 44),
      });
    }

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

    // 说明文档 is required on the edit form as well. Without re-attaching it the
    // submit is rejected by validation, which shows no message at all.
    if (cfg.documentContent && !docAlreadyShown(scope, cfg.documentName)) {
      var doc = await attachDocument(scope, cfg.documentName || 'local-dev-mcp.md', cfg.documentContent);
      report.push({ field: 'document', ok: doc.ok, detail: doc.detail });
    } else if (cfg.documentContent) {
      report.push({ field: 'document', ok: true, detail: 'already attached' });
    }
    return report;
  }

  /** Whether the upload widget already shows this file. */
  function docAlreadyShown(scope, name) {
    if (!name) return false;
    var els = (scope || document).querySelectorAll('div,span,strong,p,li');
    for (var i = 0; i < els.length; i++) {
      var el = els[i];
      if (el.children.length > 0 || !visible(el)) continue;
      if ((el.textContent || '').indexOf(name) !== -1) return true;
    }
    return false;
  }

  /**
   * Wait until the entry form is gone from the document.
   *
   * This is the only reliable success signal. Watching the dialog element does
   * not work: the platform keeps other elements whose class contains "dialog"
   * and the view it navigates to after a successful save has dialogs of its
   * own, so a scope-based check stayed "open" forever and reported a save that
   * had in fact gone through as a failure.
   */
  async function waitFormGone(ms) {
    var deadline = Date.now() + ms;
    while (Date.now() < deadline) {
      if (!findField('url', 'text', document)) return true;
      await sleep(200);
    }
    return false;
  }

  /**
   * Read the dialog the platform shows after a save, without dismissing it.
   *
   * The platform reports its verdict here rather than in the form: a rejected
   * save arrives as "更新失败 / 疑似原因：目标 URL 不可达". Clicking that away
   * without reading it made every failure look like a success, so the text is
   * carried back out.
   *
   * Both buttons are returned because the choice matters: 关闭 discards the
   * filled form, while 返回修改 puts it back. A caller that intends to retry
   * needs the latter.
   */
  function readResultDialog(scope) {
    var root = scope && scope !== document && document.contains(scope) ? scope : document;
    var labels = ['关闭', '知道了', '确定', '完成', '返回修改'];
    for (var i = 0; i < labels.length; i++) {
      var btn = findClickable(labels[i], root);
      if (!btn) continue;
      var box = btn.closest('[role=dialog],[class*=dialog],[class*=modal]') || btn.parentElement;
      return {
        said: box ? (box.textContent || '').replace(/\s+/g, ' ').trim().slice(0, 140) : '',
        close: findClickable('关闭', root) || findClickable('知道了', root) || btn,
        back: findClickable('返回修改', root) || null,
      };
    }
    return null;
  }

  /**
   * Save whichever form is open.
   *
   * The button is not always called "确认": an edit form says "确认更新", and a
   * create form says "确认". Matching on the bare word alone silently missed the
   * update case, leaving the dialog open while the log claimed success — so the
   * longer, more specific labels are tried first.
   */
  async function saveForm(scope) {
    var labels = ['确认更新', '确认修改', '保存修改', '确认创建', '确认提交', '保存', '提交', '确认', '确定'];

    // Anchor the search to the container that holds the URL field.
    //
    // Searching from `document` picks up whatever button comes first in document
    // order, which on this platform is a hidden create dialog — so the click went
    // somewhere invisible while the visible form stayed open.
    var anchor = null;
    var urlField = findField('url', 'text', document);
    if (urlField) {
      var cur = urlField.el;
      for (var d = 0; d < 10 && cur && cur !== document.body; d++) {
        if (cur.querySelector && cur.querySelector('button')) {
          anchor = cur;
          break;
        }
        cur = cur.parentElement;
      }
    }
    var searchRoot = anchor || (scope && scope !== document ? scope : document);

    var btn = null;
    for (var i = 0; i < labels.length && !btn; i++) {
      btn = findClickable(labels[i], searchRoot);
    }
    if (!btn && searchRoot !== document) {
      // Fall back to the wider document only if the anchored search found nothing.
      for (var k = 0; k < labels.length && !btn; k++) {
        btn = findClickable(labels[k], document);
      }
    }
    if (!btn) return { ok: false, detail: '未找到保存按钮' };

    var label = (btn.textContent || '').trim();

    // An attached document is processed asynchronously; submitting immediately
    // gets rejected by validation without any visible message. Retry a few
    // times rather than reporting a failure the user cannot act on.
    for (var attempt = 0; attempt < 6; attempt++) {
      // Native click, and a real one at the element's own coordinates: the
      // component-library button ignores a bare synthetic event.
      realClick(btn);

      if (!(await waitFormGone(9000))) {
        // Re-resolve: a re-render replaces the button element, and its label can
        // change for the duration of a submit.
        var again = null;
        for (var j = 0; j < labels.length && !again; j++) {
          again = findClickable(labels[j], searchRoot);
        }
        if (!again && searchRoot !== document) again = findClickable(label, document);
        if (!again) break;
        btn = again;
        continue;
      }

      await sleep(600);
      var dlg = readResultDialog(scope);
      var said = dlg ? dlg.said : '';
      // The platform reports a rejected save in that dialog, not in the form, so
      // "the dialog closed" is not success.
      var blocked = /不可达|无法连接|连接失败|保存失败|更新失败/.test(said);

      if (blocked && dlg && dlg.back && attempt < 4) {
        // It refused because its own connectivity check did not get through.
        // That is a statement about the network right now, not about the form —
        // and the tunnel frequently comes up moments later. 返回修改 puts the
        // filled form back, so retry from there rather than making the user
        // start over.
        pushLog('平台没连上隧道，20 秒后重试（第 ' + (attempt + 2) + ' 次）…');
        realClick(dlg.back);
        await sleep(20000);
        var back = findField('url', 'text', document)
          ? findClickable('确认更新', searchRoot) || findClickable('确认更新', document)
          : null;
        if (!back) break;
        btn = back;
        continue;
      }

      if (dlg && dlg.close) {
        realClick(dlg.close);
        await sleep(400);
      }
      return {
        ok: !blocked,
        detail: '已点「' + label + '」' + (said ? '｜平台提示：' + said : '，表单已提交并关闭'),
      };
    }

    var errs = [];
    var root = scope && scope !== document && document.contains(scope) ? scope : document;
    var nodes = root.querySelectorAll('*');
    for (var n = 0; n < nodes.length; n++) {
      var el = nodes[n];
      if (el.children.length > 0) continue;
      var r = el.getBoundingClientRect();
      if (r.width === 0 || r.height === 0) continue;
      var t = (el.textContent || '').trim();
      if (!t || t.length > 50) continue;
      if (/必填|必选|不能为空|请选择|请上传|请填写|格式|required|invalid/i.test(t)) errs.push(t);
    }
    // Nothing matched: hand back the form's own text rather than reporting "no
    // visible error", which reads as a mystery when it is usually a plain
    // validation line the regex above simply did not recognise.
    var tail = '';
    if (!errs.length && root !== document) {
      tail = (root.textContent || '').replace(/\s+/g, ' ').trim().slice(0, 200);
    }
    return {
      ok: false,
      detail: '点了「' + label + '」但表单仍开着' +
        (errs.length ? '：' + errs.slice(0, 3).join(' / ') : tail ? '：' + tail : '（无可见错误）'),
    };
  }

  async function doFill(forceRefill, retriedExisting) {
    panelState.busy = true;
    // Collapse the panel while driving the page: it sits over the top-right
    // corner, which is exactly where the platform puts 编辑 and 删除.
    var wasOpen = panelState.open;
    panelState.open = false;
    if (panelState.panelEl) panelState.panelEl.style.display = 'none';
    blockInput(true);
    render();
    pushLog('开始填入…');
    try {
      // Wait for the address, but NOT for it to become reachable.
      //
      // An earlier version blocked here until the tunnel answered, which on a
      // slow launch meant minutes of nothing happening after a click — from the
      // outside indistinguishable from a hang. The address is all the form
      // needs; whether the platform can reach it is a separate fight, reported
      // where it actually happens.
      pushLog('等待公网隧道…');
      var cfg = await call('fillConfig');
      for (var attempt = 0; attempt < 15 && cfg.error; attempt++) {
        await sleep(2000);
        cfg = await call('fillConfig');
      }
      if (cfg.error) {
        pushLog('隧道未就绪（' + cfg.error + '），请稍后再点一次');
        return;
      }
      if (!cfg.warm) {
        // A warmup failure describes *this* machine's network, not the
        // platform's. On the machine this was developed on, the local resolver
        // could not resolve *.trycloudflare.com at all (immediate NXDOMAIN)
        // while 223.5.5.5 and 8.8.8.8 both resolved it fine — the platform, with
        // its own DNS, still saved the entry successfully. So warn, do not stop.
        pushLog('（本机探测隧道失败：' + (cfg.warmWhy || '原因未知') + '）');
        pushLog('这只是本机网络的结论——平台用自己那套 DNS，仍然可能连上。继续填表。');
      }
      pushLog('拿到配置：' + cfg.name + ' / ' + cfg.mcpUrl);

      // Nothing to do when the platform already holds this exact address. That
      // is the normal case on every launch after the first, and re-submitting
      // identical values re-uploads the document and re-runs the platform's
      // connectivity check for no reason. 「强制重填」 overrides this.
      if (!forceRefill && lastSavedURL() === cfg.mcpUrl) {
        pushLog('✓ 平台里已经是这个地址，跳过填表');
        pushLog('（隧道地址没变。要强制覆盖请点「强制重填」）');
        var en0 = await enableInChat(cfg);
        pushLog((en0.ok ? '✓ ' : '✗ ') + 'enable — ' + en0.detail);
        return;
      }

      // The panel is reachable from any page, so the flow starts by navigating
      // to the MCP management page rather than assuming the user is already there.
      pushLog('进入 MCP 工具页…');
      if (!(await ensureMcpPage())) {
        pushLog('导航失败，当前 hash=' + location.hash);
        return;
      }
      pushLog('已在 ' + location.hash);
      await sleep(2500);

      // Say which side is broken when the platform refuses the address. The two
      // causes look identical on screen but need opposite responses: a tunnel
      // that never came up is worth retrying, while one that works locally but
      // not from the platform is a routing problem only another tunnel fixes.
      var explainUnreachable = function (saved) {
        if (!/不可达|无法连接|连接失败|更新失败|保存失败/.test(saved.detail)) return;
        if (cfg.warm) {
          pushLog('本机自测这条隧道是通的，平台却连不上——这是平台到 Cloudflare 的线路问题，本程序修不了。');
          pushLog('按 README 的「能固定地址吗」换成自建 frp（启动加 -tunnel frp 那一组参数）。');
        } else {
          pushLog('这条隧道本机自测也不通：' + (cfg.warmWhy || '原因未知'));
          pushLog('检查网络后重试；持续不通就换 frp。');
        }
      };

      // Re-use an existing entry: a duplicate name is rejected, and the tunnel
      // address differs every launch, so the old one has to be updated.
      var existing = await findExisting(cfg.name);
      if (existing) {
        pushLog('发现已创建的「' + cfg.name + '」，改走更新');
        // The edit form may be a dialog, or a form rendered inline on the detail
        // page. Waiting for the URL field itself covers both, whereas waiting for
        // a dialog only covered the first.
        var urlField = findField('url', 'text', document);
        if (!urlField) {
          var editBtn = findClickable('编辑') || findClickable('修改');
          if (!editBtn) {
            pushLog('详情页上没有「编辑」入口（' + location.hash + '）');
            return;
          }
          realClick(editBtn);
          await waitForPage(function () {
            return !!findField('url', 'text', document);
          }, 10000);
          urlField = findField('url', 'text', document);
        }
        if (!urlField) {
          pushLog('点了「编辑」但没出现 URL 字段，请手动打开编辑表单后重试');
          return;
        }

        // The edit form opens blank on this platform — it does not back-fill the
        // saved values — so a partial update leaves six required fields empty and
        // validation rejects the submit with no visible message. Fill the whole
        // form exactly as the create path does.
        var editScope = urlField.el.closest('form') || findFormScope();
        if (editScope === document) editScope = document;

        // The address is the whole point of an update, and the edit form
        // back-fills the OLD one — so it is set and reported explicitly, with
        // both values in the log. Folding it into the generic field pass made a
        // silent no-op ("found the control, set a value") look identical to a
        // real replacement, which is how the address stayed stale across every
        // "successful" update.
        var urlReport = await updateUrl(editScope, cfg);
        urlReport.forEach(function (r) {
          if (r.field !== 'url') return; // the pass below covers the other fields
          pushLog((r.ok ? '✓ ' : '✗ ') + 'url — ' + r.detail);
        });

        var filled = await fillForm(cfg, editScope);
        filled.forEach(function (r) {
          if (r.field === 'url') return; // reported by updateUrl, with the diff
          pushLog((r.ok ? '✓ ' : '✗ ') + r.field + (r.detail ? ' — ' + r.detail : ''));
        });
        var bad = filled.filter(function (r) { return !r.ok; }).length;
        if (bad) {
          pushLog('有 ' + bad + ' 个字段没填上，请核对后再点确认更新');
          return;
        }
        var saved = await saveForm(editScope);
        pushLog((saved.ok ? '✓ ' : '✗ ') + 'save — ' + saved.detail);
        if (!saved.ok) {
          explainUnreachable(saved);
          return;
        }
        markSavedURL(cfg.mcpUrl);
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
        if (!created.ok) {
          explainUnreachable(created);
          // 该名称已被使用 is proof the entry exists and the lookup was simply
          // too early — this list takes tens of seconds to render. Close the
          // dialog and run the whole flow once more, which will now find it and
          // take the update path.
          if (!retriedExisting && /已被使用|已存在|重复/.test(created.detail)) {
            pushLog('这个名称已经存在：刚才列表还没渲染出来。关掉创建框重来一次…');
            var cancel = findClickable('取消', document) || findClickable('关闭', document);
            if (cancel) realClick(cancel);
            await sleep(2500);
            return await doFill(forceRefill, true);
          }
          return;
        }
        markSavedURL(cfg.mcpUrl);
      }

      // Finally get the user to the chat page. Creating the entry is not
      // enough: the composer only offers servers that are switched on, and that
      // switch sits behind a menu this automation deliberately does not click.
      pushLog('启用对话页里的 ' + cfg.name + '…');
      var en = await enableInChat(cfg);
      pushLog((en.ok ? '✓ ' : '✗ ') + 'enable — ' + en.detail);
      if (en.ok) pushLog('全部完成，模型现在可以调用本机了');
    } catch (err) {
      pushLog('失败：' + (err && err.message ? err.message : String(err)));
    } finally {
      panelState.busy = false;
      blockInput(false);
      // Bring the panel back so the run's log is readable.
      panelState.open = wasOpen;
      if (panelState.panelEl) panelState.panelEl.style.display = wasOpen ? 'block' : 'none';
      render();
    }
  }

  /**
   * Switch our MCP on in the chat composer.
   *
   * Creating the entry is not enough: the platform only offers MCP tools that
   * are switched on, and a new entry starts off. The platform names the tools it
   * exposes to the model `mcp_<server>_<tool>`, so an entry that was never
   * switched on simply does not exist as far as the model is concerned — which
   * looks exactly like a broken tunnel from the outside.
   *
   * The switch is four levels down a chain of component-library menus, each of
   * which only exists while the one above it is open:
   *
   *   composer → the ⌘ menu → MCP → 管理我的MCP → the card's .w-switch
   *
   * Every selector here was read off a recording of the real clicks, because the
   * menus do not render for synthetic events and cannot be found by probing.
   */
  /**
   * The ⌘ button that opens the composer's tool menu.
   *
   * It sits between the attach button and 深度研究, inside div.left-origin. It is
   * matched by that anchor rather than by position, because the only thing
   * distinguishing it from the buttons on either side is its class, and
   * w-btn-shape-square is shared.
   */
  function findToolMenuButton() {
    var candidates = document.querySelectorAll('button.w-btn-shape-square');
    for (var i = 0; i < candidates.length; i++) {
      if (candidates[i].getBoundingClientRect().height === 0) continue;
      var box = candidates[i].closest('div.left-origin') || candidates[i].parentElement;
      if (box && (box.textContent || '').indexOf('深度研究') !== -1) return candidates[i];
    }
    return null;
  }

  async function enableInChat(cfg) {
    var steps = [];

    // Wait for the button itself, not for the route. The hash changes before the
    // view renders, and checking "am I already on the chat page" skipped the
    // wait entirely when the flow had started there — so the button was looked
    // for on a page that had not drawn it yet, and the whole step failed in the
    // same second it began.
    if (location.hash.indexOf('#/assistant') !== 0) {
      location.hash = '#/assistant';
    }
    var ready = await waitForPage(function () {
      return !!findToolMenuButton();
    }, 25000);
    if (!ready) {
      return { ok: false, steps: steps, detail: '对话页的 ⌘ 按钮没出现' };
    }
    steps.push('已切到对话页');

    var trigger = findToolMenuButton();
    realClick(trigger);
    await sleep(1500);
    steps.push('打开工具菜单');

    // 2. the MCP section
    if (!(await clickOwnText('div.flex.w-full.items-center', 'MCP'))) {
      return { ok: false, steps: steps, detail: '工具菜单里没有 MCP 分区' };
    }
    await sleep(1200);
    steps.push('展开 MCP');

    // 3. the management panel
    if (!(await clickOwnText('span', '管理我的MCP'))) {
      return { ok: false, steps: steps, detail: '没有「管理我的MCP」入口' };
    }
    steps.push('打开管理面板');

    // 4. our card's switch
    var list = null;
    for (var t = 0; t < 25 && !list; t++) {
      list = document.querySelector('div.mcp-grid-list');
      if (!list) await sleep(300);
    }
    if (!list) return { ok: false, steps: steps, detail: '管理面板没有打开' };

    var cards = list.querySelectorAll('div.w-list-item');
    var target = null;
    for (var c = 0; c < cards.length; c++) {
      var nm = (cards[c].querySelector('span') || {}).textContent || '';
      if (nm.trim() === cfg.name) {
        target = cards[c];
        break;
      }
    }
    if (!target) return { ok: false, steps: steps, detail: '列表里没有 ' + cfg.name };

    var sw = target.querySelector('.w-switch');
    if (!sw) return { ok: false, steps: steps, detail: cfg.name + ' 那一行没有开关' };

    // The component library marks the on state with a class, not a property.
    if (/checked/.test(String(sw.className))) {
      await closeDialog();
      return { ok: true, steps: steps, detail: cfg.name + ' 已经是启用状态' };
    }

    realClick(sw);
    await sleep(2200);

    // Look the card up again rather than reusing `target`. Flipping the switch
    // re-renders the list, and the old node reference goes stale — reading its
    // class then reported "did not turn on" for a switch that had in fact just
    // turned on.
    var freshCard = null;
    var again = list.querySelectorAll('div.w-list-item');
    for (var d = 0; d < again.length; d++) {
      var nm2 = (again[d].querySelector('span') || {}).textContent || '';
      if (nm2.trim() === cfg.name) {
        freshCard = again[d];
        break;
      }
    }
    var after = freshCard ? freshCard.querySelector('.w-switch') : null;
    var on = !!after && /checked/.test(String(after.className));
    await closeDialog();
    return {
      ok: on,
      steps: steps,
      detail: on ? cfg.name + ' 已启用' : '点了开关但没有变成启用态',
    };
  }

  /** Click the first visible element of `selector` whose own text is exactly `text`. */
  async function clickOwnText(selector, text) {
    for (var i = 0; i < 25; i++) {
      var els = document.querySelectorAll(selector);
      for (var k = 0; k < els.length; k++) {
        var el = els[k];
        if (el.getBoundingClientRect().height === 0) continue;
        var own = '';
        for (var n = 0; n < el.childNodes.length; n++) {
          if (el.childNodes[n].nodeType === 3) own += el.childNodes[n].textContent;
        }
        if (own.trim() !== text) continue;
        realClick(el);
        return true;
      }
      await sleep(300);
    }
    return false;
  }

  /** Dismiss whichever modal is open. */
  async function closeDialog() {
    await sleep(600);
    var labels = ['取消', '关闭'];
    var btns = document.querySelectorAll('button');
    for (var i = 0; i < labels.length; i++) {
      for (var k = 0; k < btns.length; k++) {
        if ((btns[k].textContent || '').trim() !== labels[i]) continue;
        if (btns[k].getBoundingClientRect().height === 0) continue;
        realClick(btns[k]);
        await sleep(500);
        return true;
      }
    }
    return false;
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
    console.log('[scienceone-desktop] probe', info);
    return info;
  }

  // exposed for the console, so the automation can be driven by hand
  window.__scienceoneDesktopAPI = {
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
    recordClicks: recordClicks,
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
