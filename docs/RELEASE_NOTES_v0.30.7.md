# AgentChat LAN v0.30.7

Test and CI repair release for v0.30.6.

## Fixed

- Updated the remaining Windows `servicePathObject` fixture for the new
  `isDir` field.
- Removed the temporary junction explicitly before test-directory cleanup.
- Restored the full Windows `go test ./...` and `go vet ./...` suite.

## Notes

- No product behavior changed from v0.30.6.
- The Windows installer is not Authenticode-signed yet.
