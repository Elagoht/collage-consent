package consent

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
)

//go:embed consent.js
var scriptFS embed.FS

// scriptJS is the embedded consent.js, read once.
var scriptJS = func() []byte {
	b, err := scriptFS.ReadFile("consent.js")
	if err != nil {
		panic(err) // embedded at build time; cannot be missing
	}
	return b
}()

// scriptHash is the first 12 hex digits of consent.js's SHA-256. It goes in the
// tag's query, so a changed file is a new URL and the long cache stays safe. The
// mount ignores the query: v busts caches and is not checked.
var scriptHash = func() string {
	sum := sha256.Sum256(scriptJS)
	return hex.EncodeToString(sum[:])[:12]
}()

const (
	scriptPrefix = "/_collage/consent/"
	scriptPath   = scriptPrefix + "consent.js"
	// longCache is served with consent.js whatever its URL says: the hash in the
	// query, not the name, is what changes with the file.
	longCache = "public, max-age=31536000, immutable"
)

// scriptConfig is what consent.js reads from data-consent-config.
type scriptConfig struct {
	Version    int        `json:"version"`
	Categories []Category `json:"categories"`
	Text       Text       `json:"text"`
	PolicyURL  string     `json:"policyURL"`
	MaxAge     int        `json:"maxAge"`
}

// scriptTag is the script element for one locale. encoding/json escapes <, > and
// &, and html.EscapeString then escapes the quotes, so no configured text can
// leave the attribute.
func scriptTag(cfg Config, locale, defaultLocale string) (template.HTML, error) {
	data, err := json.Marshal(scriptConfig{
		Version:    cfg.Version,
		Categories: cfg.Categories,
		Text:       cfg.textFor(locale, defaultLocale),
		PolicyURL:  cfg.PolicyURL,
		MaxAge:     cfg.MaxAgeDays * 86400,
	})
	if err != nil {
		return "", fmt.Errorf("elagoht/consent: encode the script config: %w", err)
	}
	return template.HTML(`<script defer src="` + scriptPath + `?v=` + scriptHash +
		`" data-consent-config="` + html.EscapeString(string(data)) + `"></script>`), nil // built from escaped values
}
