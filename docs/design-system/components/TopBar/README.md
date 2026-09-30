# TopBar

The 52px header on every signed-in page: the mark, the project switcher with its three sections, search, and the user menu. That is the whole top bar; there is no fifth thing.

**Provide** `org`, `project`, `section` (`monitors`, `incidents`, `settings`), `incidents` (open incidents; shown as a `down` count, hidden at 0) and `user`. `hrefs` overrides the section links.

**Use** the switcher button (`w4j / homelab`) to open a menu of the user's projects; the three section links follow it inside the same `nav`. The current section gets `aria-current="page"` and an `accent` underline. `/` focuses search from anywhere.

**Don't** add links for org admin or docs here; org admin lives in the switcher menu, docs in the user menu. Status pages have no top bar.

```html
<a class="vk-top__link" href="/o/w4j/p/homelab/incidents">Incidents<span class="vk-top__count" title="2 open"><i class="vk-glyph vk-glyph--down" aria-hidden="true"></i>2</span></a>
```
