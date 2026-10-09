# MonitorRow

One monitor in the list: state glyph, name and slug, kind, last observation, trend or next due, tags. The whole row is a link that opens the drawer.

**Provide** `state`, `name`, `slug`, `kind`, `last` (relative, with the one datum that matters: status code, latency, run duration), `lastAbs` (absolute time in the monitor's timezone, shown on hover), `points` for pull monitors or `next` for heartbeats, and its `tags` (usually up to three; the column shows the whole tags that fit).

**Use** 44px rows, hairline separators, no zebra striping, no card per row. Sort is `down`, `late`, then name. The list body is polled every 15 s with `hx-trigger="every 15s [document.visibilityState=='visible']"` and swapped whole. The tags column (200px, 112px under 960px) shows whole tags only: tags that don't fit are left out from the end, one tag wider than the column is cut with …, and the cell's `title` lists them all. Beside an open drawer under 1744px the row drops its tags (and under 1280px its trend); on a wider screen the docked drawer leaves room for every column. Under 640px the row is two lines, name then last observation, with the glyph beside both; kind, trend and tags go.

**Don't** colour the row background by state; the glyph and the `last` column carry it.

```html
<a class="vk-row" href="/o/w4j/p/homelab/m/api-health" hx-get="/o/w4j/p/homelab/m/api-health" hx-target="#drawer" hx-push-url="true">…</a>
```
