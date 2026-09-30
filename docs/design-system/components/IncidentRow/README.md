# IncidentRow

One incident: monitor, reason, when it opened, how long, and what happens next. Open incidents come first with an Ack button; acked ones say who acked; resolved ones are muted with the up glyph.

**Provide** `state` (`open`, `acked`, `resolved`), `name`, `slug`, `href` (the monitor), `reason`, `opened` (relative, with `openedAbs` on hover), `duration`, `ackedBy` or `resolved`.

**Use** 44px rows like the monitor list. Ack posts with htmx and swaps the row; acking silences repeats but keeps the incident open until the monitor is up.
