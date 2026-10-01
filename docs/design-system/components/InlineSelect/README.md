# InlineSelect

A 28px select inside a list row that saves on change (`hx-post`, then the row is swapped): a member's role.

**Provide** `label` (the accessible name, "Role for Bram Jansen"), `name`, `options`, `value`, and `disabled` when vink doesn't own the value: roles that come from proxy groups, or the last owner's own role. Disabled values stay readable in `ink-muted`; the row's second line says why.
