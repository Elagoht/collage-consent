package consent

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Elagoht/collage/pkg/collage"
	"github.com/Elagoht/collage/pkg/collagetest"
)

// grantedSite starts an application whose home page renders Granted(rc, "stats")
// under a consent plugin configured with categories.
func grantedSite(t *testing.T, categories []Category) *collagetest.Site {
	t.Helper()
	cfg := browserConfig()
	cfg.Categories = categories
	en := cfg.Text
	en.Categories = map[string]string{}
	for _, c := range categories {
		en.Categories[c.Name] = c.Name
	}
	cfg.Text = en
	home := collage.NewFragment("home", "home.html").WithData(collage.Load(
		func(_ context.Context, rc *collage.RenderContext) (bool, error) { return Granted(rc, "stats"), nil })).Build()
	return collagetest.NewSite(t,
		collagetest.WithPlugins(New(cfg)),
		collagetest.WithoutHome(),
		collagetest.WithTemplates(map[string]string{"home.html": `<p>stats={{.}}</p>`}),
		collagetest.WithRegister(collage.NewPage("home").
			WithLayouts(collage.NewFragment("layout", "layout.html").Build()).
			WithContent(home).WithPath("en", "/").Build()),
	)
}

// Two applications in one process each read their own configuration: a second
// App's Init does not change what Granted answers for the first.
func TestGranted_TwoApps(t *testing.T) {
	first := grantedSite(t, []Category{{Name: "stats", Required: true}})
	second := grantedSite(t, []Category{{Name: "other"}})
	if body := first.Get("/").Body; !strings.Contains(body, "stats=true") {
		t.Errorf("first app: %s, want stats=true (its own required category)", body)
	}
	if body := second.Get("/").Body; !strings.Contains(body, "stats=false") {
		t.Errorf("second app: %s, want stats=false (no such category)", body)
	}
}

// text was keyed by locale before v0.2.0; that shape now stops the application,
// naming the stray key.
func TestOldTextShapeFails(t *testing.T) {
	_, err := collagetest.TrySite(t,
		collagetest.WithPlugins(New(Options{})),
		collagetest.WithPluginConfig(Name, `{"categories": [{"name": "essential", "required": true}],
			"text": {"en": {"categories": {"essential": "Essential"}}}}`))
	if err == nil || !strings.Contains(err.Error(), `"en"`) {
		t.Errorf("start = %v, want an error naming the old locale key", err)
	}
}

// text is the default locale's wording and locales the others', each key
// falling back to text's.
func TestTextAndLocalesFromConfig(t *testing.T) {
	p := New(Options{})
	collagetest.NewSite(t,
		collagetest.WithPlugins(p),
		collagetest.WithPluginConfig(Name, `{"categories": [{"name": "essential", "required": true}],
			"text": {"title": "Cookies", "accept": "Accept", "categories": {"essential": "Essential"}},
			"locales": {"tr": {"title": "Çerezler"}}}`),
		collagetest.WithConfig(func(c *collage.Config) {
			c.Locale = collage.LocaleConfig{Default: "en", Supported: []string{"en", "tr"}}
		}),
	)
	en, tr := p.cfg.textFor("en", "en"), p.cfg.textFor("tr", "en")
	if en.Title != "Cookies" || tr.Title != "Çerezler" || tr.Accept != "Accept" || tr.Categories["essential"] != "Essential" {
		t.Errorf("en = %+v, tr = %+v", en, tr)
	}
}

// An action is not rendered, so no render key reaches it: Granted reads the
// App's configuration from the request's context, which the middleware set.
func TestGranted_InAction(t *testing.T) {
	cfg := serverConfig()
	cfg.ServerPaths = []string{"/shop"}
	var essential, analytics, media atomic.Bool
	action := collage.NewAction("buy").WithPath("en", "/shop/buy").WithMethods(http.MethodPost).WithoutCSRF().
		WithHandler(func(_ context.Context, rc *collage.RenderContext) (*collage.ActionResult, error) {
			essential.Store(Granted(rc, "essential"))
			analytics.Store(Granted(rc, "analytics"))
			media.Store(Granted(rc, "media"))
			return collage.SeeOther("/"), nil
		}).Build()
	site := collagetest.NewSite(t, collagetest.WithPlugins(New(cfg)), collagetest.WithRegister(action))
	req := site.Request(http.MethodPost, "/shop/buy", nil)
	req.Header.Set("Cookie", "collage_consent=v=1&c=analytics&t=1")
	site.Do(req).WantStatus(http.StatusSeeOther)
	if !essential.Load() || !analytics.Load() || media.Load() {
		t.Errorf("in an action: essential=%v analytics=%v media=%v, want true true false", essential.Load(), analytics.Load(), media.Load())
	}
}
