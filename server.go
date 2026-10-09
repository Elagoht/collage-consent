package consent

import (
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Elagoht/collage/pkg/collage"
)

// cookieName is the consent cookie's name.
const cookieName = "collage_consent"

// varyHeader is the plugin's own request header, which carries the visitor's
// reduced choice and is what the page cache varies on. It is not Cookie: varying
// on Cookie would make collage keep the whole Cookie header in a shared render,
// and a handler there reading any other cookie (a session) would bake the first
// visitor's value into the page every later visitor is served. The middleware
// sets it on every request, over whatever a client sent, so only the plugin
// speaks through it.
const varyHeader = "X-Collage-Consent"

// skipPrefixes are never varied, whatever serverPaths covers: a stream pushes one
// fragment render to every subscriber (collage-live's /_live/), and /_collage/
// holds static files, the plugin's own among them.
var skipPrefixes = []string{"/_live", "/_collage"}

// serverState is what Granted needs, set by the last successful Init.
type serverState struct {
	cfg    Config
	logger *slog.Logger
	// warned holds the category names Granted has already warned about.
	warned sync.Map
}

// active is the configuration Granted reads. Granted is a plain function, given
// only a render's context, so it cannot reach a Plugin value: there is one
// consent plugin per process, and a second App's Init replaces the first's. The
// per-request state, the visitor's choice, does not live here; it is the Vary
// value, which is in the cache key and so survives a shared render.
var active atomic.Pointer[serverState]

// Granted reports whether the visitor rendered for has granted category, on the
// server. A required category is always granted, and an unknown one never is.
//
// The choice is readable only on a path under serverPaths, where the plugin's
// middleware varies the page cache on it; everywhere else Granted reports false.
// That is logged once per category, since the page's author most likely forgot
// the path, or another plugin answered the request before the consent
// middleware ran. A static build and a build's capture request are not visitors,
// so there Granted is false without a word.
//
// Granted reads the configuration of the last App the plugin was initialised
// for: one consent plugin per process.
func Granted(rc *collage.RenderContext, category string) bool {
	st := active.Load()
	if st == nil {
		return false
	}
	known, required := st.cfg.required(category)
	if !known {
		return false
	}
	if required {
		return true
	}
	if rc == nil || rc.Request == nil {
		return false
	}
	combo, ok := collage.Varied(rc, varyHeader)
	if !ok {
		if visitor(rc) {
			if _, seen := st.warned.LoadOrStore(category, true); !seen {
				st.logger.Warn(`elagoht/consent: Granted("`+category+`") is false: the choice was not read for this request; its path is not under serverPaths, or the request was answered before the consent middleware ran`,
					"category", category, "path", rc.Request.URL.Path)
			}
		}
		return false
	}
	return slices.Contains(strings.Split(combo, ","), category)
}

// visitor reports whether rc renders for a visitor's request, which is when an
// unreadable choice is worth a warning. A static build renders with a synthetic
// request collage is not serving, which Vary tells apart: it answers
// ErrVaryOutsideRequest for a request without collage's vary set, before it
// looks at the header name. The empty name makes every other request an error
// too, so the probe never declares anything, even for a page rendered before
// routing (another plugin's early ServeStatus). A build's capture request is
// served, but it is the build, not a visitor.
func visitor(rc *collage.RenderContext) bool {
	if collage.IsCapture(rc.Request.Context()) {
		return false
	}
	err := collage.Vary(rc.Request, "", "")
	return !errors.Is(err, collage.ErrVaryOutsideRequest)
}

// middleware reads the visitor's choice on every request under cfg.ServerPaths,
// but not under skipPrefixes: the granted non-required categories, sorted and
// comma-joined, "" for none. It puts that in varyHeader and declares it with
// collage.Vary, so the cache keys on the choice, not the raw cookie, and visitors
// with one choice share an entry; setting the header overrides any value a
// client sent, and Granted reads only the declared value, never the header. The
// response also says Vary: Cookie, because that is the header a CDN sees the
// choice in. A build's capture request is varied
// like any other: it has no cookie, so its combo is "", and collage keeps Vary
// out of the headers it deploys.
func middleware(cfg Config, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if underPaths(r.URL.Path, cfg.ServerPaths) && !underPaths(r.URL.Path, skipPrefixes) {
				granted, _ := readCookie(r, cfg)
				combo := strings.Join(granted, ",")
				r.Header.Set(varyHeader, combo)
				w.Header().Add("Vary", "Cookie")
				if err := collage.Vary(r, varyHeader, combo); err != nil {
					logger.Warn("elagoht/consent: cannot vary on the consent cookie", "path", r.URL.Path, "error", err)
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// readCookie is the visitor's choice: the granted non-required categories and
// whether there is a decision. r.Cookie finds the cookie by the rules consent.js
// follows (1-3: the first collage_consent whose value passes, one pair of quotes
// unwrapped), and parseCookie reads it (4-9).
func readCookie(r *http.Request, cfg Config) ([]string, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return nil, false
	}
	return parseCookie(c.Value, cfg.Version, func(name string) bool {
		known, required := cfg.required(name)
		return known && !required
	})
}

// parseCookie applies consent.js's parse rules 4-9 to a cookie value. known
// reports a configured, non-required category. The result is the granted known
// names, sorted and without duplicates, and whether the value is a decision; an
// invalid value is no decision and grants nothing. Keep it in step with parse in
// consent.js.
func parseCookie(value string, version int, known func(string) bool) (granted []string, ok bool) {
	// 4. At most 4096 bytes.
	if len(value) > 4096 {
		return nil, false
	}
	// 5. Exactly v, c and t, once each, in any order; every piece has "=".
	var v, c, t string
	var seenV, seenC, seenT bool
	for _, piece := range strings.Split(value, "&") {
		k, val, found := strings.Cut(piece, "=")
		if !found {
			return nil, false
		}
		var seen *bool
		var dst *string
		switch k {
		case "v":
			seen, dst = &seenV, &v
		case "c":
			seen, dst = &seenC, &c
		case "t":
			seen, dst = &seenT, &t
		default:
			return nil, false
		}
		if *seen {
			return nil, false
		}
		*seen, *dst = true, val
	}
	if !seenV || !seenC || !seenT {
		return nil, false
	}
	// 6. v is ^[1-9][0-9]*$ and, as a string, the configured version.
	if !digits(v) || v[0] == '0' || v != strconv.Itoa(version) {
		return nil, false
	}
	// 7. t is ^[0-9]+$.
	if !digits(t) {
		return nil, false
	}
	// 8. c is empty or at most 64 entries; nothing decoded; unknown, required
	// and empty entries dropped; duplicates once.
	granted = []string{}
	if c == "" {
		return granted, true
	}
	entries := strings.Split(c, ",")
	if len(entries) > 64 {
		return nil, false
	}
	for _, e := range entries {
		if known(e) && !slices.Contains(granted, e) {
			granted = append(granted, e)
		}
	}
	slices.Sort(granted)
	// 9. A decision.
	return granted, true
}

// digits reports whether s is one or more ASCII digits.
func digits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// underPaths reports whether path is one of prefixes or below one, by whole
// segments: "/shop" covers "/shop" and "/shop/x", never "/shopping"; "/" covers
// everything.
func underPaths(path string, prefixes []string) bool {
	for _, p := range prefixes {
		p = strings.TrimSuffix(p, "/")
		if p == "" || path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}
