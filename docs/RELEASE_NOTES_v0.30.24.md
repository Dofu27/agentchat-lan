# AgentChat LAN v0.30.24

Service state check fix.

## Fixed

- `ensureServiceStoppedForReplace` treats only
  `ERROR_SERVICE_DOES_NOT_EXIST` as not installed. Access-denied and other
  `OpenService` errors abort the replacement.

## Notes

- No other behavior changed from v0.30.23.
- The installer is not Authenticode-signed yet.
