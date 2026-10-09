package consent

// These tests share the package-level state Init sets (one consent plugin per
// process), so none of them, nor any test that builds an app, runs in parallel.

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

func TestParseCookie_Table(t *testing.T) {
	cfg := browserLikeConfig()
	for _, tc := range cookieCases() {
		t.Run(tc.name, func(t *testing.T) {
			c := cfg
			c.Version = tc.version
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.Header.Set("Cookie", "collage_consent="+tc.seed)
			granted, ok := readCookie(r, c)
			if ok != tc.decided {
				t.Errorf("decided = %v, want %v", ok, tc.decided)
			}
			want := tc.granted
			if want == nil {
				want = []string{}
			}
			if granted == nil {
				granted = []string{}
			}
			if !slices.Equal(granted, want) {
				t.Errorf("granted = %q, want %q", granted, want)
			}
			if !ok && len(granted) != 0 {
				t.Errorf("no decision, yet granted %q", granted)
			}
		})
	}
}

// Several collage_consent cookies: the first whose value passes Go's own value
// check is the one read, as consent.js does.
func TestReadCookie_FirstValid(t *testing.T) {
	cfg := browserLikeConfig()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Cookie", `collage_consent=v=1&c=media\x&t=1; collage_consent=v=1&c=analytics&t=1`)
	if g, ok := readCookie(r, cfg); !ok || !slices.Equal(g, []string{"analytics"}) {
		t.Errorf("granted %q, %v", g, ok)
	}
	r = httptest.NewRequest(http.MethodGet, "/", nil)
	if g, ok := readCookie(r, cfg); ok || len(g) != 0 {
		t.Errorf("no cookie: granted %q, %v", g, ok)
	}
}

func TestUnderPaths(t *testing.T) {
	tests := []struct {
		path     string
		prefixes []string
		want     bool
	}{
		{"/shop", []string{"/shop"}, true},
		{"/shop/", []string{"/shop"}, true},
		{"/shop/cart", []string{"/shop"}, true},
		{"/shopping", []string{"/shop"}, false},
		{"/sho", []string{"/shop"}, false},
		{"/", []string{"/shop"}, false},
		{"/shop/cart", []string{"/shop/"}, true},
		{"/shop", []string{"/shop/"}, true},
		{"/shopping", []string{"/shop/"}, false},
		{"/anything/at/all", []string{"/"}, true},
		{"/", []string{"/"}, true},
		{"/account", []string{"/shop", "/account"}, true},
		{"/tr/shop", []string{"/shop"}, false},
		{"/shop", nil, false},
	}
	for _, tc := range tests {
		if got := underPaths(tc.path, tc.prefixes); got != tc.want {
			t.Errorf("underPaths(%q, %q) = %v, want %v", tc.path, tc.prefixes, got, tc.want)
		}
	}
}

// browserLikeConfig has the browser tests' categories: essential (required),
// analytics and media.
func browserLikeConfig() Config {
	c := browserConfig()
	return c.withDefaults()
}

// logRecorder is a slog.Handler keeping every record at Warn or above.
type logRecorder struct {
	mu   sync.Mutex
	recs []string
}

func (l *logRecorder) Enabled(_ context.Context, lv slog.Level) bool { return lv >= slog.LevelWarn }
func (l *logRecorder) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Level.String() + " " + r.Message)
	r.Attrs(func(a slog.Attr) bool {
		b.WriteString(" " + a.String())
		return true
	})
	l.mu.Lock()
	l.recs = append(l.recs, b.String())
	l.mu.Unlock()
	return nil
}
func (l *logRecorder) WithAttrs([]slog.Attr) slog.Handler { return l }
func (l *logRecorder) WithGroup(string) slog.Handler      { return l }

func (l *logRecorder) warns(sub string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, r := range l.recs {
		if strings.Contains(r, sub) {
			out = append(out, r)
		}
	}
	return out
}

// grantedView is what the test fragment renders.
type grantedView struct {
	Analytics, Media, Essential, Bogus bool
}

const grantedHTML = `<p>analytics={{.Analytics}} media={{.Media}} essential={{.Essential}} bogus={{.Bogus}}</p>`

// serverSite is an app with the browser categories, serverPaths ["/shop"], the
// page cache on, and these pages, each rendering Granted for every category:
//   - /shop: Incremental(time.Hour), under serverPaths;
//   - /open: dynamic, outside them; its handler asks twice per render.
//
// renders counts /shop's data handler runs.
type serverSite struct {
	app     *collage.App
	logs    *logRecorder
	renders atomic.Int32
}

func newServerSite(t *testing.T, static bool) *serverSite {
	t.Helper()
	s := &serverSite{logs: &logRecorder{}}
	cfg := browserLikeConfig()
	cfg.ServerPaths = []string{"/shop"}
	a, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{
			"t/layout.html":  {Data: []byte(layoutHTML)},
			"t/granted.html": {Data: []byte(grantedHTML)},
		}, Root: "t"},
		Locale:  collage.LocaleConfig{Default: "en", Supported: []string{"en"}},
		Cache:   collage.CacheConfig{Enabled: true},
		Logger:  slog.New(s.logs),
		Plugins: []collage.Plugin{NewWith(cfg)},
	})
	if err != nil {
		t.Fatal(err)
	}
	view := func(rc *collage.RenderContext) grantedView {
		return grantedView{
			Analytics: Granted(rc, "analytics"), Media: Granted(rc, "media"),
			Essential: Granted(rc, "essential"), Bogus: Granted(rc, "bogus"),
		}
	}
	layout := collage.NewFragment("layout", "layout.html").WithSlot("content", true, false).Build()
	shop := collage.NewFragment("shop", "granted.html").WithData(collage.Load(func(_ context.Context, rc *collage.RenderContext) (grantedView, error) {
		s.renders.Add(1)
		return view(rc), nil
	})).Build()
	open := collage.NewFragment("open", "granted.html").WithData(collage.Load(func(_ context.Context, rc *collage.RenderContext) (grantedView, error) {
		view(rc)
		return view(rc), nil
	})).Build()
	shopPage := collage.NewPage("shop").WithLayouts(layout).WithContent(shop).WithPath("en", "/shop")
	openPage := collage.NewPage("open").WithLayouts(layout).WithContent(open).WithPath("en", "/open")
	if static {
		shopPage, openPage = shopPage.Static(), openPage.Static()
	} else {
		shopPage = shopPage.Incremental(time.Hour)
	}
	for _, p := range []*collage.Page{shopPage.Build(), openPage.Build()} {
		if err := a.RegisterPage(p); err != nil {
			t.Fatal(err)
		}
	}
	s.app = a
	return s
}

func (s *serverSite) get(t *testing.T, path, cookie string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if cookie != "" {
		r.Header.Set("Cookie", cookie)
	}
	w := httptest.NewRecorder()
	s.app.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d: %s", path, w.Code, w.Body)
	}
	return w
}

// para is the test fragment's paragraph in body, or body when it has none.
func para(body string) string {
	if i, j := strings.Index(body, "<p>"), strings.Index(body, "</p>"); i >= 0 && j > i {
		return body[i : j+4]
	}
	return body
}

func varies(w *httptest.ResponseRecorder, header string) bool {
	for _, v := range w.Header().Values("Vary") {
		for _, h := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(h), header) {
				return true
			}
		}
	}
	return false
}

func TestServer_CacheSplitsByConsent(t *testing.T) {
	s := newServerSite(t, false)
	steps := []struct {
		who     string
		cookie  string
		want    string
		renders int32
	}{
		{"A, analytics", "collage_consent=v=1&c=analytics&t=1", "analytics=true media=false", 1},
		{"B, no cookie", "", "analytics=false media=false", 2},
		{"A again, from the cache", "collage_consent=v=1&c=analytics&t=1", "analytics=true media=false", 2},
		{"A', same combo, another raw cookie", "other=1; collage_consent=v=1&c=analytics,evil&t=999", "analytics=true media=false", 2},
		{"B', a decision granting nothing", "collage_consent=v=1&c=&t=5", "analytics=false media=false", 2},
		{"B'', malformed, so no decision", "collage_consent=v=abc&c=analytics&t=1", "analytics=false media=false", 2},
		{"C, both", "collage_consent=v=1&c=media,analytics&t=1", "analytics=true media=true", 3},
	}
	for _, st := range steps {
		w := s.get(t, "/shop", st.cookie)
		body := w.Body.String()
		if !strings.Contains(body, st.want) || !strings.Contains(body, "essential=true bogus=false") {
			t.Errorf("%s: %s, want %q", st.who, para(body), st.want)
		}
		if got := s.renders.Load(); got != st.renders {
			t.Errorf("%s: renders = %d, want %d", st.who, got, st.renders)
		}
		if !varies(w, "Cookie") {
			t.Errorf("%s: Vary = %q, want Cookie", st.who, w.Header().Values("Vary"))
		}
	}
	if w := s.logs.warns(""); len(w) != 0 {
		t.Errorf("warnings: %q", w)
	}
}

func TestGranted_OutsidePathsWarnsOnce(t *testing.T) {
	s := newServerSite(t, false)
	for range 2 {
		w := s.get(t, "/open", "collage_consent=v=1&c=analytics,media&t=1")
		if !strings.Contains(w.Body.String(), "analytics=false media=false essential=true bogus=false") {
			t.Errorf("outside serverPaths: %s", para(w.Body.String()))
		}
		if varies(w, "Cookie") {
			t.Errorf("outside serverPaths, Vary = %q", w.Header().Values("Vary"))
		}
	}
	for _, name := range []string{"analytics", "media"} {
		if w := s.logs.warns(`"` + name + `"`); len(w) != 1 || !strings.Contains(w[0], "serverPaths") {
			t.Errorf("warnings naming %s: %q, want one naming serverPaths", name, w)
		}
	}
	if w := s.logs.warns(""); len(w) != 2 {
		t.Errorf("warnings: %q, want 2", w)
	}
}

func TestGranted_Required(t *testing.T) {
	s := newServerSite(t, false)
	w := s.get(t, "/shop", "")
	if !strings.Contains(w.Body.String(), "essential=true bogus=false") {
		t.Errorf("required/unknown: %s", para(w.Body.String()))
	}
	if !Granted(nil, "essential") || Granted(nil, "analytics") || Granted(nil, "bogus") {
		t.Error("Granted with no render context")
	}
	if w := s.logs.warns(""); len(w) != 0 {
		t.Errorf("warnings: %q", w)
	}
}

func TestGranted_StaticExport(t *testing.T) {
	s := newServerSite(t, true)
	out := t.TempDir()
	b, err := collage.NewBuilder(s.app, collage.BuildOptions{OutDir: out})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"shop/index.html", "open/index.html"} {
		page, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(f)))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(page), "analytics=false media=false essential=true bogus=false") {
			t.Errorf("%s: %s", f, para(string(page)))
		}
	}
	if w := s.logs.warns(""); len(w) != 0 {
		t.Errorf("a build warned: %q", w)
	}
}

// The plugin keeps the configuration of the last App it was initialised for.
func TestGranted_NoPlugin(t *testing.T) {
	active.Store(nil)
	if Granted(nil, "essential") {
		t.Error("Granted without an initialised plugin")
	}
}
