# Vendored files

| file | source | version | licence |
| --- | --- | --- | --- |
| `htmx.min.js` | https://github.com/bigskysoftware/htmx/releases/tag/v4.0.0 (`dist/htmx.min.js`) | 4.0.0, sha256 e484d9171a9db30a39c8f16e3d709d4137f3211c659f8e6125816635033d593f | Zero-Clause BSD, `htmx-LICENSE.txt` (the minified file carries no header) |
| `tokens.css`, `bundle.css` | `docs/design-system/` in this repo | copied verbatim; a test fails when they drift | vink, MIT |
| `fonts/*.woff2` | `docs/design-system/fonts/` (JetBrains Mono, SIL OFL) | copied verbatim | SIL Open Font License 1.1, `fonts/OFL.txt` |
| `favicon.svg`, `favicon-down.svg` | `assets/brand/` | copied verbatim | vink, MIT |

Nothing here is loaded from a CDN; the binary embeds this directory.
