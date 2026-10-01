vink is a self-hosted heartbeat and uptime monitor: jobs ping it, and it probes services, in one Go binary with a server-rendered web UI, a CLI and an API. It lives at `github.com/w4jnl/vink` beside flok, and it shares flok's bracket, weights, ground and type. A person who knows flok should recognise vink as a sibling at a glance. The name is Dutch: *vink* is a finch, and *vinkje* is the checkmark you tick off a list.

## Voice

- Write like flok's README: technical, direct, honest about scope. Short sentences, no exclamation marks, no emoji.
- The product name is always lowercase: `vink`, never "Vink" or "VINK", even at the start of a sentence. Start the sentence another way if it looks wrong.
- States are lowercase words: up, late, down, paused, new. Say "down for 4 min", not "Critical".
- Name things by what people recognise: *monitor*, *ping URL*, *check*, *alert channel*. Not "entity", "probe config" or "webhook integration".
- Buttons are verbs that say what happens: "Check now", "Pause", "Delete monitor". Confirmations repeat the verb: "Really delete?".
- Errors say what is wrong and how to fix it, next to the field: "Grace must be at least 60s." No apologies.
- Times are relative first ("3 min ago", "due in 21 h") with the absolute time in the monitor's timezone on hover.

## The mark

A terminal bracket holding a tick and a cursor. The bracket is the same frame flok uses. flok's chevron is a bird in flight; in vink the same bird has landed on the line as a tick. The cursor is the prompt: vink lives next to your shell.

- Construction is on a 24-unit grid with flok's exact weights: frame stroke 1.6, tick stroke 2.9, cursor 4.8 × 2.2 with round ends. Thin frame, heavy tick: the frame is a container, not a subject.
- Ink: `mark` (cyan `#3FD0D4` on dark grounds, W4J teal `#12999D` on light). Use `vink-mark-ink.svg` for single-colour print. On a `brand` tile the mark is white.
- Something down: `vink-mark-down.svg` fills the frame solid and knocks out a cross and the cursor. At 16–22px the eye catches value, not detail, so the state lives in the whole icon rather than in a swapped glyph.
- Clear space: one bracket width (3.9 units on the 24 grid) on all sides. Minimum size `mark-min` (16px); below that use the favicon tile.
- Never place the mark on a ground between `#4A5A5D` and `#A8B4B6` (the frame loses contrast). Never outline, rotate, recolour per state, or put the tick outside the bracket.
- Lockups: `vink-lockup-*` sets the mark beside the wordmark at flok's lockup spacing; `vink-lockup-tagline-*` adds "heartbeat and uptime monitor"; `vink-wordmark-*` is `[vink]` for places without the mark. All text is outlined; no font needed.

## The w4jnl family

Every w4jnl tool uses the same system: the bracket frame, one glyph inside it, JetBrains Mono, the `ground` colour, W4J teal (`brand`) as the tie, and a 6px `accent` bar on the left edge of banners. flok's glyph is the chevron and cursor; vink's is the tick and cursor. Keep the bracket geometry identical across tools and change only the glyph.

## Colour

- `ground` is the page, `surface` raises the drawer and panels, and `surface-sunk` recesses inputs, the ping URL and code. Separate rows with `line`; give every control a `line-strong` border.
- Set text in `ink`, secondary text in `ink-muted`, placeholders only in `ink-faint`.
- `accent` is the only interactive colour: links, the primary button, a pressed chip, the focus ring. Text on an accent fill is `on-accent`, never literal white.
- The dark theme is the brand's home and the first theme. Light is complete too, and both are chosen per viewer with `prefers-color-scheme`.
- State colours mean state and nothing else: `up`, `late`, `down`, with their `-soft` fills for pills and banners. `paused` and `new` use `ink-muted`. `down` also marks destructive buttons, with `on-down` text when armed.
- Every state has its own glyph shape (disc, half disc, diamond, two bars, dashed ring) and word, so the UI is readable in greyscale and to colour-blind viewers. Never show a state by colour alone.
- Maintenance is `accent` on `accent-soft` with the pause glyph; it is not a state colour.
- All text pairs named in the token notes are 4.5:1 or better in both themes; control borders and the focus ring are 3:1 or better. Focus is a 2px solid `focus` ring with 2px offset on every interactive element.

## Type

- JetBrains Mono (700 for the wordmark and headlines, 400 for data) is the brand face, as in flok. The three weights ship in `fonts/` (SIL Open Font License); embed them and never load them from a CDN.
- UI words use `sans`, the system face: `title` once per page, `heading` for drawer sections, `body` for everything else, `label` for field labels and headers. Sentence case; no uppercase labels.
- Anything a person copies, compares or greps is mono with tabular figures: slugs, URLs, cron expressions, times, latencies, counts (`data`, `data-strong`, `code`). The one big number (uptime %) is `numeral`.

## Layout and components

- The UI is server-rendered HTML with htmx. The component functions in `components/bundle.js` return the exact markup each Go template partial must produce, and `bundle.css` styles it. There is no framework. The UI sends no inline styles (the CSP forbids them), so layout comes from `vk-*` classes only.
- Simplicity budget: the top bar holds four things: the mark, the project switcher with its three sections (Monitors, Incidents with the open count, Settings), search, and the user menu. A page has at most one filter bar, one list and one drawer. A form shows at most eight fields before one `Advanced` disclosure. Anything that does not fit belongs in the API or the YAML `apply` file.
- Forms: create and edit a monitor in the drawer (`…/m/new`, `…/m/{slug}/edit`), kind first, then only that kind's fields, then `Advanced`, then `As YAML`. Settings add and edit inline in a `Panel` that replaces the row; Save is the one primary. Validation errors sit under their field; results of an action (a test, a new key) are a `Notice` under the thing they are about.
- Org settings (`/o/{org}/admin`: Members, Projects, Agents) and instance admin (`/admin`: Orgs, Users, Server) are tabbed pages like project settings, reached from the switcher and user menus. Values vink doesn't own (roles from proxy groups, quotas on the org pages) are shown disabled or read-only with the reason on the row. An agent row opens a drawer like a monitor does.
- Sign-in, the proxy's 403 and "no org yet" are one card on `ground` with the mark above it and no top bar. They say what happened and what to do next, and show a request id instead of the reason, which goes to the log.
- The monitor list is 44px rows (`row-h`) with hairlines and no cards or zebra stripes. Sort by down, late, then name. The drawer (`drawer-w`, `surface`, `shadow-drawer`) and an open top-bar menu are the only things that cast a shadow.
- No modals. Destructive actions arm in place (`data-confirm`), and forms show errors inline.
- Live parts poll only while the tab is visible: list 15 s, drawer 10 s, status page 60 s. Use `ETag` so unchanged content costs nothing.
- Radii: `radius-xs` for pills, tags and uptime cells; `radius-sm` for controls; `radius-md` for panels and banners; `radius-tile` for app icon tiles.
- Under 640px the list keeps glyph, name and last observation, and the drawer becomes a full page.

## Iconography

- Monoline icons on a 16 grid with 1.5 strokes and round caps and joins, drawn in `currentColor`: the five state glyphs and six kind glyphs (heartbeat, http, tcp, dns, tls, icmp) in the Icons group. Ship them as one inline SVG sprite; the files in Icons are drawn in `#7E8D90` for display only.
- No emoji and no icon fonts. Never use an icon without a word or `title` nearby.

## Favicon and menu bar

- `favicon.svg` is the teal tile with the white mark. While any monitor in the open project is down, swap to `favicon-down.svg` (a `down` tile with a white cross), so a background tab shows the outage.
- A future menu-bar app follows flok: a template icon in black and alpha, with the solid down variant while anything is down. Optionally alternate the two every ~1150 ms, and stop when the window gets focus. The mark holds a cursor, so blinking is the one animation it is allowed.

## CLI and logs

- `vink version` and `vink doctor` print the ASCII lockup:

```
  ╭─────────╮
  │   ✓     │   vink 0.1.0
  │   ▁     │   heartbeat and uptime monitor
  ╰─────────╯   w4j.nl · MIT

[✓▁] vink  ·  12 monitors  ·  ● 10 up  ◐ 1 late  ◆ 1 down
```

- `-d` logs in colour with slogcolor, mapped to the palette: DEBUG `ink-muted`, INFO `accent`, WARN `late`, ERROR `down`. Colour is off when stdout is not a TTY or `NO_COLOR` is set. State words in CLI tables get their state glyph and colour, and never colour alone.

## Assets

- Logos: marks (teal, cyan, ink, down), lockups and wordmarks for light and dark grounds. The repo copies under `assets/brand/` use `currentColor` so docs and templates can tint them.
- Tiles: favicon, favicon-down, avatar (460px) and app icon (512px) on the `brand` tile at `radius-tile`.
- Banners: the README hero (1280×320) and social card (1280×640), in flok's layouts on `ground` with the 6px `accent` bar.
- Icons: state and kind glyphs.

## Consuming this system

- Files: `tokens.css` (every token as a CSS variable; light by default, dark by `prefers-color-scheme`, and `data-theme="light"` or `"dark"` forces a theme on `<html>` or on any element), `components/bundle.css` (every `vk-*` class), `components/bundle.js` (namespace `Vink`: functions that return HTML strings, plus `Vink.wire()` for Copy, two-step confirm and local switches). Load `tokens.css`, then `bundle.css`, then `bundle.js`.
- The functions are not React components and cannot be mounted with `x-import`. On a design canvas, write the `vk-*` markup they return (the screens in the handoff kit's `docs/design-system/screens/` are that markup) and link `tokens.css` and `components/bundle.css`. Put `class="vk-app"` and `data-theme` on the root element, and give JetBrains Mono its `@font-face` from uploaded font files.
- In the Go app, each function becomes an `html/template` partial with the same markup; `bundle.css`, `tokens.css`, the fonts and a small `wire` script ship in `static/`.
