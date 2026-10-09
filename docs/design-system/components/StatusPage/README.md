# StatusPage

The public status page at `/s/{slug}`: mark and title, one banner, the groups, a 90-day bar per monitor, the open incidents, the past incidents when the page asks, a quiet footer.

The page is centred and up to 1480px wide. Each group stays one card across the page, and its monitors flow left to right into columns at least 440px wide, the same columns in every group, so bars line up and a long group stays short; a bar fills its column. Under about 1000px (and on phones) there is one column, as before.

A page belongs to a project or to an org. Groups follow the page's tags (a project page, or an org page grouped by tag) or its projects (an org page grouped by project); see the screens README, Status pages.

No top bar, no auth, no scripts beyond the 60 s poll, no external assets; cacheable for 30 s.
