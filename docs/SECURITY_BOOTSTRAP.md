# Security hardening of the 4577c38 baseline

This change is code-only. It neither installs services nor approves production
rollout. No tag/release is created by this branch.

## Windows service executable

Service installation keeps handles to every component from the local volume
root to the executable. Each component is opened with OPEN_REPARSE_POINT;
junctions/symlinks and non-file executable targets are rejected. Generic read
access with no write/delete sharing pins the objects. The volume serial and
file index, plus final handle path, are rechecked before SCM creation. These
handles remain open through installation/start.
The install action takes one configuration snapshot; its SCM command uses
the verified final executable handle path and Windows ComposeCommandLine for
every argument. The guard covers creation and the install action's immediate
start, and closes when the handler returns. It is not a runtime guard for the
running service. The separate start action controls an existing SCM entry and
does not revalidate that entry's stored binPath; administrator-maintained SCM
configuration remains a deployment precondition for that action.

Every component's security descriptor is evaluated with Windows AccessCheck
and a restricted ordinary-user impersonation token (admin groups disabled,
privileges removed), even when the console is elevated. Effective data-write,
append, attribute/EA write, delete/delete-child, WRITE_DAC or WRITE_OWNER rights
cause rejection. Program Files/Windows names alone never grant trust. This
also rejects non-machine owners and write grants to unrelated account SIDs,
whose rights cannot be proven safe using only the current user's token. Unknown
ACL entry types fail closed rather than skipping the check. This
strict policy can reject an existing machine layout, including a writable volume
ancestor; use a separately secured machine deployment. The per-user installer
does not become an elevated service installer.

Tests use temporary junctions and synthetic security descriptors; no real
administrator service installation or production ACL modification is performed.
The TokenGroups test directly requires any present Administrators SID in the
restricted token to be deny-only and not enabled; an ordinary non-admin token
may omit that SID. Actual elevated-console and SYSTEM runs remain unverified.
An administrator able to change machine ACLs is outside the ordinary-user
threat model. Printed installation hints are advisory; installation via the
console revalidates and pins the executable at the time of the operation.
Do not copy a printed `next` command as a security-validated installation path:
its guards have already closed. Use the console's installation action for the
verified operation. Provision a local, non-reparse machine path with machine
ownership and no ordinary-user mutation rights on every ancestor before using
that action; an existing writable volume root deliberately fails this policy.

## Contained tool execution

The shared executor creates/configures a KILL_ON_JOB_CLOSE Job before starting
a suspended process. It must open and assign that process successfully before
resuming its primary thread. All failures kill/reap the suspended process.
Per-call API injection tests cover creation, configuration, opening, assignment,
and resume failures without mutable global syscall hooks. Cancellation kills the
Job; natural parent exit closes it too. WaitDelay bounds inherited-pipe waits.
Every descendant remaining in that Job is terminated when the Job closes,
including a directly spawned child intended to remain running. This executor
does not enable breakaway or offer a variant that disables KILL_ON_JOB_CLOSE.
Persistent processes must be started by an external owner such as SCM or Task
Scheduler, not directly spawned by the contained command. A future explicit
detachment API would need its own ownership and cleanup contract.
On Windows, bridge install registers an ONLOGON task or Startup entry; it does
not directly start an agent. The --start-now flag controls that future agent's
initial inbox cutoff, not immediate process startup. SCM starts the hub service
outside the sc.exe command's descendant tree. These are source-level ownership
checks; actual SCM/Task Scheduler lifecycle tests were not performed here.
Capture is limited to 16 KiB at write time, not just in the UI. Excess bytes are
discarded and String appends [output truncated]; error classification does not
depend on obtaining complete output. Credential fields and values are
redacted, including Authorization/Authentication, standalone Bearer, JSON,
query strings, URL credentials and private key blocks. Arbitrary unlabelled
secrets cannot be inferred; trusted tools must not print secret material.
Capture precedes regex redaction. Complete-form tests do not establish safety
when truncation cuts through a quoted credential or a private-key block; this
combination remains unverified.

## Bootstrap credential delivery and recovery

With `hub init --admin-token-file <path>`, a new random credential is created
exclusively, fully written, synced and closed **before** its hash is committed
to SQLite. Windows creates the file with a protected DACL for the initiating
user and SYSTEM, and WRITE_THROUGH; Unix uses mode 0600 and syncs the parent.
Write/short-write/sync/close failure never calls DB initialization. No failure
path deletes the credential by name: a failed or interrupted initializer keeps
the file for inspection. Existing files are never overwritten by normal init.

After successful credential persistence, an initialization error or lost response
can be resumed explicitly:

```text
agentchat hub init --data <same-directory> --admin-token-file <retained-file> --resume-init
```

Resume rejects a final symlink/non-regular file and checks the opened identity.
Before reading the credential, Windows also rechecks that same handle's owner
(initiating user or SYSTEM), protected DACL, absence of inherited/inherit-only/unsupported
ACEs, and absence of allow grants to any other account.
OWNER RIGHTS (OW) aliases are also rejected: the allowlist accepts only the
explicit initiating-process user SID and SYSTEM, without alias equivalence.
New files explicitly set the initiating user as owner. ACL widening after creation blocks
resume; fix permissions through an authorized local recovery procedure first.
Here initiating user means the creating process token's TokenUser SID, not an
interactive user inferred from another session. A SYSTEM process uses SYSTEM;
this API does not assign ownership to an arbitrary other user.
Current ACL validation cannot prove historical secrecy. If the credential may
already have been disclosed, repairing its ACL alone does not revoke leaked
copies; use an authorized credential-rotation procedure.
Windows MkdirAll(0700) does not restrict the parent directory's DACL. Credential
ancestor pinning and protection against replacement through a writable parent
are not provided by this file-only permission check.
Unix resume requires initiating-user ownership and no group/other permissions.
It validates the credential before opening the DB. The same token/hash is
idempotent; a different existing admin hash is never replaced. Partial or corrupt
credential files require operator inspection; they never authorize a DB commit.
No plaintext credential appears in ordinary errors or file-backed init output.
An error about this attempt not committing an admin credential is not evidence
that a DB file is absent or disposable. InitWithToken opens/migrates the DB
before checking the admin hash, and an interrupted response can follow a commit.
Retain the DB and credential together and inspect/resume; do not delete the DB
based on an initialization error message.
The existing no-file CLI mode retains its explicit one-time stdout delivery.
Keep the retained credential private; do not paste it into logs or review chat.

Filesystem write/sync/close failures are injected in tests. Real power loss,
storage-controller failure, malicious same-user processes and physical disk-full
experiments are not claimed as tested. Rollback to an older binary must omit
`--resume-init`; retain the credential and database together, without a down
migration or token rotation.

## Federation admission

Each peer retains its own worker and FIFO claim ordering. A worker first obtains
one of eight global delivery slots and only then claims a leased job.
Each peer has at most one synchronous delivery in flight, so one slow peer
occupies only one global slot rather than consuming all shared consumers.
Empty claims, storage errors and cancellation release admission; lease time is never
spent waiting for capacity. Tests assert 32 valid peer deliveries, zero dead
jobs from admission waiting, at most eight concurrent requests, healthy-peer
progress during another peer's backlog, and drain after that peer recovers.
The 32-peer case also asserts exactly 32 attempts. Cancellation after a claim
leaves its lease for expiry; recovery tests assert one delivered job, zero dead
jobs and two attempts despite stale success/failure completions. Network retries
remain at-least-once; receiver idempotency, rather than leases alone, prevents
duplicate stored messages.
