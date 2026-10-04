# Contributing

vink is a personal tool, published because the approach may be useful to others. Read
[Scope and status](README.md#scope-and-status) first: features are the ones I need, and requests
that do not fit will probably be declined, politely.

What is welcome:

- Bug reports with a reproduction and the relevant lines of `vink serve -d`, secrets removed (the
  issue form asks for them).
- Fixes with a test. Tests are table-driven, use `httptest` and a temporary SQLite file per test,
  and never mock the database. `make lint` (gofmt, vet, golangci-lint, the tenancy gate and the
  egress gate), `make test` (`go test -race ./...`, which includes the cross-tenant suite in
  `internal/http/crosstenant_test.go`) and `make e2e` (the four smoke tests against the built
  binary, about six minutes) must pass.
- Small, focused pull requests. Describe what changed and what you verified, in plain words.

Before a larger change, open an issue and ask; it saves both of us a rewrite.

`ping/` is a separate Go module, `github.com/w4jnl/vink/ping`, which other projects import. It
stays on the standard library and on `go 1.22`, and CI tests it on both Go 1.22 and the current
release. `make lint` and `make test` cover it. It is released apart from vink, by tagging
`ping/vX.Y.Z` on main when it changes:

```sh
git tag -a ping/v0.1.1 -m "ping v0.1.1" && git push origin ping/v0.1.1
```

`docs/design.md` is the specification; ask before deviating from it. UI changes follow
`docs/design-system/`: the markup is what `components/bundle.js` returns, styles come from
`bundle.css` and `tokens.css`, and `make golden` checks the Go components against it. Every
project-scoped query filters by `project_id`, and a request for another tenant's resource is a
404. The [Development](README.md#development) section of the README has the layout and the make
targets.
