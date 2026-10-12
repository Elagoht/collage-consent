package consent

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Category is one kind of processing the visitor can allow or refuse. A required
// category is always granted and has no switch.
type Category struct {
	// Name is the category's name, matching ^[a-z0-9-]+$: what data-consent names.
	Name string `json:"name"`
	// Required marks a category that is always granted and has no switch.
	Required bool `json:"required"`
}

// Text is the banner's wording in one locale. In Options.Locales a key left
// empty falls back to Options.Text's.
type Text struct {
	// Title is the banner's heading.
	Title string `json:"title"`
	// Body is the banner's text.
	Body string `json:"body"`
	// Accept is the label of the button granting every category.
	Accept string `json:"accept"`
	// Reject is the label of the button refusing every optional category.
	Reject string `json:"reject"`
	// Save is the label of the button saving the ticked categories.
	Save string `json:"save"`
	// Settings is the label of the button showing one checkbox per category.
	Settings string `json:"settings"`
	// Placeholder stands before a held-back iframe; {host} is its host.
	Placeholder string `json:"placeholder"`
	// Allow is the placeholder's grant label; {category} is the category's label.
	Allow string `json:"allow"`
	// Policy is the policy link's text; without it the link reads as PolicyURL.
	Policy string `json:"policy"`
	// Categories are each category's label, by name.
	Categories map[string]string `json:"categories"`
}

// Options configures the plugin.
type Options struct {
	// Version is bumped to ask every visitor again. Default 1.
	Version int `json:"version"`
	// Categories are the kinds of processing; at least one is required.
	Categories []Category `json:"categories"`
	// Text is the wording in the application's default locale. It must label
	// every category.
	Text Text `json:"text"`
	// Locales holds the wording of other locales, by locale; a key left empty
	// falls back to Text's. An entry for the default locale is merged over Text.
	Locales map[string]Text `json:"locales"`
	// PolicyURL links the banner to the privacy policy.
	PolicyURL string `json:"policyURL"`
	// MaxAgeDays is how long the choice is remembered, 1 to 400. Default 180.
	MaxAgeDays int `json:"maxAgeDays"`
	// ServerPaths are the paths where the server may read the choice with
	// Granted, by whole segments: "/shop" covers "/shop/x", never "/shopping".
	// Responses there carry Vary: Cookie.
	ServerPaths []string `json:"serverPaths"`
}

var categoryName = regexp.MustCompile(`^[a-z0-9-]+$`)

func (c Options) withDefaults() Options {
	if c.Version == 0 {
		c.Version = 1
	}
	if c.MaxAgeDays == 0 {
		c.MaxAgeDays = 180
	}
	return c
}

func (c Options) validate(defaultLocale string) error {
	if len(c.Categories) == 0 {
		return fmt.Errorf("consent: no categories configured")
	}
	seen := map[string]bool{}
	for _, cat := range c.Categories {
		if !categoryName.MatchString(cat.Name) {
			return fmt.Errorf("consent: category name %q must match ^[a-z0-9-]+$", cat.Name)
		}
		if seen[cat.Name] {
			return fmt.Errorf("consent: duplicate category %q", cat.Name)
		}
		seen[cat.Name] = true
	}
	if c.Version < 1 {
		return fmt.Errorf("consent: version %d must be at least 1", c.Version)
	}
	labels := c.textFor(defaultLocale, defaultLocale).Categories
	for _, cat := range c.Categories {
		if labels[cat.Name] == "" {
			return fmt.Errorf("consent: text has no label for category %q", cat.Name)
		}
	}
	if c.MaxAgeDays < 1 || c.MaxAgeDays > 400 {
		return fmt.Errorf("consent: maxAgeDays %d must be between 1 and 400", c.MaxAgeDays)
	}
	if err := checkPolicyURL(c.PolicyURL); err != nil {
		return err
	}
	for _, p := range c.ServerPaths {
		if !strings.HasPrefix(p, "/") {
			return fmt.Errorf("consent: serverPaths entry %q must begin with /", p)
		}
	}
	return nil
}

// checkPolicyURL accepts "", a path beginning with a single "/", or an absolute
// http or https URL with a host. Anything else, javascript: and data: included,
// never reaches the banner's link.
func checkPolicyURL(v string) error {
	if v == "" {
		return nil
	}
	if strings.HasPrefix(v, "/") && !strings.HasPrefix(v, "//") && !strings.HasPrefix(v, `/\`) {
		return nil
	}
	u, err := url.Parse(v)
	if err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		return nil
	}
	return fmt.Errorf("consent: policyURL %q must be a path beginning with / or an absolute http(s) URL", v)
}

// textFor is the wording for locale: Text with Locales[locale] merged over it,
// each key it leaves empty falling back to Text's. The result shares nothing with the configuration.
func (c Options) textFor(locale, defaultLocale string) Text {
	out := c.Text
	out.Categories = make(map[string]string, len(out.Categories))
	for k, v := range c.Text.Categories {
		out.Categories[k] = v
	}
	t, ok := c.Locales[locale]
	if !ok {
		return out
	}
	for dst, src := range map[*string]string{
		&out.Title: t.Title, &out.Body: t.Body, &out.Accept: t.Accept, &out.Reject: t.Reject,
		&out.Save: t.Save, &out.Settings: t.Settings, &out.Placeholder: t.Placeholder, &out.Allow: t.Allow,
		&out.Policy: t.Policy,
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
func (c Options) required(name string) (known, required bool) {
	for _, cat := range c.Categories {
		if cat.Name == name {
			return true, cat.Required
		}
	}
	return false, false
}
