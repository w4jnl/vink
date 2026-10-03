#!/usr/bin/env python3
"""Check every page of the demo instance at phone and tablet widths.

    scripts/demo/seed.sh up
    python3 scripts/demo/mobile.py            # iPhone 13 (390x844, touch) and iPad Mini (768x1024)
    scripts/demo/seed.sh down

For each page it asserts that nothing scrolls sideways (the document is no wider than the
viewport) and, on the drawer deep links, that the drawer is the page: #drawer visible and the
list hidden. Screenshots land in $DEMO_DIR/mobile/<name>-<width>.png. Exits non-zero on any
failure. Needs Playwright for Python with WebKit (pip install playwright && playwright install webkit)
and $DEMO_DIR/env (default /tmp/vink-demo/env) from seed.sh.
"""
import os
import pathlib
import re
import sys

from playwright.sync_api import sync_playwright

DEMO = pathlib.Path(os.environ.get("DEMO_DIR", "/tmp/vink-demo"))
OUT = DEMO / "mobile"
PROJECT = "/o/homelab/p/homelab"
ORG = "/o/homelab/admin"

PAGES = [
    ("list", PROJECT),
    ("drawer", PROJECT + "/m/nightly-backup"),
    ("new", PROJECT + "/m/new?kind=heartbeat"),
    ("new-http", PROJECT + "/m/new?kind=http"),
    ("edit", PROJECT + "/m/web/edit"),
    ("history", PROJECT + "/m/nightly-backup/history"),
    ("incidents", PROJECT + "/incidents"),
    ("settings-channels", PROJECT + "/settings/channels"),
    ("settings-channels-add", PROJECT + "/settings/channels?add=1"),
    ("settings-routes", PROJECT + "/settings/routes"),
    ("settings-maintenance", PROJECT + "/settings/maintenance"),
    ("settings-pages", PROJECT + "/settings/pages"),
    ("settings-keys", PROJECT + "/settings/keys"),
    ("org-members", ORG + "/members"),
    ("org-projects", ORG + "/projects"),
    ("org-agents", ORG + "/agents"),
    ("org-audit", ORG + "/audit"),
    ("instance-orgs", "/admin/orgs"),
    ("instance-users", "/admin/users"),
    ("instance-server", "/admin/server"),
    ("instance-audit", "/admin/audit"),
    ("account", "/account"),
    ("projects", "/projects"),
    ("status", "/s/homelab"),
]
DRAWER_PAGES = {"drawer", "new", "new-http", "edit"}


def env() -> dict:
    text = (DEMO / "env").read_text()
    return dict(re.findall(r'export (\w+)="([^"]*)"', text))


def main() -> None:
    e = env()
    base = e["DEMO_BASE"]
    OUT.mkdir(parents=True, exist_ok=True)
    failures = []
    with sync_playwright() as p:
        browser = p.webkit.launch()
        for device in ("iPhone 13", "iPad Mini"):
            d = p.devices[device]
            width = d["viewport"]["width"]
            ctx = browser.new_context(**d, color_scheme="dark")
            page = ctx.new_page()
            page.goto(f"{base}/login")
            page.fill("#username", e["DEMO_USER"])
            page.fill("#password", e["DEMO_PASSWORD"])
            page.click("button[type=submit]")
            page.wait_for_url(re.compile(r"/o/homelab/"))
            for name, path in PAGES + [("login", "/login")]:
                if name == "login":
                    ctx.clear_cookies()
                resp = page.goto(f"{base}{path}")
                if resp is None or resp.status >= 400:
                    status = resp.status if resp else "?"
                    if status == 403 and name.startswith("instance"):
                        print(f"{device:10} {name:24} skipped (403)")
                        continue
                    failures.append(f"{device} {name}: HTTP {status}")
                    continue
                page.wait_for_load_state("networkidle")
                doc = page.evaluate("[document.documentElement.scrollWidth, window.innerWidth]")
                scroll_w, inner_w = doc
                problems = []
                if scroll_w > inner_w:
                    problems.append(f"scrolls sideways: {scroll_w}px in {inner_w}px")
                if name in DRAWER_PAGES:
                    drawer = page.locator("#drawer").first
                    main = page.locator("main.vk-main").first
                    if not drawer.is_visible():
                        problems.append("drawer not visible")
                    if main.count() and main.is_visible():
                        problems.append("list still visible beside the drawer")
                page.screenshot(path=str(OUT / f"{name}-{width}.png"), full_page=True)
                flag = "  " + "; ".join(problems) if problems else ""
                print(f"{device:10} {name:24} {scroll_w}/{inner_w}{flag}")
                for pr in problems:
                    failures.append(f"{device} {name}: {pr}")
            ctx.close()
        browser.close()
    if failures:
        print("\nFAILED")
        for f in failures:
            print(" ", f)
        sys.exit(1)
    print("\nall pages fit")


if __name__ == "__main__":
    main()
