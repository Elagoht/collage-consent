package consent

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

func valid() Config {
	return Config{
		Categories: []Category{{Name: "essential", Required: true}, {Name: "analytics"}},
		Text: map[string]Text{"en": {
			Title: "Cookies", Accept: "Accept", Reject: "Reject",
			Categories: map[string]string{"essential": "Essential", "analytics": "Analytics"},
		}},
	}
}

func TestValidate_Table(t *testing.T) {
	tests := []struct {
		name string
		mut  func(*Config)
		want string
	}{
		{"ok", func(*Config) {}, ""},
		{"bad name", func(c *Config) { c.Categories[1].Name = "Ana lytics" }, "category name"},
		{"duplicate", func(c *Config) { c.Categories[1].Name = "essential" }, "duplicate"},
		{"no categories", func(c *Config) { c.Categories = nil }, "no categories"},
		{"version", func(c *Config) { c.Version = -1 }, "version"},
		{"no default text", func(c *Config) { c.Text = map[string]Text{"tr": {}} }, "no text for the default locale"},
		{"missing label", func(c *Config) {
			c.Text["en"] = Text{Categories: map[string]string{"essential": "E"}}
		}, "label"},
		{"maxAge low", func(c *Config) { c.MaxAgeDays = -1 }, "maxAgeDays"},
		{"maxAge high", func(c *Config) { c.MaxAgeDays = 401 }, "maxAgeDays"},
		{"server path", func(c *Config) { c.ServerPaths = []string{"account"} }, "serverPaths"},
		{"empty name", func(c *Config) { c.Categories[1].Name = "" }, "category name"},
		{"unicode name", func(c *Config) { c.Categories[1].Name = "çerez" }, "category name"},
		{"empty label", func(c *Config) { c.Text["en"].Categories["analytics"] = "" }, "label"},
		{"maxAge 1", func(c *Config) { c.MaxAgeDays = 1 }, ""},
		{"maxAge 400", func(c *Config) { c.MaxAgeDays = 400 }, ""},
		{"policyURL path", func(c *Config) { c.PolicyURL = "/privacy" }, ""},
		{"policyURL https", func(c *Config) { c.PolicyURL = "https://example.com/privacy" }, ""},
		{"policyURL http", func(c *Config) { c.PolicyURL = "http://example.com/p" }, ""},
		{"policyURL javascript", func(c *Config) { c.PolicyURL = "javascript:alert(1)" }, "policyURL"},
		{"policyURL protocol-relative", func(c *Config) { c.PolicyURL = "//evil.example/p" }, "policyURL"},
		{"policyURL relative", func(c *Config) { c.PolicyURL = "privacy" }, "policyURL"},
		{"policyURL data", func(c *Config) { c.PolicyURL = "data:text/html,x" }, "policyURL"},
		{"policyURL scheme only", func(c *Config) { c.PolicyURL = "https://" }, "policyURL"},
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
			if !strings.HasPrefix(err.Error(), "elagoht/consent: ") || !strings.Contains(err.Error(), tc.want) {
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
	c = Config{Version: 3, MaxAgeDays: 30}.withDefaults()
	if c.Version != 3 || c.MaxAgeDays != 30 {
		t.Errorf("explicit values overwritten: %d, %d", c.Version, c.MaxAgeDays)
	}
}

func TestTextFor_Fallback(t *testing.T) {
	c := valid()
	c.Text["en"] = Text{Title: "T", Save: "Save", Accept: "A", Policy: "Privacy policy", Categories: map[string]string{"essential": "Essential", "analytics": "Analytics"}}
	c.Text["tr"] = Text{Title: "Baslik", Categories: map[string]string{"essential": "Zorunlu"}}
	got := c.textFor("tr", "en")
	if got.Title != "Baslik" || got.Save != "Save" || got.Accept != "A" || got.Policy != "Privacy policy" {
		t.Errorf("text = %+v", got)
	}
	if got.Categories["essential"] != "Zorunlu" || got.Categories["analytics"] != "Analytics" {
		t.Errorf("categories = %v", got.Categories)
	}
	c.Text["tr"] = Text{Policy: "Gizlilik", Categories: map[string]string{}}
	if got := c.textFor("tr", "en"); got.Policy != "Gizlilik" || got.Title != "T" {
		t.Errorf("tr policy = %q, title = %q", got.Policy, got.Title)
	}
	if en := c.textFor("de", "en"); en.Title != "T" {
		t.Errorf("unknown locale should give default, got %+v", en)
	}
	if c.Text["tr"].Categories["analytics"] != "" {
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
	a, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte("x")}}, Root: "t"},
		Plugins:  []collage.Plugin{p},
	})
	if err != nil {
		return err
	}
	return a.Start()
}

func TestInit_Validates(t *testing.T) {
	if err := build(t, New()); err == nil || !strings.Contains(err.Error(), "no categories") {
		t.Errorf("New() err = %v", err)
	}
	p := NewWith(valid())
	if err := build(t, p); err != nil {
		t.Fatal(err)
	}
	if p.cfg.Version != 1 || p.cfg.MaxAgeDays != 180 {
		t.Errorf("defaults not kept: %+v", p.cfg)
	}
	if p.Name() != Name || p.Version() != "0.1.2" {
		t.Error("identity")
	}
}

func TestInit_Version(t *testing.T) {
	c := valid()
	c.Version = 0
	p := NewWith(c)
	if err := build(t, p); err != nil {
		t.Fatal(err)
	}
	if p.cfg.Version != 1 {
		t.Errorf("version 0 became %d, want 1", p.cfg.Version)
	}
	c.Version = -1
	if err := build(t, NewWith(c)); err == nil || !strings.Contains(err.Error(), "elagoht/consent: version -1") {
		t.Errorf("version -1 err = %v", err)
	}
}
