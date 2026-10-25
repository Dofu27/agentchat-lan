# AgentChat LAN v0.30.42

Self-join, invite management, and Host setup layout.

## Added

- **One-click join this Hub** in the Chat join panel. It creates a short
  single-use invite through the admin API and joins the local agent.
- Admin invite list now has a delete action per invite.

## Changed

- Firewall controls moved outside the collapsed Host setup summary, so they
  remain available after initialization.

## Fixed

- Clearing Hub data stops the service, removes data and the managed admin token,
  and clears the saved data path so initialization becomes available again.
- Certificate/firewall/data status is shown outside the collapsed body.

## Notes

- The installer is still unsigned.
- Real GUI/UAC testing is still required.
