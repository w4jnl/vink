// vink components are server-rendered HTML. Each function returns the canonical markup
// (a string) that the matching Go html/template partial must produce. No React.
export type State = 'up' | 'late' | 'down' | 'paused' | 'new';
export type Kind = 'heartbeat' | 'http' | 'tcp' | 'dns' | 'tls' | 'icmp';

/** Shape + colour + word for one monitor state. `pill` adds the soft fill; `since` appends a relative time in mono. */
export declare function StateBadge(props: { state: State; pill?: boolean; label?: string; since?: string }): string;
/** 32px button. `quiet` (default) for most actions, `primary` once per view, `danger` for destructive ones; `confirm` arms a two-step confirm with that text. */
export declare function Button(props: { label: string; variant?: 'quiet' | 'primary' | 'danger'; confirm?: string; disabled?: boolean }): string;
/** Filter chip with an optional state glyph and a count; `pressed` = filter active. */
export declare function Chip(props: { label: string; count?: number; pressed?: boolean; state?: State }): string;
/** A monitor tag, mono, no hash sign. */
export declare function Tag(props: { label: string }): string;
/** 16px monoline glyph for a monitor kind. */
export declare function KindIcon(props: { kind: Kind }): string;
/** Label + input + hint or inline error. `mono` for URLs, cron expressions, slugs. */
export declare function Field(props: { id: string; label: string; value?: string; placeholder?: string; hint?: string; error?: string; mono?: boolean }): string;
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
/** Wires data-copy and data-confirm inside `root`; call on load and on htmx:afterSettle. */
export declare function wire(root?: ParentNode): void;
declare global { interface Window { Vink: { StateBadge: typeof StateBadge; Button: typeof Button; Chip: typeof Chip; Tag: typeof Tag; KindIcon: typeof KindIcon; Field: typeof Field; PingUrl: typeof PingUrl; Sparkline: typeof Sparkline; MonitorRow: typeof MonitorRow; UptimeBar: typeof UptimeBar; StatusBanner: typeof StatusBanner; EmptyState: typeof EmptyState; Mark: typeof Mark; wire: typeof wire } } }
