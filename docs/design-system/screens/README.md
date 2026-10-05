# vink screens

The target design for every signed-in page, the create form, settings, and the sign-in and access pages. Each screen is a static HTML file built from `../components/bundle.js`, so its markup is exactly what the Go templates must render; `*-dark.png` and `*-light.png` show both themes. Open an `.html` file in a browser to inspect it; add `data-theme="light"` or `"dark"` on `<html>` to force a theme.

The people, hosts, keys and incidents in the screens are made up.

| Screen | Route | Shows | Components |
| --- | --- | --- | --- |
| [monitors](monitors.html) | `/o/{org}/p/{project}` and `…/m/{slug}` | List with filter bar, drawer for the selected monitor. Beside an open drawer the list drops its tags column. | TopBar, Chip, MonitorRow, StateBadge, UptimeBar |
| monitor-history (no picture yet; the markup is the live `history.html` and `_history.html` templates) | `…/m/{slug}/history` | The drawer's head over the compact 24 h and 90 d bars, then one timeline of observations and state changes, newest first, grouped by day; kind chips and a period or exact window in one filter bar; Older at the bottom. | StateBadge, Tag, UptimeBar, Chip, Segmented, Notice, Button, `vk-listhead`, `vk-obs` |
| [monitor-new-heartbeat](monitor-new-heartbeat.html) | `…/m/new` | Create form in the drawer: kind first, 7 fields, `Advanced` closed with its summary, ping URL, `As YAML` open. | KindPicker, FieldRow, Field, Segmented, Disclosure, PingUrl, Code |
| [monitor-new-http](monitor-new-http.html) | `…/m/new?kind=http` | The HTTP fields, one inline error (timeout ≥ interval), `Advanced` open: request, failures, body match, TLS. | as above, Checkbox |
| [incidents](incidents.html) | `…/incidents` | Open incidents first (Ack, acked by), resolved below muted; tag chips and a period filter. | IncidentRow, Chip, Segmented |
| [settings-channels](settings-channels.html) | `…/settings/channels` | Add-channel panel (ntfy fields), rows with enabled switch, a passed and a failed test result under their rows. | Tabs, Panel, SettingsRow, Switch, Notice |
| [settings-routes](settings-routes.html) | `…/settings/routes` | Ordered routes; route 2 in inline edit (tags, repeat, channels, send on). | SettingsRow, Panel, Checkbox, StateBadge |
| [settings-maintenance](settings-maintenance.html) | `…/settings/maintenance` | Add-window panel (once/weekly, weekdays, from/to, timezone, next occurrence); an active window. | Panel, Segmented, SettingsRow |
| [settings-pages](settings-pages.html) | `…/settings/pages` | Status page in inline edit (address prefix, tags order, public/password, custom domain). The live panel adds an Incidents select after Tags, see [Status pages](#status-pages). | Panel, Field (prefix), Segmented |
| [settings-keys](settings-keys.html) | `…/settings/keys` | Ping key with Copy and two-step Rotate; new API key shown once; create row; revoke. | Field, Notice, Segmented, SettingsRow |
| [login](login.html) | `/login` | Local sign-in with the one error message (never which field was wrong). | Field, Notice, Button |
| [proxy-denied](proxy-denied.html) | any UI route, proxy mode, no identity | 403 page: what happened, what to do, request id. The reason goes to the log only. | auth layout, `vk-kv` |
| [no-access](no-access.html) | any UI route, identity but no membership | Who the proxy said you are, your groups, the group pattern that grants access. | auth layout, `vk-kv` |
| [status](status.html) | `/s/{slug}` | Public status page: banner, groups, 90-day bars, open incidents. The live page adds groups by project on an org page and, when the page asks, the incidents resolved lately, see [Status pages](#status-pages). | StatusBanner, UptimeBar |
| [switcher-menu](switcher-menu.html) | any signed-in page | The project switcher open: projects per org with their down and late counts, New project, Org settings, a second org where you are viewer. | TopBar, Menu, StateCounts |
| [org-members](org-members.html) | `/o/{org}/admin/members` | Members with roles (editable for local accounts, locked for proxy groups), an invite just created with its one-time link. | Tabs, SettingsRow, Avatar, InlineSelect, Notice |
| [org-projects](org-projects.html) | `/o/{org}/admin/projects` | Quota line, Add project panel, projects with their state counts. | Usage, Panel, SettingsRow, StateCounts |
| org-pages (no picture yet; the markup is the live `settings.html` page templates) | `/o/{org}/admin/pages` | The org's status pages: rows like the project tab's, and the same panel with Projects and Group by added. | Tabs, Panel, Field, Checkbox, Segmented, SettingsRow |
| [org-agents](org-agents.html) | `/o/{org}/admin/agents` | A new agent's token and `vink agent` command shown once; agents connected, offline, waiting. | Notice, Code, SettingsRow, StateBadge, Usage |
| [org-agent-detail](org-agent-detail.html) | `/o/{org}/admin/agents/{name}` | The offline agent in the drawer: why its monitors are late, connection details, assigned monitors. | SettingsRow (current), drawer, Notice, MonitorRow |
| [monitor-edit](monitor-edit.html) | `…/m/{slug}/edit` | The edit form: Kind locked, Run from an agent first in Advanced, Save changes and Delete monitor. | KindPicker (locked), Segmented, Field, Disclosure |
| [instance-orgs](instance-orgs.html) | `/admin/orgs` | Instance admin: Add org with first owner and quotas, orgs with quota meters, delete only when empty. | Tabs, Panel, SettingsRow, Usage |
| [invite](invite.html) | `/invite/{token}` | Accepting an invite as a new local account. | auth layout, Field |
| [org-members-manage](org-members-manage.html) | `/o/{org}/admin/members` | Phase 3, as an owner: the Invite panel open, invites open, expired and used, and Owner actions (Transfer ownership, Delete org disabled while projects exist). | Panel, SettingsRow (prose), InlineSelect, Tag |
| [org-audit](org-audit.html) | `/o/{org}/admin/audit` | Audit log: kind chips, project, who and period filters, rows per day; a change opened with its YAML diff, an API apply with request details, state flips by vink. | AuditRow, Diff, Chip, InlineSelect, Segmented |
| [instance-users](instance-users.html) | `/admin/users` | Every user with source, orgs and instance-admin flag; Bram in the edit panel: instance admin, reset two-factor, password reset link shown once, Disable account. | Chip, SettingsRow, Panel, Checkbox, Notice |
| [instance-server](instance-server.html) | `/admin/server` | Read-only facts (build, database, sign-in, network and health) with a warning when the last backup is old. | Notice, Panel, `vk-kv`, `vk-facts` |
| [account](account.html) | `/account` | Profile, password, two-factor just turned on with the ten recovery codes shown once, sessions with Sign out. | Field, SettingsRow, Notice, RecoveryCodes |
| [account-totp-setup](account-totp-setup.html) | `/account` (setup open) | Two-factor setup: QR code, the same key typed out with Copy, the six-digit code field. | Panel, Qr, Field (otp) |
| [login-totp](login-totp.html) | `/login/code` | The second sign-in step after the password, with the wrong-code error and the recovery-code link. | auth layout, Field (otp) |
| [login-oidc](login-oidc.html) | `/login` with OIDC on | Continue with the provider first; the local form below a divider for break-glass and service users. | auth layout, Divider, Button |
| [invite-expired](invite-expired.html) | `/invite/{token}` (expired or used) | The invite card without fields: who invited, the role, when it ran out, Go to sign in. | auth layout, `vk-kv` |

## Behaviour the pictures can't show

- **Edit monitor** is the create form in the drawer at `…/m/{slug}/edit`: titled with the monitor's name, `KindPicker` locked, Save instead of Create monitor, and Delete monitor (two-step) at the right of the footer. Changing the kind is an API/YAML operation that recreates the monitor.
- **Kind switch**: changing the kind radio does `hx-get` of the same form with `?kind=…` and swaps the fields below the picker, keeping Name, Slug and Tags.
- **Live hints** ("Next runs", "Late at 03:00:30, down at 03:30", the failures sentence) come from a `hx-post` of the form to a small preview handler on `change`, debounced 300 ms; without JavaScript they appear after submit. That handler is an addition to docs/design.md: a UI-only route that validates and describes the spec without saving it.
- **Advanced** stays open when any field inside it has an error. The summary line always shows the current values.
- **Settings panels**: one open at a time; opening one hides the tab's Add button. Save swaps the panel for the row; Cancel restores the row. Delete is inside the edit panel (two-step), except Revoke on API keys.
- **Channel test** posts to `/api/v1/channels/{id}/test` and puts the result `Notice` under the row: the notifier's error verbatim in mono on failure.
- **Switch** posts the toggle at once (`hx-post`) and swaps the row; it never needs a Save.
- **New API key**: the plaintext appears once, in the `Notice` above the create row, until the page is left.
- **Top bar**: the Incidents count is open incidents (acked included) in the project and disappears at 0. The project switcher opens a menu of the user's projects; org admin lives there, sign-out in the user menu.
- **No inline styles**: the CSP (`style-src 'self'`) forbids them, so every layout piece is a `vk-*` class in `bundle.css`.

### Org settings, instance admin, agents

- **Menus**: the switcher and the user button are `<details class="vk-popover">` in every page, so they open without JavaScript. The switcher lists every org and project the user can see, with problems only (down and late counts, or "all up"); New project and Org settings appear only for admins and owners. The user menu holds Account, Instance admin (instance admins only), API reference and Sign out (to `auth.proxy.logout_url` in proxy mode). `wire()` closes one when the other opens, on Escape, and on a click outside.
- **Org pages** have no current section in the top bar; the switcher keeps showing the last project, and Monitors, Incidents and Settings return to it.
- **Roles**: a role select saves on change and swaps the row. Memberships with `source = header` (proxy groups or `default_org`) are read-only, Remove is disabled, and the row's second line names the group. An owner cannot demote or remove themselves while they are the last owner. Transfer ownership and Delete org live at the bottom of Members for owners (see org-members-manage).
- **Invites**: Invite opens a panel with "For" (a note) and Role; creating it shows the link once in a note under the new row. Invites are local accounts only, work once, expire after 7 days, and can be revoked. The invite page shows who invited you, the role and the expiry; an expired or used link shows the same card with "This invite has expired" and no fields.
- **Quotas** are set by instance admins and read-only for org admins. Creating a monitor or agent past a quota fails with an inline error that names the limit ("This org can have 5 agents; ask the instance admin for more.").
- **Agents**: Add agent asks for a name and labels, then shows the token and the full `vink agent` command once. Rows open the agent drawer; beside it the list keeps name and state only. Revoke token disconnects the agent at once; its monitors turn late with reason agent offline and stay late until they are moved or it comes back. The agent quota counts agents, not connections.
- **Run from** (phase 2): This server, An agent (select with each agent's state) or Agents with labels (a `site=dc1` selector; the least loaded matching agent runs it). The interval hint rises to 30s when an agent runs the check.
- **Routes that are additions to docs/design.md**: `/o/{org}/admin/{members|projects|agents}` and `…/agents/{name}` (the doc has one `/o/{org}/admin` page), `/admin/{orgs|users|server}` for instance admins, and `/invite/{token}`. Add them to the page inventory when you build these.
- **Instance admin** (`/admin`): Orgs, Users and Server are tabs in the same shape; all three are drawn.

### Phase 3: invites, audit log, instance admin, accounts

- **Invite panel**: For (a note, not a username) and Role; Create link adds the open row with the link shown once in a note under it (as in org-members). Open invites can be revoked; expired ones are removed with Remove; used ones stay, muted, with "joined as <username>" and no action. The counts sit in the list heading.
- **Owner actions** (owners only): Transfer ownership picks a member from an inline select and confirms two-step; the old owner stays on as admin. Delete org is disabled with the reason in the row while the org has projects; when it has none it confirms two-step and goes to `/`.
- **Audit log** lists, newest first: config changes by people and API keys (create, update, delete, apply), access events (sign-ins with their method, failed sign-ins, invites, role changes, key create/rotate/revoke, two-factor on/off/reset) and state flips written by the checker or ping handler. It is one query over the `events` table and a new `audit` table for admin actions; rows are never edited or deleted and, like `events`, are not pruned by the retention job.
  - Chips filter by kind (changes, access, state) and are additive; the project and who selects and the period are query parameters, so a filtered view is a link. 50 rows per page, grouped by day in the viewer's timezone, with "Older" at the bottom.
  - A row with a diff or meta is a `<details>`; the diff is the stored YAML before and after, secrets as `***`. Diffs never use state colours.
  - `via` is `web`, `api <key prefix>`, `cli`, `checker` or `ping`. Viewers and members see the log for their projects; org events (members, invites, keys) are for admins and owners. Instance admins have the same list across all orgs at `/admin/audit` with an org column; it is not drawn because it is the same component.
- **Users** (instance admins): chips by source and flag. Edit opens the panel in place. Instance admin is a checkbox (saved with Save); proxy and OIDC users get it from `instance_admin_group` instead, so for them it is read-only with the group named. Reset two-factor and Make a reset link only exist for local accounts; the reset link is shown once in an ok Notice and goes to `/reset/{token}` (a new-password form in the auth layout, not drawn: same shape as invite). Disable account signs the user out everywhere and blocks sign-in; the row turns muted and Edit offers Enable. You cannot disable or demote yourself.
- **Server** is read-only; vink.toml is the source. The backup warning appears when `vink admin backup` has not run for more than 24 h (or never).
- **Account** (`/account`, from the user menu for everyone): Profile and Sessions for all; Password and Two-factor only for local accounts. Turning two-factor on opens the setup panel in place: the QR (server-rendered SVG, black on white in both themes) and the same key typed out; the code must verify before it is saved. Right after, the row shows on with the ten recovery codes once in an ok Notice. New codes replaces the set (two-step); Turn off asks for the password. `auth.local.totp = "optional" | "required"`: required sends a local user without two-factor to the setup panel right after the password, before any other page.
- **Sign-in code** (`/login/code`): after a correct password for a user with two-factor; the same error for a wrong or reused code; 5 wrong codes lock the attempt and send back to `/login`. "Use a recovery code" swaps the field for a recovery-code field (mono, not otp).
- **OIDC sign-in**: when `auth.oidc` is on, `/login` leads with "Continue with <display name>" and keeps the local form below the divider while local accounts are on. With only OIDC, the button is the whole card and `/login` may redirect straight to the provider (`auth.oidc.auto_redirect`). Errors from the callback come back to `/login` as one Notice with the provider's error code in mono.
- **Expired invite**: the same card without fields; a used link shows it too, so a link never tells whether someone joined.
- **Routes that are additions to docs/design.md** (phase 3): `/o/{org}/admin/audit`, `/admin/users`, `/admin/server`, `/admin/audit`, `/account`, `/login/code`, `/reset/{token}`, `/auth/oidc/callback`; the `audit` table; and config keys `auth.local.totp`, `auth.oidc.display_name`, `auth.oidc.auto_redirect`, `auth.oidc.instance_admin_group`. Add them to the page inventory and config reference when you build these.

### Monitor history

- **One timeline**, not tabs: observations and state changes interleaved, newest first, grouped by day under `vk-listhead` headings in the monitor's timezone (the heartbeat's own, else the project's). Rows are `ObsRow`; an event row carries the glyph of the state entered and reads `up → down · fail signal`.
- **Rows open in place**, like the audit log: an observation with facts (from, agent, method, run, took, exit, then the check's detail), a message longer than the row, or a stored body is a `<details>` with a chevron at the left. The panel shows the whole message, the facts in a `vk-kv` block and the body in a `Code` box with Copy, loaded by htmx when the panel is revealed; without JavaScript the panel holds the link to the body as text. Event rows stay flat. The drawer's rows are the same component.
- **Filter bar** (the page's one): chips `All · ok · failures · runs · changes` are single-select and submit `kind`; the pressed chip submits an empty value and clears. `ok` is every successful ping or check, warn checks included; `failures` is fail pings, non-zero exits, run timeouts and failed checks, confirming attempts included; `runs` is start and log; `changes` is state flips only. The period `24 h · 7 d · 30 d · 90 d` (default 7 d) is a mono Segmented. An exact window comes from `since`/`until` in the URL: the Segmented is then unchecked and the window rides in hidden inputs, so a chip keeps it and a period replaces it. Each day heading's date links to that day's window.
- **Older** sits in a `vk-actions` row at the end and carries `hx-trigger="revealed"`: the next 50 rows and the next Older replace it as it scrolls into view. Without JavaScript it is a plain link to the same page from that cursor. A page that continues a day does not repeat the heading.
- **Width**: the head is a `vk-section`, so the title row, badge, summary and both bars share the list's `--content-max` column; on a wide screen the page is one left-aligned column, as the audit log is.
- **Live**: the head polls every 10 s while the tab is visible, with the stream's newest row on its URL; when the server has newer rows it renders a `Notice` with Reload. The stream is a snapshot until then, so loaded pages never shift under the reader.
- **Actions** (Pause, Resume, Check now) post plain forms with a hidden `next` and come back to the page; Edit and Back are links, Back to the list with the drawer open.

### Status pages

- **Two owners.** A project's pages live under its settings, for members who can edit; an org's
  pages under org settings, for its admins and owners, as a Status pages tab between Projects and
  Agents. Both are served at `/s/{slug}`, and the address is unique across the instance.
- **The org panel** is the project panel with two more fields after Address: Projects, a
  `vk-checks` set with one Checkbox per project of the org (none ticked means every project, new
  ones included, which the hint says), and Group by, a Segmented `Project · Tag`. Tags then
  filters across the chosen projects. Its rows read the projects ("every project" or their names)
  where the project tab reads the tags.
- **Incidents** is a select on both panels: `Open incidents` (the default), `Open and the last
  7 days`, `30 days`, `90 days`, or `None`. The row shows it in a cell.
- **Groups.** A project page, and an org page grouped by tag, have one `vk-status__group` per tag
  in `match_tags` order, as before. An org page grouped by project has one per project, named
  after it, in the order the page lists them (by name when it lists none). On an org page grouped
  by tag, a monitor's name is followed by its project's name in `vk-muted`, since two projects
  may name a monitor alike.
- **Past incidents** is a `vk-status__group` after Open incidents, headed "Past incidents", with
  one `vk-status__item` per incident resolved in the window, newest first: the monitor (with its
  project on an org page) and, in `vk-muted vk-mono`, the day and time it opened and how long it
  lasted (`3 Oct 09:29 · 12 min`). Nothing says why: the reason stays inside vink. With no
  incident in the window the group reads "No incidents in the last 30 days." in `vk-muted`.
- **Time** is the project's timezone on a project page; on an org page, the timezone its projects
  share, else UTC, and the footer names it.
- **Badges** stay `/s/{slug}/badge/{monitor}.svg`; on an org page where two projects use the slug,
  `/s/{slug}/badge/{project}/{monitor}.svg` names one.

### Phone

Boards of width 390 in `index.json` are the same screens rendered at a phone width by `scripts/kit/screens.py` (Playwright over the kit's own HTML and CSS, 390×844 at 2x, dark and light): `<file>-phone-dark.png` and `<file>-phone-light.png` next to the desktop pictures. They show what the pictures above cannot: the two-row top bar, the drawer as the page (`monitors.html` has the drawer open, so its phone board is the drawer; the `list` variant removes the aside and adds `vk-page--full` to show the list), two-line rows, stacked form fields with the kind picker two across, wrapped settings rows, the scrolling tab strip. The desktop pictures were made with a tool outside this repo; only the phone pictures are regenerated here. The kit's `status.html` predates the UptimeBar component, so the status page's phone look is checked on the live page instead (`scripts/demo/mobile.py`).
