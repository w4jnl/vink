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

Later phases: "Plan phase 1 from docs/design.md, same rules." For screens that aren't designed yet (create form, settings, org admin), design them in Claude Design with the vink design system first, then save the result under docs/design-system/screens/ before asking Claude Code to build them.
