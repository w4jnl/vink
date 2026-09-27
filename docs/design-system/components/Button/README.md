# Button

A 32px action: `quiet` by default, `primary` for the one main action in a view, `danger` for destructive actions.

**Provide** `label` (a verb that says what happens: "Pause", "Check now", "Delete monitor"), `variant`, and for destructive actions `confirm` with the second-step text.

**Use** at most one primary per view (Create monitor, Save). Destructive actions never open a modal: the first click arms the button (`data-armed`, filled `down`, label becomes the confirm text), a second click within 4 s performs it, otherwise it disarms.

**Don't** use icons without words, or `primary` for Delete.

```html
<button type="button" class="vk-btn vk-btn--danger" data-confirm="Really delete?">Delete monitor</button>
```
