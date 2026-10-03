# Tabs

Sections of one page, as links: the settings tabs Channels, Routes, Maintenance, Status pages, Keys. Each tab is its own URL (`/settings/{tab}`), so a tab is a page load, not client state.

**Provide** `tabs` (`id`, `label`, `count`, `href`) and `current`. Counts are mono and optional.

**Use** once per page, under the page title. The current tab has `aria-current="page"` and the `accent` underline, like the top bar. Under 640px the strip scrolls sideways without a scrollbar and the current tab is scrolled into view on load.
