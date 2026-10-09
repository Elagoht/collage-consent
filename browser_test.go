//go:build unix

package consent

// Browser tests: consent.js in a real headless Chrome, against a real collage app.
// The page drives itself with ?scenario=<name> and POSTs what it observed to a
// test-only action; the Go side asserts on that. Chrome is found at $CHROME (and
// only there, when it is set), else in the usual places; without it these skip.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

// scenarioTimeout bounds one Chrome run, from launch to the result POST.
const scenarioTimeout = 15 * time.Second

// findChrome is $CHROME when it is set (an unusable value means no Chrome, so
// CHROME=/nonexistent skips), else the first browser found in the usual places.
func findChrome() string {
	if c, ok := os.LookupEnv("CHROME"); ok {
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
		return ""
	}
	for _, c := range []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"google-chrome",
		"chromium",
	} {
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	return ""
}

// The head script runs before consent.js (which is deferred): it records what the
// page does, seeds the cookie and fakes GPC, as each scenario's query asks.
const browserLayout = `<!doctype html><html><head><title>t</title>
<script nonce="abc">
(function () {
  var q = new URLSearchParams(location.search);
  window.order = []; window.warns = []; window.errors = []; window.events = []; window.cookieWrites = [];
  window.ran = false; window.bogusRan = false;
  var warn = console.warn;
  console.warn = function () { window.warns.push(Array.prototype.join.call(arguments, " ")); return warn.apply(console, arguments); };
  window.addEventListener("error", function (e) { window.errors.push(String(e.message)); });
  var d = Object.getOwnPropertyDescriptor(Document.prototype, "cookie");
  Object.defineProperty(document, "cookie", {
    configurable: true,
    get: function () { return d.get.call(document); },
    set: function (v) { window.cookieWrites.push(v); d.set.call(document, v); }
  });
  try {
    window.loads = Number(sessionStorage.getItem("loads") || "0") + 1;
    sessionStorage.setItem("loads", String(window.loads));
  } catch (e) { window.loads = -1; }
  window.seedStored = null;
  if (q.get("seed") !== null && window.loads === 1) {
    d.set.call(document, "collage_consent=" + q.get("seed") + "; Path=/");
    var stored = d.get.call(document).split("; ").filter(function (p) { return p.indexOf("collage_consent=") === 0; });
    window.seedStored = stored.length === 1 && stored[0] === "collage_consent=" + q.get("seed");
  }
  if (q.get("nomodal") === "1") delete HTMLDialogElement.prototype.showModal;
  if (q.get("gpc") === "1") {
    Object.defineProperty(Navigator.prototype, "globalPrivacyControl", { configurable: true, get: function () { return true; } });
  }
  document.addEventListener("collage:consent", function (e) { window.events.push(e.detail); });
})();
</script>
{{hoist "head"}}</head><body>{{slot "content"}}</body></html>`

// The page's own markup: the gate, two buttons to hold focus, an opener link, and
// the scenario driver. It runs on DOMContentLoaded, so after consent.js.
const browserContent = `<button id="opener">opener</button>
<button id="opener2">second opener</button>
<a href="#settings" id="reopen" data-consent-open>Cookie settings</a>
<script type="text/plain" data-consent="analytics" id="gs1" data-extra="x" src="/_test/files/a.js"></script>
<script type="text/plain" data-consent="analytics">window.order.push("inline"); window.ran = true;</script>
<script type="text/plain" data-consent="bogus">window.bogusRan = true;</script>
<iframe id="media" data-consent="media" data-src="/_test/files/frame.html" title="media"></iframe>
<span id="extra"></span>
<script nonce="abc">
(function () {
  var q = new URLSearchParams(location.search);
  var sc = q.get("scenario");
  if (sc === "focus") document.getElementById("opener").focus();
  function sleep(ms) { return new Promise(function (r) { setTimeout(r, ms); }); }
  async function until(f, ms) {
    var end = Date.now() + ms;
    while (!f()) { if (Date.now() > end) return false; await sleep(20); }
    return true;
  }
  function dialog() { return document.querySelector("dialog"); }
  function isOpen() { var d = dialog(); return !!(d && d.open); }
  function cookie() {
    var m = document.cookie.split("; ").filter(function (p) { return p.indexOf("collage_consent=") === 0; });
    return m.length ? m[0].slice("collage_consent=".length) : "";
  }
  function button(label) {
    var bs = dialog() ? dialog().querySelectorAll("button") : [];
    for (var i = 0; i < bs.length; i++) if (bs[i].textContent === label) return bs[i];
    throw new Error("no button " + label);
  }
  function box(name) { return dialog().querySelector("input[type=checkbox][value=" + name + "]"); }
  function focusables() {
    return Array.prototype.filter.call(dialog().querySelectorAll("a[href],button,input,select,textarea"), function (e) {
      return !e.disabled && e.getClientRects().length > 0;
    });
  }
  function key(k, shift) {
    var t = document.activeElement || document.body;
    t.dispatchEvent(new KeyboardEvent("keydown", { key: k, shiftKey: !!shift, bubbles: true, cancelable: true }));
  }
  function snap(extra) {
    var cc = window.collageConsent;
    var o = {
      loaded: !!cc, ran: window.ran, bogusRan: window.bogusRan, order: window.order,
      iframeSrc: document.getElementById("media").getAttribute("src") || "",
      dialogOpen: isOpen(), granted: cc ? cc.get() : [], cookie: cookie(),
      cookieWrites: window.cookieWrites, events: window.events, warns: window.warns,
      errors: window.errors, loads: window.loads, seedStored: window.seedStored, checks: {}
    };
    for (var k in extra || {}) o[k] = extra[k];
    return o;
  }
  function post(o) {
    return fetch("/_test/result", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(o) });
  }
  function gated(category, attrs, text) {
    var s = document.createElement("script");
    s.type = "text/plain";
    s.setAttribute("data-consent", category);
    for (var k in attrs) s.setAttribute(k, attrs[k]);
    if (text) s.text = text;
    return s;
  }
  var S = {
    stale: async function () {
      if (window.loads !== 1) return snap({ stash: JSON.parse(sessionStorage.getItem("stash") || "null") });
      collageConsent.set({ analytics: true });
      await until(function () { return window.order.length >= 2; }, 3000);
      if (q.get("how") === "clear") document.cookie = "collage_consent=; Path=/; Max-Age=0";
      else document.cookie = "collage_consent=v=1&c=&t=1; Path=/";
      collageConsent.set({ media: true });
      sessionStorage.setItem("stash", JSON.stringify({ cookie: cookie(), granted: collageConsent.get() }));
      await sleep(3000);
      return snap({ error: "the page did not reload after its running category was withdrawn elsewhere" });
    },
    late_granted: async function () {
      await until(function () { return window.order.length >= 2; }, 3000);
      var wrap = document.createElement("div");
      wrap.append(gated("analytics", { src: "/_test/files/b.js" }), gated("analytics", {}, 'window.order.push("late");'));
      document.body.append(wrap);
      await until(function () { return window.order.length >= 4; }, 3000);
      await sleep(200);
      return snap();
    },
    late_iframe: async function () {
      var f = document.createElement("iframe");
      f.id = "late"; f.setAttribute("data-consent", "media"); f.setAttribute("data-src", "/_test/files/frame.html");
      document.body.append(f);
      await sleep(300);
      var p = f.previousElementSibling;
      return snap({ checks: {
        placeholder: !!p && p.classList.contains("collage-consent-placeholder"),
        noSrc: !f.hasAttribute("src")
      } });
    },
    late_withdrawn: async function () {
      await until(function () { return window.order.length >= 2; }, 3000);
      document.cookie = "collage_consent=v=1&c=&t=1; Path=/";
      document.body.append(gated("analytics", {}, 'window.order.push("late");'));
      await sleep(500);
      return snap();
    },
    after_set: async function () {
      collageConsent.set({ analytics: true });
      await until(function () { return window.order.length >= Number(q.get("want")); }, 3000);
      await sleep(500);
      return snap();
    },
    gate_inert_before_grant: async function () {
      await sleep(300);
      return snap({ checks: {
        placeholder: document.querySelectorAll(".collage-consent-placeholder").length === 1,
        stillPlain: document.querySelectorAll("script[type='text/plain'][data-consent]").length === 3
      } });
    },
    grant_runs_in_order: async function () {
      collageConsent.set({ analytics: true });
      collageConsent.set({ analytics: true });
      await until(function () { return window.order.length >= 2; }, 3000);
      await sleep(300);
      var s = document.getElementById("gs1");
      return snap({ checks: {
        onlyBogusLeft: document.querySelectorAll("script[type='text/plain'][data-consent]").length === 1,
        attrsCopied: !!s && s.type === "" && !s.hasAttribute("data-consent") && s.getAttribute("data-extra") === "x" && s.async === false
      } });
    },
    cookie_format: async function () {
      var c = {}; var cookies = [];
      c.openOnLoad = isOpen();
      button("Reject all").click(); cookies.push(cookie());
      c.closedAfterReject = !isOpen();
      collageConsent.open(); button("Choose").click(); box("media").click(); button("Save choices").click(); cookies.push(cookie());
      c.closedAfterSave = !isOpen();
      collageConsent.open(); button("Accept all").click(); cookies.push(cookie());
      c.closedAfterAccept = !isOpen();
      collageConsent.set({ analytics: true }); cookies.push(cookie());
      return snap({ cookies: cookies, checks: c });
    },
    version_bump: async function () { await sleep(300); return snap(); },
    policy_label: async function () {
      var a = dialog().querySelector("a[href]");
      return snap({ checks: {
        label: !!a && a.textContent === q.get("want"),
        href: !!a && a.getAttribute("href") === "/privacy"
      } });
    },
    cookie_honoured: async function () {
      await until(function () { return window.order.length >= 2; }, 3000);
      return snap();
    },
    malformed_cookie: async function () { await sleep(300); return snap(); },
    placeholder_grants_one: async function () {
      var b = document.querySelector(".collage-consent-placeholder");
      var c = {
        isButton: !!b && b.tagName === "BUTTON",
        hostText: !!b && b.textContent.indexOf("This content loads from " + location.host + ".") >= 0,
        categoryText: !!b && b.textContent.indexOf("Allow Embedded media") >= 0
      };
      b.click();
      await sleep(300);
      c.placeholderGone = document.querySelectorAll(".collage-consent-placeholder").length === 0;
      return snap({ checks: c });
    },
    withdraw_reloads: async function () {
      if (window.loads !== 1) return snap();
      collageConsent.set({ analytics: true });
      await until(function () { return window.order.length >= 2; }, 3000);
      collageConsent.set({ analytics: false });
      await sleep(3000);
      return snap({ error: "the page did not reload after a withdrawal" });
    },
    gpc_defaults: async function () {
      var c = { gpc: (navigator.globalPrivacyControl === true) === (q.get("gpc") === "1"), openOnLoad: isOpen() };
      var before = collageConsent.get();
      button("Choose").click();
      c.analyticsOff = !box("analytics").checked;
      c.mediaOff = !box("media").checked;
      c.essentialOnAndFixed = box("essential").checked && box("essential").disabled;
      button("Accept all").click();
      return snap({ before: before, checks: c });
    },
    text_is_text: async function () {
      await sleep(200);
      var d = dialog();
      var p = document.querySelector(".collage-consent-placeholder");
      return snap({ checks: {
        noImg: document.querySelectorAll("img").length === 0,
        noB: d.querySelector("b") === null,
        bodyLiteral: d.textContent.indexOf("<img src=x onerror=window.pwned=1>") >= 0,
        titleLiteral: d.textContent.indexOf("<b>Cookies</b>") >= 0,
        placeholderLiteral: !!p && p.textContent.indexOf("<img src=y> " + location.host) >= 0,
        notPwned: !window.pwned
      } });
    },
    focus: async function () {
      var c = {}; var opener = document.getElementById("opener");
      c.openOnLoad = isOpen();
      c.modalAsAsked = (typeof dialog().showModal === "function") === (q.get("nomodal") !== "1");
      c.focusInside = isOpen() && dialog().contains(document.activeElement);
      var f = focusables(); var first = f[0]; var last = f[f.length - 1];
      c.severalFocusables = f.length >= 3;
      first.focus(); key("Tab"); c.tabMoves = document.activeElement === f[1];
      last.focus(); key("Tab"); c.tabWraps = document.activeElement === first;
      first.focus(); key("Tab", true); c.shiftTabWraps = document.activeElement === last;
      key("Escape");
      c.escapeCloses = !isOpen();
      c.focusReturned = document.activeElement === opener;
      var o2 = document.getElementById("opener2");
      o2.focus(); collageConsent.open();
      c.reopened = isOpen() && dialog().contains(document.activeElement);
      dialog().dispatchEvent(new Event("cancel", { cancelable: true }));
      c.cancelCloses = !isOpen();
      c.focusReturned2 = document.activeElement === o2;
      return snap({ checks: c });
    },
    open_link: async function () {
      var c = { closedOnLoad: !isOpen() };
      document.getElementById("reopen").click();
      c.linkOpens = isOpen();
      c.noNavigation = location.hash === "";
      key("Escape");
      c.closedAgain = !isOpen();
      var a = document.createElement("a"); a.href = "#late"; a.setAttribute("data-consent-open", "");
      var span = document.createElement("span"); span.textContent = "late"; a.appendChild(span);
      document.body.appendChild(a);
      span.click();
      c.lateLinkOpens = isOpen();
      return snap({ checks: c });
    }
  };
  document.addEventListener("DOMContentLoaded", async function () {
    try {
      if (!window.collageConsent) { await post(snap({ error: "window.collageConsent is not defined" })); return; }
      if (!S[sc]) { await post(snap({ error: "unknown scenario " + sc })); return; }
      var r = await S[sc]();
      await post(r);
    } catch (e) {
      await post(snap({ error: String((e && e.stack) || e) }));
    }
  });
})();
</script>`

// observed is what the page reports.
type observed struct {
	Error        string          `json:"error"`
	Loaded       bool            `json:"loaded"`
	Ran          bool            `json:"ran"`
	BogusRan     bool            `json:"bogusRan"`
	Order        []string        `json:"order"`
	IframeSrc    string          `json:"iframeSrc"`
	DialogOpen   bool            `json:"dialogOpen"`
	Granted      []string        `json:"granted"`
	Cookie       string          `json:"cookie"`
	CookieWrites []string        `json:"cookieWrites"`
	Events       [][]string      `json:"events"`
	Warns        []string        `json:"warns"`
	Errors       []string        `json:"errors"`
	Loads        int             `json:"loads"`
	SeedStored   *bool           `json:"seedStored"`
	Checks       map[string]bool `json:"checks"`
	Cookies      []string        `json:"cookies"`
	Before       []string        `json:"before"`
	Stash        *struct {
		Cookie  string   `json:"cookie"`
		Granted []string `json:"granted"`
	} `json:"stash"`
}

// serveBrowserSite starts a real app with cfg on a free loopback port and returns
// its base URL and the channel the page's result arrives on.
func serveBrowserSite(t *testing.T, cfg Config, extra, csp string) (string, <-chan []byte) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	a, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "127.0.0.1", Port: port},
		Template: collage.TemplateConfig{FS: fstest.MapFS{
			"t/layout.html":  {Data: []byte(browserLayout)},
			"t/content.html": {Data: []byte(strings.Replace(browserContent, `<span id="extra"></span>`, extra, 1))},
		}, Root: "t"},
		Locale:  collage.LocaleConfig{Default: "en", Supported: []string{"en"}},
		Plugins: []collage.Plugin{NewWith(cfg)},
	})
	if err != nil {
		t.Fatal(err)
	}
	layout := collage.NewFragment("layout", "layout.html").WithSlot("content", true, false).Build()
	content := collage.NewFragment("content", "content.html").Build()
	if err := a.RegisterPage(collage.NewPage("home").WithLayouts(layout).WithContent(content).WithPath("en", "/").Build()); err != nil {
		t.Fatal(err)
	}
	if err := a.Mount("/_test/files/", fstest.MapFS{
		"a.js":       {Data: []byte(`window.order.push("src");`)},
		"b.js":       {Data: []byte(`window.order.push("b");`)},
		"frame.html": {Data: []byte(`<!doctype html><title>frame</title><p>frame</p>`)},
	}); err != nil {
		t.Fatal(err)
	}
	results := make(chan []byte, 4)
	result := collage.NewAction("test-result").WithPath("en", "/_test/result").WithMethods(http.MethodPost).WithoutCSRF().
		WithHandler(func(_ context.Context, rc *collage.RenderContext) (*collage.ActionResult, error) {
			b, err := io.ReadAll(io.LimitReader(rc.Request.Body, 1<<20))
			if err != nil {
				return nil, err
			}
			select {
			case results <- b:
			default:
			}
			return collage.NoContent(http.StatusNoContent), nil
		}).Build()
	if err := a.RegisterAction(result); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	if csp != "" {
		inner := h
		h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Security-Policy", csp)
			inner.ServeHTTP(w, r)
		})
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return "http://127.0.0.1:" + strconv.Itoa(port), results
}

// runChrome opens pageURL in a fresh headless Chrome profile and returns the first
// result the page POSTs. Chrome is killed, with its whole process group, before
// the profile directory is removed.
func runChrome(t *testing.T, chrome, pageURL string, results <-chan []byte) []byte {
	t.Helper()
	profile := t.TempDir()
	var output bytes.Buffer
	cmd := exec.Command(chrome,
		"--headless=new", "--disable-gpu", "--no-first-run", "--no-default-browser-check",
		"--disable-extensions", "--disable-background-networking", "--disable-sync",
		"--user-data-dir="+profile, pageURL)
	cmd.Stdout, cmd.Stderr = &output, &output
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start chrome: %v", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	var waitErr error
	done := false
	stop := func() {
		if done {
			return
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		waitErr = <-exited
		done = true
	}
	defer stop()

	timer := time.NewTimer(scenarioTimeout)
	defer timer.Stop()
	select {
	case b := <-results:
		return b
	case waitErr = <-exited:
		done = true
		t.Fatalf("chrome exited before the page reported: %v\n%s", waitErr, output.String())
	case <-timer.C:
		stop()
		t.Fatalf("no result within %s (chrome: %v)\n%s", scenarioTimeout, waitErr, output.String())
	}
	return nil
}

type browserScenario struct {
	name   string // the subtest's name
	page   string // ?scenario=; name when empty
	cfg    func(*Config)
	seed   string // a raw collage_consent value set before consent.js runs
	gpc    bool
	params url.Values // more query parameters for the page
	extra  string     // markup added after the shared gate markup
	csp    string     // a Content-Security-Policy header for every response
	checks []string   // names the page must report, each true
	check  func(*testing.T, observed)
}

// cookieWrite is one document.cookie assignment consent.js makes, attributes and all.
var cookieWrite = regexp.MustCompile(`^collage_consent=v=([1-9][0-9]*)&c=((?:[a-z0-9-]+(?:,[a-z0-9-]+)*)?)&t=([0-9]+); Path=/; SameSite=Lax; Max-Age=([0-9]+)$`)

// cookieValue is the stored value, as document.cookie reads it back.
var cookieValue = regexp.MustCompile(`^v=([1-9][0-9]*)&c=((?:[a-z0-9-]+(?:,[a-z0-9-]+)*)?)&t=([0-9]+)$`)

func wantGranted(t *testing.T, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("granted = %q, want %q", got, want)
	}
}

func wantInert(t *testing.T, o observed) {
	t.Helper()
	if o.Ran || len(o.Order) != 0 {
		t.Errorf("a gated script ran: ran=%v order=%q", o.Ran, o.Order)
	}
	if o.IframeSrc != "" {
		t.Errorf("gated iframe has src %q", o.IframeSrc)
	}
}

func browserScenarios() []browserScenario {
	malformed := func(name, seed string, version int, dialog bool, granted ...string) browserScenario {
		return browserScenario{
			name: "malformed_cookie/" + name, page: "malformed_cookie", seed: seed,
			cfg: func(c *Config) { c.Version = version },
			check: func(t *testing.T, o observed) {
				if o.DialogOpen != dialog {
					t.Errorf("dialog open = %v, want %v", o.DialogOpen, dialog)
				}
				wantGranted(t, o.Granted, granted...)
				if !slices.Contains(granted, "analytics") && (o.Ran || len(o.Order) != 0) {
					t.Errorf("analytics ran without its grant: ran=%v order=%q", o.Ran, o.Order)
				}
				if !slices.Contains(granted, "media") && o.IframeSrc != "" {
					t.Errorf("media iframe loaded without its grant: %q", o.IframeSrc)
				}
				if len(o.CookieWrites) != 0 {
					t.Errorf("cookie written without a decision: %q", o.CookieWrites)
				}
			},
		}
	}
	// The cookie cases are TestParseCookie_Table's, so the JS and Go parsers
	// answer the same list.
	var malformedCookies []browserScenario
	for _, c := range cookieCases() {
		if c.goOnly {
			continue
		}
		granted := append([]string{"essential"}, c.granted...)
		slices.Sort(granted)
		malformedCookies = append(malformedCookies, malformed(c.name, c.seed, c.version, !c.decided, granted...))
	}
	return append(malformedCookies, []browserScenario{
		{
			name:   "gate_inert_before_grant",
			checks: []string{"placeholder", "stillPlain"},
			check: func(t *testing.T, o observed) {
				wantInert(t, o)
				if !o.DialogOpen {
					t.Error("no dialog without a decision")
				}
				wantGranted(t, o.Granted, "essential")
				if o.Cookie != "" || len(o.CookieWrites) != 0 || len(o.Events) != 0 {
					t.Errorf("decided without the visitor: cookie=%q writes=%q events=%q", o.Cookie, o.CookieWrites, o.Events)
				}
			},
		},
		{
			name:   "grant_runs_in_order",
			checks: []string{"onlyBogusLeft", "attrsCopied"},
			check: func(t *testing.T, o observed) {
				if !slices.Equal(o.Order, []string{"src", "inline"}) {
					t.Errorf("order = %q, want [src inline] exactly once", o.Order)
				}
				if !o.Ran {
					t.Error("inline script did not run")
				}
				if o.IframeSrc != "" {
					t.Errorf("media iframe loaded with only analytics granted: %q", o.IframeSrc)
				}
				wantGranted(t, o.Granted, "analytics", "essential")
				if o.DialogOpen {
					t.Error("dialog still open after set")
				}
				if len(o.Events) != 2 || !slices.Equal(o.Events[0], []string{"analytics", "essential"}) {
					t.Errorf("events = %q, want two of [analytics essential]", o.Events)
				}
			},
		},
		{
			name:   "cookie_format",
			checks: []string{"openOnLoad", "closedAfterReject", "closedAfterSave", "closedAfterAccept"},
			check: func(t *testing.T, o observed) {
				wantC := []string{"", "media", "analytics,media", "analytics,media"}
				if len(o.Cookies) != len(wantC) {
					t.Fatalf("cookies = %q", o.Cookies)
				}
				for i, c := range o.Cookies {
					m := cookieValue.FindStringSubmatch(c)
					if m == nil {
						t.Errorf("cookie %d = %q, not the spec format", i, c)
						continue
					}
					if m[1] != "1" || m[2] != wantC[i] {
						t.Errorf("cookie %d = %q, want v=1 c=%q", i, c, wantC[i])
					}
					if ts, _ := strconv.ParseInt(m[3], 10, 64); time.Since(time.Unix(ts, 0)).Abs() > time.Hour {
						t.Errorf("cookie %d t=%s is not now", i, m[3])
					}
				}
				if len(o.CookieWrites) != 4 {
					t.Errorf("cookie writes = %q, want 4", o.CookieWrites)
				}
				for _, w := range o.CookieWrites {
					m := cookieWrite.FindStringSubmatch(w)
					if m == nil {
						t.Errorf("cookie write %q lacks the spec's attributes", w)
					} else if m[4] != strconv.Itoa(180*86400) {
						t.Errorf("Max-Age = %s, want %d", m[4], 180*86400)
					}
				}
				wantGranted(t, o.Granted, "analytics", "essential", "media")
				if len(o.Events) != 4 || !slices.Equal(o.Events[0], []string{"essential"}) {
					t.Errorf("events = %q", o.Events)
				}
			},
		},
		{
			name: "version_bump",
			cfg:  func(c *Config) { c.Version = 2 },
			seed: "v=1&c=analytics&t=1",
			check: func(t *testing.T, o observed) {
				if !o.DialogOpen {
					t.Error("an older version did not reopen the dialog")
				}
				wantGranted(t, o.Granted, "essential")
				wantInert(t, o)
			},
		},
		{
			name: "cookie_honoured",
			cfg:  func(c *Config) { c.Version = 2 },
			seed: "v=2&c=analytics&t=1",
			check: func(t *testing.T, o observed) {
				if o.DialogOpen {
					t.Error("dialog open with a current decision")
				}
				wantGranted(t, o.Granted, "analytics", "essential")
				if !slices.Equal(o.Order, []string{"src", "inline"}) {
					t.Errorf("order = %q", o.Order)
				}
				if o.IframeSrc != "" || len(o.CookieWrites) != 0 {
					t.Errorf("iframe src %q, writes %q", o.IframeSrc, o.CookieWrites)
				}
			},
		},
		{
			name:   "placeholder_grants_one",
			checks: []string{"isButton", "hostText", "categoryText", "placeholderGone"},
			check: func(t *testing.T, o observed) {
				wantGranted(t, o.Granted, "essential", "media")
				if o.IframeSrc != "/_test/files/frame.html" {
					t.Errorf("iframe src = %q", o.IframeSrc)
				}
				if o.Ran || len(o.Order) != 0 {
					t.Errorf("analytics ran: %q", o.Order)
				}
				if m := cookieValue.FindStringSubmatch(o.Cookie); m == nil || m[2] != "media" {
					t.Errorf("cookie = %q", o.Cookie)
				}
			},
		},
		{
			name: "withdraw_reloads",
			check: func(t *testing.T, o observed) {
				if o.Loads != 2 {
					t.Errorf("loads = %d, want 2", o.Loads)
				}
				wantGranted(t, o.Granted, "essential")
				wantInert(t, o)
				if m := cookieValue.FindStringSubmatch(o.Cookie); m == nil || m[2] != "" {
					t.Errorf("cookie after withdrawal = %q", o.Cookie)
				}
				if o.DialogOpen {
					t.Error("dialog open after a decision")
				}
			},
		},
		{
			name: "stale_tab_withdrawn", page: "stale", params: url.Values{"how": {"withdraw"}},
			check: func(t *testing.T, o observed) {
				if o.Stash == nil {
					t.Fatal("first load stashed nothing")
				}
				if m := cookieValue.FindStringSubmatch(o.Stash.Cookie); m == nil || m[2] != "media" {
					t.Errorf("cookie after set({media:true}) in a stale tab = %q, want c=media", o.Stash.Cookie)
				}
				wantGranted(t, o.Stash.Granted, "essential", "media")
				if o.Loads != 2 {
					t.Errorf("loads = %d: the tab that ran analytics did not reload", o.Loads)
				}
				wantGranted(t, o.Granted, "essential", "media")
				if o.Ran || len(o.Order) != 0 {
					t.Errorf("analytics ran after reload: %q", o.Order)
				}
			},
		},
		{
			name: "stale_tab_cleared", page: "stale", params: url.Values{"how": {"clear"}},
			check: func(t *testing.T, o observed) {
				if o.Stash == nil {
					t.Fatal("first load stashed nothing")
				}
				if m := cookieValue.FindStringSubmatch(o.Stash.Cookie); m == nil || m[2] != "media" {
					t.Errorf("cookie after set({media:true}) in a stale tab = %q, want c=media", o.Stash.Cookie)
				}
				wantGranted(t, o.Stash.Granted, "essential", "media")
				if o.Loads != 2 {
					t.Errorf("loads = %d: the tab that ran analytics did not reload", o.Loads)
				}
				wantGranted(t, o.Granted, "essential", "media")
				if o.Ran || len(o.Order) != 0 {
					t.Errorf("analytics ran after reload: %q", o.Order)
				}
			},
		},
		{
			name: "late_granted", seed: "v=1&c=analytics&t=1",
			check: func(t *testing.T, o observed) {
				if !slices.Equal(o.Order, []string{"src", "inline", "b", "late"}) {
					t.Errorf("order = %q, want [src inline b late]", o.Order)
				}
			},
		},
		{
			name: "late_iframe", seed: "v=1&c=&t=1",
			checks: []string{"placeholder", "noSrc"},
		},
		{
			name: "late_withdrawn", seed: "v=1&c=analytics&t=1",
			check: func(t *testing.T, o observed) {
				if !slices.Equal(o.Order, []string{"src", "inline"}) {
					t.Errorf("order = %q: a late node ran in a category withdrawn in the cookie", o.Order)
				}
			},
		},
		{
			name: "csp_nonce", page: "after_set", params: url.Values{"want": {"2"}},
			csp:   "script-src 'self' 'nonce-abc'",
			extra: `<script type="text/plain" data-consent="analytics" nonce="abc">window.order.push("nonce");</script>`,
			check: func(t *testing.T, o observed) {
				if !slices.Equal(o.Order, []string{"src", "nonce"}) {
					t.Errorf("order = %q, want [src nonce]", o.Order)
				}
				if o.Ran {
					t.Error("a gated inline script without the nonce ran under the CSP")
				}
			},
		},
		{
			name: "nomodule", page: "after_set", params: url.Values{"want": {"3"}},
			extra: `<script type="text/plain" data-consent="analytics" src="/_test/files/b.js" nomodule></script>
<script type="text/plain" data-consent="analytics">window.order.push("after-nomodule");</script>`,
			check: func(t *testing.T, o observed) {
				if !slices.Equal(o.Order, []string{"src", "inline", "after-nomodule"}) {
					t.Errorf("order = %q: a nomodule script stalled the queue or ran", o.Order)
				}
			},
		},
		{
			name: "gpc_defaults/no_gpc", page: "gpc_defaults",
			checks: []string{"gpc", "openOnLoad", "analyticsOff", "mediaOff", "essentialOnAndFixed"},
			check: func(t *testing.T, o observed) {
				if !slices.Equal(o.Before, []string{"essential"}) {
					t.Errorf("granted before deciding = %q", o.Before)
				}
			},
		},
		{
			name:   "gpc_defaults",
			gpc:    true,
			checks: []string{"gpc", "openOnLoad", "analyticsOff", "mediaOff", "essentialOnAndFixed"},
			check: func(t *testing.T, o observed) {
				if !slices.Equal(o.Before, []string{"essential"}) {
					t.Errorf("granted before deciding = %q", o.Before)
				}
				wantGranted(t, o.Granted, "analytics", "essential", "media")
			},
		},
		{
			name: "text_is_text",
			cfg: func(c *Config) {
				en := c.Text["en"]
				en.Title = "<b>Cookies</b>"
				en.Body = "<img src=x onerror=window.pwned=1>"
				en.Placeholder = "<img src=y> {host}"
				c.Text["en"] = en
			},
			checks: []string{"noImg", "noB", "bodyLiteral", "titleLiteral", "placeholderLiteral", "notPwned"},
		},
		{
			name: "focus",
			checks: []string{"openOnLoad", "modalAsAsked", "focusInside", "severalFocusables", "tabMoves", "tabWraps", "shiftTabWraps",
				"escapeCloses", "focusReturned", "reopened", "cancelCloses", "focusReturned2"},
			check: func(t *testing.T, o observed) {
				if o.Cookie != "" || len(o.CookieWrites) != 0 || len(o.Events) != 0 {
					t.Errorf("closing decided: cookie=%q writes=%q events=%q", o.Cookie, o.CookieWrites, o.Events)
				}
				wantGranted(t, o.Granted, "essential")
			},
		},
		{
			name: "focus/nomodal", page: "focus", params: url.Values{"nomodal": {"1"}},
			checks: []string{"openOnLoad", "modalAsAsked", "focusInside", "severalFocusables", "tabMoves", "tabWraps", "shiftTabWraps",
				"escapeCloses", "focusReturned", "reopened", "cancelCloses", "focusReturned2"},
			check: func(t *testing.T, o observed) {
				if o.Cookie != "" || len(o.CookieWrites) != 0 || len(o.Events) != 0 {
					t.Errorf("closing decided: cookie=%q writes=%q events=%q", o.Cookie, o.CookieWrites, o.Events)
				}
				wantGranted(t, o.Granted, "essential")
			},
		},
		{
			name: "policy_label", params: url.Values{"want": {"Privacy policy"}},
			cfg: func(c *Config) {
				en := c.Text["en"]
				en.Policy = "Privacy policy"
				c.Text["en"] = en
			},
			checks: []string{"label", "href"},
		},
		{
			name: "policy_label/url", page: "policy_label", params: url.Values{"want": {"/privacy"}},
			checks: []string{"label", "href"},
		},
		{
			name:   "open_link",
			seed:   "v=1&c=&t=1",
			checks: []string{"closedOnLoad", "linkOpens", "noNavigation", "closedAgain", "lateLinkOpens"},
		},
	}...)
}

// browserChrome is the Chrome to run, or why the browser tests skip. On CI
// (CI=true) they run only when CHROME is set explicitly.
func browserChrome() (path, skipReason string) {
	if _, set := os.LookupEnv("CHROME"); !set && os.Getenv("CI") == "true" {
		return "", "CI: set CHROME to run the browser tests"
	}
	if c := findChrome(); c != "" {
		return c, ""
	}
	return "", "no Chrome: set CHROME or install Google Chrome to run the browser tests"
}

func TestBrowserChrome_CISkips(t *testing.T) {
	t.Setenv("CI", "true")
	t.Setenv("CHROME", "")
	if err := os.Unsetenv("CHROME"); err != nil {
		t.Fatal(err)
	}
	if p, why := browserChrome(); p != "" || why != "CI: set CHROME to run the browser tests" {
		t.Errorf("CI without CHROME: path %q, reason %q", p, why)
	}
	t.Setenv("CHROME", "/nonexistent")
	if p, why := browserChrome(); p != "" || !strings.HasPrefix(why, "no Chrome") {
		t.Errorf("CI with CHROME=/nonexistent: path %q, reason %q", p, why)
	}
}

func TestBrowser(t *testing.T) {
	chrome, why := browserChrome()
	if chrome == "" {
		t.Skip(why)
	}
	for _, sc := range browserScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			cfg := browserConfig()
			if sc.cfg != nil {
				sc.cfg(&cfg)
			}
			base, results := serveBrowserSite(t, cfg, sc.extra, sc.csp)
			page := sc.page
			if page == "" {
				page = sc.name
			}
			q := url.Values{"scenario": {page}}
			if sc.seed != "" {
				q.Set("seed", sc.seed)
			}
			if sc.gpc {
				q.Set("gpc", "1")
			}
			for k, v := range sc.params {
				q[k] = v
			}
			raw := runChrome(t, chrome, base+"/?"+q.Encode(), results)
			var o observed
			if err := json.Unmarshal(raw, &o); err != nil {
				t.Fatalf("result %s: %v", raw, err)
			}
			if o.Error != "" {
				t.Fatalf("page error: %s\nobserved: %s", o.Error, raw)
			}
			if !o.Loaded {
				t.Fatalf("consent.js did not load: %s", raw)
			}
			if len(o.Errors) != 0 {
				t.Errorf("page errors: %q", o.Errors)
			}
			// The seed must be what the page's cookie holds, so a scenario tests the
			// value it names and not one the browser rewrote or refused.
			if sc.seed != "" && o.Loads == 1 && (o.SeedStored == nil || !*o.SeedStored) {
				t.Errorf("the seeded cookie is not stored as %q", sc.seed)
			}
			if o.BogusRan {
				t.Error("a script gated on an unknown category ran")
			}
			if n := countContaining(o.Warns, `"bogus"`); n != 1 {
				t.Errorf("warnings naming the unknown category: %d, want 1 (%q)", n, o.Warns)
			}
			for _, name := range sc.checks {
				if v, ok := o.Checks[name]; !ok || !v {
					t.Errorf("check %s = %v (reported: %v)", name, v, ok)
				}
			}
			for name, v := range o.Checks {
				if !v && !slices.Contains(sc.checks, name) {
					t.Errorf("unlisted check %s is false", name)
				}
			}
			if sc.check != nil {
				sc.check(t, o)
			}
			if t.Failed() {
				t.Logf("observed: %s", raw)
			}
		})
	}
}

func countContaining(list []string, sub string) int {
	n := 0
	for _, s := range list {
		if strings.Contains(s, sub) {
			n++
		}
	}
	return n
}

// CHROME pointing nowhere means no Chrome, even where one is installed.
func TestFindChrome_EnvWins(t *testing.T) {
	t.Setenv("CHROME", "/nonexistent")
	if c := findChrome(); c != "" {
		t.Fatalf("CHROME=/nonexistent found %q", c)
	}
}
