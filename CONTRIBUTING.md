# Contributing to go9p-netfs

Thanks for helping improve **go9p-netfs**.

## Principles

- Keep changes focused: fix one issue or deliver one coherent improvement per pull
  request when possible.
- Follow existing naming, formatting, and structure in touched files unless there
  is a strong reason to refactor.
- Do not widen scope with unrelated cleanup or documentation churn.

## Reporting issues

Please include:

- What you ran (command line, kernel mount options, etc.)
- What you expected
- What happened instead (paste logs verbatim when relevant)
- Host OS / architecture when it matters

## Pull requests

- Run **`go test ./...`** locally before opening a PR.
- If your change touches the QEMU kernel end-to-end path (`scripts/v9fs/`),
  describe how you exercised it or link to the relevant CI job on your fork.

## Coding standards

The project tracks **Go 1.26** (`go.mod`). Format with `gofmt` (`go fmt ./...`).
Prefer readable error handling over broad panics outside `main`.

## Maintainer workflow

Maintainers are listed in [`MAINTAINERS`](./MAINTAINERS).

## Licensing

By contributing you agree your contributions are made under the same license as
the project (see [`LICENSE`](./LICENSE)).
