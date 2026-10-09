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

// serverConfig adds ads and ads-personal, one name a prefix of the other, so a
// substring test of the combo shows.
func serverConfig() Config {
	c := browserLikeConfig()
	c.Categories = append(c.Categories, Category{Name: "ads"}, Category{Name: "ads-personal"})
	en := c.Text["en"]
	en.Categories = map[string]string{"essential": "Essential", "analytics": "Analytics", "media": "Embedded media", "ads": "Ads", "ads-personal": "Personal ads"}
	c.Text = map[string]Text{"en": en}
	return c
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

// grantedView is what the test fragment renders: Granted for each category,
// and the visitor's session cookie, which a shared render must never see.
type grantedView struct {
	Analytics, Media, Essential, Bogus, Ads, AdsPersonal bool
	Session                                              string
}

const grantedHTML = `<p>analytics={{.Analytics}} media={{.Media}} essential={{.Essential}} bogus={{.Bogus}} ads={{.Ads}} ads-personal={{.AdsPersonal}} session={{.Session}}</p>`

// serverSite is an app with serverConfig's categories, the page cache on, an
// error page, and these pages, each rendering grantedView:
//   - /shop: Incremental(time.Hour) (Static in a build), with a fragment path
//     /shop/frag;
//   - /open: dynamic (Static in a build); its handler asks twice per render.
//
// renders counts /shop's data handler runs.
type serverSite struct {
	app     *collage.App
	logs    *logRecorder
	renders atomic.Int32
	probe   *headerProbe
}

// siteOpts shapes a serverSite.
type siteOpts struct {
	static      bool
	serverPaths []string                        // default ["/shop"]
	before      []collage.Plugin                // registered before consent
	appUse      func(http.Handler) http.Handler // the application's own middleware
}

// headerProbe is a plugin with a BuildFinishedHook, which is what makes a build
// send its capture requests; it keeps each captured file's headers by path.
type headerProbe struct {
	captured map[string]http.Header
}

func (*headerProbe) Name() string                             { return "test/header-probe" }
func (*headerProbe) Version() string                          { return "0.0.0" }
func (*headerProbe) Init(context.Context, collage.Host) error { return nil }
func (*headerProbe) Shutdown(context.Context) error           { return nil }
func (h *headerProbe) OnBuildFinished(_ context.Context, ev *collage.BuildFinishedEvent) error {
	h.captured = map[string]http.Header{}
	for _, f := range ev.Files {
		if f.Captured {
			h.captured[f.Path] = f.Headers
		}
	}
	return nil
}

func view(rc *collage.RenderContext) grantedView {
	v := grantedView{
		Analytics: Granted(rc, "analytics"), Media: Granted(rc, "media"),
		Essential: Granted(rc, "essential"), Bogus: Granted(rc, "bogus"),
		Ads: Granted(rc, "ads"), AdsPersonal: Granted(rc, "ads-personal"),
	}
	if rc.Request != nil {
		if c, err := rc.Request.Cookie("session"); err == nil {
			v.Session = c.Value
		}
	}
	return v
}

func newServerSite(t *testing.T, o siteOpts) *serverSite {
	t.Helper()
	s := &serverSite{logs: &logRecorder{}, probe: &headerProbe{}}
	cfg := serverConfig()
	cfg.ServerPaths = o.serverPaths
	if cfg.ServerPaths == nil {
		cfg.ServerPaths = []string{"/shop"}
	}
	a, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{
			"t/layout.html":  {Data: []byte(layoutHTML)},
			"t/granted.html": {Data: []byte(grantedHTML)},
		}, Root: "t"},
		Locale:  collage.LocaleConfig{Default: "en", Supported: []string{"en"}},
		Cache:   collage.CacheConfig{Enabled: true},
		Logger:  slog.New(s.logs),
		Plugins: append(o.before, NewWith(cfg), s.probe),
	})
	if err != nil {
		t.Fatal(err)
	}
	if o.appUse != nil {
		if err := a.Use(o.appUse); err != nil {
			t.Fatal(err)
		}
	}
	frag := func(name string, fn func(context.Context, *collage.RenderContext) (grantedView, error)) *collage.Fragment {
		return collage.NewFragment(name, "granted.html").WithData(collage.Load(fn)).Build()
	}
	plain := func(_ context.Context, rc *collage.RenderContext) (grantedView, error) { return view(rc), nil }
	layout := collage.NewFragment("layout", "layout.html").WithSlot("content", true, false).Build()
	shop := frag("shop", func(_ context.Context, rc *collage.RenderContext) (grantedView, error) {
		s.renders.Add(1)
		return view(rc), nil
	})
	open := frag("open", func(_ context.Context, rc *collage.RenderContext) (grantedView, error) {
		view(rc)
		return view(rc), nil
	})
	shopPage := collage.NewPage("shop").WithLayouts(layout).WithContent(shop).WithPath("en", "/shop").
		WithFragmentPath("en", "/shop/frag", frag("inner", plain))
	openPage := collage.NewPage("open").WithLayouts(layout).WithContent(open).WithPath("en", "/open")
	if o.static {
		shopPage, openPage = shopPage.Static(), openPage.Static()
	} else {
		shopPage = shopPage.Incremental(time.Hour)
	}
	for _, p := range []*collage.Page{shopPage.Build(), openPage.Build()} {
		if err := a.RegisterPage(p); err != nil {
			t.Fatal(err)
		}
	}
	if !o.static {
		errPage := collage.NewPage("error").WithLayouts(layout).WithContent(frag("errc", plain)).Build()
		if err := a.RegisterErrorPage(errPage); err != nil {
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
	s := newServerSite(t, siteOpts{})
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
		{"D, ads-personal alone", "collage_consent=v=1&c=ads-personal&t=1", "ads=false ads-personal=true", 4},
		{"E, ads alone", "collage_consent=v=1&c=ads&t=1", "ads=true ads-personal=false", 5},
		{"D again, from the cache", "collage_consent=v=1&c=ads-personal&t=1", "ads=false ads-personal=true", 5},
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
	s := newServerSite(t, siteOpts{})
	for range 2 {
		w := s.get(t, "/open", "collage_consent=v=1&c=analytics,media&t=1")
		if !strings.Contains(w.Body.String(), "analytics=false media=false essential=true bogus=false") {
			t.Errorf("outside serverPaths: %s", para(w.Body.String()))
		}
		if varies(w, "Cookie") {
			t.Errorf("outside serverPaths, Vary = %q", w.Header().Values("Vary"))
		}
	}
	names := []string{"analytics", "media", "ads", "ads-personal"}
	for _, name := range names {
		if w := s.logs.warns(`Granted("` + name + `")`); len(w) != 1 || !strings.Contains(w[0], "serverPaths") {
			t.Errorf("warnings naming %s: %q, want one naming serverPaths", name, w)
		}
	}
	if w := s.logs.warns(""); len(w) != len(names) {
		t.Errorf("warnings: %q, want %d", w, len(names))
	}
}

func TestGranted_Required(t *testing.T) {
	s := newServerSite(t, siteOpts{})
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
	s := newServerSite(t, siteOpts{static: true})
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
	// The build also captured each page's headers through the handler, so
	// Granted ran in capture requests too, and the check below covers them.
	for _, path := range []string{"/shop", "/open"} {
		if _, ok := s.probe.captured[path]; !ok {
			t.Fatalf("%s was not captured: %v", path, s.probe.captured)
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

// C1: the cache varies on the reduced choice, not on the Cookie header, so a
// shared render sees no cookie at all; a session cookie read there is empty for
// every visitor rather than the first visitor's, frozen for the rest.
func TestServer_SharedRenderSeesNoOtherCookie(t *testing.T) {
	s := newServerSite(t, siteOpts{})
	for _, who := range []string{"alice", "bob"} {
		w := s.get(t, "/shop", "session="+who+"; collage_consent=v=1&c=analytics&t=1")
		body := para(w.Body.String())
		if !strings.Contains(body, "analytics=true") || !strings.Contains(body, "session=</p>") {
			t.Errorf("%s: %s, want analytics granted and no session", who, body)
		}
		if !varies(w, "Cookie") {
			t.Errorf("%s: Vary = %q, want Cookie", who, w.Header().Values("Vary"))
		}
	}
	if got := s.renders.Load(); got != 1 {
		t.Errorf("renders = %d, want 1 shared", got)
	}
}

// The private header is the plugin's own: a client sending it changes nothing.
func TestServer_ClientConsentHeaderIgnored(t *testing.T) {
	s := newServerSite(t, siteOpts{})
	get := func(path, cookie, header string) string {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if cookie != "" {
			r.Header.Set("Cookie", cookie)
		}
		r.Header.Set(varyHeader, header)
		w := httptest.NewRecorder()
		s.app.Handler().ServeHTTP(w, r)
		return para(w.Body.String())
	}
	if b := get("/open", "", "analytics,media"); !strings.Contains(b, "analytics=false media=false") {
		t.Errorf("outside serverPaths, a sent header granted: %s", b)
	}
	if b := get("/shop", "collage_consent=v=1&c=&t=1", "analytics,media"); !strings.Contains(b, "analytics=false media=false") {
		t.Errorf("under serverPaths, a sent header beat the cookie: %s", b)
	}
	if b := get("/shop", "collage_consent=v=1&c=media&t=1", ""); !strings.Contains(b, "analytics=false media=true") {
		t.Errorf("under serverPaths, a sent empty header beat the cookie: %s", b)
	}
}

// I1: another middleware's Vary on Cookie is not a consent.
func TestGranted_ForeignCookieVary(t *testing.T) {
	s := newServerSite(t, siteOpts{appUse: func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if c, err := r.Cookie("theme"); err == nil {
				_ = collage.Vary(r, "Cookie", c.Value)
			}
			next.ServeHTTP(w, r)
		})
	}})
	if b := para(s.get(t, "/open", "theme=media").Body.String()); !strings.Contains(b, "media=false") {
		t.Errorf("outside serverPaths: %s", b)
	}
	if b := para(s.get(t, "/shop", "theme=media").Body.String()); !strings.Contains(b, "media=false") {
		t.Errorf("under serverPaths, no consent cookie: %s", b)
	}
	if b := para(s.get(t, "/shop", "theme=dark; collage_consent=v=1&c=media&t=1").Body.String()); !strings.Contains(b, "media=true") {
		t.Errorf("under serverPaths, with consent: %s", b)
	}
}

// streamPlugin renders /shop/frag from a handler at /_live/, as collage-live
// pushes a fragment, and reports whether the render was shared.
type streamPlugin struct{ host collage.Host }

func (*streamPlugin) Name() string                   { return "test/stream" }
func (*streamPlugin) Version() string                { return "0.0.0" }
func (*streamPlugin) Shutdown(context.Context) error { return nil }
func (p *streamPlugin) Init(_ context.Context, h collage.Host) error {
	p.host = h
	return h.Handle("/_live/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fr, err := p.host.RenderFragment(r, collage.FragmentRequest{Path: "/shop/frag"})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(fr.HTML))
	}))
}

// I2: a pushed fragment is one render sent to every subscriber, so the stream
// paths and the plugin's own files are never varied, even under serverPaths "/".
func TestServer_SkipsStreamPaths(t *testing.T) {
	s := newServerSite(t, siteOpts{serverPaths: []string{"/"}, before: []collage.Plugin{&streamPlugin{}}})
	const granted = "collage_consent=v=1&c=analytics,media&t=1"
	w := s.get(t, "/_live/stream", granted)
	if b := para(w.Body.String()); !strings.Contains(b, "analytics=false media=false") {
		t.Errorf("a stream render read the choice: %s", b)
	}
	if varies(w, "Cookie") {
		t.Errorf("stream Vary = %q", w.Header().Values("Vary"))
	}
	w = s.get(t, scriptPath+"?v="+scriptHash, granted)
	if varies(w, "Cookie") || varies(w, varyHeader) {
		t.Errorf("consent.js Vary = %q", w.Header().Values("Vary"))
	}
	if b := para(s.get(t, "/shop", granted).Body.String()); !strings.Contains(b, "analytics=true media=true") {
		t.Errorf("serverPaths / no longer covers /shop: %s", b)
	}
}

// blockPlugin answers /shop/blocked with the site's 403 page from middleware
// that runs before consent's, and records what that request declared.
type blockPlugin struct {
	host     collage.Host
	declared []string
}

func (*blockPlugin) Name() string                   { return "test/block" }
func (*blockPlugin) Version() string                { return "0.0.0" }
func (*blockPlugin) Shutdown(context.Context) error { return nil }
func (p *blockPlugin) Init(_ context.Context, h collage.Host) error {
	p.host = h
	return h.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/shop/blocked" {
				next.ServeHTTP(w, r)
				return
			}
			p.host.ServeStatus(w, r, http.StatusForbidden)
			for _, h := range []string{varyHeader, "Cookie", ""} {
				if v, ok := collage.VariedContext(r.Context(), h); ok {
					p.declared = append(p.declared, h+"="+v)
				}
			}
		})
	})
}

// M1, M2: a page rendered before routing (another plugin's early answer) has an
// open vary set; Granted must declare nothing in it, and its warning must not
// claim the path is missing from serverPaths.
func TestGranted_EarlyStatusPage(t *testing.T) {
	bp := &blockPlugin{}
	s := newServerSite(t, siteOpts{before: []collage.Plugin{bp}})
	r := httptest.NewRequest(http.MethodGet, "/shop/blocked", nil)
	r.Header.Set("Cookie", "collage_consent=v=1&c=analytics&t=1")
	w := httptest.NewRecorder()
	s.app.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "analytics=false") {
		t.Fatalf("status %d: %s", w.Code, para(w.Body.String()))
	}
	if len(bp.declared) != 0 {
		t.Errorf("Granted declared %q", bp.declared)
	}
	if varies(w, varyHeader) {
		t.Errorf("Vary = %q", w.Header().Values("Vary"))
	}
	warns := s.logs.warns(`Granted("analytics")`)
	if len(warns) != 1 || !strings.Contains(warns[0], "before") {
		t.Errorf("warnings: %q, want one saying the request may have been answered before the middleware", warns)
	}
}

// M6: a hand-built render context has no collage vary state; Granted fails
// closed instead of panicking.
func TestGranted_HandBuiltContext(t *testing.T) {
	s := newServerSite(t, siteOpts{})
	s.get(t, "/open", "") // starts the app, so the plugin is initialised
	r := httptest.NewRequest("GET", "/shop", nil)
	if Granted(&collage.RenderContext{Request: r}, "analytics") {
		t.Error("Granted on a hand-built context")
	}
	if !Granted(&collage.RenderContext{Request: r}, "essential") {
		t.Error("a required category must stay granted")
	}
}

// M6: inside a served request (vary state set, so the warning path runs), a
// hand-built context also fails closed.
func TestGranted_HandBuiltContextInRequest(t *testing.T) {
	var got atomic.Int32
	s := newServerSite(t, siteOpts{appUse: func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/open" {
				if Granted(&collage.RenderContext{Request: r}, "analytics") {
					got.Store(1)
				} else {
					got.Store(2)
				}
			}
			next.ServeHTTP(w, r)
		})
	}})
	s.get(t, "/open", "collage_consent=v=1&c=analytics&t=1")
	if got.Load() != 2 {
		t.Errorf("hand-built context in a request: %d, want 2 (false)", got.Load())
	}
}

// A bare request (no URL, no context) in a context fails closed too.
func TestGranted_BareRequest(t *testing.T) {
	s := newServerSite(t, siteOpts{})
	s.get(t, "/open", "")
	for _, rc := range []*collage.RenderContext{{}, {Request: &http.Request{}}} {
		if Granted(rc, "analytics") {
			t.Errorf("Granted on %+v", rc)
		}
	}
}
