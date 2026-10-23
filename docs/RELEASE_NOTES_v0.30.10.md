# AgentChat LAN v0.30.10

Data-directory and URL validation hardening.

## Security

- Hub data initialization now validates and pins the data parent before the
  database is created.
- The default `%ProgramData%\AgentChat` parent is created with a protected
  DACL granting only SYSTEM and Administrators full control, and Users read
  access.
- A pre-existing data parent that is writable by an ordinary user, or is a
  reparse point, is rejected.
- Windows extended paths, path aliases, and directory identity are canonicalized
  when enforcing the "data must not be inside the install directory" rule.
- All unspecified IPv6 forms are rejected as Hub URLs, not just `::`.

## Notes

- Custom data directories must have a protected parent directory. A broad
  shared folder such as `G:\work\Teamshare` is rejected until its AgentChat
  subdirectory is created with a restricted ACL.
- Console still requires administrator approval at every launch.
- The installer is not Authenticode-signed yet.
