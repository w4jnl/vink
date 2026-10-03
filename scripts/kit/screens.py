#!/usr/bin/env python3
"""Render the phone boards of the design-system kit.

    python3 scripts/kit/screens.py            # every board of width 390 in screens/index.json
    python3 scripts/kit/screens.py login      # one file

Opens the kit's own screens/<file>.html (which link ../tokens.css and ../components/bundle.css)
at 390x844 and 2x, dark then light, and writes <file>-phone-dark.png and <file>-phone-light.png
next to the HTML. A board's "variant": "list" shows the monitor list instead of the open drawer.
The desktop boards (width 1280) were made outside this repo and are left alone.
Needs Playwright for Python with Chromium (pip install playwright && playwright install chromium).
"""
import json
import pathlib
import sys

from playwright.sync_api import sync_playwright

ROOT = pathlib.Path(__file__).resolve().parent.parent.parent
SCREENS = ROOT / "docs" / "design-system" / "screens"
MAX_HEIGHT = 2400


def main() -> None:
    boards = json.loads((SCREENS / "index.json").read_text())
    only = sys.argv[1:]
    todo = [b for b in boards if b.get("width") == 390 and (not only or b["file"] in only)]
    if not todo:
        sys.exit("no phone boards to render")
    with sync_playwright() as p:
        browser = p.chromium.launch()
        for b in todo:
            html = SCREENS / f"{b['file']}.html"
            for scheme in ("dark", "light"):
                ctx = browser.new_context(viewport={"width": 390, "height": 844}, device_scale_factor=2, color_scheme=scheme, has_touch=True, is_mobile=True)
                page = ctx.new_page()
                page.goto(html.as_uri())
                page.wait_for_load_state("networkidle")
                if b.get("variant") == "list":
                    page.evaluate("""() => {
                        const page = document.getElementById('page') || document.querySelector('.vk-page');
                        const drawer = document.getElementById('drawer') || document.querySelector('.vk-drawer');
                        if (drawer) drawer.remove();
                        if (page) page.classList.add('vk-page--full');
                    }""")
                height = min(page.evaluate("document.documentElement.scrollHeight"), MAX_HEIGHT)
                suffix = "-phone" if b["file"] != "monitors" or b.get("variant") != "list" else "-list-phone"
                out = SCREENS / f"{b['file']}{suffix}-{scheme}.png"
                page.screenshot(path=str(out), clip={"x": 0, "y": 0, "width": 390, "height": height}, full_page=True)
                width = page.evaluate("document.documentElement.scrollWidth")
                flag = "" if width <= 390 else f"  OVERFLOW {width}px"
                print(f"{out.name} {height}px{flag}")
                ctx.close()
        browser.close()


if __name__ == "__main__":
    main()
