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

Later phases: "Plan phase N from docs/design.md, same rules. Build the UI to docs/design-system/screens/." Every screen in docs/design.md is designed now, including org settings, agents and instance admin (phases 2 and 3). Anything new (the TOTP step, the audit log view) gets designed in Claude Design with the vink design system first and saved under docs/design-system/screens/ before asking for the build.
