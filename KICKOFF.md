# Kickoff prompt for Claude Code

Unpack this folder as the repo root, `git init`, then start `claude` in it. Use plan mode for the first message, then paste:

---

Read CLAUDE.md, docs/design.md and docs/design-system/README.md, then look at docs/design-system/screens/monitors-dark.png and status-light.png.

We are building phase 0 only (foundation; gate: "replaces Healthchecks for my own cron jobs"). Make a plan that:
1. lists the phase 0 tasks from docs/design.md in the order you will do them, with the files each one creates;
2. names every Go dependency you will add and why;
3. lists the open decisions from the design doc's table that phase 0 depends on, with the default you'll take.

Do not write code until I approve the plan. After approval, work task by task: implement, add tests, run make lint and make test, then commit. Stop after phase 0 and show me how to run it end to end: `vink admin init`, create a heartbeat monitor, ping it with curl, watch it go late and then down, and receive the email or webhook alert.

---

## Phase 1

The screens are designed now (docs/design-system/screens/README.md). Copy this kit's `docs/design-system/`, `CLAUDE.md` and `KICKOFF.md` over the repo (bundle.css, bundle.js and tokens.css changed), commit, then in plan mode paste:

---

Read docs/design-system/screens/README.md and look at every screen PNG it lists, then the new components in docs/design-system/components/ (TopBar, Tabs, FieldRow, Checkbox, Switch, Segmented, KindPicker, Disclosure, Notice, Code, Panel, IncidentRow, SettingsRow).

Plan phase 1 from docs/design.md, same rules as phase 0. The plan must:
1. start with a UI task that brings the phase 0 pages up to the designed screens: the top bar with its section links, the create/edit form in the drawer, incidents, the settings tabs for channels, routes and keys, the login page, and the proxy 403 and "no org yet" pages. List each template partial you add or change and the component whose markup it copies;
2. then the phase 1 tasks in order, with the maintenance and status pages tabs, the kind-specific forms and the YAML view built to their screens;
3. name every Go dependency you add, and the open decisions from the design doc that phase 1 depends on, with your default;
4. call out anything in the screens README that is not in docs/design.md yet (the form preview handler) and propose how to add it.

Do not write code until I approve the plan. Stop at the phase 1 gate and show me the homelab replacing Uptime Kuma: an HTTP, a TCP and a TLS monitor, a maintenance window, and the public status page.

---

## Phase 2

"Plan phase 2 from docs/design.md, same rules. Build the agents UI to docs/design-system/screens/ (org-agents, org-agent-detail, monitor-edit)."

## Phase 3

The phase 3 screens are designed now: members with invites and owner actions, the audit log, instance admin users and server, the account page with two-factor, the sign-in code step, OIDC sign-in and the expired invite. Copy this kit's `docs/design-system/` and `KICKOFF.md` over the repo (bundle.css, bundle.js, index.d.ts, README.md and five new components changed), commit, then in plan mode paste:

---

Read docs/design-system/screens/README.md, the section "Phase 3: invites, audit log, instance admin, accounts" first, and look at the PNGs for org-members-manage, org-audit, instance-users, instance-server, account, account-totp-setup, login-totp, login-oidc and invite-expired. Then read the new components in docs/design-system/components/ (AuditRow, Diff, Qr, RecoveryCodes, Divider) and the `otp` option on Field and `prose` on SettingsRow.

Plan phase 3 from docs/design.md, same rules as before. The plan must:
1. start with the data model: the `audit` table (actor, actor kind, org, project, action, target, before/after spec as YAML with secrets masked, via, request id, remote address), how every admin action and API apply writes to it in the same transaction as the change, and the one query that merges it with `events` for the log; plus the columns for two-factor (secret encrypted at rest, recovery code hashes), invites, reset tokens and disabled users. Give the migrations;
2. then the tasks in order: org admin UI (members with invites, roles, owner actions; projects; quotas), instance admin (orgs, users, server), audit log (org and instance), account page and TOTP (enrolment, the sign-in code step, recovery codes, `auth.local.totp = "optional" | "required"`), native OIDC (`coreos/go-oidc`, PKCE, the same `subject + groups` mapping as proxy mode), then the Postgres backend and the Terraform provider. For each UI task, list the template partials and the component whose markup each copies;
3. name every Go dependency you add (TOTP and QR libraries included; the QR must render as inline SVG on the server, no JavaScript and no external service), and the open decisions phase 3 depends on, with your default;
4. list everything in the screens README that is not in docs/design.md yet (routes, the `audit` table, the new config keys) and propose the doc changes, so I can approve them with the plan;
5. include tests for: cross-tenant access to the audit log and admin pages (404), the last-owner rule, invite expiry and single use, TOTP window and replay, recovery code single use, and the OIDC callback's state and nonce checks.

Do not write code until I approve the plan. Stop at the phase 3 gate and show me a second org onboarded without hand-holding: create the org with a quota as instance admin, invite its first owner, accept the invite, turn on two-factor, add a project and a monitor, and find all of it in the audit log.

---
