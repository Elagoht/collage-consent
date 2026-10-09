package consent

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

// FuzzReadCookie: whatever the header, the result is sorted, unique, only
// configured optional names, and empty without a decision.
func FuzzReadCookie(f *testing.F) {
	for _, c := range cookieCases() {
		f.Add("collage_consent="+c.seed, c.version)
	}
	f.Add("collage_consent=v=1&c=essential,analytics&t=1; collage_consent=v=1&c=media&t=1", 1)
	cfg := browserLikeConfig()
	f.Fuzz(func(t *testing.T, header string, version int) {
		c := cfg
		c.Version = version
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header["Cookie"] = []string{header}
		g, ok := readCookie(r, c)
		g2, ok2 := parseCookie(header, version, func(n string) bool { return n == "analytics" || n == "media" })
		for _, res := range []struct {
			g  []string
			ok bool
		}{{g, ok}, {g2, ok2}} {
			if !res.ok && len(res.g) != 0 {
				t.Fatalf("no decision but granted %q", res.g)
			}
			for _, n := range res.g {
				if n != "analytics" && n != "media" {
					t.Fatalf("granted %q", n)
				}
			}
			if !slices.IsSorted(res.g) || len(slices.Compact(slices.Clone(res.g))) != len(res.g) {
				t.Fatalf("not sorted/unique %q", res.g)
			}
		}
	})
}
