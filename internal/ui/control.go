package ui

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Control exposes the injected automation over local HTTP so it can be driven
// without a mouse.
//
// The window is the primary interface, but synthesising clicks to test or to
// script the form filling is unreliable, and there is no other way to reach the
// page's JavaScript from outside the process. Bound to loopback and gated by the
// same token as the MCP endpoint.
type Control struct {
	App   *App
	Token string
}

func (c *Control) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if c.Token != "" {
		got := r.Header.Get("X-MCP-Token")
		if got == "" {
			got = r.URL.Query().Get("token")
		}
		if got != c.Token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	action := strings.TrimPrefix(r.URL.Path, "/control/")
	var js string
	switch action {
	case "eval":
		// Run arbitrary JS in the page. The endpoint is bound to loopback and
		// gated on the same token as everything else, and it exists because every
		// question about this UI — where is the tool menu, what does this button
		// do — is answered faster by asking the page than by guessing at it.
		code := r.URL.Query().Get("js")
		if code == "" {
			writeControlJSON(w, map[string]any{"error": "eval needs ?js="})
			return
		}
		js = wrapped("eval", code)
	case "probe":
		js = "window.__scienceoneDesktopAPI.probe()"
	case "fill":
		js = "window.__scienceoneDesktopAPI.fill()"
	case "open":
		js = "window.__scienceoneDesktopAPI.state.open = true; window.__scienceoneDesktopAPI.refresh()"
	case "fields":
		// Which element each label resolves to, and what it looks like — the
		// only way to tell a mis-resolved label from a picker that will not open.
		js = wrapped("fields", `
			var api = window.__scienceoneDesktopAPI;
			var out = [];
			[['名称','text'],['描述','text'],['URL','text'],['鉴权方式','picker'],['学科','picker']].forEach(function(pair){
				var el = api.fieldByLabel(pair[0], pair[1]);
				if (!el) { out.push(pair[0] + ' (' + pair[1] + ') -> NOT FOUND'); return; }
				var r = el.getBoundingClientRect();
				out.push(pair[0] + ' (' + pair[1] + ') -> <' + el.tagName.toLowerCase() +
					' class="' + String(el.className).slice(0,44) + '" val="' + String(el.value).slice(0,26) +
					'" rect=' + Math.round(r.width) + 'x' + Math.round(r.height) + '>');
			});
			return out.join('\n');
		`)
	case "picker":
		// Diff the DOM across a click: whatever appears is the dropdown, and its
		// markup is the only reliable thing to match on.
		js = wrapped("picker", `
			var api = window.__scienceoneDesktopAPI;
			var el = api.fieldByLabel(LABEL, 'picker');
			if (!el) return 'control not found';

			function snapshot() {
				var set = {};
				var all = document.querySelectorAll('*');
				for (var i = 0; i < all.length; i++) {
					var e = all[i];
					var r = e.getBoundingClientRect();
					if (r.width === 0 || r.height === 0) continue;
					var t = (e.textContent || '').trim();
					if (!t || t.length > 24) continue;
					if (e.children.length > 0) continue;
					set[e.tagName.toLowerCase() + '|' + String(e.className).slice(0,44) + '|' + t + '|' + Math.round(r.left) + ',' + Math.round(r.top)] = 1;
				}
				return set;
			}

			var before = snapshot();
			var wrapper = el.closest('[class*=w-select]') || el.parentElement;
			wrapper.dispatchEvent(new MouseEvent('mousedown', {bubbles:true}));
			wrapper.dispatchEvent(new MouseEvent('mouseup', {bubbles:true}));
			wrapper.click();

			setTimeout(function(){
				var after = snapshot();
				var fresh = [];
				for (var k in after) { if (!before[k]) fresh.push(k); }
				window.__scienceoneCall('report', { msg: 'control:picker-result',
					data: fresh.length ? fresh.slice(0,25).join(' ~ ') : 'nothing new appeared' });
			}, 1000);
			return 'opened';
		`)
		js = strings.ReplaceAll(js, "LABEL", strconv.Quote(r.URL.Query().Get("label")))
	case "values":
		// Read the form back. A screenshot shows what looks filled; this shows
		// what the framework actually holds.
		js = wrapped("values", `
			var api = window.__scienceoneDesktopAPI;
			var scope = api.findFormScope();
			var out = [];
			[['名称','text'],['描述','text'],['URL','text'],['鉴权方式','picker'],['学科','picker']].forEach(function(pair){
				var el = api.fieldByLabel(pair[0], pair[1]);
				if (!el) { out.push(pair[0] + ' = <not found>'); return; }
				// A picker is a span, so its state is textContent, not value.
				var v = pair[1] === 'picker' ? el.textContent : el.value;
				out.push(pair[0] + ' = ' + JSON.stringify(String(v).trim()));
			});
			out.push('请求头 = ' + JSON.stringify(api.headerPairs(scope).map(function(p){
				return p.name + ':' + p.value;
			}).join(' | ')));
			var extra = [];
			var els = scope.querySelectorAll('input,textarea');
			for (var i = 0; i < els.length; i++) {
				var el = els[i];
				var r = el.getBoundingClientRect();
				if (r.width === 0 || r.height === 0) continue;
				if (el.value) extra.push((el.getAttribute('placeholder')||el.className).slice(0,30) + ' = ' + JSON.stringify(el.value).slice(0,80));
			}
			return out.join('\n') + '\n--- all non-empty ---\n' + extra.join('\n');
		`)
	case "area":
		// Dump the elements of a region with positions, so a layout can be read
		// off by where things sit rather than by guessing class names.
		// raw=1 scans the whole document and keeps icon-only controls, which
		// carry no text at all and are skipped otherwise.
		raw := "false"
		if r.URL.Query().Get("raw") != "" {
			raw = "true"
		}
		js = wrapped("area", `
			var RAW = @RAW@;
			var api = window.__scienceoneDesktopAPI;
			var scope = RAW ? document : api.findFormScope();
			var rows = [];
			var all = scope.querySelectorAll('*');
			for (var i = 0; i < all.length; i++) {
				var el = all[i];
				var r = el.getBoundingClientRect();
				if (r.width === 0 || r.height === 0) continue;
				if (r.height > 400) continue;               // skip page wrappers
				var own = '';
				for (var k = 0; k < el.childNodes.length; k++) {
					if (el.childNodes[k].nodeType === 3) own += el.childNodes[k].textContent;
				}
				own = own.trim();
				var val = el.value !== undefined ? el.value : '';
				var ph = el.getAttribute ? (el.getAttribute('placeholder') || '') : '';
				if (!RAW && !own && !val && !ph && el.tagName !== 'INPUT' && el.tagName !== 'TEXTAREA') continue;
				rows.push({
					y: Math.round(r.top), x: Math.round(r.left),
					w: Math.round(r.width), h: Math.round(r.height),
					tag: el.tagName.toLowerCase(),
					cls: String(el.className).slice(0, 34),
					text: own.slice(0, 22), val: String(val).slice(0, 30), ph: ph.slice(0, 24)
				});
			}
			rows.sort(function(a, b) { return a.y - b.y || a.x - b.x; });
			return rows.slice(0, 400).map(function(r) {
				return r.y + ',' + r.x + ' ' + r.w + 'x' + r.h + ' ' + r.tag + '.' + r.cls +
					(r.text ? ' T=' + JSON.stringify(r.text) : '') +
					(r.val ? ' V=' + JSON.stringify(r.val) : '') +
					(r.ph ? ' P=' + JSON.stringify(r.ph) : '');
			}).join('\n');
		`)
		js = strings.ReplaceAll(js, "@RAW@", raw)
	case "text":
		// Dump the visible leaf text of the page top-to-bottom. Needed to read
		// back what the platform actually stored (the detail view), which no
		// dialog-scoped probe can reach.
		js = wrapped("text", `
			var out = [], seen = {};
			var all = document.querySelectorAll('body *');
			for (var i = 0; i < all.length; i++) {
				var el = all[i];
				if (el.children.length > 0) continue;
				var r = el.getBoundingClientRect();
				if (r.width === 0 || r.height === 0) continue;
				var t = (el.textContent || '').trim();
				if (!t || t.length > 90) continue;
				var key = Math.round(r.top) + ':' + t;
				if (seen[key]) continue;
				seen[key] = 1;
				out.push([Math.round(r.top), Math.round(r.left), t]);
			}
			out.sort(function(a, b) { return a[0] - b[0] || a[1] - b[1]; });
			return out.slice(0, 80).map(function(r) {
				return r[0] + ',' + r[1] + ' ' + r[2];
			}).join('\n');
		`)
	case "click":
		// Eval discards the result, so the outcome travels back through the
		// report channel and lands in bridge.log.
		text := r.URL.Query().Get("text")
		if text == "" {
			writeControlJSON(w, map[string]any{"error": "click needs ?text="})
			return
		}
		js = wrapped("click "+text, `
			var hit = null, loose = null;
			var els = document.querySelectorAll('button,a,div[role=button],span[role=button],li');
			for (var i = 0; i < els.length; i++) {
				var el = els[i];
				var r = el.getBoundingClientRect();
				if (r.width === 0 && r.height === 0) continue;
				var t = (el.textContent || '').trim();
				if (t === TEXT) { hit = el; break; }
				if (!loose && t.length <= 12 && t.indexOf(TEXT) !== -1) loose = el;
			}
			hit = hit || loose;
			if (!hit) return 'not found: ' + TEXT;
			hit.click();
			return 'clicked: ' + (hit.textContent || '').trim();
		`)
		js = strings.ReplaceAll(js, "TEXT", strconv.Quote(text))
	case "clickxy":
		// Click whatever is at a viewport point. Needed for icon-only controls,
		// which have no text to match on.
		xs := r.URL.Query().Get("x")
		ys := r.URL.Query().Get("y")
		if xs == "" || ys == "" {
			writeControlJSON(w, map[string]any{"error": "clickxy needs ?x=&y="})
			return
		}
		js = wrapped("clickxy "+xs+","+ys, `
			var el = document.elementFromPoint(X, Y);
			if (!el) return 'nothing at ' + X + ',' + Y;
			var desc = el.tagName.toLowerCase() + '.' + String(el.className).slice(0,40);
			// Climb to the nearest thing that looks clickable.
			var target = el;
			for (var i = 0; i < 4 && target; i++) {
				var c = String(target.className || '');
				if (target.tagName === 'BUTTON' || /btn|icon|button/i.test(c)) break;
				target = target.parentElement;
			}
			if (!target) target = el;
			var r = target.getBoundingClientRect();
			target.click();
			return 'clicked ' + desc + ' -> ' + target.tagName.toLowerCase() + '.' +
				String(target.className).slice(0,40) + ' @' + Math.round(r.left) + ',' + Math.round(r.top);
		`)
		js = strings.ReplaceAll(js, "X", xs)
		js = strings.ReplaceAll(js, "Y", ys)
	case "submit":
		// Press the primary button and explain what happened. A dialog that
		// stays open with no visible error usually means a required field is
		// unset in the framework's own state, not in the DOM.
		js = wrapped("submit", `
			var api = window.__scienceoneDesktopAPI;
			var scope = api.findFormScope();

			function report(tag) {
				var out = [tag];
				var files = document.querySelectorAll('input[type=file]');
				for (var i = 0; i < files.length; i++) {
					out.push('fileInput[' + i + '].files=' + (files[i].files ? files[i].files.length : 'n/a'));
				}
				// Any text that looks like a validation complaint, however it is styled.
				var all = scope.querySelectorAll('*');
				var msgs = [];
				for (var j = 0; j < all.length; j++) {
					var el = all[j];
					if (el.children.length > 0) continue;
					var r = el.getBoundingClientRect();
					if (r.width === 0 || r.height === 0) continue;
					var t = (el.textContent || '').trim();
					if (!t || t.length > 50) continue;
					if (/必填|必选|不能为空|请选择|请上传|请填写|格式|required|invalid/i.test(t)) {
						msgs.push('"' + t + '"');
					}
				}
				out.push('validation: ' + (msgs.length ? msgs.slice(0, 6).join(' | ') : 'none'));
				// The primary button, by its own class rather than by its label.
				var prim = scope.querySelector('[class*=primary-btn],button.mcp-create-primary-btn');
				if (prim) {
					out.push('primary: disabled=' + !!prim.disabled +
						' aria=' + (prim.getAttribute('aria-disabled') || '-') +
						' cls=' + String(prim.className).slice(0, 50));
				} else {
					out.push('primary: not found');
				}
				return out.join(' | ');
			}

			var before = report('BEFORE');
			var btn = scope.querySelector('[class*=primary-btn]') ||
				api.findClickable('确认', scope) || api.findClickable('确认更新', scope);
			if (!btn) return before + ' || NO BUTTON';
			btn.click();
			return before + ' || clicked ' + String(btn.className).slice(0, 40);
		`)
	case "after":
		js = wrapped("after", `
			var api = window.__scienceoneDesktopAPI;
			var scope = api.findFormScope();
			var out = [];
			out.push('dialogStillOpen=' + (scope !== document));
			if (scope !== document) {
				var all = scope.querySelectorAll('*');
				var msgs = [];
				for (var j = 0; j < all.length; j++) {
					var el = all[j];
					if (el.children.length > 0) continue;
					var r = el.getBoundingClientRect();
					if (r.width === 0 || r.height === 0) continue;
					var t = (el.textContent || '').trim();
					if (!t || t.length > 50) continue;
					if (/必填|必选|不能为空|请选择|请上传|请填写|格式|required|invalid|失败|错误/i.test(t)) {
						msgs.push('"' + t + '"');
					}
				}
				out.push('messages: ' + (msgs.length ? msgs.slice(0, 6).join(' | ') : 'none'));
			}
			return out.join(' | ');
		`)
	case "goto":
		// Navigate the window, so a whole flow can be driven from outside.
		//
		// A one-shot query value is added on purpose. Navigating to a URL that
		// differs only in its hash does not reload the document, and this SPA
		// does not notice a hash change made from outside itself — the address
		// moved while the view stayed on the previous page. Changing the query
		// forces a real document load, so the router always starts from the
		// requested route.
		target := r.URL.Query().Get("url")
		if target == "" {
			writeControlJSON(w, map[string]any{"error": "goto needs ?url="})
			return
		}
		head, frag := target, ""
		if i := strings.Index(target, "#"); i >= 0 {
			head, frag = target[:i], target[i:]
		}
		sep := "?"
		if strings.Contains(head, "?") {
			sep = "&"
		}
		target = head + sep + "_nav=" + strconv.FormatInt(time.Now().UnixNano(), 36) + frag
		c.App.navigate(target)
		writeControlJSON(w, map[string]any{"navigated": target})
		return
	case "cards":
		// Dump every element whose own text is one of the list entries, with its
		// ancestor chain — enough to see which element a click would actually hit.
		js = wrapped("cards", `
			var wanted = ['desktop', 'local-dev-mcp'];
			var out = [];
			var all = document.querySelectorAll('*');
			for (var i = 0; i < all.length; i++) {
				var el = all[i];
				var t = (el.textContent || '').trim();
				if (wanted.indexOf(t) === -1) continue;
				var r = el.getBoundingClientRect();
				if (r.width === 0 || r.height === 0) continue;
				var chain = [];
				var cur = el;
				for (var d = 0; d < 5 && cur && cur !== document.body; d++) {
					chain.push(cur.tagName.toLowerCase() + '.' + String(cur.className || '').split(' ').slice(0,2).join('.'));
					cur = cur.parentElement;
				}
				out.push('T="' + t + '" <' + el.tagName.toLowerCase() + '.' + String(el.className).slice(0,40) +
					'> @' + Math.round(r.left) + ',' + Math.round(r.top) + ' ' + Math.round(r.width) + 'x' + Math.round(r.height) +
					'  chain: ' + chain.join(' < '));
			}
			// What the click would actually land on, by hit-testing the centre.
			var hits = [];
			for (var j = 0; j < wanted.length; j++) {
				var api = window.__scienceoneDesktopAPI;
				var el2 = api.findClickable(wanted[j]);
				if (!el2) { hits.push(wanted[j] + ' -> findClickable returned null'); continue; }
				var rr = el2.getBoundingClientRect();
				var cx = rr.left + rr.width / 2, cy = rr.top + rr.height / 2;
				var top = document.elementFromPoint(cx, cy);
				hits.push(wanted[j] + ' -> picked <' + el2.tagName.toLowerCase() + '.' +
					String(el2.className).slice(0,30) + '> centre ' + Math.round(cx) + ',' + Math.round(cy) +
					'; elementFromPoint = <' + (top ? top.tagName.toLowerCase() + '.' + String(top.className).slice(0,30) : 'none') + '>');
			}
			return out.join('\n') + '\n--- hit test ---\n' + hits.join('\n');
		`)
	case "page":
		// Location plus visible buttons plus open dialogs. This is what tells
		// "the save worked" apart from "there was nothing to click".
		js = wrapped("page", `
			var out = [];
			out.push('URL: ' + location.href);
			var bs = document.querySelectorAll('button,a,div[role=button],span[role=button]');
			var seen = [];
			for (var i = 0; i < bs.length; i++) {
				var b = bs[i];
				var r = b.getBoundingClientRect();
				if (r.width === 0 || r.height === 0) continue;
				var t = (b.textContent || '').trim();
				if (!t || t.length > 16) continue;
				seen.push('"' + t + '"@' + Math.round(r.left) + ',' + Math.round(r.top));
			}
			out.push('buttons: ' + seen.join('  '));
			var dlg = document.querySelectorAll('[role=dialog],[class*=dialog],[class*=modal]');
			var open = 0;
			for (var j = 0; j < dlg.length; j++) {
				var rr = dlg[j].getBoundingClientRect();
				if (rr.width > 0 && rr.height > 0) open++;
			}
			out.push('open dialogs: ' + open);
			var errs = document.querySelectorAll('[class*=error],[class*=Error],[class*=danger]');
			var msgs = [];
			for (var k = 0; k < errs.length; k++) {
				var er = errs[k].getBoundingClientRect();
				if (er.width === 0 || er.height === 0) continue;
				var et = (errs[k].textContent || '').trim();
				if (et && et.length < 60) msgs.push(et);
			}
			out.push('errors: ' + (msgs.length ? msgs.slice(0,5).join(' | ') : 'none'));
			return out.join('\n');
		`)
	case "composer":
		// The chat composer's tool affordances: focus the box and list everything
		// hugging it, since the MCP picker lives there, not in the page chrome.
		js = wrapped("composer", `
			var ta = document.querySelector('textarea.w-textarea') || document.querySelector('textarea');
			if (!ta) return 'no composer textarea found';
			ta.focus();

			var r = ta.getBoundingClientRect();
			var pad = 220;
			var els = document.querySelectorAll('div,span,button,i,label,svg');
			var hits = [];
			for (var i = 0; i < els.length; i++) {
				var el = els[i];
				var b = el.getBoundingClientRect();
				if (b.width === 0 || b.height === 0) continue;
				if (b.top < r.top - 60 || b.top > r.bottom + pad) continue;
				if (b.left < r.left - pad || b.left > r.right + pad) continue;
				var t = (el.textContent || '').trim();
				var cls = String(el.className || '');
				if (t.length > 16) continue;
				if (!t && !/btn|icon|tool|mcp|plugin|add|plus|skill|attach|menu|select/i.test(cls)) continue;
				hits.push(el.tagName.toLowerCase() + '.' + cls.slice(0, 34) +
					(t ? ' T=' + JSON.stringify(t) : '') +
					' @' + Math.round(b.left) + ',' + Math.round(b.top) +
					' ' + Math.round(b.width) + 'x' + Math.round(b.height));
			}
			return 'composer at ' + Math.round(r.left) + ',' + Math.round(r.top) + ' ' +
				Math.round(r.width) + 'x' + Math.round(r.height) + '\n' + hits.slice(0, 45).join('\n');
		`)
	default:
		writeControlJSON(w, map[string]any{
			"error": "unknown action",
			// Kept in sync by listing what the switch actually handles.
			"actions": []string{
				"probe", "fill", "open", "fields", "picker", "values", "area",
				"click", "clickxy", "goto", "page", "cards", "submit", "after",
			},
		})
		return
	}

	c.App.Eval(js)
	writeControlJSON(w, map[string]any{"dispatched": action, "js": js})
}

// wrapped runs a snippet and reports whatever it returns or throws, because the
// webview's Eval does not return a value to the caller.
func wrapped(label, body string) string {
	return "(function(){try{var v=(function(){" + body + "})();" +
		"window.__scienceoneCall('report',{msg:'control:" + label + "',data:v});" +
		"}catch(e){window.__scienceoneCall('report',{msg:'control:" + label + " error',data:String(e)});}})()"
}

func writeControlJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}