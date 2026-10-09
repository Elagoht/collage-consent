// Package consent is a collage plugin with a cookie-consent banner, a gate that
// holds scripts and iframes back until their category is granted, and a JS API.
package consent

import (
	"context"

	"github.com/Elagoht/collage/pkg/collage"
)

// Name is the plugin's name, and its key in the application's plugin configuration.
const Name = "elagoht/consent"

// Plugin is the consent plugin.
type Plugin struct {
	cfg Config
}

// New returns a plugin configured entirely from the application's configuration.
func New() *Plugin { return &Plugin{} }

// NewWith returns a plugin with cfg as its starting point, which the application's
// own configuration is then decoded over.
func NewWith(cfg Config) *Plugin { return &Plugin{cfg: cfg} }

func (p *Plugin) Name() string                   { return Name }
func (p *Plugin) Version() string                { return "0.1.0" }
func (p *Plugin) Shutdown(context.Context) error { return nil }

var _ collage.Plugin = (*Plugin)(nil)

// Init reads the configuration, applies the defaults and checks it against the
// default locale.
func (p *Plugin) Init(_ context.Context, host collage.Host) error {
	cfg, err := collage.PluginConfig(host, p.cfg)
	if err != nil {
		return err
	}
	cfg = cfg.withDefaults()
	def, _ := host.Locales()
	if err := cfg.validate(def); err != nil {
		return err
	}
	p.cfg = cfg
	return nil
}
