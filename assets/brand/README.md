# vink brand assets

## The mark
A terminal bracket holding a tick and a cursor. The bracket is flok's frame, the tick
is flok's bird landed on the line, and the cursor is the prompt. Thin frame (1.6),
heavy tick (2.9), cursor 4.8 × 2.2: the same weights as flok on the same 24 grid.

## The two states
| file | meaning |
| --- | --- |
| `vink-mark.svg` | all clear: nothing is down |
| `vink-mark-down.svg` | something is down: a solid tile with the cross and cursor knocked out |

At 16–22px only value reads, not detail, so the down state fills the whole icon.
The web UI swaps `favicon.svg` for `favicon-down.svg` while any monitor in the open
project is down.

## Files
| file | use |
| --- | --- |
| `vink-mark.svg` | mark, `currentColor`: templates, docs, inline |
| `vink-mark-down.svg` | down variant, `currentColor` |
| `vink-mark-teal.svg` | W4J teal, for light backgrounds |
| `vink-lockup.svg` | mark + wordmark, horizontal, `currentColor` |
| `vink-lockup-tagline.svg` | lockup + "heartbeat and uptime monitor" |
| `vink-wordmark.svg` | `[vink]`, the wordmark for places without the mark |
| `favicon.svg` / `favicon-down.svg` | 32px tiles: teal with white mark / down red with white cross |
| `vink-hero.png` | README banner, 1280×320 |
| `vink-social.png` | GitHub social preview, 1280×640 |
| `vink-avatar.png` / `vink-512.png` | avatar 460px / app icon 512px |
| `icons/` | state and kind glyphs, 16 grid, 1.5 stroke, `currentColor`, for the UI sprite |
| `gen.py` | regenerates every SVG from one geometry; text is outlined from JetBrains Mono 700 |

All wordmark text is converted to outlines, so no font is needed to render it.

## Palette
Dark is the brand's home; the web UI has a light theme too.

| token | dark | light | role |
| --- | --- | --- | --- |
| W4J teal (`brand`) | `#12999D` | `#12999D` | the tie to w4jnl: tiles, favicon |
| `accent` | `#3FD0D4` | `#0B7A7E` | interactive colour, focus ring, mark on dark |
| `up` | `#5FC47A` | `#1F7A3A` | up |
| `late` | `#E8963C` | `#9A5A0E` | late |
| `down` | `#F0645A` | `#C0342B` | down, destructive |
| `ink-muted` | `#8A999C` | `#5A686B` | paused, new, secondary text |
| `ground` | `#14181A` | `#F6F8F8` | page |

Every state also has a glyph shape (disc, half disc, diamond, two bars, dashed ring),
so colour is never the only signal.

## Type
JetBrains Mono 700 for the wordmark and headlines and 400 for data, as in flok. The web
UI sets words in the system face. Embed the fonts; never load them from a CDN.

## ASCII lockup
For `vink version`, `vink doctor`, prompts and footers:

```
  ╭─────────╮
  │   ✓     │   vink 0.1.0
  │   ▁     │   heartbeat and uptime monitor
  ╰─────────╯   w4j.nl · MIT

[✓▁] vink  ·  12 monitors  ·  ● 10 up  ◐ 1 late  ◆ 1 down
```

## Clear space
One bracket width (3.9 units on the 24 grid) on all sides. Never place the mark on a
background between `#4A5A5D` and `#A8B4B6`, where the frame loses contrast.

The full system (tokens, components and the web UI screens) is in the vink Design System artifact.
