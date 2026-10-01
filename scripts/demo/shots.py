#!/usr/bin/env python3
"""Capture the README screenshots from the demo instance scripts/demo/seed.sh leaves running.

    scripts/demo/seed.sh up
    python3 scripts/demo/shots.py          # writes assets/readme/*.png
    scripts/demo/seed.sh down

Needs Playwright for Python with Chromium (pip install playwright && playwright install chromium)
and $DEMO_DIR/env (default /tmp/vink-demo/env). 1440×900 viewport at 2x; the theme is the OS
preference the page is told to follow.
"""
import os
import pathlib
import re
import sys

from playwright.sync_api import sync_playwright

ROOT = pathlib.Path(__file__).resolve().parent.parent.parent
OUT = ROOT / "assets" / "readme"
DEMO = pathlib.Path(os.environ.get("DEMO_DIR", "/tmp/vink-demo"))


def env() -> dict:
    text = (DEMO / "env").read_text()
    return dict(re.findall(r'export (\w+)="([^"]*)"', text))


def main() -> None:
    e = env()
    base = e["DEMO_BASE"]
    with sync_playwright() as p:
        browser = p.chromium.launch()

        def context(scheme: str):
            ctx = browser.new_context(viewport={"width": 1440, "height": 900}, device_scale_factor=2, color_scheme=scheme)
            page = ctx.new_page()
            page.goto(f"{base}/login")
            page.fill("#username", e["DEMO_USER"])
            page.fill("#password", e["DEMO_PASSWORD"])
            page.click("button[type=submit]")
            page.wait_for_url(re.compile(r"/o/homelab/"))
            return ctx, page

        # 1. the monitor list, dark, with nightly-backup's drawer open on the failed run
        ctx, page = context("dark")
        page.goto(f"{base}/o/homelab/p/homelab/m/nightly-backup")
        page.wait_for_selector("#drawer")
        page.wait_for_selector("text=exit 1")
        page.screenshot(path=str(OUT / "monitors-dark.png"))
        print("monitors-dark.png")

        # 2. the create form, dark, a cron heartbeat with the next-runs hint. Query values
        # prefill the form and the server fills the hints on that first render (the live
        # preview's hx-trigger does not fire under htmx 4, see the PR).
        page.goto(f"{base}/o/homelab/p/homelab/m/new?kind=heartbeat&name=Nightly+restic&slug=nightly-restic&schedule_type=cron&schedule=0+3+*+*+*&grace=30m&tags=backup,prod")
        page.wait_for_selector("text=Next runs:", timeout=10_000)
        page.screenshot(path=str(OUT / "new-monitor-dark.png"))
        print("new-monitor-dark.png")
        ctx.close()

        # 3. the org audit log, light, with the newest change opened on its diff
        ctx, page = context("light")
        page.goto(f"{base}/o/homelab/admin/audit")
        # the newest row with facts opens by itself; open the newest one with a diff instead
        page.wait_for_selector("details.vk-audit")
        page.evaluate("document.querySelectorAll('details.vk-audit[open]').forEach(d => { d.open = false })")
        page.locator("details.vk-audit:has(.vk-diff)").first.locator("summary").click()
        page.wait_for_selector("details.vk-audit[open] .vk-diff")
        page.evaluate("window.scrollTo(0, 0)")
        box = page.locator("details.vk-audit[open]").first.bounding_box()
        height = min(int(box["y"] + box["height"]) + 24, 1800)
        page.screenshot(path=str(OUT / "audit-light.png"), full_page=True, clip={"x": 0, "y": 0, "width": 1440, "height": height})
        print("audit-light.png")

        # 4. the public status page, light, full height up to 1200 px
        page.goto(f"{base}/s/homelab")
        page.wait_for_selector(".vk-bar, .vk-uptime, meter, .vk-status", timeout=10_000)
        height = min(page.evaluate("document.documentElement.scrollHeight"), 1200)
        page.screenshot(path=str(OUT / "status-light.png"), full_page=True, clip={"x": 0, "y": 0, "width": 1440, "height": height})
        print("status-light.png")
        ctx.close()
        browser.close()
    for name in ("monitors-dark", "new-monitor-dark", "audit-light", "status-light"):
        f = OUT / f"{name}.png"
        print(f"{f.name}: {f.stat().st_size // 1024} KB")
    if any((OUT / f"{n}.png").stat().st_size > 600 * 1024 for n in ("monitors-dark", "new-monitor-dark", "audit-light", "status-light")):
        print("a capture is over 600 KB; run oxipng -o4 or pngquant on it", file=sys.stderr)


if __name__ == "__main__":
    main()
