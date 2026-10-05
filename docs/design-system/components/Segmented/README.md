# Segmented

Two to seven short options in one control: Period, Cron or OnCalendar; ro or rw; Once or Weekly; weekdays.

**Provide** `name`, `label` (the group's accessible name), `options` (strings or `{value, label}`), `value`; `multi: true` for several at once (weekdays), `mono` for data values.

**Use** where a dropdown would hide two or three choices. It is a radio or checkbox group underneath, so it works without JavaScript.

**Looks**: the selected option is filled with `accent` and labelled in `on-accent`, semibold, like a primary button, so the choice reads at a glance in both themes (at least 4.5 : 1 against the track).
