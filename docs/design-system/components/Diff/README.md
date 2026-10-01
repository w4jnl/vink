# Diff

A change to a stored spec as YAML lines: context in `ink-muted`, the old line marked `-`, the new line marked `+` on `accent-soft`. Diff lines never use state colours; red and green mean down and up in vink.

**Provide** `lines`: `[op, text]` with op `' '`, `'-'` or `'+'`. Secrets show as `***` on both sides. **Use** inside an AuditRow, and for the `apply --dry-run` preview if that ever gets a UI.
