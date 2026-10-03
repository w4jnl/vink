# UptimeBar

History as one cell per period: 90 days on the status page, 24 hours (`compact`) in the drawer. A cell is the worst state seen in its period; `none` means no data.

**Provide** `days` (oldest first), `label` for screen readers ("99.94% up over 90 days") and optionally a three-part `legend`.

**Use** the full-height bar only on the status page. Cells are 2px apart with `radius-xs`-scale corners, 1px apart under 640px so 90 of them fit a phone.
