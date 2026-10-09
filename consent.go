// Package consent is a collage plugin with a cookie-consent banner, a gate that
// holds scripts and iframes back until their category is granted, and a JS API.
package consent

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"sync/atomic"

	"github.com/Elagoht/collage/pkg/collage"
)

// Name is the plugin's name, and its key in the application's plugin configuration.
const Name = "elagoht/consent"

// Plugin is the consent plugin.
type Plugin struct {
	cfg Config
	// tags is the hoisted script element for the default and every supported locale.
	tags   map[string]template.HTML
	inited atomic.Bool
}

// New returns a plugin configured entirely from the application's configuration.
func New() *Plugin { return &Plugin{} }

// NewWith returns a plugin with cfg as its starting point, which the application's
// own configuration is then decoded over.
func NewWith(cfg Config) *Plugin { return &Plugin{cfg: cfg} }

func (p *Plugin) Name() string                   { return Name }
func (p *Plugin) Version() string                { return "0.1.2" }
func (p *Plugin) Shutdown(context.Context) error { return nil }

var (
	_ collage.Plugin           = (*Plugin)(nil)
	_ collage.BeforeRenderHook = (*Plugin)(nil)
)

// Init reads the configuration, applies the defaults, checks it against the
// default locale, builds the script element for each locale and mounts consent.js.
// With serverPaths it adds the middleware that varies those paths on the cookie,
// and it makes this App's configuration the one Granted reads.
// A Plugin value serves one App: once an Init has succeeded, another fails, since
// the value keeps that App's configuration.
func (p *Plugin) Init(_ context.Context, host collage.Host) error {
	if p.inited.Load() {
		return errors.New("elagoht/consent: a Plugin value serves one App; create another with New")
	}
	cfg, err := collage.PluginConfig(host, p.cfg)
	if err != nil {
		return err
	}
	cfg = cfg.withDefaults()
	def, locales := host.Locales()
	if err := cfg.validate(def); err != nil {
		return err
	}
	tags := make(map[string]template.HTML, len(locales)+1)
	for _, l := range append([]string{def}, locales...) {
		if tags[l], err = scriptTag(cfg, l, def); err != nil {
			return err
		}
	}
	if err := host.Mount(scriptPrefix, scriptFS, collage.WithCacheControl(longCache)); err != nil {
		return fmt.Errorf("elagoht/consent: serve consent.js: %w", err)
	}
	if len(cfg.ServerPaths) > 0 {
		if err := host.Use(middleware(cfg, host.Logger())); err != nil {
			return fmt.Errorf("elagoht/consent: read the cookie on serverPaths: %w", err)
		}
	}
	p.cfg, p.tags = cfg, tags
	p.inited.Store(true)
	active.Store(&serverState{cfg: cfg, logger: host.Logger()})
	return nil
}

// OnBeforeRender hoists the script element into the page's head, in the page's
// locale. Hoisted at depth zero under "consent", so it appears once however many
// fragments the page has, and a page can replace it by declaring the same key.
func (p *Plugin) OnBeforeRender(_ context.Context, ev *collage.BeforeRenderEvent) error {
	if ev.Context == nil || p.tags == nil {
		return nil
	}
	// Init built a tag for the default locale and every supported one, and collage
	// refuses a page in any other locale, so a render's locale is always present.
	ev.Context.Hoist("head", "consent", p.tags[ev.Context.Locale])
	return nil
}
