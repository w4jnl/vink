# IncidentRow

One incident: monitor, reason, when it opened, how long, and what happens next. Open incidents come first with an Ack button; acked ones say who acked over when, on two lines (a long name is cut with …, its `title` says it whole); resolved ones are muted with the up glyph.

**Provide** `state` (`open`, `acked`, `resolved`), `name`, `slug`, `href` (the monitor), `reason`, `opened` (relative, with `openedAbs` on hover), `duration`, `ackedBy` and `ackedAt`, or `resolved`.

**Use** 44px rows like the monitor list; under 640px a row is two lines, glyph, name and the action above (who and when acked on one line), reason and the opening clock below, the duration left out. Ack posts with htmx and swaps the row; acking silences repeats but keeps the incident open until the monitor is up.
