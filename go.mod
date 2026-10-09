// A collage plugin that adds a cookie-consent banner, holds scripts and iframes back
// until their category is granted, and exposes the choice to the page and the server.
module github.com/Elagoht/collage-consent

go 1.26

require github.com/Elagoht/collage v0.57.0

retract v0.1.1 // its test suite pinned the previous version string; use v0.1.2
