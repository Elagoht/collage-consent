package consent

import "strings"

// cookieCase is one collage_consent value, read under a configuration whose
// categories are essential (required), analytics and media. The same list drives
// TestParseCookie_Table (Go: r.Cookie, then parseCookie) and TestBrowser's
// malformed_cookie scenarios (consent.js in Chrome), so the two parsers are held
// to one set of answers.
type cookieCase struct {
	name    string
	seed    string // the value as the browser stores it, quotes and all
	version int    // the configured version
	decided bool   // false: no decision, the dialog opens
	granted []string
	// goOnly cases cannot be set in a browser: Chrome refuses a cookie over
	// 4096 bytes before consent.js ever reads it, and the harness seeds only a
	// non-empty value.
	goOnly bool
}

// cookieCases are the parse rules' cases: the 13 Task 3 ran in Chrome first, then
// the rest.
func cookieCases() []cookieCase {
	entries := func(n int) string { return strings.Repeat("analytics,", n-1) + "analytics" }
	// exactly is a valid value padded to n bytes with unknown entries.
	exactly := func(n int) string {
		v := "v=1&c=analytics,&t=1"
		return strings.Replace(v, "analytics,", "analytics,"+strings.Repeat("x", n-len(v)), 1)
	}
	return []cookieCase{
		// The brief's cookie, under both versions: the duplicate v alone makes it
		// invalid, so first-wins and last-wins parsers each fail one of these.
		{name: "duplicate_v_1", seed: "c=analytics,evil&v=1&t=1&v=2", version: 1},
		{name: "duplicate_v_2", seed: "c=analytics,evil&v=1&t=1&v=2", version: 2},
		{name: "duplicate_c", seed: "v=1&c=analytics&c=media&t=1", version: 1},
		{name: "missing_t", seed: "v=1&c=analytics", version: 1},
		{name: "extra_key", seed: "v=1&c=analytics&t=1&x=1", version: 1},
		{name: "v_not_number", seed: "v=abc&c=analytics&t=1", version: 1},
		{name: "v_leading_zero", seed: "v=01&c=analytics&t=1", version: 1},
		{name: "t_not_number", seed: "v=1&c=analytics&t=now", version: 1},
		{name: "too_many_entries", seed: "v=1&c=" + entries(65) + "&t=1", version: 1},
		{name: "quoted_value", seed: `"v=1&c=analytics&t=1"`, version: 1, decided: true, granted: []string{"analytics"}},
		{name: "unknown_dropped", seed: "v=1&c=analytics,evil&t=1", version: 1, decided: true, granted: []string{"analytics"}},
		{name: "percent_not_decoded", seed: "v=1&c=%61nalytics&t=1", version: 1, decided: true},
		{name: "required_in_c", seed: "v=1&c=essential&t=1", version: 1, decided: true},

		{name: "valid", seed: "v=1&c=analytics,media&t=1700000000", version: 1, decided: true, granted: []string{"analytics", "media"}},
		{name: "valid_any_order", seed: "t=1&c=media,analytics&v=1", version: 1, decided: true, granted: []string{"analytics", "media"}},
		{name: "valid_version_2", seed: "v=2&c=media&t=1", version: 2, decided: true, granted: []string{"media"}},
		{name: "version_mismatch", seed: "v=1&c=analytics&t=1", version: 2},
		{name: "v_zero", seed: "v=0&c=analytics&t=1", version: 1},
		{name: "empty_c", seed: "v=1&c=&t=1", version: 1, decided: true},
		{name: "only_commas", seed: "v=1&c=,,&t=1", version: 1, decided: true},
		{name: "duplicate_entries", seed: "v=1&c=media,analytics,media&t=1", version: 1, decided: true, granted: []string{"analytics", "media"}},
		{name: "case_sensitive", seed: "v=1&c=Analytics&t=1", version: 1, decided: true},
		{name: "percent_comma", seed: "v=1&c=analytics%2Cmedia&t=1", version: 1, decided: true},
		{name: "entry_spaces_kept", seed: "v=1&c= analytics&t=1", version: 1, decided: true},
		{name: "exactly_64_entries", seed: "v=1&c=" + entries(64) + "&t=1", version: 1, decided: true, granted: []string{"analytics"}},
		{name: "piece_without_eq", seed: "v=1&c=&t=1&x", version: 1},
		{name: "empty_t", seed: "v=1&c=analytics&t=", version: 1},
		{name: "empty_key", seed: "v=1&c=analytics&t=1&=1", version: 1},
		{name: "value_with_eq", seed: "v=1&c=analytics=1&t=1", version: 1, decided: true},
		{name: "exactly_4096_bytes", seed: exactly(4096), version: 1, decided: true, granted: []string{"analytics"}, goOnly: true},
		{name: "over_4096_bytes", seed: exactly(4097), version: 1, goOnly: true},
		{name: "empty_value", seed: "", version: 1, goOnly: true},
	}
}

// browserConfig is the browser tests' configuration, and the server tests'
// categories: essential (required), analytics and media.
func browserConfig() Config {
	return Config{
		Categories: []Category{{Name: "essential", Required: true}, {Name: "analytics"}, {Name: "media"}},
		Text: map[string]Text{"en": {
			Title: "Cookies", Body: "We use cookies.", Accept: "Accept all", Reject: "Reject all",
			Save: "Save choices", Settings: "Choose",
			Placeholder: "This content loads from {host}.", Allow: "Allow {category}",
			Categories: map[string]string{"essential": "Essential", "analytics": "Analytics", "media": "Embedded media"},
		}},
		PolicyURL: "/privacy",
	}
}
