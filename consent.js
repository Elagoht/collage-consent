"use strict";
// elagoht/consent: the banner, the gate for scripts and iframes, the cookie, GPC
// and window.collageConsent. Plain ES2020, no dependencies.
//
// Absent-safe by design: gated markup is inert on its own (type="text/plain",
// data-src), so a page whose copy of this file never loads runs nothing gated.
// Nothing here ever falls back to running gated content.
//
// All text is set with textContent; nothing here uses innerHTML.
(function () {
  var COOKIE = "collage_consent";

  // ---- configuration --------------------------------------------------------

  var el = document.currentScript ||
    document.querySelector('script[data-consent-config][src*="/_collage/consent/consent.js"]');
  var cfg;
  try {
    cfg = JSON.parse(el.dataset.consentConfig);
  } catch (e) {
    console.warn("elagoht/consent: no readable data-consent-config; nothing gated will run");
    return;
  }
  var version = String(cfg.version);
  var text = cfg.text || {};
  var labels = text.categories || {};
  var maxAge = Number(cfg.maxAge) || 0;
  var required = new Set();
  var optional = new Set(); // configured, not required
  (cfg.categories || []).forEach(function (c) {
    (c.required ? required : optional).add(c.name);
  });

  // ---- the cookie -------------------------------------------------------------
  //
  // PARSE RULES. Task 4's Go parser mirrors these exactly; keep the two in step.
  //
  // Finding the cookie (this mirrors Go's net/http Request.Cookie, go1.26):
  //  1. Split the cookie string on ";". Trim each part of space, tab, CR and LF; skip
  //     empty parts. Split each part at its first "=" into name and value; trim
  //     the name (not the value). Consider only parts whose name is exactly
  //     "collage_consent".
  //  2. If the value is at least 2 characters and both starts and ends with '"',
  //     drop those two quotes (once).
  //  3. The value must then consist only of bytes 0x20-0x7E other than '"', ';'
  //     and '\'. A value that does not is skipped, and the next part named
  //     collage_consent is tried. The first that passes is THE cookie; with none,
  //     there is no cookie (no decision).
  //
  // Parsing the value. Any failure below makes it INVALID, which means "no
  // decision": only the required categories are granted and the dialog opens.
  //  4. The value is at most 4096 bytes (it is ASCII by rule 3, so characters).
  //  5. Split it on "&". Each piece must contain "="; its key is the text before
  //     the first "=", its value the rest. The keys must be exactly "v", "c" and
  //     "t", each exactly once, in any order. Any other key, a repeat, a missing
  //     key or a piece without "=" is invalid.
  //  6. v matches ^[1-9][0-9]*$ and, compared as a string, equals the configured
  //     version written in decimal. Otherwise: no decision (an older version is
  //     a new question).
  //  7. t matches ^[0-9]+$. Its value is not otherwise used.
  //  8. c is "" (zero entries) or is split on "," into entries; more than 64
  //     entries is invalid. No decoding of any kind (no percent-decoding, no
  //     trimming, case-sensitive). An entry that is not a configured,
  //     non-required category name is dropped silently (unknown names, required
  //     names, empty entries); duplicates count once.
  //  9. The result is a decision: the granted set is the required categories
  //     plus the surviving entries of c.
  //
  // WRITING: collage_consent=v=<version>&c=<granted non-required names, sorted,
  // comma-joined>&t=<unix seconds>; Path=/; SameSite=Lax; Max-Age=<maxAge>
  // plus "; Secure" on https. Not HttpOnly.

  function trimSpace(s) {
    return s.replace(/^[ \t\r\n]+|[ \t\r\n]+$/g, "");
  }

  function validValue(v) {
    for (var i = 0; i < v.length; i++) {
      var b = v.charCodeAt(i);
      if (b < 0x20 || b > 0x7e || b === 0x22 || b === 0x3b || b === 0x5c) return false;
    }
    return true;
  }

  // rawCookie is the collage_consent value by rules 1-3, or null.
  function rawCookie(all) {
    var parts = all.split(";");
    for (var i = 0; i < parts.length; i++) {
      var part = trimSpace(parts[i]);
      if (part === "") continue;
      var eq = part.indexOf("=");
      var name = trimSpace(eq < 0 ? part : part.slice(0, eq));
      var val = eq < 0 ? "" : part.slice(eq + 1);
      if (name !== COOKIE) continue;
      if (val.length > 1 && val[0] === '"' && val[val.length - 1] === '"') val = val.slice(1, -1);
      if (validValue(val)) return val;
    }
    return null;
  }

  // parse applies rules 4-9: a Set of granted non-required names, or null for
  // no decision.
  function parse(value) {
    if (value === null || value.length > 4096) return null;
    var seen = {};
    var pieces = value.split("&");
    for (var i = 0; i < pieces.length; i++) {
      var eq = pieces[i].indexOf("=");
      if (eq < 0) return null;
      var k = pieces[i].slice(0, eq);
      if ((k !== "v" && k !== "c" && k !== "t") || Object.prototype.hasOwnProperty.call(seen, k)) return null;
      seen[k] = pieces[i].slice(eq + 1);
    }
    if (!("v" in seen) || !("c" in seen) || !("t" in seen)) return null;
    if (!/^[1-9][0-9]*$/.test(seen.v) || seen.v !== version) return null;
    if (!/^[0-9]+$/.test(seen.t)) return null;
    var granted = new Set();
    if (seen.c !== "") {
      var entries = seen.c.split(",");
      if (entries.length > 64) return null;
      entries.forEach(function (n) {
        if (optional.has(n)) granted.add(n);
      });
    }
    return granted;
  }

  // ---- state ------------------------------------------------------------------

  var chosen = parse(rawCookie(document.cookie)); // null: no decision yet
  var decided = chosen !== null;
  if (!chosen) chosen = new Set();

  function isGranted(name) {
    return required.has(name) || chosen.has(name);
  }

  function grantedList() {
    var out = Array.from(required);
    chosen.forEach(function (n) { out.push(n); });
    return out.sort();
  }

  function save(next) {
    var withdrawn = false;
    chosen.forEach(function (n) {
      if (!next.has(n)) withdrawn = true;
    });
    chosen = next;
    decided = true;
    var c = Array.from(next).sort().join(",");
    var cookie = COOKIE + "=v=" + version + "&c=" + c + "&t=" + Math.floor(Date.now() / 1000) +
      "; Path=/; SameSite=Lax; Max-Age=" + maxAge;
    if (location.protocol === "https:") cookie += "; Secure";
    document.cookie = cookie;
    if (dialog && dialog.open) closeDialog();
    gate();
    document.dispatchEvent(new CustomEvent("collage:consent", { detail: grantedList() }));
    if (withdrawn) location.reload(); // a script that has run cannot be undone
  }

  function set(obj) {
    var next = new Set(chosen);
    if (obj && typeof obj === "object") {
      Object.keys(obj).forEach(function (k) {
        if (!optional.has(k)) return;
        if (obj[k] === true) next.add(k);
        else if (obj[k] === false) next.delete(k);
      });
    }
    save(next);
  }

  // ---- the gate ---------------------------------------------------------------

  var warned = new Set();
  function known(name) {
    if (required.has(name) || optional.has(name)) return true;
    if (!warned.has(name)) {
      warned.add(name);
      console.warn('elagoht/consent: data-consent="' + name + '" is not a configured category; it stays inert');
    }
    return false;
  }

  // Scripts run one at a time, in document order: a src script must load (or
  // fail) before the next is inserted, since an inserted inline script runs at
  // once. One queue for the page; a node is taken once.
  var queue = [];
  var taken = new WeakSet();
  var waiting = false;

  function pump() {
    while (!waiting && queue.length) {
      var old = queue.shift();
      if (!old.isConnected) continue;
      var s = document.createElement("script");
      for (var i = 0; i < old.attributes.length; i++) {
        var a = old.attributes[i];
        if (a.name !== "type" && a.name !== "data-consent") s.setAttribute(a.name, a.value);
      }
      s.text = old.text;
      if (s.hasAttribute("src")) {
        s.async = false;
        waiting = true;
        var next = function () {
          waiting = false;
          pump();
        };
        s.addEventListener("load", next, { once: true });
        s.addEventListener("error", next, { once: true });
      }
      old.replaceWith(s);
    }
  }

  var placeholders = new WeakMap();

  function placeholderFor(frame, name) {
    if (placeholders.has(frame)) return;
    var host = "";
    try {
      host = new URL(frame.getAttribute("data-src"), location.href).host;
    } catch (e) { /* leave it empty */ }
    var label = labels[name] || name;
    var b = document.createElement("button");
    b.type = "button";
    b.className = "collage-consent-placeholder";
    var msg = document.createElement("span");
    msg.textContent = String(text.placeholder || "").split("{host}").join(host);
    var allow = document.createElement("span");
    allow.className = "collage-consent-allow";
    allow.textContent = String(text.allow || "").split("{category}").join(label);
    b.append(msg, " ", allow);
    b.addEventListener("click", function () {
      var o = {};
      o[name] = true;
      set(o);
    });
    frame.before(b);
    placeholders.set(frame, b);
  }

  function gate() {
    document.querySelectorAll('script[type="text/plain"][data-consent]').forEach(function (s) {
      var name = s.getAttribute("data-consent");
      if (taken.has(s) || !known(name) || !isGranted(name)) return;
      taken.add(s);
      queue.push(s);
    });
    pump();
    document.querySelectorAll("iframe[data-consent][data-src]").forEach(function (f) {
      var name = f.getAttribute("data-consent");
      if (!known(name)) return;
      if (isGranted(name)) {
        if (!f.hasAttribute("src")) f.setAttribute("src", f.getAttribute("data-src"));
        var p = placeholders.get(f);
        if (p) {
          p.remove();
          placeholders.delete(f);
        }
      } else {
        placeholderFor(f, name);
      }
    });
  }

  // ---- the dialog -------------------------------------------------------------

  var dialog = null;
  var boxes = {};
  var choices, saveBtn, settingsBtn;
  var returnTo = null;

  var css =
    ".collage-consent{box-sizing:border-box;max-width:min(36rem,calc(100vw - 2rem));padding:1.25rem;" +
    "border:1px solid var(--consent-border,#d0d0d0);border-radius:var(--consent-radius,.5rem);" +
    "background:var(--consent-bg,#fff);color:var(--consent-fg,#1a1a1a);font:var(--consent-font,inherit);" +
    "box-shadow:0 .5rem 2rem rgba(0,0,0,.2)}" +
    ".collage-consent::backdrop{background:var(--consent-backdrop,rgba(0,0,0,.4))}" +
    ".collage-consent:focus{outline:none}" +
    ".collage-consent h2{margin:0 0 .5rem;font-size:1.15em}" +
    ".collage-consent p{margin:0 0 .75rem}" +
    ".collage-consent a{color:var(--consent-link,var(--consent-accent,#1a56db))}" +
    ".collage-consent fieldset{border:0;margin:0 0 .75rem;padding:0}" +
    ".collage-consent label{display:flex;gap:.5rem;align-items:center;padding:.25rem 0}" +
    ".collage-consent .collage-consent-buttons{display:flex;flex-wrap:wrap;gap:.5rem}" +
    ".collage-consent button{font:inherit;padding:.5rem 1rem;cursor:pointer;" +
    "border:1px solid var(--consent-accent,#1a56db);border-radius:var(--consent-button-radius,.375rem);" +
    "background:var(--consent-button-bg,transparent);color:var(--consent-button-fg,var(--consent-accent,#1a56db))}" +
    ".collage-consent button.collage-consent-primary{background:var(--consent-accent,#1a56db);" +
    "color:var(--consent-accent-fg,#fff)}" +
    ".collage-consent :focus-visible{outline:2px solid var(--consent-focus,var(--consent-accent,#1a56db));outline-offset:2px}" +
    ".collage-consent-placeholder{display:block;max-width:100%;padding:1rem;font:inherit;cursor:pointer;" +
    "border:1px dashed var(--consent-border,#d0d0d0);border-radius:var(--consent-radius,.5rem);" +
    "background:var(--consent-placeholder-bg,#f4f4f4);color:var(--consent-fg,#1a1a1a);text-align:start}" +
    ".collage-consent-placeholder .collage-consent-allow{font-weight:600;color:var(--consent-accent,#1a56db)}";

  function addStyle() {
    var style = document.createElement("style");
    style.setAttribute("data-collage-consent", "");
    style.textContent = css;
    (document.head || document.documentElement).appendChild(style);
  }

  function button(label, onClick, primary) {
    var b = document.createElement("button");
    b.type = "button";
    b.textContent = label || "";
    if (primary) b.className = "collage-consent-primary";
    b.addEventListener("click", onClick);
    return b;
  }

  function all(value) {
    var next = new Set();
    if (value) optional.forEach(function (n) { next.add(n); });
    save(next);
  }

  function build() {
    dialog = document.createElement("dialog");
    dialog.className = "collage-consent";
    dialog.tabIndex = -1;
    dialog.setAttribute("aria-labelledby", "collage-consent-title");
    dialog.setAttribute("aria-describedby", "collage-consent-body");

    var h = document.createElement("h2");
    h.id = "collage-consent-title";
    h.textContent = text.title || "";
    var p = document.createElement("p");
    p.id = "collage-consent-body";
    p.textContent = text.body || "";
    dialog.append(h, p);
    if (cfg.policyURL) {
      var lp = document.createElement("p");
      var a = document.createElement("a");
      a.href = cfg.policyURL;
      a.textContent = cfg.policyURL;
      lp.append(a);
      dialog.append(lp);
    }

    choices = document.createElement("fieldset");
    choices.hidden = true;
    (cfg.categories || []).forEach(function (c) {
      var label = document.createElement("label");
      var box = document.createElement("input");
      box.type = "checkbox";
      box.value = c.name;
      if (c.required) {
        box.checked = true;
        box.disabled = true;
      }
      var span = document.createElement("span");
      span.textContent = labels[c.name] || c.name;
      label.append(box, span);
      choices.append(label);
      boxes[c.name] = box;
    });
    dialog.append(choices);

    var row = document.createElement("div");
    row.className = "collage-consent-buttons";
    settingsBtn = button(text.settings, function () {
      choices.hidden = false;
      saveBtn.hidden = false;
      settingsBtn.hidden = true;
      var first = choices.querySelector("input:not(:disabled)");
      if (first) first.focus();
    });
    saveBtn = button(text.save, function () {
      var next = new Set();
      optional.forEach(function (n) {
        if (boxes[n].checked) next.add(n);
      });
      save(next);
    }, true);
    row.append(
      button(text.accept, function () { all(true); }, true),
      button(text.reject, function () { all(false); }),
      settingsBtn,
      saveBtn
    );
    dialog.append(row);

    // Escape and the native cancel both close without deciding.
    dialog.addEventListener("cancel", function (e) {
      e.preventDefault();
      closeDialog();
    });
    dialog.addEventListener("keydown", function (e) {
      if (e.key === "Escape") {
        e.preventDefault();
        closeDialog();
      } else if (e.key === "Tab") {
        trapTab(e);
      }
    });
    document.body.append(dialog);
  }

  // trapTab moves focus itself, so Tab stays inside the dialog whatever the
  // browser would have done.
  function trapTab(e) {
    var list = Array.prototype.filter.call(
      dialog.querySelectorAll("a[href],button,input,select,textarea,[tabindex]"),
      function (n) {
        return n !== dialog && !n.disabled && n.tabIndex >= 0 && n.getClientRects().length > 0;
      });
    if (!list.length) return;
    e.preventDefault();
    var i = list.indexOf(document.activeElement);
    var next;
    if (e.shiftKey) next = i <= 0 ? list[list.length - 1] : list[i - 1];
    else next = i < 0 || i === list.length - 1 ? list[0] : list[i + 1];
    next.focus();
  }

  function open() {
    if (!dialog) build();
    if (dialog.open) return;
    // Boxes show the current decision. Without one nothing optional is ticked:
    // under GPC that is the signal's own default, and otherwise boxes are never
    // pre-ticked.
    var gpc = !decided && navigator.globalPrivacyControl === true;
    optional.forEach(function (n) {
      boxes[n].checked = gpc ? false : chosen.has(n);
    });
    choices.hidden = true;
    saveBtn.hidden = true;
    settingsBtn.hidden = false;
    returnTo = document.activeElement;
    if (typeof dialog.showModal === "function") dialog.showModal();
    else dialog.setAttribute("open", "");
    dialog.focus();
  }

  function closeDialog() {
    if (!dialog) return;
    if (typeof dialog.close === "function") dialog.close();
    else dialog.removeAttribute("open");
    var to = returnTo;
    returnTo = null;
    if (to && to !== document.body && to.isConnected && typeof to.focus === "function") to.focus();
  }

  // ---- start ------------------------------------------------------------------

  window.collageConsent = {
    get: grantedList,
    set: set,
    open: open
  };

  document.addEventListener("click", function (e) {
    var t = e.target;
    var opener = t && t.closest ? t.closest("[data-consent-open]") : null;
    if (!opener) return;
    e.preventDefault();
    open();
  });

  addStyle();
  gate();
  if (!decided) open();
})();
