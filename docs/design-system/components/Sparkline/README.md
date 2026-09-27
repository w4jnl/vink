# Sparkline

A 96×20 latency trend for the last 24 h, the last point marked; colour follows the monitor state.

**Provide** `points` (latency samples, oldest first) and `state`. Server-rendered inline SVG; no charting library.

**Use** in the list row's trend column for pull monitors; heartbeat monitors show "next due" there instead.
