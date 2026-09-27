# API errors

Every error is an RFC 7807 `application/problem+json` document. `type` links to a section here.

| type | status | meaning |
| --- | --- | --- |
| `#malformed` | 400 | The body is not valid JSON, has an unknown field, or a query parameter is out of range. |
| `#unauthorized` | 401 | No credential, an unknown or revoked API key, or an expired session. |
| `#forbidden` | 403 | The caller's role does not allow this, the key is read-only, or the CSRF token is missing. |
| `#not-found` | 404 | Nothing at this address, including anything that belongs to another project. |
| `#conflict` | 409 | A slug or name is taken, or the incident is already resolved. |
| `#validation` | 422 | One or more fields are invalid; `errors[]` lists `field` and `msg`. |
| `#rate-limit` | 429 | Slow down; `Retry-After` says how long. |
| `#method-not-allowed` | 405 | The monitor restricts ping methods. |
| `#internal` | 500 | Something broke; `detail` carries the request id to find in the log. |
