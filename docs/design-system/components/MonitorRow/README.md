# MonitorRow

One monitor in the list: state glyph, name and slug, kind, last observation, trend or next due, tags. The whole row is a link that opens the drawer.

**Provide** `state`, `name`, `slug`, `kind`, `last` (relative, with the one datum that matters: status code, latency, run duration), `lastAbs` (absolute time in the monitor's timezone, shown on hover), `points` for pull monitors or `next` for heartbeats, and up to three `tags`.

**Use** 44px rows, hairline separators, no zebra striping, no card per row. Sort is `down`, `late`, then name. The list body is polled every 15 s with `hx-trigger="every 15s [document.visibilityState=='visible']"` and swapped whole. Beside an open drawer the row drops its tags (and under 1280px its trend). Under 640px the row is two lines, name then last observation, with the glyph beside both; kind, trend and tags go.

**Don't** colour the row background by state; the glyph and the `last` column carry it.

```html
<a class="vk-row" href="/o/w4j/p/homelab/m/api-health" hx-get="/o/w4j/p/homelab/m/api-health" hx-target="#drawer" hx-push-url="true">…</a>
```
