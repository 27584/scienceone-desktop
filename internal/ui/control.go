package ui

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
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
	case "probe":
		js = "window.__panshiDesktopAPI.probe()"
	case "fill":
		js = "window.__panshiDesktopAPI.fill()"
	case "open":
		js = "window.__panshiDesktopAPI.state.open = true; window.__panshiDesktopAPI.refresh()"
	case "fields":
		// Which element each label resolves to, and what it looks like — the
		// only way to tell a mis-resolved label from a picker that will not open.
		js = wrapped("fields", `
			var api = window.__panshiDesktopAPI;
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
			var api = window.__panshiDesktopAPI;
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
				window.__panshiCall('report', { msg: 'control:picker-result',
					data: fresh.length ? fresh.slice(0,25).join(' ~ ') : 'nothing new appeared' });
			}, 1000);
			return 'opened';
		`)
		js = strings.ReplaceAll(js, "LABEL", strconv.Quote(r.URL.Query().Get("label")))
	case "values":
		// Read the form back. A screenshot shows what looks filled; this shows
		// what the framework actually holds.
		js = wrapped("values", `
			var api = window.__panshiDesktopAPI;
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
		// Dump the dialog top-to-bottom with positions, so a region can be
		// identified by where it sits rather than by guessing class names.
		js = wrapped("area", `
			var api = window.__panshiDesktopAPI;
			var scope = api.findFormScope();
			var rows = [];
			var all = scope.querySelectorAll('*');
			for (var i = 0; i < all.length; i++) {
				var el = all[i];
				var r = el.getBoundingClientRect();
				if (r.width === 0 || r.height === 0) continue;
				if (r.height > 260) continue;               // skip whole-form wrappers
				var own = '';
				for (var k = 0; k < el.childNodes.length; k++) {
					if (el.childNodes[k].nodeType === 3) own += el.childNodes[k].textContent;
				}
				own = own.trim();
				var val = el.value !== undefined ? el.value : '';
				var ph = el.getAttribute ? (el.getAttribute('placeholder') || '') : '';
				if (!own && !val && !ph && el.tagName !== 'INPUT' && el.tagName !== 'TEXTAREA') continue;
				rows.push({
					y: Math.round(r.top), x: Math.round(r.left),
					w: Math.round(r.width), h: Math.round(r.height),
					tag: el.tagName.toLowerCase(),
					cls: String(el.className).slice(0, 34),
					text: own.slice(0, 22), val: String(val).slice(0, 30), ph: ph.slice(0, 24)
				});
			}
			rows.sort(function(a, b) { return a.y - b.y || a.x - b.x; });
			return rows.map(function(r) {
				return r.y + ',' + r.x + ' ' + r.w + 'x' + r.h + ' ' + r.tag + '.' + r.cls +
					(r.text ? ' T=' + JSON.stringify(r.text) : '') +
					(r.val ? ' V=' + JSON.stringify(r.val) : '') +
					(r.ph ? ' P=' + JSON.stringify(r.ph) : '');
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
	case "goto":
		// Navigate the window, so a whole flow can be driven from outside.
		target := r.URL.Query().Get("url")
		if target == "" {
			writeControlJSON(w, map[string]any{"error": "goto needs ?url="})
			return
		}
		c.App.navigate(target)
		writeControlJSON(w, map[string]any{"navigated": target})
		return
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
			"error":   "unknown action",
			"actions": []string{"probe", "fill", "open", "fields", "picker", "values", "area", "click", "goto", "page"},
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
		"window.__panshiCall('report',{msg:'control:" + label + "',data:v});" +
		"}catch(e){window.__panshiCall('report',{msg:'control:" + label + " error',data:String(e)});}})()"
}

func writeControlJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}