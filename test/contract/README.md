# Contract fixtures

Golden JSON used by the hub, CLI, and MCP server tests.

Rules:

- Add a fixture for every new object or request shape.
- Do not put real tokens or private data in a fixture.
- Keep timestamps RFC 3339 with an explicit offset.
- Unknown fields must be ignored by decoders.
- A breaking field rename requires a new API version.

Run:

```bash
go test ./test/contract
```
