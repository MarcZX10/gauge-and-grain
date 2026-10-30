## What and why

## Checklist

- [ ] `go vet ./...` and `gofmt -l .` are clean, and `go test ./...` passes
- [ ] Tests cover the change, and a negative control was run (the test fails without the fix)
- [ ] Docs are updated where behaviour, flags, numbers or claims changed (README, `docs/`, the in-app notes)
- [ ] Any new or changed log fixture is sanitised: no prompts, code, names or private paths
- [ ] Prices, if changed, carry a source URL and a date
- [ ] Parser changes bump `ParserVersion()`
