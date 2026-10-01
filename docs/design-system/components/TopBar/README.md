# TopBar

The 52px header on every signed-in page: the mark, the project switcher with its three sections, search, and the user menu. That is the whole top bar; there is no fifth thing.

**Provide** `org`, `project`, `section` (`monitors`, `incidents`, `settings`), `incidents` (open incidents; shown as a `down` count, hidden at 0) and `user`. `hrefs` overrides the section links.

**Use** the switcher (`w4j / homelab`) to open the project menu (`menu`, see Menu); the three section links follow it inside the same `nav`. The user button opens `userMenu`. Both are `<details>`; `open: 'switcher'` or `'user'` draws one open. On org and instance pages `section` is `'none'`: no link is current and the switcher still shows the last project. The current section gets `aria-current="page"` and an `accent` underline. `/` focuses search from anywhere.

**Don't** add links for org admin or docs here; Org settings and New project live in the switcher menu, Instance admin, API reference and Sign out in the user menu. Status pages have no top bar.

```html
<a class="vk-top__link" href="/o/w4j/p/homelab/incidents">Incidents<span class="vk-top__count" title="2 open"><i class="vk-glyph vk-glyph--down" aria-hidden="true"></i>2</span></a>
```
