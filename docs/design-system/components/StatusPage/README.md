# StatusPage

The public status page at `/s/{slug}`: mark and title, one banner, the groups, a 90-day bar per monitor, the open incidents, the past incidents when the page asks, a quiet footer.

A page belongs to a project or to an org. Groups follow the page's tags (a project page, or an org page grouped by tag) or its projects (an org page grouped by project); see the screens README, Status pages.

No top bar, no auth, no scripts beyond the 60 s poll, no external assets; cacheable for 30 s.
