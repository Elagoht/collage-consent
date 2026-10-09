package consent

import (
	"fmt"
	"regexp"
	"strings"
)

// Category is one kind of processing the visitor can allow or refuse. A required
// category is always granted and has no switch.
type Category struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
}

// Text is the banner's wording in one locale. A key missing from a non-default
// locale falls back to the default locale's.
type Text struct {
	Title       string            `json:"title"`
	Body        string            `json:"body"`
	Accept      string            `json:"accept"`
	Reject      string            `json:"reject"`
	Save        string            `json:"save"`
	Settings    string            `json:"settings"`
	Placeholder string            `json:"placeholder"`
	Allow       string            `json:"allow"`
	Categories  map[string]string `json:"categories"`
}

// Config is the plugin's configuration.
type Config struct {
	// Version is bumped to ask every visitor again. Default 1.
	Version int `json:"version"`
	// Categories are the kinds of processing; at least one is required.
	Categories []Category `json:"categories"`
	// Text is the wording per locale; the default locale must be present.
	Text map[string]Text `json:"text"`
	// PolicyURL links the banner to the privacy policy.
	PolicyURL string `json:"policyURL"`
	// MaxAgeDays is how long the choice is remembered, 1 to 400. Default 180.
	MaxAgeDays int `json:"maxAgeDays"`
	// ServerPaths are the paths where the server may read the choice.
	ServerPaths []string `json:"serverPaths"`
}

var categoryName = regexp.MustCompile(`^[a-z0-9-]+$`)

func (c Config) withDefaults() Config {
	if c.Version == 0 {
		c.Version = 1
	}
	if c.MaxAgeDays == 0 {
		c.MaxAgeDays = 180
	}
	return c
}

func (c Config) validate(defaultLocale string) error {
	if len(c.Categories) == 0 {
		return fmt.Errorf("elagoht/consent: no categories configured")
	}
	seen := map[string]bool{}
	for _, cat := range c.Categories {
		if !categoryName.MatchString(cat.Name) {
			return fmt.Errorf("elagoht/consent: category name %q must match ^[a-z0-9-]+$", cat.Name)
		}
		if seen[cat.Name] {
			return fmt.Errorf("elagoht/consent: duplicate category %q", cat.Name)
		}
		seen[cat.Name] = true
	}
	if c.Version < 1 {
		return fmt.Errorf("elagoht/consent: version %d must be at least 1", c.Version)
	}
	t, ok := c.Text[defaultLocale]
	if !ok {
		return fmt.Errorf("elagoht/consent: no text for the default locale %q", defaultLocale)
	}
	for _, cat := range c.Categories {
		if t.Categories[cat.Name] == "" {
			return fmt.Errorf("elagoht/consent: no label for category %q in locale %q", cat.Name, defaultLocale)
		}
	}
	if c.MaxAgeDays < 1 || c.MaxAgeDays > 400 {
		return fmt.Errorf("elagoht/consent: maxAgeDays %d must be between 1 and 400", c.MaxAgeDays)
	}
	for _, p := range c.ServerPaths {
		if !strings.HasPrefix(p, "/") {
			return fmt.Errorf("elagoht/consent: serverPaths entry %q must begin with /", p)
		}
	}
	return nil
}

// textFor is the wording for locale, each key falling back to the default
// locale's. The result shares nothing with the configuration.
func (c Config) textFor(locale, defaultLocale string) Text {
	out := c.Text[defaultLocale]
	out.Categories = make(map[string]string, len(out.Categories))
	for k, v := range c.Text[defaultLocale].Categories {
		out.Categories[k] = v
	}
	if locale == defaultLocale {
		return out
	}
	t, ok := c.Text[locale]
	if !ok {
		return out
	}
	for dst, src := range map[*string]string{
		&out.Title: t.Title, &out.Body: t.Body, &out.Accept: t.Accept, &out.Reject: t.Reject,
		&out.Save: t.Save, &out.Settings: t.Settings, &out.Placeholder: t.Placeholder, &out.Allow: t.Allow,
	} {
		if src != "" {
			*dst = src
		}
	}
	for k, v := range t.Categories {
		if v != "" {
			out.Categories[k] = v
		}
	}
	return out
}

// required reports whether name is a configured category and whether it is required.
func (c Config) required(name string) (known, required bool) {
	for _, cat := range c.Categories {
		if cat.Name == name {
			return true, cat.Required
		}
	}
	return false, false
}
