package consent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

const (
	layoutHTML = `<!doctype html><html><head><title>t</title>{{hoist "head"}}</head><body>{{slot "content"}}</body></html>`
	outerHTML  = `<main>{{slot "inner"}}</main>`
	innerHTML  = `<p>inner</p>`
)

// tagPattern is the hoisted tag, with the brief's attribute order.
var tagPattern = regexp.MustCompile(`<script defer src="/_collage/consent/consent\.js\?v=([0-9a-f]{12})" data-consent-config="([^"]*)"></script>`)

// siteConfig is valid() with a tr block that lacks Accept, so the tr page must
// fall back to en's.
func siteConfig() Config {
	c := valid()
	en := c.Text["en"]
	en.Body = "We use cookies."
	c.Text["en"] = en
	c.Text["tr"] = Text{Title: "Çerezler", Reject: "Reddet", Categories: map[string]string{"essential": "Zorunlu"}}
	c.PolicyURL = "/privacy"
	return c
}

// site is a real app with en and tr pages, each a layout around a fragment that
// holds another fragment.
func site(t *testing.T, cfg Config) *collage.App {
	t.Helper()
	return siteWith(t, cfg, func(*collage.Config) {})
}

func siteWith(t *testing.T, cfg Config, tweak func(*collage.Config)) *collage.App {
	t.Helper()
	conf := &collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{
			"t/layout.html": {Data: []byte(layoutHTML)},
			"t/outer.html":  {Data: []byte(outerHTML)},
			"t/inner.html":  {Data: []byte(innerHTML)},
		}, Root: "t"},
		Locale:  collage.LocaleConfig{Default: "en", Supported: []string{"en", "tr"}},
		Plugins: []collage.Plugin{NewWith(cfg)},
	}
	tweak(conf)
	a, err := collage.New(conf)
	if err != nil {
		t.Fatal(err)
	}
	layout := collage.NewFragment("layout", "layout.html").WithSlot("content", true, false).Build()
	inner := collage.NewFragment("inner", "inner.html").Build()
	outer := collage.NewFragment("outer", "outer.html").WithSlot("inner", true, false).WithSlotFragment("inner", inner).Build()
	page := collage.NewPage("home").WithLayouts(layout).WithContent(outer).
		WithPath("en", "/").WithPath("tr", "/anasayfa").Static().Build()
	if err := a.RegisterPage(page); err != nil {
		t.Fatal(err)
	}
	return a
}

func fetch(a *collage.App, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

// hoisted returns the page's only consent tag's src version and decoded config.
func hoisted(t *testing.T, body string) (string, scriptConfig) {
	t.Helper()
	m := tagPattern.FindAllStringSubmatch(body, -1)
	if len(m) != 1 {
		t.Fatalf("want exactly one consent tag, found %d in\n%s", len(m), body)
	}
	head := body[strings.Index(body, "<head>"):strings.Index(body, "</head>")]
	if !strings.Contains(head, m[0][0]) {
		t.Fatalf("tag is not in the head:\n%s", body)
	}
	var cfg scriptConfig
	if err := json.Unmarshal([]byte(html.UnescapeString(m[0][2])), &cfg); err != nil {
		t.Fatalf("config attribute: %v", err)
	}
	return m[0][1], cfg
}

func TestHoist_OncePerPage(t *testing.T) {
	w := fetch(site(t, siteConfig()), "/")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if n := strings.Count(w.Body.String(), "/_collage/consent/consent.js"); n != 1 {
		t.Fatalf("script referenced %d times:\n%s", n, w.Body)
	}
	v, cfg := hoisted(t, w.Body.String())
	if v != scriptHash {
		t.Errorf("v = %s, want %s", v, scriptHash)
	}
	if cfg.Version != 1 || cfg.MaxAge != 180*86400 || cfg.PolicyURL != "/privacy" {
		t.Errorf("config = %+v", cfg)
	}
	want := []Category{{Name: "essential", Required: true}, {Name: "analytics"}}
	if len(cfg.Categories) != 2 || cfg.Categories[0] != want[0] || cfg.Categories[1] != want[1] {
		t.Errorf("categories = %+v", cfg.Categories)
	}
}

func TestHoist_LocaleText(t *testing.T) {
	a := site(t, siteConfig())
	_, en := hoisted(t, fetch(a, "/").Body.String())
	if en.Text.Title != "Cookies" || en.Text.Body != "We use cookies." || en.Text.Categories["essential"] != "Essential" {
		t.Errorf("en text = %+v", en.Text)
	}
	w := fetch(a, "/tr/anasayfa")
	if w.Code != http.StatusOK {
		t.Fatalf("tr status %d: %s", w.Code, w.Body)
	}
	_, tr := hoisted(t, w.Body.String())
	if tr.Text.Title != "Çerezler" || tr.Text.Reject != "Reddet" || tr.Text.Categories["essential"] != "Zorunlu" {
		t.Errorf("tr text = %+v", tr.Text)
	}
	if tr.Text.Accept != "Accept" || tr.Text.Body != "We use cookies." || tr.Text.Categories["analytics"] != "Analytics" {
		t.Errorf("tr text did not fall back to en: %+v", tr.Text)
	}
}

func TestHoist_EscapesText(t *testing.T) {
	const hostile = `<img src=x onerror=alert(1)>"'`
	c := siteConfig()
	en := c.Text["en"]
	en.Body = hostile
	c.Text["en"] = en
	body := fetch(site(t, c), "/").Body.String()
	for _, raw := range []string{"<img", "onerror=alert(1)>", `>"'`} {
		if strings.Contains(body, raw) {
			t.Errorf("raw %q in the page:\n%s", raw, body)
		}
	}
	_, cfg := hoisted(t, body)
	if cfg.Text.Body != hostile {
		t.Errorf("decoded body = %q, want %q", cfg.Text.Body, hostile)
	}
}

func TestScript_Served(t *testing.T) {
	a := site(t, siteConfig())
	v, _ := hoisted(t, fetch(a, "/").Body.String())
	w := fetch(a, "/_collage/consent/consent.js?v="+v)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	for k, want := range map[string]string{
		"Content-Type":           "text/javascript; charset=utf-8",
		"X-Content-Type-Options": "nosniff",
		"Cache-Control":          "public, max-age=31536000, immutable",
	} {
		if got := w.Header().Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if !bytes.Equal(w.Body.Bytes(), scriptJS) {
		t.Error("body is not the embedded file")
	}
	// v only busts caches: the file is the same whatever it says.
	if w := fetch(a, "/_collage/consent/consent.js?v=000000000000"); w.Code != http.StatusOK {
		t.Errorf("wrong v: status %d", w.Code)
	}
}

func TestScript_DevModeNoStore(t *testing.T) {
	a := siteWith(t, siteConfig(), func(c *collage.Config) { c.DevMode = true })
	w := fetch(a, "/_collage/consent/consent.js?v="+scriptHash)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("dev Cache-Control = %q, want no-store", got)
	}
}

// The script's mount must leave the rest of /_collage/ to others.
func TestScript_SharesCollagePrefix(t *testing.T) {
	a := site(t, siteConfig())
	if err := a.Mount("/_collage/other/", fstest.MapFS{"x.txt": {Data: []byte("x")}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(); err != nil {
		t.Fatalf("start with a second /_collage/ mount: %v", err)
	}
	if w := fetch(a, "/"); w.Code != http.StatusOK {
		t.Errorf("/ status %d: %s", w.Code, w.Body)
	}
	if w := fetch(a, "/_collage/other/x.txt"); w.Code != http.StatusOK {
		t.Errorf("other mount status %d", w.Code)
	}
}

func TestScript_Hash(t *testing.T) {
	sum := sha256.Sum256(scriptJS)
	if want := hex.EncodeToString(sum[:])[:12]; scriptHash != want {
		t.Errorf("hash %q, want %q", scriptHash, want)
	}
	if !bytes.HasPrefix(scriptJS, []byte(`"use strict";`)) {
		t.Errorf("consent.js does not start in strict mode")
	}
}

func TestExport_WritesScript(t *testing.T) {
	out := t.TempDir()
	b, err := collage.NewBuilder(site(t, siteConfig()), collage.BuildOptions{OutDir: out})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	js, err := os.ReadFile(filepath.Join(out, "_collage", "consent", "consent.js"))
	if err != nil || !bytes.Equal(js, scriptJS) {
		t.Errorf("_collage/consent/consent.js not written as embedded: %v", err)
	}
	page, err := os.ReadFile(filepath.Join(out, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := hoisted(t, string(page)); v != scriptHash {
		t.Errorf("exported v = %s", v)
	}
}

func TestInit_Twice(t *testing.T) {
	p := NewWith(valid())
	if err := build(t, p); err != nil {
		t.Fatal(err)
	}
	err := build(t, p)
	const want = "elagoht/consent: a Plugin value serves one App; create another with New"
	// collage wraps a plugin's Init error with the plugin's name.
	if err == nil || !strings.HasSuffix(err.Error(), ": "+want) {
		t.Errorf("second Init err = %v, want %q", err, want)
	}
}

// styleCSS is the stylesheet consent.js builds: the string literals of its
// `var css = ...` statement, joined.
func styleCSS(t *testing.T) string {
	t.Helper()
	src := string(scriptJS)
	start := strings.Index(src, "var css =")
	end := strings.Index(src, "function addStyle")
	if start < 0 || end < start {
		t.Fatal("consent.js: cannot find the css statement")
	}
	var b strings.Builder
	for _, m := range regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`).FindAllString(src[start:end], -1) {
		s, err := strconv.Unquote(m)
		if err != nil {
			t.Fatalf("consent.js css literal %s: %v", m, err)
		}
		b.WriteString(s)
	}
	return b.String()
}

// The README gives the style-src hash of the banner's stylesheet. A CSS edit
// changes it, and this fails until the README is updated.
func TestREADME_StyleHash(t *testing.T) {
	css := styleCSS(t)
	if !strings.Contains(css, ".collage-consent{") {
		t.Fatalf("extracted css looks wrong: %.80q", css)
	}
	sum := sha256.Sum256([]byte(css))
	want := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(readme), want) {
		t.Errorf("README.md lacks the banner stylesheet's CSP hash %s; update the Content Security Policy section", want)
	}
}
