# KindPicker

The first field of the create form: six radio cards, one per monitor kind, each with its glyph, name and what it does. The fields below it change with the kind (`hx-get` on change swaps the kind's fields).

**Provide** `value`; `locked: true` on the edit form, because changing the kind recreates the monitor and resets its state.

**Use** only in the monitor form. The cards are three across in the 560px drawer.
