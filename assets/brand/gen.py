"""Generate vink brand SVGs from one geometry, on flok's 24-unit grid.

The mark: flok's terminal bracket holding a tick and a cursor. flok's chevron is a
bird in flight; vink's tick is the same bird landed on the line. Thin frame (1.6),
heavy bird (2.9), cursor 4.8 x 2.2 - identical weights to flok.
Wordmark text is outlined from JetBrains Mono so files render without the font.
"""
import os, io
from fontTools.ttLib import TTFont
from fontTools.pens.svgPathPen import SVGPathPen
from fontTools.pens.transformPen import TransformPen

HERE = os.path.dirname(os.path.abspath(__file__))
FONTS = os.environ.get('VINK_FONTS', os.path.join(HERE, 'fonts'))
OUT_REPO = os.path.join(HERE, 'repo', 'assets', 'brand')
OUT_DS = os.path.join(HERE, 'ds')
for d in (OUT_REPO, OUT_DS):
    os.makedirs(d, exist_ok=True)

# ---- palette (tokens.json is the source of truth; these mirror it) ----
TEAL = '#12999D'     # W4J teal - brand tie
CYAN = '#3FD0D4'     # accent on dark
INK_D = '#F2F5F5'    # text on dark
INK_L = '#14181A'    # text on light / ground
MUTED = '#8A999C'    # tagline
DOWN = '#C0342B'     # down, light theme / tile fill
ICON = '#7E8D90'     # display ink for single-ink icons (3:1 on light and dark)

# ---- geometry ----
BRACKET = ('<g stroke="{c}" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" fill="none">'
           '<path d="M8 3.2 H6 a2.2 2.2 0 0 0 -2.2 2.2 V18.6 a2.2 2.2 0 0 0 2.2 2.2 H8"/>'
           '<path d="M16 3.2 H18 a2.2 2.2 0 0 1 2.2 2.2 V18.6 a2.2 2.2 0 0 1 -2.2 2.2 H16"/></g>')
TICK_D = 'M7.5 10.1 L10.5 12.9 L16.5 7.1'
TICK = '<path d="' + TICK_D + '" stroke="{c}" stroke-width="2.9" stroke-linecap="round" stroke-linejoin="round" fill="none"/>'
CURSOR = '<rect x="9.6" y="16.2" width="4.8" height="2.2" rx="1.1" fill="{c}"/>'
CROSS_D = 'M9.1 7.3 L14.9 13.1 M14.9 7.3 L9.1 13.1'

def mark_inner(c):
    return BRACKET.format(c=c) + TICK.format(c=c) + CURSOR.format(c=c)

def mark_down_inner(c, mid='vinkDown'):
    # solid tile, cross and cursor knocked out: value, not detail, carries the state at 22px
    return (f'<mask id="{mid}"><rect x="2.6" y="2.2" width="18.8" height="19.6" rx="5.2" fill="#fff"/>'
            f'<path d="{CROSS_D}" stroke="#000" stroke-width="3.1" stroke-linecap="round" fill="none"/>'
            f'<rect x="9.6" y="16.4" width="4.8" height="2.3" rx="1.15" fill="#000"/></mask>'
            f'<rect x="2.6" y="2.2" width="18.8" height="19.6" rx="5.2" fill="{c}" mask="url(#{mid})"/>')

def svg(w, h, body, title, vb=None):
    vb = vb or f'0 0 {w} {h}'
    return f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="{vb}" width="{w}" height="{h}"><title>{title}</title>{body}</svg>\n'

# ---- outlined text ----
_fonts = {}
def font(weight):
    if weight not in _fonts:
        _fonts[weight] = TTFont(os.path.join(FONTS, f'jetbrains-mono-latin-{weight}-normal.woff2'))
    return _fonts[weight]

def text_path(txt, size, x, y, weight=700, spacing=0.0):
    f = font(weight)
    gs = f.getGlyphSet(); cmap = f.getBestCmap(); upm = f['head'].unitsPerEm
    s = size / upm
    parts = []
    cx = x
    for ch in txt:
        gname = cmap[ord(ch)]
        pen = SVGPathPen(gs)
        tp = TransformPen(pen, (s, 0, 0, -s, cx, y))
        gs[gname].draw(tp)
        d = pen.getCommands()
        if d:
            parts.append(d)
        cx += gs[gname].width * s + spacing
    return ' '.join(parts), cx

def text_el(txt, size, x, y, fill, weight=700, spacing=0.0):
    d, _ = text_path(txt, size, x, y, weight, spacing)
    return f'<path d="{d}" fill="{fill}"/>'

def r(v):
    return round(v, 3)

# ---- builders ----
def mark(c, title='vink'):
    return svg(24, 24, mark_inner(c), title)

def mark_down(c):
    return svg(24, 24, mark_down_inner(c), 'vink - something is down')

def lockup(mark_c, word_c):
    body = f'<g transform="scale(1.83333)">{mark_inner(mark_c)}</g>' + text_el('vink', 34, 56, 32, word_c, 700, -1)
    return svg(150, 44, body, 'vink - horizontal lockup')

TAGLINE = 'heartbeat and uptime monitor'
def lockup_tagline(mark_c, word_c, tag_c):
    body = (f'<g transform="translate(0 4) scale(1.83333)">{mark_inner(mark_c)}</g>'
            + text_el('vink', 28, 56, 30, word_c, 700, -1)
            + text_el(TAGLINE, 11, 57, 46, tag_c, 400, 0))
    return svg(260, 52, body, 'vink - lockup with tagline')

def wordmark(bracket_c, word_c):
    body = (f'<g stroke="{bracket_c}" stroke-width="4.2" stroke-linecap="round" stroke-linejoin="round" fill="none">'
            '<path d="M16 5 H9 a5 5 0 0 0 -5 5 V42 a5 5 0 0 0 5 5 H16"/>'
            '<path d="M116 5 H123 a5 5 0 0 1 5 5 V42 a5 5 0 0 1 -5 5 H116"/></g>'
            + text_el('vink', 40, 26, 38, word_c, 700, -1))
    return svg(132, 52, body, '[vink]')

def favicon(tile, fg):
    return svg(32, 32, f'<rect width="32" height="32" rx="7" fill="{tile}"/><g transform="translate(4 4)">{mark_inner(fg)}</g>', 'vink')

def favicon_down(tile, fg):
    body = (f'<rect width="32" height="32" rx="7" fill="{tile}"/><g transform="translate(4 4)">'
            f'<path d="{CROSS_D}" stroke="{fg}" stroke-width="3.1" stroke-linecap="round" fill="none"/>'
            + BRACKET.format(c=fg) + CURSOR.format(c=fg) + '</g>')
    return svg(32, 32, body, 'vink - something is down')

# icons: 16 grid, 1.5 monoline, round caps; single ink
def icon(body, title, c=ICON):
    return svg(16, 16, body.replace('{c}', c), title)

STATE_ICONS = {
    'state-up':     ('<circle cx="8" cy="8" r="4.6" fill="{c}"/>', 'up'),
    'state-late':   ('<circle cx="8" cy="8" r="4.1" stroke="{c}" stroke-width="1.5" fill="none"/><path d="M8 3.9 A4.1 4.1 0 0 0 8 12.1 Z" fill="{c}"/>', 'late'),
    'state-down':   ('<path d="M8 2.9 L13.1 8 L8 13.1 L2.9 8 Z" fill="{c}" stroke="{c}" stroke-width="1" stroke-linejoin="round"/>', 'down'),
    'state-paused': ('<path d="M6 4.6 V11.4 M10 4.6 V11.4" stroke="{c}" stroke-width="2" stroke-linecap="round"/>', 'paused'),
    'state-new':    ('<circle cx="8" cy="8" r="4.1" stroke="{c}" stroke-width="1.5" stroke-dasharray="2.15 2.15" fill="none"/>', 'new'),
}
KIND_ICONS = {
    'kind-heartbeat': ('<path d="M1.5 9 H4.4 L6.4 4 L9.4 12.5 L11.1 8 H14.5" stroke="{c}" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" fill="none"/>', 'heartbeat'),
    'kind-http':      ('<g stroke="{c}" stroke-width="1.5" fill="none"><circle cx="8" cy="8" r="5.75"/><ellipse cx="8" cy="8" rx="2.4" ry="5.75"/><path d="M2.4 8 H13.6" stroke-linecap="round"/></g>', 'http'),
    'kind-tcp':       ('<g stroke="{c}" stroke-width="1.5" fill="none" stroke-linecap="round"><circle cx="3.6" cy="8" r="1.9"/><circle cx="12.4" cy="8" r="1.9"/><path d="M5.6 8 H10.4"/></g>', 'tcp'),
    'kind-dns':       ('<g stroke="{c}" stroke-width="1.5" fill="none" stroke-linecap="round" stroke-linejoin="round"><path d="M5 2.2 V13.8"/><path d="M5 3.6 H11.2 L13.2 5.5 L11.2 7.4 H5"/></g>', 'dns'),
    'kind-tls':       ('<g stroke="{c}" stroke-width="1.5" fill="none" stroke-linecap="round" stroke-linejoin="round"><rect x="3.4" y="7.2" width="9.2" height="6.6" rx="1.6"/><path d="M5.6 7.2 V5.2 a2.4 2.4 0 0 1 4.8 0 V7.2"/></g>', 'tls'),
    'kind-icmp':      ('<g stroke="{c}" stroke-width="1.5" fill="none" stroke-linecap="round"><path d="M3.2 8.6 a4.2 4.2 0 0 1 4.2 4.2"/><path d="M3.2 4.4 a8.4 8.4 0 0 1 8.4 8.4"/></g><circle cx="3.6" cy="12.4" r="1.4" fill="{c}"/>', 'icmp'),
}

def write(path, text):
    with open(path, 'w') as fh:
        fh.write(text)

# ---- repo set (currentColor masters, like w4jnl/flok assets/brand) ----
R = OUT_REPO
write(f'{R}/vink-mark.svg', mark('currentColor'))
write(f'{R}/vink-mark-down.svg', mark_down('currentColor'))
write(f'{R}/vink-mark-teal.svg', mark(TEAL))
write(f'{R}/vink-lockup.svg', lockup('currentColor', 'currentColor'))
write(f'{R}/vink-lockup-tagline.svg', lockup_tagline('currentColor', 'currentColor', MUTED))
write(f'{R}/vink-wordmark.svg', wordmark('currentColor', 'currentColor'))
write(f'{R}/favicon.svg', favicon(TEAL, '#ffffff'))
write(f'{R}/favicon-down.svg', favicon_down(DOWN, '#ffffff'))
os.makedirs(f'{R}/icons', exist_ok=True)
for name, (b, t) in {**STATE_ICONS, **KIND_ICONS}.items():
    write(f'{R}/icons/{name}.svg', icon(b, t, 'currentColor'))

# ---- design-system set (fixed inks: <img> cannot inherit colour) ----
D = OUT_DS
os.makedirs(f'{D}/Logos', exist_ok=True); os.makedirs(f'{D}/App icons', exist_ok=True); os.makedirs(f'{D}/Icons', exist_ok=True)
write(f'{D}/Logos/vink-mark-teal.svg', mark(TEAL))
write(f'{D}/Logos/vink-mark-cyan.svg', mark(CYAN))
write(f'{D}/Logos/vink-mark-ink.svg', mark(INK_L))
write(f'{D}/Logos/vink-mark-down.svg', mark_down(DOWN))
write(f'{D}/Logos/vink-lockup-light.svg', lockup(TEAL, INK_L))
write(f'{D}/Logos/vink-lockup-dark.svg', lockup(CYAN, INK_D))
write(f'{D}/Logos/vink-lockup-tagline-light.svg', lockup_tagline(TEAL, INK_L, '#5A686B'))
write(f'{D}/Logos/vink-lockup-tagline-dark.svg', lockup_tagline(CYAN, INK_D, MUTED))
write(f'{D}/Logos/vink-wordmark-light.svg', wordmark(TEAL, INK_L))
write(f'{D}/Logos/vink-wordmark-dark.svg', wordmark(CYAN, INK_D))
write(f'{D}/App icons/favicon.svg', favicon(TEAL, '#ffffff'))
write(f'{D}/App icons/favicon-down.svg', favicon_down(DOWN, '#ffffff'))
for name, (b, t) in {**STATE_ICONS, **KIND_ICONS}.items():
    write(f'{D}/Icons/{name}.svg', icon(b, t, ICON))

# inline copies for HTML renders (hero, social, avatar, cover)
write(os.path.join(HERE, 'inline-mark.txt'), mark_inner('currentColor'))
write(os.path.join(HERE, 'inline-lockup-tagline.txt'),
      f'<g transform="translate(0 4) scale(1.83333)">{mark_inner("currentColor")}</g>'
      + text_el('vink', 28, 56, 30, INK_D, 700, -1) + text_el(TAGLINE, 11, 57, 46, MUTED, 400, 0))
write(os.path.join(HERE, 'inline-lockup.txt'),
      f'<g transform="scale(1.83333)">{mark_inner("currentColor")}</g>' + text_el('vink', 34, 56, 32, INK_D, 700, -1))
print('ok')
