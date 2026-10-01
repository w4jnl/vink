#!/usr/bin/env python3
"""Render the README images in assets/readme/ from their HTML sources.

    python3 assets/readme/render.py            # how-it-works-dark.png, how-it-works-light.png

Needs Playwright for Python with Chromium (pip install playwright && playwright install chromium).
Tokens, glyphs and fonts come from docs/design-system, so no network is used. The page is shot as
one element at 2x, 1200 css px wide; its height follows the content. Keep every label true of the
code: edit how-it-works.html, then re-run this script and commit the PNGs with the HTML.
"""
import asyncio
import pathlib

from playwright.async_api import async_playwright

HERE = pathlib.Path(__file__).resolve().parent
PAGES = [("how-it-works.html", ".dg", "how-it-works")]


async def main() -> None:
    async with async_playwright() as p:
        browser = await p.chromium.launch()
        for src, selector, out in PAGES:
            for theme in ("dark", "light"):
                page = await browser.new_page(viewport={"width": 1200, "height": 900}, device_scale_factor=2)
                await page.goto(f"{(HERE / src).as_uri()}?theme={theme}")
                await page.evaluate("document.fonts.ready")
                loaded = await page.evaluate("[...document.fonts].filter(f => f.status === 'loaded').map(f => f.family + ' ' + f.weight)")
                if not any("JetBrains Mono" in f for f in loaded):
                    raise SystemExit(f"{src}: JetBrains Mono did not load; check docs/design-system/fonts")
                target = HERE / f"{out}-{theme}.png"
                await page.locator(selector).screenshot(path=str(target))
                print(target.relative_to(HERE.parent.parent))
                await page.close()
        await browser.close()


if __name__ == "__main__":
    asyncio.run(main())
