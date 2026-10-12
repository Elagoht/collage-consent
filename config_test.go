package consent

import (
	"strings"
	"testing"

	"github.com/Elagoht/collage/pkg/collagetest"
)

func valid() Options {
	return Options{
		Categories: []Category{{Name: "essential", Required: true}, {Name: "analytics"}},
		Text: Text{
			Title: "Cookies", Accept: "Accept", Reject: "Reject",
			Categories: map[string]string{"essential": "Essential", "analytics": "Analytics"},
		},
	}
}

func TestValidate_Table(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*Options)
		want string
	}{
		{"ok", func(*Options) {}, ""},
		{"bad name", func(c *Options) { c.Categories[1].Name = "Ana lytics" }, "category name"},
		{"duplicate", func(c *Options) { c.Categories[1].Name = "essential" }, "duplicate"},
		{"no categories", func(c *Options) { c.Categories = nil }, "no categories"},
		{"version", func(c *Options) { c.Version = -1 }, "version"},
		{"default in locales", func(c *Options) { c.Locales = map[string]Text{"en": {}} }, "locales has the default locale"},
		{"missing label", func(c *Options) {
			c.Text = Text{Categories: map[string]string{"essential": "E"}}
		}, "label"},
		{"maxAge low", func(c *Options) { c.MaxAgeDays = -1 }, "maxAgeDays"},
		{"maxAge high", func(c *Options) { c.MaxAgeDays = 401 }, "maxAgeDays"},
		{"server path", func(c *Options) { c.ServerPaths = []string{"account"} }, "serverPaths"},
		{"empty name", func(c *Options) { c.Categories[1].Name = "" }, "category name"},
		{"unicode name", func(c *Options) { c.Categories[1].Name = "çerez" }, "category name"},
		{"empty label", func(c *Options) { c.Text.Categories["analytics"] = "" }, "label"},
		{"maxAge 1", func(c *Options) { c.MaxAgeDays = 1 }, ""},
		{"maxAge 400", func(c *Options) { c.MaxAgeDays = 400 }, ""},
		{"policyURL path", func(c *Options) { c.PolicyURL = "/privacy" }, ""},
		{"policyURL https", func(c *Options) { c.PolicyURL = "https://example.com/privacy" }, ""},
		{"policyURL http", func(c *Options) { c.PolicyURL = "http://example.com/p" }, ""},
		{"policyURL javascript", func(c *Options) { c.PolicyURL = "javascript:alert(1)" }, "policyURL"},
		{"policyURL protocol-relative", func(c *Options) { c.PolicyURL = "//evil.example/p" }, "policyURL"},
		{"policyURL relative", func(c *Options) { c.PolicyURL = "privacy" }, "policyURL"},
		{"policyURL data", func(c *Options) { c.PolicyURL = "data:text/html,x" }, "policyURL"},
		{"policyURL scheme only", func(c *Options) { c.PolicyURL = "https://" }, "policyURL"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := valid()
			tc.mut(&c)
			err := c.withDefaults().validate("en")
			if tc.want == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.want)
			}
			if !strings.HasPrefix(err.Error(), "consent: ") || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q lacks prefix or %q", err, tc.want)
			}
		})
	}
}

func TestDefaults(t *testing.T) {
	c := valid().withDefaults()
	if c.Version != 1 || c.MaxAgeDays != 180 {
		t.Errorf("defaults = %d, %d", c.Version, c.MaxAgeDays)
	}
	c = Options{Version: 3, MaxAgeDays: 30}.withDefaults()
	if c.Version != 3 || c.MaxAgeDays != 30 {
		t.Errorf("explicit values overwritten: %d, %d", c.Version, c.MaxAgeDays)
	}
}

func TestTextFor_Fallback(t *testing.T) {
	c := valid()
	c.Text = Text{Title: "T", Save: "Save", Accept: "A", Policy: "Privacy policy", Categories: map[string]string{"essential": "Essential", "analytics": "Analytics"}}
	c.Locales = map[string]Text{"tr": Text{Title: "Baslik", Categories: map[string]string{"essential": "Zorunlu"}}}
	got := c.textFor("tr", "en")
	if got.Title != "Baslik" || got.Save != "Save" || got.Accept != "A" || got.Policy != "Privacy policy" {
		t.Errorf("text = %+v", got)
	}
	if got.Categories["essential"] != "Zorunlu" || got.Categories["analytics"] != "Analytics" {
		t.Errorf("categories = %v", got.Categories)
	}
	c.Locales = map[string]Text{"tr": Text{Policy: "Gizlilik", Categories: map[string]string{}}}
	if got := c.textFor("tr", "en"); got.Policy != "Gizlilik" || got.Title != "T" {
		t.Errorf("tr policy = %q, title = %q", got.Policy, got.Title)
	}
	if en := c.textFor("de", "en"); en.Title != "T" {
		t.Errorf("unknown locale should give default, got %+v", en)
	}
	if c.Locales["tr"].Categories["analytics"] != "" {
		t.Error("textFor mutated the config")
	}
}

func TestRequired(t *testing.T) {
	c := valid()
	if k, r := c.required("essential"); !k || !r {
		t.Error("essential")
	}
	if k, r := c.required("analytics"); !k || r {
		t.Error("analytics")
	}
	if k, _ := c.required("nope"); k {
		t.Error("nope")
	}
}

func build(t *testing.T, p *Plugin) error {
	t.Helper()
	_, err := collagetest.TrySite(t, collagetest.WithPlugins(p))
	return err
}

func TestInit_Validates(t *testing.T) {
	if err := build(t, New(Options{})); err == nil || !strings.Contains(err.Error(), "no categories") {
		t.Errorf("New(Options{}) err = %v", err)
	}
	p := New(valid())
	if err := build(t, p); err != nil {
		t.Fatal(err)
	}
	if p.cfg.Version != 1 || p.cfg.MaxAgeDays != 180 {
		t.Errorf("defaults not kept: %+v", p.cfg)
	}
	if p.Name() != Name || p.Version() != "0.2.0" {
		t.Error("identity")
	}
}

func TestInit_Version(t *testing.T) {
	c := valid()
	c.Version = 0
	p := New(c)
	if err := build(t, p); err != nil {
		t.Fatal(err)
	}
	if p.cfg.Version != 1 {
		t.Errorf("version 0 became %d, want 1", p.cfg.Version)
	}
	c.Version = -1
	if err := build(t, New(c)); err == nil || !strings.Contains(err.Error(), "consent: version -1") {
		t.Errorf("version -1 err = %v", err)
	}
}
