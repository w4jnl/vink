# ObsRow

One observation or state change in a monitor's list: the state glyph, the clock, what it said, and a right cell with a run's duration or a check's latency. The drawer's "Recent observations" and "Events" and the history page's timeline are made of these.

**Provide** `state`, `clock` (with `abs` for the hover), `text`, optionally `title` (the hover on the text, such as the user agent), `right`, and, when the row has more to show, `facts` (`[key, value]` pairs: from, agent, method, run, took, exit, then the check's detail), `message` (the whole message when the row cut it) and `bodyHref` (the stored body as text). A row with any of those is a `<details>` that opens in place, like AuditRow, with a chevron at the left; `open` draws it open. With `bodyLoad` the body panel starts as a placeholder that htmx replaces with the body in a `Code` box when it scrolls into view (`hx-trigger="revealed"`); without JavaScript the placeholder is the link to the body as text.

**Use** inside a drawer section or a `vk-irows` list on the history page, newest first. The absolute time is the first fact, so a touch screen reaches what the clock's hover shows; every observation row therefore opens. Event rows have no facts and stay flat. Under 640px the clock column narrows and the panel loses its indent; on touch screens the row is taller. A row's words never change when it opens; the panel only adds.

**Don't** put actions in the panel, or open more than the row the person clicked.
