## What changed

<!-- A short summary. Link the issue if there is one. -->

## Contract impact

- [ ] No wire-contract change
- [ ] Contract changed and `docs/CONTRACT.md` was updated
- [ ] New fixtures added under `test/contract`

## Verification

<!-- Paste the commands you ran and their result. -->

```bash
gofmt -l .
go vet ./...
go test -race ./...
```

## Screenshots

<!-- For UI changes only. Remove this section otherwise. -->

## Rollback

<!-- Required for storage, auth, or migration changes. -->
