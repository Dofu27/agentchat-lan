# Code signing policy

Status: **application submitted to SignPath Foundation; not yet approved**.
Until SignPath confirms acceptance, no release is claimed to be signed by
SignPath or by SignPath Foundation.

## Scope

When accepted, the signing scope is limited to Windows binaries built from this
repository by GitHub Actions (`.github/workflows/release.yml`):

- `agentchat.exe`
- `AgentChatConsole.exe`
- `AgentChatConsole-<version>-installer.exe`

Third-party binaries are not signed under this project's certificate.

## Roles

- Committers and reviewers: [@Dofu27](https://github.com/Dofu27)
- Approvers: [@Dofu27](https://github.com/Dofu27)

The maintainer must keep multi-factor authentication enabled for GitHub and
SignPath. Every signing request requires explicit approval by the approver.

## Origin and build policy

- Only artifacts produced by the repository's GitHub Actions workflow may be
  submitted for signing.
- Each release is manually approved after reviewing the source revision,
  workflow run, and artifact checksums.
- The project does not sign locally rebuilt or modified binaries.

## Privacy

See [PRIVACY.md](PRIVACY.md). The application does not include telemetry and
only contacts the hub configured by the user.

## Attribution after acceptance

Once SignPath Foundation approves the project, this section will state:

> Free code signing provided by [SignPath.io](https://about.signpath.io),
> certificate by [SignPath Foundation](https://signpath.org).

That statement must not be published as a current fact before approval.
