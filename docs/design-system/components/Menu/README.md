# Menu

The popover panel of the two `<details>` in the top bar: the project switcher (projects per org with their problems, New project, Org settings) and the user menu (Instance admin, API reference, Sign out). It is the only popover in vink.

**Provide** `groups`: `{ label, role, items: [{ label, href, current, meta, quiet }] }`. `meta` is trusted HTML, usually `StateCounts({ problems: true })`; `quiet` items are actions rather than places. Groups are separated by a hairline.

**Use** through `TopBar({ menu, userMenu, open })`. The menus are native `<details>`, so they open without JavaScript; `wire()` closes the other one, closes on Escape and on a click outside. Items are links, so every entry is a page load.

**Don't** put forms or destructive actions in a menu, or add a third popover. An open menu and the drawer are the only things that cast a shadow.
