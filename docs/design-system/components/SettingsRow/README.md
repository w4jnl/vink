# SettingsRow

One row of a settings list (channel, route, window, page, API key): a two-line name with a mono detail, a few fixed-width cells, and at most two actions.

**Provide** `title` (or `titleHtml` for tags), `sub`, `cells` (`text` or `html`, `size` `s`/`m`/`l`, `mono`, `ink`), `actions`, optional `lead` (a route's order) and `muted` (disabled channel). Every row in one list uses the same cell sizes, so the columns line up without headers.

`prose: true` sets `sub` as a sentence instead of mono data (owner actions); `href` makes the title a link that opens the row's drawer (agents), `current` marks the open one; beside an open drawer the row keeps its name and first cell only. **Use** inside `.vk-srows`. A result for a row (a test) goes in `.vk-srow__note` right after it. Delete lives in the row's edit panel, except Revoke on API keys.
