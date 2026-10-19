# Agent attribution

GitHub personal accounts do not have subaccounts. This repository distinguishes
the two agents with Git author identity, a commit trailer, and branch/PR naming.
No second GitHub account is required.

## Identities

| Agent | Git author | Trailer |
|---|---|---|
| DSH | `DSH <dsh@localhost>` | `Agent: dsh` |
| Codex | `Codex <codex@localhost>` | `Agent: codex` |

The email addresses are local machine identities, not GitHub-verified addresses.
GitHub shows the author name but does not link it to a separate account. That is
intentional: the repository remains under one owner.

## Setup

Run once per checkout:

```bash
bash scripts/setup-attribution.sh dsh    # DSH workspace
bash scripts/setup-attribution.sh codex  # Codex workspace
```

This sets `user.name`, `user.email`, `agent.identity`, and the tracked
`.githooks/commit-msg` hook. The hook appends the correct `Agent:` trailer when a
commit does not already have one.

## Commit format

```text
<type>: <short summary>

Agent: dsh
Co-authored-by: Codex <codex@localhost>
```

Use `Co-authored-by` when one agent commits work produced by the other. Do not
add a trailer that claims work the agent did not do.

## Branches and pull requests

- DSH branches: `dsh/<topic>`
- Codex branches: `codex/<topic>`
- PR titles start with `[dsh]` or `[codex]`
- PR descriptions list the files and behavior changed by that agent

## History

Commits created before this convention are authored by `DSH <dsh@localhost>`,
including work that DSH reviewed and committed on Codex's behalf. Do not rewrite
published history to change authors; attribution starts with the first commit
made under this convention.

## If separate GitHub accounts are required later

Create a GitHub Organization and two machine users, each with its own email,
2FA, and SSH key. Machine users are full accounts, not subaccounts. Use them only
for automation, and keep the organization's audit log enabled.
