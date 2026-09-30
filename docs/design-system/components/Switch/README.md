# Switch

On/off that takes effect at once, with the word beside the track: a channel's enabled state in its row.

**Provide** `checked` and `label` (read by screen readers: "ntfy enabled"). In the app the button carries `hx-post` to the toggle endpoint and the response swaps the row; without htmx, `wire()` flips it locally for previews.

**Don't** use a switch inside a form that has a Save button; use `Checkbox` there.
