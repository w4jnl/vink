# Chip

A filter toggle in the list's filter bar, with an optional state glyph and a live count.

**Provide** `label`, `count`, `pressed`, and `state` for the state chips. The filter bar holds, in order: the five state chips, then tag chips, then a kind menu and the search field.

**Use** `aria-pressed` for the on state; pressed chips are `accent` on `accent-soft`. Counts are mono and update with the 15 s list poll.

**Don't** add more than one filter bar per page, or chips for things that are not filters.

```html
<button type="button" class="vk-chip" aria-pressed="true"><i class="vk-glyph vk-glyph--down" aria-hidden="true"></i>down<span class="vk-chip__n">1</span></button>
```
