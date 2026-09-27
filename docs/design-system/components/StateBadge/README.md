# StateBadge

The state of one monitor as glyph, colour and word together; the glyph shape alone must identify the state.

**Provide** `state` (`up`, `late`, `down`, `paused`, `new`); optionally `pill` for the soft fill and `since` for a relative time.

**Use** the plain form inside rows and sentences, the pill in the drawer header and on the status page. States are always lowercase words.

| state | glyph | means |
| --- | --- | --- |
| up | filled disc | last check passed, or the heartbeat arrived on time |
| late | half disc | past the deadline but inside grace, or a failed check not yet confirmed |
| down | diamond | grace ran out, or failures reached `failure_threshold` |
| paused | two bars | someone paused it; nothing alerts |
| new | dashed ring | no observation yet |

**Don't** use a state colour without its glyph, or a glyph without its word, except in the list row where the column header carries the word.

```html
<span class="vk-state vk-state--down vk-state--pill"><i class="vk-glyph vk-glyph--down" aria-hidden="true"></i>down <span class="vk-state__since">4 min</span></span>
```
