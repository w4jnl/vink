# Usage

Quota use as a native `<meter>` and words in mono: `12 / 100 monitors`. The bar is `accent`, turns `late` from 80% and `down` from 95%. Without a quota it says "no quota" and draws no bar.

**Provide** `value`, `max` (omit for no quota) and `label`. **Use** on the org's Projects and Agents tabs (read-only: the instance admin sets quotas) and in the instance admin's org list. Creating a monitor or agent over quota fails with an inline error that names the quota.
