// vink components are server-rendered HTML. Each function returns the canonical markup
// (a string) that the matching Go html/template partial must produce. No React.
export type State = 'up' | 'late' | 'down' | 'paused' | 'new';
export type Kind = 'heartbeat' | 'http' | 'tcp' | 'dns' | 'tls' | 'icmp';

/** Shape + colour + word for one monitor state. `pill` adds the soft fill; `since` appends a relative time in mono. */
export declare function StateBadge(props: { state: State; pill?: boolean; label?: string; since?: string }): string;
/** 32px button. `quiet` (default) for most actions, `primary` once per view, `danger` for destructive ones; `confirm` arms a two-step confirm with that text. */
export declare function Button(props: { label: string; variant?: 'quiet' | 'primary' | 'danger'; confirm?: string; disabled?: boolean; href?: string; type?: 'button' | 'submit'; block?: boolean }): string;
/** Filter chip with an optional state glyph and a count; `pressed` = filter active. */
export declare function Chip(props: { label: string; count?: number; pressed?: boolean; state?: State }): string;
/** A monitor tag, mono, no hash sign. */
export declare function Tag(props: { label: string }): string;
/** 16px monoline glyph for a monitor kind. */
export declare function KindIcon(props: { kind: Kind }): string;
export type Option = string | { value: string; label: string };
/** Label + control + hint or inline error. `mono` for URLs, cron expressions, slugs. `html` replaces the control; `before`/`after` sit beside it (trusted HTML). */
export declare function Field(props: { id?: string; name?: string; label: string; value?: string; placeholder?: string; hint?: string; error?: string; mono?: boolean;
  control?: 'input' | 'select' | 'textarea'; options?: Option[]; rows?: number; type?: 'text' | 'password' | 'url' | 'email'; prefix?: string; suffix?: string;
  disabled?: boolean; autocomplete?: string; html?: string; before?: string; after?: string; otp?: boolean }): string;
/** Two to four rendered Fields side by side; `lead` makes the first column 112px. */
export declare function FieldRow(props: { fields: string[]; lead?: boolean }): string;
/** Native checkbox with label and optional hint. */
export declare function Checkbox(props: { label?: string; labelHtml?: string; name?: string; id?: string; value?: string; checked?: boolean; disabled?: boolean; hint?: string }): string;
/** Immediate on/off with the word beside the track; `label` is the accessible name. */
export declare function Switch(props: { checked?: boolean; label?: string }): string;
/** Radio (or `multi` checkbox) group drawn as one control. */
export declare function Segmented(props: { name?: string; label: string; options: Option[]; value?: string | string[]; multi?: boolean; mono?: boolean }): string;
/** The six monitor kinds as radio cards; `locked` on edit. */
export declare function KindPicker(props?: { value?: Kind; locked?: boolean }): string;
/** <details> with a mono summary of its current values. `body` is trusted HTML. */
export declare function Disclosure(props: { title?: string; summary?: string; body?: string; open?: boolean }): string;
/** Inline result beside what caused it. `html` is trusted extra content. */
export declare function Notice(props: { tone?: 'ok' | 'error' | 'warn' | 'info'; title?: string; text?: string; html?: string }): string;
/** Read-only code with optional Copy; `yaml` dims keys. */
export declare function Code(props: { text: string; copy?: boolean; copyLabel?: string; yaml?: boolean }): string;
/** Inline add/edit form on surface. `body`/`actions` are trusted HTML. */
export declare function Panel(props: { title?: string; note?: string; body?: string; actions?: string; id?: string }): string;
/** Signed-in header: mark, project switcher + Monitors / Incidents / Settings, search, user. `menu`/`userMenu` are Menu() panels; `open` draws one open. */
export declare function TopBar(props: { org?: string; project?: string; section?: 'monitors' | 'incidents' | 'settings' | 'none' | 'org'; incidents?: number; user?: string;
  hrefs?: Partial<Record<'home' | 'monitors' | 'incidents' | 'settings', string>>; menu?: string; userMenu?: string; open?: 'switcher' | 'user' }): string;
/** Popover panel for the switcher and user menus. `meta` is trusted HTML. */
export declare function Menu(props: { groups: Array<{ label?: string; role?: string; items: Array<{ label: string; href?: string; current?: boolean; meta?: string; quiet?: boolean }> }> }): string;
/** Monitors per state as glyph + number; `problems` shows only down and late, or "all up". */
export declare function StateCounts(props: { down?: number; late?: number; up?: number; paused?: number; new?: number; problems?: boolean }): string;
/** Initial in a 28px circle. */
export declare function Avatar(props: { name: string }): string;
/** Row-level select that saves on change; `label` is the accessible name. */
export declare function InlineSelect(props: { label: string; name?: string; options: Option[]; value?: string; disabled?: boolean }): string;
/** Quota use as <meter> + words; no `max` means no quota. */
export declare function Usage(props: { value: number; max?: number; label: string }): string;
/** YAML change lines: [' ' | '-' | '+', text]. */
export declare function Diff(props: { lines: Array<[' ' | '-' | '+', string]> }): string;
/** One audit log entry; rows with diff or meta are <details>. `text` is trusted HTML. */
export declare function AuditRow(props: { time: string; timeAbs?: string; actor?: string; actorKind?: 'user' | 'key' | 'system'; state?: State; text: string; scope?: string; via?: string;
  diff?: Array<[' ' | '-' | '+', string]>; meta?: Array<[string, string]>; open?: boolean }): string;
/** One observation or state change row; with `facts`, a cut `message` or a body it opens in place like AuditRow. `bodyLoad` is the partial that loads the body when the panel is revealed, `bodyHref` the body as text. */
export declare function ObsRow(props: { state: State; clock: string; abs?: string; text: string; title?: string; right?: string; bodyHref?: string; bodyLoad?: string; message?: string; facts?: Array<[string, string]>; open?: boolean }): string;
/** Server-rendered QR SVG framed black on white. */
export declare function Qr(props: { svg: string; label?: string; caption?: string }): string;
/** One-time recovery codes with Copy. */
export declare function RecoveryCodes(props: { codes: string[] }): string;
/** Hairline with words in the middle. */
export declare function Divider(props?: { label?: string }): string;
/** Page sections as links (settings tabs). */
export declare function Tabs(props: { tabs: Array<{ id: string; label: string; count?: number; href?: string }>; current?: string; label?: string }): string;
/** One incident row: open (Ack), acked, or resolved. */
export declare function IncidentRow(props: { state?: 'open' | 'acked' | 'resolved'; name: string; slug: string; href?: string; reason: string; opened: string; openedAbs?: string; duration: string; ackedBy?: string; resolved?: string }): string;
/** One settings list row; `lead`, `titleHtml`, `actions`, cell `html` are trusted HTML. */
export declare function SettingsRow(props: { title?: string; titleHtml?: string; sub?: string; prose?: boolean; lead?: string; muted?: boolean; href?: string; current?: boolean;
  cells?: Array<{ text?: string; html?: string; size?: 's' | 'm' | 'l'; mono?: boolean; ink?: boolean }>; actions?: string }): string;
/** The project ping URL with the monitor slug highlighted and a Copy button (`data-copy`). */
export declare function PingUrl(props: { base?: string; key?: string; slug?: string }): string;
/** 96x20 latency trend; the last point is marked. Colour follows state. */
export declare function Sparkline(props: { points: number[]; state?: State }): string;
/** One row of the monitor list: glyph, name + slug, kind, last observation, trend or next due, tags. */
export declare function MonitorRow(props: { state: State; name: string; slug: string; kind: Kind; last: string; lastAbs?: string; next?: string; points?: number[]; tags?: string[]; href?: string; current?: boolean }): string;
/** One cell per day (status page, 90) or per hour (drawer, 24, `compact`). */
export declare function UptimeBar(props: { days: Array<'up' | 'late' | 'down' | 'none'>; compact?: boolean; label?: string; legend?: [string, string, string] }): string;
/** Status page headline: all clear, degraded, outage or maintenance. */
export declare function StatusBanner(props: { state: 'up' | 'late' | 'down' | 'maintenance'; text: string; detail?: string }): string;
/** First-run panel: the ping URL pattern as a crontab line. */
export declare function EmptyState(props: { base?: string; key?: string }): string;
/** The vink mark as inline SVG in currentColor. */
export declare function Mark(props?: { size?: number }): string;
/** Wires data-copy, data-confirm and local switches inside `root`; call on load and on htmx:afterSettle. */
export declare function wire(root?: ParentNode): void;
declare global { interface Window { Vink: { StateBadge: typeof StateBadge; Button: typeof Button; Chip: typeof Chip; Tag: typeof Tag; KindIcon: typeof KindIcon; Field: typeof Field; PingUrl: typeof PingUrl; Sparkline: typeof Sparkline; MonitorRow: typeof MonitorRow; UptimeBar: typeof UptimeBar; StatusBanner: typeof StatusBanner; EmptyState: typeof EmptyState; Mark: typeof Mark;
  TopBar: typeof TopBar; Tabs: typeof Tabs; FieldRow: typeof FieldRow; Checkbox: typeof Checkbox; Switch: typeof Switch; Segmented: typeof Segmented; KindPicker: typeof KindPicker;
  Disclosure: typeof Disclosure; Notice: typeof Notice; Code: typeof Code; Panel: typeof Panel; IncidentRow: typeof IncidentRow; SettingsRow: typeof SettingsRow;
  Menu: typeof Menu; StateCounts: typeof StateCounts; Avatar: typeof Avatar; InlineSelect: typeof InlineSelect; Usage: typeof Usage;
  AuditRow: typeof AuditRow; ObsRow: typeof ObsRow; Diff: typeof Diff; Qr: typeof Qr; RecoveryCodes: typeof RecoveryCodes; Divider: typeof Divider; wire: typeof wire } } }
