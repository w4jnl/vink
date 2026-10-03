# Webhook templates

A `webhook` channel sends the JSON payload by default. With a `body_template` it sends whatever the Go template renders over that payload, so one channel kind covers the services below. Each file here is a template; paste it into the channel's Body template field (or the `body_template` key of the apply file) and set the URL and headers the service wants.

The payload has `event` (`down`, `up`, `late`, `test`), `title`, `text`, `at`, `repeat`, `reason`, `message` and `exit_code` (what the failing ping said: its `?msg=` or the tail of its body, and its exit code; absent when there was none), `monitor` (`slug`, `name`, `kind`, `tags`, `state`, `state_since`), `project` (`slug`, `name`, `timezone`), `incident` (`id`, `opened_at`, `acked_at`), `from_state`, `to_state` and `links` (`monitor`, the monitor's history page with the failing ping and its body; `incident`; `ack`). The template functions `json`, `upper` and `lower` are available; `json` quotes and escapes a value, so use it for every string you put into JSON.

| Service | URL | Headers | Template | Route |
| --- | --- | --- | --- | --- |
| PagerDuty Events v2 | `https://events.pagerduty.com/v2/enqueue` | none (the routing key sits in the body) | `pagerduty-events-v2.json.tmpl` | on `down, up`: down triggers, up resolves by `dedup_key` |
| Opsgenie | `https://api.opsgenie.com/v2/alerts` | `Authorization: GenieKey <key>` | `opsgenie-create.json.tmpl` | on `down` |
| Opsgenie (close) | `https://api.opsgenie.com/v2/alerts/vink-<project>-<slug>/close?identifierType=alias` | `Authorization: GenieKey <key>` | `opsgenie-close.json.tmpl` | on `up`, one channel per monitor because the alias is in the URL |
| Discord | the channel's webhook URL | none | `discord.json.tmpl` | on `down, up, late` |
| Telegram | `https://api.telegram.org/bot<token>/sendMessage` | none | `telegram.json.tmpl` | on `down, up, late`; put the chat id in the template |
| ilert | `https://api.ilert.com/api/v1/events/<integration key>` | none | `pagerduty-events-v2.json.tmpl` works: ilert accepts the Events v2 shape | on `down, up` |

Replace the placeholders written in capitals (`YOUR_ROUTING_KEY`, `CHAT_ID`) before saving. Test the channel with Send test: the payload then has `event: test`.
