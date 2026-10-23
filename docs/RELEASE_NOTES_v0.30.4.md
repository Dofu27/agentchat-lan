# AgentChat LAN v0.30.4

Security follow-up to v0.30.3.

## Fixed from Codex review

- The service installer now requires the CLI to match the SHA-256 payload
  embedded in the Console build. An arbitrary AppData executable is no longer
  promoted to LocalSystem.
- The source is opened with read-only sharing and its content is hashed from
  the same handle used for the copy.
- Program Files and the AgentChat target directory are opened and ACL-checked
  before any temporary file is created; reparse points are rejected before
  writing.
- An existing protected binary with the correct digest is reused. A stale
  binary with a different digest is replaced through the verified path.
- Cancelling or failing the UAC elevation no longer closes the Console.

## Notes

- The Console and CLI must be installed together from the same release; the
  Console carries the expected CLI digest.
- Service installation still requires administrator rights.
- The installer and binaries are not Authenticode-signed yet.
