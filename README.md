# elagoht/consent

A collage plugin that adds a cookie-consent banner, holds scripts and iframes back
until their category is granted, and exposes the visitor's choice to the page
through a small JS API. On paths you list, the server can read the choice too, with
`consent.Granted(rc, category)`, and the page cache keeps visitors with different
choices apart.

```go
app, err := collage.New(&collage.Config{
	Plugins: []collage.Plugin{consent.New()}, // configured from plugins-config.json
})
```

Requires collage v0.57.0 or later. The layout places the script with
`{{hoist "head"}}`. Without that marker nothing appears and nothing gated ever runs.

Consent is decided in the browser. Every visitor gets the same HTML, gated markup
sits inert until its category is granted, and the page cache and static export work
as before. Reading the choice on the server is opt-in and limited to the paths you
list (see [Reading the choice on the server](#reading-the-choice-on-the-server)).

## Configuration

You can configure the plugin in `plugins-config.json` under `"elagoht/consent"`, or
pass a starting point with `consent.NewWith(consent.Config{…})`. The application's
own configuration is decoded over that starting point.

```json
{ "elagoht/consent": {
    "version": 1,
    "categories": [
      { "name": "necessary", "required": true },
      { "name": "analytics" },
      { "name": "media" }
    ],
    "text": {
      "en": {
        "title": "Cookies", "body": "We use cookies to measure visits and to show embedded media.",
        "accept": "Accept all", "reject": "Reject all", "save": "Save choices", "settings": "Choose",
        "placeholder": "This content loads from {host}.", "allow": "Allow {category}",
        "policy": "Privacy policy",
        "categories": { "necessary": "Necessary", "analytics": "Analytics", "media": "Embedded media" }
      },
      "tr": { "title": "Çerezler", "accept": "Tümünü kabul et", "reject": "Tümünü reddet",
              "policy": "Gizlilik politikası" }
    },
    "policyURL": "/privacy",
    "maxAgeDays": 180,
    "serverPaths": []
} }
```

| Key | Meaning |
| --- | --- |
| `version` | Bump it to ask every visitor again. A stored choice from an older version counts as no decision. Default 1. |
| `categories` | The kinds of processing. At least one is required. Each `name` matches `^[a-z0-9-]+$`. A `required` category is always granted, has no switch, and is never written to the cookie. |
| `text` | The banner's wording per locale. The default locale must be present, with a label for every category. Every key in another locale, `categories` labels included, falls back to the default locale's. |
| `text.*.placeholder`, `text.*.allow` | A held-back iframe's placeholder. `{host}` is the iframe's host, and `{category}` is the category's label. |
| `text.*.policy` | The text of the link to `policyURL`. Without it, the link reads as the URL. |
| `policyURL` | Links the banner to the privacy policy. Without it, there is no link. |
| `maxAgeDays` | How long the choice is remembered, 1 to 400. Default 180. |
| `serverPaths` | Paths where the server may read the choice. Each entry begins with `/`. Default none. |

`Init` refuses a bad configuration with an error starting `elagoht/consent: `. Each
of these is refused:

- a bad or duplicate category name;
- no categories;
- `version` below 1;
- no text for the default locale, or a missing label in it;
- `maxAgeDays` out of range;
- a `serverPaths` entry without a leading `/`.

A `Plugin` value serves one App. Create another with `New` for a second App, and see
[Limits](#limits) for what two Apps in one process share.

## The script

The plugin serves one file, `/_collage/consent/consent.js`, and hoists one tag into
every page's head under the key `consent`:

```html
<script defer src="/_collage/consent/consent.js?v=<hash>" data-consent-config="…"></script>
```

- The configuration for the page's locale travels HTML-escaped in
  `data-consent-config`, so text containing `<`, quotes or markup stays text.
- The query is the first 12 hex digits of the file's SHA-256. The file is served as
  `text/javascript; charset=utf-8` with `nosniff` and
  `Cache-Control: public, max-age=31536000, immutable`, which is safe because a
  changed file gets a new URL.
- A static export writes the file beside the pages.
- A page that declares the hoist key `consent` itself replaces the tag.

## Gating scripts and iframes

Mark up anything that needs consent so it is inert as written:

```html
<script type="text/plain" data-consent="analytics" src="https://example.com/a.js"></script>
<script type="text/plain" data-consent="analytics">/* inline, too */</script>
<iframe data-consent="media" data-src="https://www.youtube-nocookie.com/embed/…" title="…"></iframe>
```

When the category is granted:

- **Gated scripts.** Each one is replaced by a real script. The new script has the
  same attributes, except that `type` and `data-consent` are dropped, and it keeps
  the same text, the same place in the document, and the same `nonce`. The scripts
  run one at a time, in document order, and each `src` script loads before the next
  script starts.
- **Gated iframes.** Each iframe gets its `src` from `data-src`. Until then, a
  placeholder button stands before it ("This content loads from {host}. Allow
  {category}"). Clicking the button grants that one category.
- **Late markup.** Gated markup added after the page loads is gated as well. That
  covers markup from a fragment swap, a router, or another script.
- **Unknown categories.** A `data-consent` that names a category not in the
  configuration stays inert, and the console warns once with its name.

A gated script cannot be a module. Its `type` is dropped on activation, so it runs
as a classic script. Gate a classic loader that imports the module instead. A
`nomodule` script is let through like any other gated script, and browsers that
understand modules ignore it.

**Withdrawal reloads the page.** A script that has run cannot be undone, so the page
reloads when the visitor takes back a category whose content has already run on it.

## The banner

The banner is a `<dialog>` that opens in three cases:

- when there is no decision;
- when the stored version is older than `version`;
- when an element with `data-consent-open` is clicked. A link works well:

  ```html
  <a href="#" data-consent-open>Cookie settings</a>
  ```

It offers Accept all, Reject all, Choose and Save choices, and links to `policyURL`.
Under Choose there is one checkbox per category. Required categories are checked and
disabled. Optional boxes are never pre-ticked.

The dialog behaves like this:

- it takes focus and keeps Tab inside;
- Escape closes it without deciding;
- focus returns to where it was before the dialog opened.

All of its text is set with `textContent`.

### Theming

The banner's stylesheet reads custom properties, each with a default. Set them on
`:root` or any ancestor:

| Property | What it styles |
| --- | --- |
| `--consent-bg`, `--consent-fg` | The dialog's background and text. |
| `--consent-border`, `--consent-radius` | The dialog's border and corner radius. |
| `--consent-font` | The dialog's font. |
| `--consent-backdrop` | The page behind the dialog. |
| `--consent-accent`, `--consent-accent-fg` | The primary buttons. |
| `--consent-link` | The policy link. |
| `--consent-button-bg`, `--consent-button-fg`, `--consent-button-radius` | The other buttons. |
| `--consent-focus` | The focus ring. |
| `--consent-placeholder-bg` | An iframe's placeholder. |

The classes are `.collage-consent` for the dialog, `.collage-consent-primary` for
the primary buttons, `.collage-consent-placeholder` for a placeholder and
`.collage-consent-allow` for its label.

## JS API

```js
collageConsent.get();                  // ["analytics", "necessary"]: granted, sorted, required included
collageConsent.set({ analytics: true }); // merge into the stored choice and save it
collageConsent.open();                 // open the banner
document.addEventListener("collage:consent", (e) => e.detail); // the granted list, after every save
```

`set` takes only configured, non-required keys whose value is `true` or `false`.
It merges into the choice the cookie holds now, not into this tab's memory, so a tab
left open for a while never writes back a choice the visitor has since changed
elsewhere.

## The cookie

The choice is kept in a first-party cookie:

```
collage_consent=v=<version>&c=<granted non-required categories, sorted, comma-joined>&t=<unix seconds>
```

It is set with `Path=/; SameSite=Lax; Max-Age=<maxAgeDays × 86400>`, plus `Secure`
on https. It is not `HttpOnly`, because the script reads it. The cookie belongs to
the required category.

Never set `collage_consent` from the server yourself, and never as `HttpOnly`. A
server-set `HttpOnly` cookie of that name is invisible to the script, but it is sent
to the server, so `Granted` reads it: the server and the browser would disagree.
The browser shows the banner and keeps everything gated while the server answers
from a choice the visitor cannot change.

Parsing is strict, and the browser and the server follow the same rules. A value
counts as no decision when any of these holds:

- it is malformed: an unknown or repeated key, a missing `t`, a non-numeric `v` or
  `t`, more than 64 entries, or more than 4096 bytes;
- it is from another version.

Entries in `c` that are not configured, optional categories are dropped. Nothing is
decoded: `c=analytics%2Cmedia` is one unknown entry.

## Reading the choice on the server

```go
// in a data handler on a path under serverPaths:
if consent.Granted(rc, "analytics") { … }
```

List the paths in `serverPaths`. Matching goes by whole segments: `/shop` covers
`/shop` and `/shop/cart`, but not `/shopping`, and `/` covers everything. The entries
are URL paths, so list each locale's spelling (`/shop`, `/tr/magaza`).

On those paths, the plugin's middleware reads the cookie and reduces it to the
granted optional categories, sorted and comma-joined (`""` when there are none). It
puts that value in its own request header, `X-Collage-Consent`, which overrides
anything a client sent, and declares it with `collage.Vary`. `Granted` reads it back
with `collage.Varied`, never from the header itself.

- **One entry per choice.** The page cache keys on the value, not on the raw cookie,
  so visitors who made the same choice share one entry. A page has at most
  2^(optional categories) entries. A malformed cookie is no decision and shares the
  entry of "nothing granted".
- **No other cookie in a shared render.** The cache does not vary on `Cookie`
  itself, so collage keeps the `Cookie` header out of a cached page's render, as it
  does everywhere. A handler there that reads a session cookie sees none, for every
  visitor. Varying on `Cookie` instead would bake the first visitor's session into
  the page served to everyone after.

`Granted` answers as follows:

- **A required category:** always true.
- **An unknown category:** false.
- **Outside `serverPaths`:** false, and logs one warning per category. The warning
  usually means the path is missing from the list.
- **Answered before the middleware ran:** false, with the same warning. This covers
  a request on a `serverPaths` path that another plugin answered first, for example
  with its own status page.
- **In a static export or a build's capture request:** false, without a warning.
  There is no visitor, so an exported page always renders as "not granted". Decide
  in the browser on a static site.

The server cannot see GPC (below), so it honours what the visitor saved.

### The cost: `Vary: Cookie`

Every response on `serverPaths` carries `Vary: Cookie`, cacheable or not, since the
middleware adds it before the response is known. Publicly cacheable ones also
carry `X-Collage-Consent`, which collage adds from the declared dimension.
`Vary: Cookie` is what keeps a CDN or a browser cache from showing one visitor's
page to another. But the `Cookie` header
differs between almost all visitors, because every other cookie is in it too. Most
CDNs therefore treat such a response as practically uncacheable, or refuse to cache
it at all. collage's own page cache is not affected, because it keys on the reduced
value.

Keep `serverPaths` to the few pages whose HTML must differ by consent, and gate
everything else in the browser. Each entry is a trade of CDN caching for a
server-side answer.

### Never cover stream paths

A pushed fragment is rendered once and sent to every subscriber. On a live or
WebSocket stream, a consent-dependent render would therefore reach every visitor
with the first one's choice. The middleware never varies `/_live/` (collage-live's
default prefix) or `/_collage/`, even when `serverPaths` is `/`, so `Granted` is
false in what they render. Do not list a stream path of your own either, such as
collage-live under another `prefix` or a WebSocket endpoint. A fix in collage
itself, where `RenderFragment` stops reporting a varied request's render as
shared, is a possible follow-up.

## Global Privacy Control and Do Not Track

- **GPC** (`navigator.globalPrivacyControl`). With no decision yet, the banner opens
  with every optional category off. Boxes are never pre-ticked, with or without GPC,
  because pre-ticked boxes are not valid consent. GPC therefore changes nothing you
  can see. It never grants anything, and Accept all still grants everything, because
  the visitor's explicit choice wins for consent itself. A plugin may add its own
  signal check on top: analytics' `RespectDNT` also honours GPC, so a GPC browser
  loads nothing even after Accept all. The server does not read the `Sec-GPC`
  header.
- **Do Not Track** is not read. It never grants or refuses a category.

## Content Security Policy

- **Scripts.** There is no inline script. The tag carries no nonce, so the policy
  must allow the site's own scripts with `'self'` in `script-src`.
  - A nonce-only policy blocks consent.js, and so does one that relies on
    `'strict-dynamic'`, because `'strict-dynamic'` makes the browser ignore `'self'`.
    Nothing gated runs then, and no banner appears.
  - A gated inline script needs its own `nonce`, which is carried over on
    activation. A gated `src` script needs its origin allowed in `script-src`.
- **Gated inline scripts under a strict CSP.** A gated inline script is activated
  as an inline script, so a `script-src` without `'unsafe-inline'` blocks it unless
  it has a `nonce` (carried over, above) or its text is allowed by hash. Add
  `'sha256-<base64 of the script text>'` to `script-src`; Chrome honours a hash for
  the activated copy. The hash is of the exact text between the tags. For example,
  analytics' Google Analytics configuration (`consentCategory` set) is such a
  script, and its hash depends on the measurement id.
- **Styles.** The stylesheet is a `<style>` element built by the script, so
  `style-src` applies to it. **Any policy without permission for it blocks it**,
  and that includes collage-secure's example policy (`default-src 'self'` with no
  `style-src`). The banner stays usable but unstyled: the browser's plain
  `<dialog>`, unstyled placeholders, and a CSP violation in the console on every
  page load. To style it under a strict policy, allow its hash:

  ```
  style-src 'self' 'sha256-eelrSqXC4J1uUIXb9KT4pg6d9Fk5Q6A+EEp1Kfc6N1k='
  ```

  The hash changes whenever the stylesheet does. A test fails if this README does
  not hold the current one, so it is updated with each release that touches the
  CSS. (`'unsafe-inline'` in `style-src` also works, at the cost of allowing every
  inline style.)

## Limits

- **One consent plugin per process.** `Granted` is a plain function, so it reads the
  configuration of the last App the plugin was initialised for. Two Apps in one
  process with different consent configurations share the last one's categories
  and version. The per-visitor choice is unaffected, because it travels with each
  request.
- **A shadowing cookie.** Another `collage_consent` cookie with a longer `Path`, or
  one set for a parent `Domain`, is sent first, and the browser and the server both
  read the first valid one. Such a cookie can defeat a withdrawal: the visitor saves
  a new choice, but the shadowing one is still what is read. The script warns in the
  console when what it reads back is not what it saved. Do not set
  `collage_consent` from anywhere else.
- **Blocked cookies.** When the browser refuses the cookie, the choice does not
  reliably survive even within the page: the script re-reads the cookie before it
  acts, so the choice resets to "nothing granted" (fail-closed) on the next
  re-check, such as a late gated node, opening the dialog again, or a placeholder
  click. The next page asks again. The same console warning appears.
- **Offline.** Without the cached script there is no banner, and gated content
  stays inert. That happens when a page is served offline and consent.js was never
  cached. Nothing gated ever falls back to running.
- **Other tabs.** A tab only learns of a choice made in another tab on its next
  save or its next load. A tab whose content ran keeps it until it reloads.
- **The `X-Collage-Consent` header is the plugin's.** Another middleware that
  declares a value for it with `collage.Vary` replaces the consent combo on the same
  request, because the last declaration wins. A `Vary` on `Cookie` by anyone else
  is never read as consent.

## Out of scope

- IAB TCF.
- Geo-targeted banners, such as one shown only in the EU.
- Server-side logs of consent records.
- Per-vendor choices; consent here is per category.
- Reading GPC on the server (`Sec-GPC`).
