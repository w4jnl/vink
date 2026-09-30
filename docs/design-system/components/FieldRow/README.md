# FieldRow

Two to four fields side by side, so a form stays short: Name beside Slug, Interval beside Timeout, From, To and Timezone.

**Provide** `fields` (rendered `Field`s); `lead: true` makes the first column 112px (Method beside Headers).

**Use** for fields that belong together and are short. Fields inside a row drop their 420px cap. `Field` itself also takes `control: 'select' | 'textarea'`, `type: 'password'`, `prefix`/`suffix` text (`vink.w4j.nl/s/`, `failures`), `disabled`, and `before`/`after`/`html` for composite controls such as a `Segmented` beside the cron input.

**Don't** put a long URL in a row, or more than eight fields before `Advanced`.
