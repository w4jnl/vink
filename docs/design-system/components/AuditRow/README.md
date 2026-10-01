# AuditRow

One line of the audit log: when, who, what, where, and through what. Three kinds share it: a change made by a person or API key (with a diff of the stored spec), an access event (sign-ins, roles, invites, keys), and a state flip made by vink itself (its state glyph instead of an avatar).

**Provide** `time` (and `timeAbs` for the hover), `actor` with `actorKind` (`user`, `key`, `system`; `state` picks the glyph for `system`), `text` (trusted HTML: the target in `<code>`), `scope` (the project or org), `via` (`web`, `api <key prefix>`, `cli`, `checker`, `ping`), and optionally `diff` and `meta` (`[key, value]` pairs such as the request id). A row with a diff or meta is a `<details>` that opens in place; `open` draws it open.

**Use** inside `.vk-audits`, grouped by day under a `vk-listhead`, newest first, 50 per page with "Older" at the bottom. Rows are never edited or deleted.
