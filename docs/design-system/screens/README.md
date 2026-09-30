# vink screens

The target design for every signed-in page, the create form, settings, and the sign-in and access pages. Each screen is a static HTML file built from `../components/bundle.js`, so its markup is exactly what the Go templates must render; `*-dark.png` and `*-light.png` show both themes. Open an `.html` file in a browser to inspect it; add `data-theme="light"` or `"dark"` on `<html>` to force a theme.

The same screens are on the Claude Design canvas "vink screens" (dark row, light row), linked for click-through in Play.

| Screen | Route | Shows | Components |
| --- | --- | --- | --- |
| [monitors](monitors.html) | `/o/{org}/p/{project}` and `…/m/{slug}` | List with filter bar, drawer for the selected monitor. Beside an open drawer the list drops its tags column. | TopBar, Chip, MonitorRow, StateBadge, UptimeBar |
| [monitor-new-heartbeat](monitor-new-heartbeat.html) | `…/m/new` | Create form in the drawer: kind first, 7 fields, `Advanced` closed with its summary, ping URL, `As YAML` open. | KindPicker, FieldRow, Field, Segmented, Disclosure, PingUrl, Code |
| [monitor-new-http](monitor-new-http.html) | `…/m/new?kind=http` | The HTTP fields, one inline error (timeout ≥ interval), `Advanced` open: request, failures, body match, TLS. | as above, Checkbox |
| [incidents](incidents.html) | `…/incidents` | Open incidents first (Ack, acked by), resolved below muted; tag chips and a period filter. | IncidentRow, Chip, Segmented |
| [settings-channels](settings-channels.html) | `…/settings/channels` | Add-channel panel (ntfy fields), rows with enabled switch, a passed and a failed test result under their rows. | Tabs, Panel, SettingsRow, Switch, Notice |
| [settings-routes](settings-routes.html) | `…/settings/routes` | Ordered routes; route 2 in inline edit (tags, repeat, channels, send on). | SettingsRow, Panel, Checkbox, StateBadge |
| [settings-maintenance](settings-maintenance.html) | `…/settings/maintenance` | Add-window panel (once/weekly, weekdays, from/to, timezone, next occurrence); an active window. | Panel, Segmented, SettingsRow |
| [settings-pages](settings-pages.html) | `…/settings/pages` | Status page in inline edit (address prefix, tags order, public/password, custom domain). | Panel, Field (prefix), Segmented |
| [settings-keys](settings-keys.html) | `…/settings/keys` | Ping key with Copy and two-step Rotate; new API key shown once; create row; revoke. | Field, Notice, Segmented, SettingsRow |
| [login](login.html) | `/login` | Local sign-in with the one error message (never which field was wrong). | Field, Notice, Button |
| [proxy-denied](proxy-denied.html) | any UI route, proxy mode, no identity | 403 page: what happened, what to do, request id. The reason goes to the log only. | auth layout, `vk-kv` |
| [no-access](no-access.html) | any UI route, identity but no membership | Who the proxy said you are, your groups, the group pattern that grants access. | auth layout, `vk-kv` |
| [status](status.html) | `/s/{slug}` | Public status page (unchanged). | StatusBanner, UptimeBar |

## Behaviour the pictures can't show

- **Edit monitor** is the create form in the drawer at `…/m/{slug}/edit`: titled with the monitor's name, `KindPicker` locked, Save instead of Create monitor, and Delete monitor (two-step) at the right of the footer. Changing the kind is an API/YAML operation that recreates the monitor.
- **Kind switch**: changing the kind radio does `hx-get` of the same form with `?kind=…` and swaps the fields below the picker, keeping Name, Slug and Tags.
- **Live hints** ("Next runs", "Late at 03:00, down at 03:30", the failures sentence) come from a `hx-post` of the form to a small preview handler on `change`, debounced 300 ms; without JavaScript they appear after submit. That handler is an addition to docs/design.md: a UI-only route that validates and describes the spec without saving it.
- **Advanced** stays open when any field inside it has an error. The summary line always shows the current values.
- **Settings panels**: one open at a time; opening one hides the tab's Add button. Save swaps the panel for the row; Cancel restores the row. Delete is inside the edit panel (two-step), except Revoke on API keys.
- **Channel test** posts to `/api/v1/channels/{id}/test` and puts the result `Notice` under the row: the notifier's error verbatim in mono on failure.
- **Switch** posts the toggle at once (`hx-post`) and swaps the row; it never needs a Save.
- **New API key**: the plaintext appears once, in the `Notice` above the create row, until the page is left.
- **Top bar**: the Incidents count is open incidents (acked included) in the project and disappears at 0. The project switcher opens a menu of the user's projects; org admin lives there, sign-out in the user menu.
- **No inline styles**: the CSP (`style-src 'self'`) forbids them, so every layout piece is a `vk-*` class in `bundle.css`.
