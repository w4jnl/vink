# Field

Label, input and one line of help: a hint by default, an inline error when validation fails.

**Provide** `id`, `label`, and `hint` or `error`; `mono` for URLs, cron expressions, durations and slugs. Errors come from the server's RFC 7807 `errors[]` and are rendered next to their field, never as a toast.

**Use** at most eight fields before the create form's `Advanced` disclosure.

```html
<div class="vk-field vk-field--error"><label class="vk-field__label" for="grace">Grace</label><input class="vk-input vk-input--mono" id="grace" value="30s" aria-invalid="true" aria-describedby="grace-msg"><span class="vk-field__error" id="grace-msg"><i class="vk-glyph vk-glyph--down" aria-hidden="true"></i>Must be at least 60s.</span></div>
```
