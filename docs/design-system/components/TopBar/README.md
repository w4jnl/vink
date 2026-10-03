# TopBar

The 52px header on every signed-in page: the mark, the project switcher, its three sections in a `nav`, search, and the user menu. That is the whole top bar; there is no fifth thing. Under 960px the mark's word goes and the search shrinks; under 640px the bar wraps into two rows, mark, switcher and user above, the section links on a 44px row below, and the search is hidden (the chips filter the list).

**Provide** `org`, `project`, `section` (`monitors`, `incidents`, `settings`), `incidents` (open incidents; shown as a `down` count, hidden at 0) and `user`. `hrefs` overrides the section links.

**Use** the switcher (`w4j / homelab`, a `details.vk-top__switch` right after the mark) to open the project menu (`menu`, see Menu); the three section links follow it in the `nav`. The user button opens `userMenu`. Both are `<details>`; `open: 'switcher'` or `'user'` draws one open. On org and instance pages `section` is `'none'`: no link is current and the switcher still shows the last project. The current section gets `aria-current="page"` and an `accent` underline. `/` focuses search wherever it is shown.

**Don't** add links for org admin or docs here; Org settings and New project live in the switcher menu, Instance admin, API reference and Sign out in the user menu. Status pages have no top bar.

```html
<a class="vk-top__link" href="/o/w4j/p/homelab/incidents">Incidents<span class="vk-top__count" title="2 open"><i class="vk-glyph vk-glyph--down" aria-hidden="true"></i>2</span></a>
```
