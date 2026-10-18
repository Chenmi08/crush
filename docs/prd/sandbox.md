# PRD: Filesystem Sandbox for Tool Execution

> Status: revised for this fork. Scope: write confinement only — no network
> isolation, no read restriction. See [Out of Scope](#out-of-scope) for why.
>
> **Fork revision.** With the sandbox as the foundation, the approval question
> collapses to one rule: only an operation that leaves the boundary prompts.
> The legacy pre-sandbox defenses (the command ban list, the safe-read-only
> allowlist, `dangerous_commands`, `exclude_commands`,
> `fail_if_unavailable`, and the in-process fence backend) were removed in
> that revision; where this document still describes them, the code no longer
> does. Two consequences define the fork's posture: `danger-full-access` is
> full delegation (nothing prompts, including network commands), and network
> egress is the one capability that still prompts under a confined policy —
> covered by a built-in network command list plus the user's
> `permissions.network_commands` additions.

## Problem Statement

Crush's only guard against an agent damaging the machine is a deny-list of
command names plus an interactive permission prompt. Both are evaluated in the
Crush process, before the command runs, against the command string. Neither is
a boundary:

- The deny-list matches `args[0]` literally. `/bin/rm`, `env rm`, `xargs rm`,
  `find -exec`, `python -c`, `make`, and `bash -c` all reach the same effects.
- The "safe read-only command" shortcut skips the prompt entirely, and its
  chaining detector does not consider redirections. A read-only command with an
  output redirect writes anywhere the user can write, without a prompt.
- `working_dir` is chosen by the model and is not validated against the
  workspace, so "allow bash for this directory" can be granted for `/` or
  `$HOME`.
- Path checks in the file tools compare absolute paths without resolving
  symlinks, and several read tools perform no containment check at all.
- Turning prompts off (YOLO) removes the last remaining check, leaving the
  agent with the full authority of the user.

The result is that a user has exactly two settings: be interrupted constantly,
or hand over the machine. There is no third option where the agent can work
autonomously *because* its blast radius is bounded.

Two specific escapes make this worse than "the agent might make a mistake":

1. **Crush executes repository-local config.** A `crushrc` in the working
   directory runs as arbitrary shell, unconditionally, before any permission
   system is consulted. An agent that can write that file gains code execution
   outside any future sandbox.
2. **No containment inside the workspace.** Git executes `.git/hooks/*` and
   `.git/config` (`core.hooksPath`, `core.sshCommand`, `credential.helper`),
   and Crush re-reads `AGENTS.md`. Anything the agent writes there executes
   later, with the user's authority, outside any sandbox.

## Solution

Introduce a filesystem sandbox for tool execution, modelled on the mechanism
DSH ships (a mode plus a set of writable roots, enforced by an OS primitive),
with three deliberate additions: a root *list* rather than a single root,
write-protected paths inside the roots, and a trust gate for repository-local
config.

The sandbox has three modes:

| Mode | Meaning |
| --- | --- |
| `read-only` | No writes except required sinks (`/dev/null`) and a private temp dir. |
| `workspace-write` | Writes allowed under a configured set of roots: the workspace, a per-run temp directory, and any extra roots the user grants. |
| `danger-full-access` | No confinement. Must be chosen explicitly. |

Enforcement is done by the OS, not by Crush:

| Platform | Backend |
| --- | --- |
| Linux | `bubblewrap` (preferred), falling back to Landlock |
| macOS | Seatbelt via `sandbox-exec` |
| Windows | Unsupported — fails closed when a sandbox is requested |
| WSL2 | The Linux backend |

In return for the boundary, Crush stops prompting for commands that are
actually confined: a command that can only write inside the workspace does not
need approval to run. Commands that leave the sandbox — matching a configured
exclusion, or when no backend is usable — keep the existing prompt. YOLO stops
meaning "no sandbox"; it now means "no prompts".

The agent is given one sanctioned way to ask for more: a `sandbox_permissions`
parameter paired with a `justification`, which raises the mode for a single
call after the user approves it.

## User Stories

1. As a developer, I want the agent's shell commands confined to my repository,
   so that an autonomous run cannot modify the rest of my machine.
2. As a developer, I want the file-editing tools confined by the same boundary
   as the shell, so that the coverage of my sandbox does not depend on which
   tool the agent happened to pick.
3. As a developer, I want a read-only mode, so that I can let the agent explore
   and explain a codebase without any chance of mutation.
4. As a developer, I want the sandbox to be on by default, so that safety is
   not opt-in for users who never read the docs.
5. As a developer, I want a visible, persistent indicator of the current
   sandbox mode, so that I always know what boundary I am trusting.
6. As a developer, I want the status indicator to distinguish "enforced" from
   "degraded" and "off", so that a partial backend is not mistaken for a full
   one.
7. As a developer, I want a clear warning and a distinct session state when the
   sandbox is unavailable, so that I can install the missing dependency instead
   of silently running unconfined.
8. As a developer, I want Crush to refuse to run a confined command when no
   backend is usable, rather than quietly running it unconfined, so that the
   mode I selected is the mode that applies.
9. As a developer, I want the failure message to name the missing dependency
   and the exact fix, so that recovering from a container or a fresh machine
   does not require reading source code.
10. As a developer, I want to grant extra writable directories (`~/.kube`,
    a shared build cache, a sibling repository), so that real workflows work
    without disabling the sandbox entirely.
11. As a developer, I want per-run temporary directories redirected inside the
    sandbox, so that tools that write temp files do not fail and do not leak
    into a shared `/tmp`.
12. As a developer, I want the default writable set to cover the user cache
    directory, so that `go build`, `go test`, `npm install`, and `pip install`
    succeed out of the box.
13. As a developer, I want a denial to tell me *which path* was refused and
    *which setting* would allow it, so that fixing a false positive is a single
    copy-paste.
14. As a developer, I want Crush's own configuration, data directory, and
    instruction files to remain unwritable even when they sit inside a
    writable root, so that the agent cannot grant itself permissions.
15. As a developer, I want Git's execution surfaces (`.git/hooks`,
    `.git/config`) protected, so that a later `git commit` cannot run code
    written by the agent.
16. As a developer, I want repository-local config to require explicit trust
    before it is executed, so that cloning an untrusted repository and running
    Crush cannot execute arbitrary shell.
17. As a developer, I want to withdraw trust from a project, so that I can
    revoke that execution path later.
18. As a developer, I want the readable set to be unchanged, so that the agent
    can still read dependency sources, module caches, and system headers that
    live outside my repository.
19. As a developer, I want the sandbox to say plainly that reads are not
    confined, so that I do not mistake it for a data-confidentiality boundary.
20. As a developer, I want commands that run inside the sandbox to be
    auto-approved, so that the sandbox reduces interruptions instead of adding
    them.
21. As a developer, I want commands that leave the sandbox's boundary —
    network egress, privilege escalation — to prompt even inside the sandbox,
    so that operations like `git push` are never granted implicitly.
    *(Fork: replaces the configured `dangerous` list; egress is the only
    remaining prompt.)*
22. *(Fork: removed. Command exclusions are gone — the boundary is the
    sandbox, and removing isolation is a mode change, not a per-command
    carve-out.)*
23. *(Fork: removed with the exclusions. A tool that cannot work confined
    prompts on its network use, fails on its writes, or is run under
    danger-full-access.)*
24. As a developer, I want YOLO to disable prompts but keep the sandbox, so
    that "trust the model" and "trust the model with my whole disk" are
    separate decisions. *(Fork: YOLO also approves escalations without a
    prompt. Trusting the model is the whole point of the mode; the sandbox is
    still enforced on the widened call, so the "whole disk" decision remains
    separate and explicit.)*
25. As a developer, I want a separate explicit flag to disable the sandbox, so
    that turning it off is a deliberate act with a visible warning.
26. As a developer, I want the agent to be able to request a wider mode for one
    operation, with a written justification, so that legitimate out-of-workspace
    work does not require me to change the session mode.
27. As a developer, I want escalation to be strictly wider than the current
    mode and to apply to exactly one call, so that an approval cannot be
    reused or silently widened.
28. As a developer, I want the approval prompt for an escalation to be
    visually distinct from an ordinary permission prompt, so that I know I am
    being asked to leave the sandbox.
29. As a developer, I want a rejected escalation to leave the operation denied
    and to explain that the agent should stop rather than work around it, so
    that denials do not turn into retries with different tools.
30. As a developer, I want the model to be told, in the tool description, that
    the sandbox is active and how to escalate, so that it requests permission
    instead of guessing.
31. As a developer, I want a denial to be reported to the model in a fixed,
    recognizable form distinct from a user denial, so that it can distinguish
    "the policy refused" from "the human refused".
32. As a developer, I want a broken sandbox runner to be reported differently
    from a policy denial, so that the model does not request escalation to
    work around what is actually infrastructure failure.
33. As a developer, I want the same denial vocabulary whether the kernel
    refused a shell write or the file tool refused an edit, so that the model
    learns one rule.
34. As a developer, I want the sandbox policy to apply to background jobs, so
    that moving a long command to the background does not outlive its
    confinement.
35. As a developer, I want the sandbox policy to apply to the streaming output
    path and the plain capture path identically, so that output mode cannot
    change what is enforced.
36. As a developer, I want bang-mode (`!`) commands to follow a documented
    policy, so that a command I type is treated predictably.
37. As a developer, I want hooks to keep running with my authority, so that
    my own automation is not broken by the agent's sandbox.
38. As a developer, I want it documented that MCP servers and LSP servers run
    outside the sandbox, so that I can reason about what is actually covered.
39. As a developer, I want to inspect the resolved policy (mode, roots,
    protected paths, backend, enforcement) with one command, so that I can
    debug a denial without guessing.
40. As a developer, I want to run a single command under the resolved policy
    for testing, so that I can validate my configuration on a new host.
41. As a developer, I want to configure the sandbox in `crushrc` alongside the
    rest of my configuration, so that I do not need a second file format.
42. As a developer, I want the JSON config to keep working for the same
    settings, so that existing setups are not forced to migrate.
43. As a developer, I want project-level configuration to be able to *tighten*
    the sandbox but never widen it, so that a checked-out repository cannot
    grant itself access.
44. As a developer, I want an agent that is denied a write to see the refusal
    as a normal tool error with a remedy, so that it adapts instead of
    retrying blindly.
45. As a developer, I want the sandbox to work on macOS and Linux without
    installing a container runtime, so that adoption is not gated on Docker.
46. As a developer on a host where bubblewrap cannot create namespaces, I want
    a documented fallback rather than an unexplained failure.
47. As a developer on a host where only Landlock is available, I want to be
    told that the enforcement is partial, rather than being told it is full.
48. As a Windows developer, I want an explicit "unsupported" status instead of
    a silently disabled sandbox, so that I can choose the mode deliberately.
49. As an operator, I want to require a mode that fails closed at startup, so
    that a fleet cannot end up running unconfined after a dependency change.
50. As a maintainer, I want one semantic definition of "which paths are
    writable", so that the shell and the file tools cannot disagree.
51. As a maintainer, I want every backend's rules derived from that single
    definition, so that adding a backend cannot silently change policy.
52. As a maintainer, I want the generated rules covered by golden tests, so
    that a refactor cannot widen the sandbox unnoticed.
53. As a maintainer, I want the in-process shell interpreter's write operations
    fenced, so that a redirect cannot bypass a boundary that only wraps child
    processes.

## Implementation Decisions

### Model

The policy is a mode plus a list of roots. The list is the one deliberate
departure from the DSH model, which carries a single `workspaceRoot`; a single
root forces users to choose between disabling the sandbox and being unable to
install dependencies.

```go
type Mode string // "read-only" | "workspace-write" | "danger-full-access"

type Policy struct {
    Mode       Mode
    Workspace  string   // canonical; the session's working directory
    WriteRoots []string // canonical, deduplicated; includes Workspace
    TempDir    string   // private to this run
}
```

A new `sandbox` package owns this type and is the only place that decides what
"writable" means. Everything else — the shell package, the file tools, every
backend — consumes it. No backend may hard-code a root (`/tmp` included).

### Modes and defaults

- Default mode for a new session is `workspace-write`.
- Default write roots: the workspace, the per-run temp directory, and the
  user cache directory. The cache directory is included because excluding it
  makes `go build`/`go test`/`npm install`/`pip install` fail, and a sandbox
  that breaks the first common command gets turned off.
- `read-only` grants nothing but `/dev/null` and the private temp directory.
- `danger-full-access` bypasses the sandbox entirely and is only reachable by
  explicit configuration or flag.

### Enforcement backends

- Linux: probe `bubblewrap` functionally (run a read-only profile around
  `true`), not by version. Prefer it. Fall back to Landlock when bubblewrap is
  absent or cannot create namespaces.
- The Landlock backend must run in a helper process, because the restriction is
  irreversible and would otherwise confine Crush itself. The helper is a
  hidden subcommand of the Crush binary; it receives the resolved policy on an
  inherited file descriptor, applies no-new-privileges, applies the ruleset,
  then replaces itself with the target argv. The policy must never travel in
  argv or the environment, both of which the sandboxed process can read.
- macOS: generate a Seatbelt profile and invoke `sandbox-exec` from its
  absolute path.
- Windows: no backend. Report `unsupported`.
- Backend selection is probed once per process and cached.
- Backends report an enforcement fact: `full` or `partial`. Landlock reports
  `partial` when the policy contains protected paths inside a writable root,
  because Landlock can only union allowlists and cannot subtract. The fact is
  surfaced, never silently dropped.

### Fail-closed contract

When a confined mode is requested and no backend is usable, the execution is
refused. It never runs unconfined. The error names the missing dependency, the
platform, and the two remedies (install it, or deliberately select
`danger-full-access`).

*(Fork: the opt-in startup requirement (`fail_if_unavailable`) was removed;
the execution-time refusal above remains.)*

### Where enforcement attaches

- The shell package is the single choke point for process execution: the bash
  tool, background jobs, hooks, shell-config loading, and command substitution
  all reach it. A wrapper is applied at the point where the shell interpreter
  spawns a child, and at the shebang dispatch path, so that both go through
  the selected backend.
- The interpreter runs in-process, so child-process wrapping alone is
  insufficient: redirections and `source` never spawn a process. The
  interpreter's file-open hook is replaced with one that checks write intent
  against the policy before opening. Read paths, directory listing, and stat
  hooks are left at their defaults, because reads are unconfined.
- The shell executes through the same path for capture, stream, and background
  jobs, so all three inherit the policy without separate plumbing.
- The policy is carried per execution, not stored on the backend, so concurrent
  sessions and one-shot escalations can hold different policies at once.

### Protected paths inside writable roots

The following are write-denied even when they fall inside a writable root, and
cannot be re-allowed:

- Crush configuration and state: `crushrc`, `.crushrc`, `crush.json`,
  `.crush.json`, the `.crush` directory, the global config directory, the data
  directory.
- Instruction files: `AGENTS.md`, `CRUSH.md`, `CLAUDE.md`, `GEMINI.md` and
  their `.local` variants.
- Git execution surfaces: `.git/hooks`, `.git/config`, `.git/config.lock`, and
  the resolved `gitdir` target for worktrees and submodules.
- Shell startup files when they fall inside a writable root (dotfiles
  repositories): `.bashrc`, `.zshrc`, `.profile`, fish config.

Mechanism per backend: bubblewrap re-mounts the path read-only after the
writable mount; Seatbelt appends a trailing deny form (last match wins).
Landlock cannot express this, which is why it reports `partial` and why
bubblewrap is preferred. (The PRD draft proposed an in-process fence backend
that checked before the syscall; the fork removed it — see the header note.)

### Project configuration trust

Repository-local `crushrc` and `crush.json` are not executed until the project
is explicitly trusted. Trust is recorded per canonical project path in user
state and can be revoked. The prompt appears once per project and names the
files that would run. Declining loads global configuration only and leaves the
project configuration unread. This closes the "write a config file, wait for a
reload" escape independently of whether the file is write-protected.

Trust is required in addition to, not instead of, write protection: a
repository can be trusted and still must not be modifiable by the agent.

### Approval interaction

- A command that is actually confined runs without a prompt.
- A command that leaves the boundary — matching the built-in network list
  (`curl`, `wget`, `ssh`, `scp`, `rsync`, `nc`, `git push`, `gh`, `sudo`,
  `su`, `doas`, and others) or the configured `network_commands` additions —
  prompts, even when confined. Every invocation in a command line is checked,
  including piped and chained ones.
- `danger-full-access` is full delegation: nothing prompts, network commands
  included.
- Hooks, MCP servers, and LSP servers remain outside the sandbox and are
  documented as such. Project-level hooks additionally require the project to
  be trusted.
- YOLO disables prompts but no longer disables the sandbox. Disabling the
  sandbox is a separate, explicitly named flag.
- *(Fork: an escalation under YOLO is approved without a prompt — "trust the
  model" covers the one-call widening. The widened policy still applies to
  exactly one call, so YOLO never becomes a standing grant to leave the
  boundary. An allow-list entry for a tool is *not* consent to widen: it is a
  statement about the tool, made before the refusal is known.)*
- *(Fork: when no approval surface is attached — a non-interactive run, a
  headless caller — an escalation fails immediately as unavailable instead of
  waiting for a prompt nobody can answer.)*
- *(Fork: the `dangerous` list and `exclude_commands` are removed. The
  network list is a prompt filter, not a boundary — an interpreter such as
  `python -c` can still reach the network unprompted; the miss costs one
  confined call, which is accepted.)*

### Escalation

The tools that can be confined (`bash`, `write`, `edit`, `multiedit`,
`download`) gain a `sandbox_permissions` argument and a paired
`justification`:

- The two are only valid together; the justification must be a non-empty
  sentence.
- The requested mode must be strictly wider than the call's effective mode, and
  the check happens at execution, not in the schema, because the effective mode
  is per-call while schemas are global.
- Approval applies to exactly one call and is not recorded as a session grant.
- The ways an escalation can be resolved without a prompt are exactly two:
  YOLO (which is the decision to trust the model) and an explicit user
  approval. A tool allow-list entry does not apply, and neither does a session
  grant or a hook allow. *(Fork: YOLO approves escalations.)*
- With no approval surface attached, an escalation is reported as unavailable
  immediately; it never waits for a prompt nobody can answer. *(Fork.)*
- A rejection leaves the operation denied and returns a message telling the
  model to stop and explain rather than retry by another route.
- The model-facing denial is a fixed marker naming the mode, so a kernel denial
  and a file-tool denial look the same to the model, followed by a hint that
  escalation is available.

### Diagnostics

Every confined execution carries two independent classifiers:

- **Denial**: the backend-specific text a refused write produces. Per backend,
  not a union — a union would claim denials a backend never emits.
- **Runner failure**: the backend's fatal signature and, where applicable, its
  reserved exit code. Checked before denials, because a runner failure means
  the command never ran, while a denial means confinement worked. Confusing the
  two would make the model escalate around a broken sandbox.

### Correctness fixes that the sandbox depends on

These are prerequisites, not separate features:

- The read-only shortcut must consider redirections and every other construct
  that can write, or must be replaced by "confined implies auto-approved".
- Path containment in the file tools must resolve symlinks before comparing.
- Read tools that currently perform no containment check must use the shared
  helper when they later gain one; today they intentionally do not check,
  because reads are unconfined.
- Write tools must complete policy and permission checks before creating parent
  directories, so a denied write leaves no filesystem trace.

### Configuration surface

A `sandbox` builtin in `crushrc`, alongside `permissions`:

```bash
sandbox mode workspace-write
sandbox allow-write ~/.kube
sandbox deny-write ./secrets
permissions network "docker push"
```

And the equivalent under `permissions.sandbox` in JSON. Project-level
configuration may only add denials; it may not add write roots, widen the
mode, or disable the sandbox. The generated JSON schema is
regenerated as part of the change.

*(Fork: `sandbox exclude-command` and `sandbox fail-if-unavailable` are
removed; `permissions network` replaces the `dangerous` list.)*

CLI: a mode flag, an explicit disable flag that prints a warning, a status
subcommand that prints the resolved policy and backend, and an execution
subcommand that runs one command under the resolved policy for debugging.

### Observability

The resolved policy, backend name, and enforcement fact are included in the
runtime-state output the agent can request, and in the session's status line.
Backend selection, enforcement level, and each denial are logged.

## Testing Decisions

### What makes a good test here

Tests assert observable behavior: which paths a command can write, what the
model sees after a denial, whether an execution was refused or run. They do not
assert internal structure — not the shape of the policy struct, not which
function built the profile, not the order of arguments except where the order
is the contract (per-backend generated rules are characterized by golden files,
because the generated text *is* the artifact).

### Seams

Five seams, in descending order of preference. The first is the one that
matters; the rest exist to keep most of the logic testable without a kernel.

1. **Shell execution entry point (existing, highest).** The package-level run
   function and the stateful shell's execute methods are already the single
   funnel for the bash tool, background jobs, hooks, and config loading. All
   behavioral enforcement tests go here: a script that writes inside the
   workspace succeeds, one that writes outside fails, a redirect to a protected
   path fails, a symlink out of the workspace fails. If only one seam survived,
   it would be this one.
2. **Policy evaluation (new, pure).** Write-containment and protected-path
   resolution are pure functions of a path and a policy. Unit tests cover
   symlinks, `..`, non-existent paths, protected paths inside and outside
   roots, and roots that are ancestors of each other — no kernel, no
   subprocess. This is where the majority of the cases live, because it is the
   only seam cheap enough to run exhaustively.
3. **Backend rule generation (new, characterization).** Each backend's argv or
   profile text is generated from the same policy and pinned with golden files,
   including cross-backend parity assertions: for one policy, every backend's
   generated rules must describe the same writable set. This is the seam that
   catches "one backend granted `$TMPDIR` and another hard-coded `/tmp`".
4. **Permission service (existing).** The existing mock permission service is
   reused to assert: confined execution is auto-approved, network commands
   still prompt, escalation requests are approved once and never persisted,
   YOLO approves an escalation, an allow-list entry does not, and a missing
   approval surface fails fast. *(Fork: dangerous-command and exclusion
   assertions were removed with the features.)*
5. **File tools (existing).** The write, edit, multiedit, and download tools are
   table-tested against a temp workspace with a policy attached, asserting that
   a denied write produces the standard denial marker and leaves no directory
   or file behind.

No per-tool sandbox seam is introduced. The tools call one shared helper, which
is covered at seam 2; the table tests at seam 5 exist to prove the wiring, not
to re-test the logic.

### Modules

- New `sandbox` package: policy type, root resolution, write check,
  protected-path resolution, backend interface, rule generation per platform,
  deny/runner-failure classifiers.
- New hidden entry point for the Landlock helper.
- `shell` package: policy on the execution options, write-intent file-open
  hook, child-process wrapping at both spawn paths, background job propagation.
- `agent/tools` package: shared write check in the mutating tools, sandbox
  parameters and escalation on the confined tools, description updates.
- `permission` package: distinguish sandboxed and unsandboxed requests;
  escalation approvals.
- `config` package: sandbox settings, trust records, layer rules for project
  configuration.
- `shellconfig` package: the `sandbox` builtin.
- `workspace` and `backend` packages: bang-mode policy plumbing.
- `cmd` package: flags, status subcommand, debug execution subcommand, schema
  regeneration.

### Prior art in the repository

- Existing shell tests already assert process-isolation properties (signals,
  TTY detachment, process-group kill) by running real commands against a temp
  directory; the sandbox behavior tests follow that shape.
- Existing tool tests build a temp workspace and drive the tool through a mock
  permission service; the file-tool sandbox tests reuse that harness.
- Existing config tests exercise layered loading and precedence through the
  shell config loader; the "project config tightens but does not widen" tests
  follow that pattern.
- The repository already regenerates golden output through the build tooling;
  backend rule characterization uses the same `-update` convention.

### Test matrix

| Layer | Runs everywhere | Requires a backend |
| --- | --- | --- |
| Policy evaluation | yes | no |
| Backend rule generation (golden) | yes | no |
| Approval and escalation | yes | no |
| File tools | yes | no |
| Shell behavior | skipped when no backend | yes |

Behavioral tests that need a kernel backend must skip — not fail — when the
dependency is missing, and the skip reason must name the dependency. Fail-closed
behavior itself (the "no backend means refuse" case) is tested without a
backend, by injecting an unavailable backend.

## Out of Scope

- **Network isolation.** No proxy, no domain allowlist, no network namespace.
  A confined command can still reach the network. This is the single largest
  deliberate gap and must be stated in user-facing documentation, because it
  changes the meaning of the mode names: `read-only` means "does not write",
  not "cannot exfiltrate". *(Fork: mitigated, not solved — a network command
  list prompts on the common egress commands; interpreters remain unprompted.
  Backend-level isolation via `--unshare-net` / `(deny network*)` is the
  clean future fix, at the cost of dropping the Landlock backend, which
  cannot express it.)*
- **Read confinement.** Reads are unrestricted in every mode. The agent can
  read `~/.ssh`, `~/.aws`, and every `.env` on the machine. Adding a read-deny
  list later is a separate feature with its own consequences (it removes the
  Landlock backend from consideration, since Landlock cannot subtract).
- **Credential masking, secret injection, environment scrubbing.** Environment
  variables are inherited as today.
- **Sandboxing hooks, MCP servers, and LSP servers.** They keep the user's full
  authority and are documented as outside the boundary. Sandboxing them is a
  separate decision because most of them need network and broad reads to
  function.
- **Windows backend.** Windows reports `unsupported` and fails closed when a
  sandbox is requested. WSL2 uses the Linux backend.
- **Container, microVM, or remote execution backends.** Those replace the
  execution capability rather than wrapping it, and are a different feature.
- **Sandboxing the config loader's own command substitutions.** Configuration
  is a trust root; it runs before and above the sandbox. This is why project
  configuration requires trust.
- **Per-agent or per-skill sandbox policy.** Policy is per session and per
  call, not per agent definition.
- **Retroactive protection.** The sandbox does not inspect or repair files that
  were already written.

## Further Notes

### Relationship to the DSH model

The mechanism — a mode plus writable roots, enforced by a per-platform runner,
selected by functional probe, failing closed — is taken directly from DSH, as
is the discipline of reporting enforcement as a fact, classifying runner
failure separately from policy denial, and providing a strictly-wider
escalation path.

Three things are deliberately different:

1. **Roots are a list.** A single root forces the choice between disabling the
   sandbox and being unable to install dependencies. The added complexity is
   negligible; the alternative is a sandbox that is off in practice.
2. **Protected paths exist.** DSH does not need them because its configuration
   lives outside the workspace. Crush executes repository-local configuration,
   so it does.
3. **Project config requires trust.** Same reason. Without it, the write
   protection on `crushrc` is the only thing standing between the agent and
   arbitrary code execution, and a single gap in that protection is a full
   escape.

DSH also demonstrates two traps worth recording: a hard-coded `/tmp` in one
backend's profile builder produced exactly the "one tool can write there, the
other cannot" drift its own design forbids; and a "single semantic home" that
is only enforced by a comment drifts. Here, the parity assertions in the
backend rule tests are the enforcement mechanism, not the comment.

### Open questions

1. **Default mode.** This document says `workspace-write`. The counter-argument
   is that a sandbox that breaks first-run workflows gets disabled, so the
   worse outcome is a loud default that users learn to bypass. The mitigation
   assumed here is the cache-root default plus actionable denial messages; if
   that proves insufficient, the fallback is `read-only` by default with a
   one-time prompt on the first write.
2. **Should the cache directory be a default root?** Included on usability
   grounds. It does mean a compromised agent can write into a cache that other
   tools later read; the risk is accepted for v1 and should be revisited if
   cache-poisoning turns out to be reachable in practice.
3. **PTY-dependent tools.** Some CLIs need a real controlling terminal that the
   detached-session model deliberately removes. Whether they belong in the
   default exclusion list, or simply fail, needs a decision backed by which
   tools actually break.
4. **Bang-mode policy.** This document assumes user-typed commands follow the
   session policy. The alternative — user-typed commands run unconfined,
   because the user typed them — is defensible but must be stated explicitly
   in the UI, not left ambiguous.
