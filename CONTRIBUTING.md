# Contributing to NexBox

Thanks for contributing. Keep changes focused and include tests for observable
behavior whenever practical.

## Development checks

Backend:

```bash
cd server
go test ./...
```

Frontend:

```bash
cd web
npm ci
npm test
npm run lint -- --quiet
npm run build
```

Run `./build.sh` when a change affects the integrated executable. Do not commit
generated frontend output, local databases, configuration files, credentials,
browser profiles, logs, or editor state.

## Pull requests

- Explain the user-visible behavior and important implementation choices.
- Link related issues and call out compatibility or migration concerns.
- Keep unrelated formatting or refactors out of the same change.
- Update documentation when configuration or operation changes.

Report security issues privately as described in [SECURITY.md](SECURITY.md).
