# Contributing to Doolla

Build and test the Go Agent in `src/doolla/` from the repository root with Go 1.25 or newer and a C compiler.

```sh
go -C src/doolla build -o /tmp/doolla ./cmd/doolla
go -C src/doolla test -race ./...
go -C src/doolla vet ./...
```

Run `go test -race ./...` from the repository root with Go 1.26 or newer to test the public API, examples and default integrations. Same-package tests in `tests/_whitebox/` are linked into their Go packages and run with the SDK command above.

See the [Go guide](GO.md) for module layout, optional driver builds and frontend regeneration.

The [web component tests](frontends/webcomponent/TEST_README.md) cover the browser regressions and interactive Go test page.
