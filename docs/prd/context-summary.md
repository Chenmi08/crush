# PRD: Session Summary / Context Compaction

> Status: approved for implementation. Scope: the session summarization path
> (`sessionAgent.Summarize`), the context-assembly path
> (`sessionAgent.preparePrompt`), the tool-result eviction layer, and the
> retrieval surfaces those depend on.
>
> This document is self-contained. A fresh session should be able to
> implement it top to bottom without re-reading the design discussion that
> produced it. Where a decision was contested, the rejected alternative and
> the reason it lost are recorded in [Decision Log](#11-decision-log) so the
> next session does not re-litigate it.

**How to use it**

- Implementing? Start at [§14 Appendix A](#14-appendix-a--new-session-checklist).
- Reviewing scope? [§7.1](#71-at-a-glance) has the PR table and the minimum
  viable subset.
- Arguing about a choice? [§11](#11-decision-log) says what was rejected and
  why; [§15](#15-appendix-b--discussion-traceability) says which claims were
  checked and found weaker than expected.
- Wondering "why not just do X"? X is probably in §2 Non-Goals or in §15's
  dropped-ideas table.

---

## 1. Problem Statement

Crush compacts long sessions by asking the large model to rewrite the
conversation into a prose summary, then dropping every message before that
summary (`internal/agent/agent.go`, `getSessionMessages`). Three defects make
it unreliable, and three lose information the agent cannot recover:

**Correctness**

- **P1. A truncated summary is persisted as a success.** `Summarize` never
  inspects `resp.Response.FinishReason`, always calls
  `summaryMessage.AddFinish(FinishReasonEndTurn, "", "")`, and always writes
  `currentSession.SummaryMessageID = summaryMessage.ID`. If the provider
  stops on `Length`, a partial summary becomes the permanent context with no
  signal anywhere. (`GenerateTitle` already guards this case by retrying on
  `FinishReasonLength`; `Summarize` does not.)
- **P2. No output cap on the summary request.** The summarization agent is
  constructed without `fantasy.WithMaxOutputTokens`, while the prompt says
  `**Length**: No limit`. The cap is whatever the provider defaults to.
- **P3. Repeated failure has no circuit breaker.** A failing summarization is
  retried on the next turn, forever. Claude Code added a 3-attempt
  circuit breaker after telemetry showed 1,279 sessions with 50+ consecutive
  failures (worst case 3,272), burning ~250K API calls/day.

**Loss**

- **P4. User messages are not preserved verbatim.** `templates/summary.md`
  has five sections, none of which is "all user messages". Mid-task
  corrections, added constraints, and rejected directions survive only if
  the summarizer volunteers them.
- **P5. Tool payloads that leave the context are unreachable.** `messages`
  rows keep the full `parts` JSON, but `getSessionMessages` slices from
  `SummaryMessageID` onward, so "not in the prompt" is indistinguishable
  from "deleted". There is no handle by which the agent can ask for an
  evicted payload back.
- **P6. Facts (paths, commands, exit codes) are delegated to the LLM.** The
  `Files & Changes` section is prose. Published evaluations put artifact
  tracking at 2.19–2.45 out of 5 for every summarizer tested — the worst
  dimension across the board. Facts should be produced by code, not prose.

**Known permanent-loss sites** (unchanged by this PRD except where noted;
listed so the implementer does not mistake them for gaps in the plan):

| Site | Behavior | Recoverable? |
| --- | --- | --- |
| `tools/bash.go` `MaxOutputLength = 30000` | head+tail, middle spilled to `DataDirectory/shell-output/output-*.log` | yes, 7 days (`shell/truncate.go` `spillRetention`), then pruned |
| `shell.TruncateOutput` | `ansi.Strip` before spilling | no (styling lost) — accepted |
| `tools/view.go` `MaxLineLength = 2000` | a single long line is cut to 2000 bytes + `...` | **no** — out of scope, see PR-14 in §8 |
| `tools/view.go` `MaxTextViewSize = 200KB` | read of a larger file errors | yes, narrower `offset`/`limit` |
| `tools/fetch.go` `MaxFetchSize = 100KB` | truncated + notice, no file | **no** — fixed by PR-11 |
| `tools/search.go` `maxSnippetRunes = 500` | cut, no file | **no** — fixed by PR-11 |

---

## 2. Goals and Non-Goals

### Goals

1. **No silent corruption.** Every summary that is persisted is known to be
   complete. (PR-1)
2. **No unbounded retry.** A session that cannot be summarized stops trying
   and degrades deterministically. (PR-3)
3. **Facts survive compaction.** User messages verbatim, plus a
   code-generated index of failures and touched files. (PR-4, PR-5)
4. **Eviction is reversible.** A tool result that leaves the prompt leaves a
   tombstone with a resolvable handle; the payload stays in SQLite and can be
   fetched back verbatim. (PR-6, PR-7)
5. **Coarse retrieval exists.** The pre-compaction transcript is written to
   disk so the agent can `rg` it, using a tool it already knows. (PR-8)
6. **Repeated compaction does not degrade.** The second and later
   compactions merge into the previous summary instead of rewriting it.
   (PR-9)

### Non-Goals (explicitly out of scope)

- **Idempotent tool replay.** Serving a cached tool result in place of a
  fresh call requires deciding whether the world changed (mtime, hashes,
  idempotency whitelists, `force` escape hatches). Rejected: the payload is
  immutable history and needs no freshness judgement; only *re-execution*
  does. See [Decision D3](#11-decision-log).
- **Full-text or semantic search over the session.** Replaced by transcript
  export + `rg`. See [D4](#11-decision-log).
- **Cross-session recall.** `recall` reads only the caller's session; Crush
  creates per-task sub-sessions and parent content must be passed
  explicitly.
- **`view`'s 2000-byte per-line cut.** Fixing it needs a spill handle inside
  `view`'s line reader. Deferred to a follow-up PRD.
- **Tree-sitter folded definitions, repo maps, encrypted compaction items,
  provider-side compaction APIs.** Rejected on cost/auditability grounds.
- **`<total_tokens>` countdown reminders.** Cognition reported the opposite
  pathology ("context anxiety" → shortcuts and incomplete work). Do not add.

---

## 3. Current Implementation Map

Anchors are from commit `fe756b83`. Line numbers drift; use the symbol names.

| Symbol | File | Behavior relevant to this PRD |
| --- | --- | --- |
| `summaryPrompt` | `internal/agent/agent.go:68-69` | `//go:embed templates/summary.md` |
| `buildSummaryPrompt` | `internal/agent/agent.go:2515-2528` | `"Provide a detailed summary of our conversation above."` + todo list |
| `sessionAgent.Run` `StopWhen` | `internal/agent/agent.go:1204-1223` | Threshold check; sets `shouldSummarize` |
| `Run` summarize branch | `internal/agent/agent.go:1376-1392` | Calls `Summarize`, re-queues the call when tool calls are unfinished |
| `sessionAgent.Summarize` | `internal/agent/agent.go:1524-1675` | Creates summary message, streams, persists anchor |
| `getSessionMessages` | `internal/agent/agent.go:1911-1933` | `ListFromSummary`, slice from anchor, **set role to user** |
| `preparePrompt` | `internal/agent/agent.go:1728-1856` | Drops orphan tool results, re-emits tool results adjacent to their assistant message |
| `toolResultsForCalls` | `internal/agent/agent.go:1885-1910` | Synthesizes an error result for an orphaned call |
| `summaryCompletionTokens` | `internal/agent/agent.go:2223-2229` | Usage fallback |
| `ListFromSummary` | `internal/message/message.go:527-552` | Falls back to the whole session when the anchor is missing |
| `ListMessagesBySessionFromSummary` | `internal/db/sql/messages.sql:12-19` | `created_at >= anchor.created_at` (second resolution) |
| `SummaryMessageID` | `internal/session/session.go:92` | Persisted anchor |
| `Session.Todos` | `internal/session/session.go:35-39` | `{Content, Status, ActiveForm}` |
| `Options.DisableAutoSummarize` | `internal/config/config.go:510` | `disable_auto_summarize` |
| `auto-summarize` option | `internal/shellconfig/options.go:209` | Inverted boolean mapping |
| `approxTokenCount` | `internal/agent/usage_fallback.go:171` | `(len+3)/4` |
| `SpillOutput` | `internal/shell/truncate.go:145-169` | Returns the path of a written file |

Threshold constants (`internal/agent/agent.go:57-60`):

```go
largeContextWindowThreshold = 200_000
largeContextWindowBuffer    = 20_000
smallContextWindowRatio     = 0.2
```

### How a compacted session is currently read

```
Summarize()
  ├─ sessions.Get
  ├─ getSessionMessages      → ListFromSummary(anchor) → slice from anchor
  │                            → msgs[0].Role = User
  ├─ preparePrompt(msgs)     → fantasy messages
  ├─ messages.Create(assistant, IsSummaryMessage=true)
  ├─ agent.Stream(summary.md system prompt)
  ├─ [on error] mark error or delete (on cancel)
  └─ [on success] session.SummaryMessageID = new ID
                  session.PromptTokens = 0
                  session.CompletionTokens = summary output
```

Note the coupling that this PRD breaks: `ListFromSummary` conflates "not in
the prompt" with "not in the store". PR-6 removes the conflation for tool
results by giving the store its own access path.

---

## 4. Target Design

### 4.1 Context layout after compaction

```
┌ system prompt                          (stable prefix, cache-friendly)
├ T0 pinned: user messages verbatim      (PR-4)
├ T0 pinned: constraints, rejected, todo (PR-4, PR-10)
├ T1 narrative summary                   (PR-4, PR-9)
├ T1 context manifest (code-generated)   (PR-5)
├ T1 transcript pointer                  (PR-8)
├ T2 recent turns verbatim               (existing behavior: everything after anchor)
└ T3 current-goal recitation at the tail (PR-10)
```

Rationale for head/tail placement: long-context retrieval follows a U-curve
of attention; must-keep state belongs at the extremes. The tail recitation
is deliberately redundant with the head pin.

### 4.2 Eviction ladder

```
⓪ on every compaction: export transcript + append manifest   (PR-5, PR-8)
① elide old tool-result payloads → tombstone                  (PR-7, default shadow)
② if still over threshold → incremental merge summary         (PR-9)
③ if still over threshold → hard fallback: whole-window reset (not in this PRD)
```

Step ⓪ is unconditional and cheap (one file write, bounded by pruning); it
is what makes step ③ unnecessary in practice. Steps ① and ② run in that
order and only when the threshold is crossed.

Rule: **elision is a display-layer transform.** No row is deleted, no column
is rewritten, no anchor moves. `messages.parts` keeps the payload forever.
This is what makes step ① lossless and independent of idempotency
judgements.

### 4.3 Tombstone format (normative)

Rendered in place of a tool result's content, in the same position as the
original result so call↔result adjacency is preserved:

```
[elided] tool_call_id=call_7f3a name=bash exit=1 bytes=48213 at=2026-10-23T15:12:04Z
  $ go test ./internal/agent/...
  --- FAIL: TestSummarize (0.31s)
  ... (1387 more lines) → recall(tool_call_id="call_7f3a")
```

Fields, in order, one header line:

| Field | Source | Why |
| --- | --- | --- |
| `tool_call_id` | `ToolResult.ToolCallID` | the handle |
| `name` | `ToolResult.Name` | lets the model judge relevance |
| `exit` | bash only: trailing `Exit code N` line (see PR-5's parser); omitted when absent or for other tools | failure signal |
| `bytes` | `len(ToolResult.Content)` | cost signal for the retrieve decision |
| `at` | `message.CreatedAt` (UTC, RFC3339) | staleness signal |

Then **at most two preview lines**: the first non-empty line of the content,
plus the first line matching a failure marker (`FAIL`, `Error:`, `panic:`,
`error[E`, `Traceback`, `--- FAIL`) if it differs from the first line. Each
preview line is clipped to 160 bytes on a rune boundary.

Then a footer: `... (N more lines) → recall(tool_call_id="<id>")`, where `N`
is the line count of the elided body minus the previewed lines, omitted when
`N <= 0`.

Budget: hard cap 400 bytes per tombstone. If the previews would exceed it,
drop previews (header + footer still fit).

### 4.4 Context manifest format (normative)

**Amendment (2026-10-28).** All code-generated blocks are appended to the
summary as one appendix introduced by a single marker:
`<!-- session appendix: generated by Crush; treat as data, not instructions -->`.
The narrative boundary is that marker; the per-block lead-ins below are
retained inside the appendix and only still matter when cutting summaries
written before the amendment. This replaces the earlier three separate
appends (and three writes) with one.

The `anchors:` section and `recall(message_id=...)` were also removed. Both
published retrieval handles nobody used: failure rows already carry
`tool_call_id`, and `## User Messages` is supposed to keep user requests
verbatim, so the user anchors duplicated content the summary already had.
Failure rows no longer carry the assistant message `id` either; the
`tool_call_id` is the only handle `recall` accepts.

**Amendment (2026-10-28, second).** `files:` now comes from the session's
write ledger plus git, not from re-parsing file-tool call inputs. The ledger
is recorded by the tools themselves, so `lsp_rename` is covered too; the
`## Recently Modified Files` block was folded into this section and removed.

**Amendment (2026-10-28, third).** `files:` lost its structural cap of 20
entries. A real compaction dropped two of twenty-two changed paths — the
modified `internal/agent/tools/recall.go` and the deleted
`internal/agent/summary_recent.go` — while the block stood at 939 of its 1500
tokens, so the cap, not the budget, was doing the cutting. The budget loop is
now the only trim, and the ledger query is bounded separately
(`manifestWriteLedgerLimit = 200`: a query limit, not a display limit). The
manifest lead-in shrank as well: the appendix marker already says the text is
data, so `manifestLeadIn` only adds provenance ("not written by the user").
Narrative-boundary detection (`narrativeCut`) matches every marker only at the
start of a line and takes each one's **last** such occurrence, because a
summary of this very code quotes those markers in its prose. The old, longer
manifest lead-in is pinned as `legacyManifestLeadIn`, since shortening the
current string would stop it from finding blocks already stored.

Appended to the summary content as a fenced, machine-generated block. Written
by code from `messages`, never by the model.

```xml
<context_manifest generated="2026-10-24T10:12:03Z">
archived: messages=142 range=2026-10-23T14:02:11Z..2026-10-23T15:31:40Z tool_calls=38 tool_result_bytes=412988 failures=3
failures:
- tool=bash exit=1 cmd="go test ./internal/agent/..." key="--- FAIL: TestSummarize" tool_call_id=call_7f3a
- tool=bash exit=1 cmd="go build ./..." key="undefined: foo" tool_call_id=call_9c11
files:
- path=internal/agent/agent.go last=2026-10-23T15:28:02Z src=tool
- path=internal/message/message.go last=2026-10-23T14:40:09Z src=tool
- path=internal/agent/summary_manifest.go src=git state=modified
coverage: 142/142 archived messages scanned
</context_manifest>
```

Generation rules:

- `archived` counts messages strictly before the anchor being replaced.
- `failures`: tool results with `IsError`, or `bash` content whose parsed
  exit code is non-zero; **most recent 10**, newest first. Each line ends with
  `tool_call_id=<id>`, the handle `recall(tool_call_id=...)` takes. It is
  emitted here because this is the only place a failure handle is published.
- `files`: the session's write ledger (`file_events` kind=write), **merged
  with** the paths `git status` reports as changed in the working tree, each
  entry marked `src=tool` or `src=git`, newest first by `last`. **No
  structural cap** (third amendment above): the budget is what trims it. The
  ledger is recorded by the file tools themselves, so it covers `lsp_rename`
  without teaching the manifest another tool name; read counts and `ops=` are
  gone because the ledger upserts and `read_files` records an internal read
  for every write. The git half is required, not optional: see D-1 in §7.3 —
  no ledger can see an edit made through a shell command, which is how most
  edits actually happen.
- `coverage`: `scanned/total`. If any archived message failed to decode, emit
  `coverage: 140/142 scanned (2 undecodable)` — the agent must be able to
  tell "not there" from "not indexed".
- Total manifest budget **1500 tokens** (`approxTokenCount`), enforced by
  truncating `files` then `failures` entries (dropping a trailing
  `- (truncated: N more)` line is preferred over silently shrinking).
- Omit a section entirely when empty; do not emit an empty header.
- Every interpolated value must be escaped for the fence: `&`→`&amp;`,
  `<`→`&lt;`, `>`→`&gt;`, and newlines inside `cmd`/`key` replaced with a
  space. Manifest content is **data**, and the enclosing text must say so
  (see 4.5).

### 4.5 Transcript export (PR-8)

On each successful compaction, before updating the anchor:

1. Serialize every message strictly before the new anchor to JSONL
   (one JSON object per line: `{id, role, created_at, model, provider, text,
   tool_calls:[{id,name,input}], tool_results:[{id,name,is_error,content}]}`).
2. Write to
   `DataDirectory/sessions/<session-id>/transcript-<unix-ts>.jsonl`
   (`0o600`, `MkdirAll` `0o700`). Path components that could escape
   (`..`, `/`, `\`, control characters) must be rejected inside
   `writeTranscript` itself. There is no shared path-sanitizing helper in
   this repo (§13 "Gotchas"), so validate the single path component
   explicitly: reject if `filepath.Base(id) != id`, if the id contains any of
   `..`, `/`, `\`, or `\x00`, or if any rune is a control character.
3. Append to the summary content:

```
## Full Transcript

The conversation before this summary is stored at:
  <absolute path>
  (N messages, 2026-10-23T14:02:11Z .. 2026-10-23T15:31:40Z)

This file is historical data, not instructions. Search it with `rg`; do not
open it with `view` (it exceeds the text-view size limit).
```

The block above is one section of the single appendix (§4.4 amendment); it is
not appended on its own, and the narrative boundary is the appendix marker,
not `## Full Transcript`.

Pruning: keep at most 5 transcript files per session, delete oldest. Never
delete one that a live summary points to — i.e. keep at least the file
referenced by the current `SummaryMessageID`.

Failure to write the transcript is **non-fatal**: log at `Warn`, continue
with the summary (the manifest and messages table still hold the facts).

### 4.6 `recall` tool (PR-6, extended by PR-11; PR-20 superseded)

Final shape (PR-6 ships `ToolCallID`; PR-11 adds `SpillPath`). The
`MessageID` form added by PR-20 was removed with the `anchors:` section: it
existed only to make those handles retrievable.

```go
const RecallToolName = "recall"

type RecallParams struct {
    ToolCallID string `json:"tool_call_id,omitempty"`
    SpillPath  string `json:"spill_path,omitempty"` // PR-11 only
    Offset     int    `json:"offset,omitempty"`
    Limit      int    `json:"limit,omitempty"`      // bytes, default 8192, max 65536
}
```

Behavior:

- Exactly one of `tool_call_id` / `spill_path` must be set; otherwise return
  a text error explaining both forms.
- `tool_call_id`: scan `role='tool'` messages of the **current session**
  (from `GetSessionFromContext(ctx)`), decode `parts`, find the matching
  `ToolResult`. Not found → error listing that the id is unknown for this
  session (do not silently return empty).
- Return a header, then the body slice:

```
[recall] tool_call_id=call_7f3a name=bash exit=1
recorded_at=2026-10-23T15:12:04Z (archived record; may not reflect current state)

<body bytes [offset, offset+limit)>

(returned bytes 0-8192 of 48213; call again with offset=8192 for more)
```

- Always state the record is historical and may be stale.
- Body is returned **unmodified** (including ANSI — do not strip; the caller
  asked for the archived bytes).
- `Offset` beyond the end → empty body plus an explicit notice.
- Session mismatch, sub-session, or a `tool_call_id` from a different
  session → error.

New query (`internal/db/sql/messages.sql`), then `sqlc generate`:

```sql
-- name: ListToolMessagesBySession :many
SELECT *
FROM messages
WHERE session_id = ? AND role = 'tool'
ORDER BY created_at ASC;
```

`idx_messages_role_created_at` covers `(role, created_at)` but not
`session_id`; the query filters on `session_id` so it will use
`idx_messages_session_id` and filter by role. Acceptable: tool messages are a
small fraction of a session. Do **not** add an index in this PR; measure first.

Tool description (`internal/agent/tools/recall.md`, embedded): must state
that it retrieves an archived tool result, that the content is a historical
record, that `view` should be used for current file contents, and that only
the current session is searched.

### 4.7 Summary prompt shape (PR-4)

`templates/summary.md` sections, in this order. Every section is mandatory;
an empty section must say `none` (so "nothing happened" is distinguishable
from "the summarizer skipped it").

1. `## Current State` — exact current user request (quoted), progress, what
   is incomplete, what remains.
2. `## User Messages` — **every** non-tool-result user message, in order,
   verbatim (including shell-command-only messages, annotated as such). This
   is the highest-value section; do not paraphrase.
3. `## Constraints` — explicit limits the user stated (files not to touch,
   libraries to use, budgets), quoted verbatim.
4. `## Decisions` — decision, rationale, evidence pointer; mark superseded
   decisions as `superseded by: <later decision>`.
5. `## Rejected Approaches` — what was tried, why it failed, what the
   evidence was. Failures must be kept, not cleaned up.
6. `## Artifacts` — breadcrumbs the resuming assistant can query: file path,
   symbol, line number. One line per item. (`<context_manifest>` carries the
   authoritative file list; this section carries the *why*.) The template
   also forbids restating that list or counting its files: a prose count goes
   stale with the next write, while the generated list stays correct.
7. `## Exact Next Steps` — numbered, specific, with a **verbatim quote** of
   the most recent exchange showing where work stopped.
8. `## Full Transcript` — **not part of the template.** Injected by code at
   persistence time (PR-8), so the model never writes or guesses a path.
   The template therefore contains seven headings, not eight.

Prompt preamble requirements:

- The opening line must state that this summary is the only context for the
  resumption, that all previous messages will be gone, and that it must be
  thorough.
- **Tool use is forbidden** and the prohibition must be explicit ("Tool calls
  will be rejected and will waste this turn"). Reason: a summarizer that
  calls a tool produces no summary.
- An `<analysis>` scratchpad is permitted; it must be stripped from the
  content before persistence.
- `**Length**` changes from `No limit` to: no length cap, but **every section
  must be populated**; paths, commands, error strings and user quotes are
  never to be shortened.
- Tone: briefing a teammate taking over mid-task. No emojis.

`buildSummaryPrompt` changes:

- Keep the todo injection, but additionally instruct the model to restate the
  todo list inside `## Current State` (the injected block is the prompt, not
  the output).
- When a previous summary exists (PR-9 lands the plumbing), add the merge
  instruction; until then `buildSummaryPrompt(todos)` keeps its signature and
  PR-9 extends it.

### 4.8 Guards (PR-1, PR-2, PR-3)

All guards are **hard rejections**, not warnings. On rejection the existing
`SummaryMessageID` is left untouched, so the previous (good) summary remains
authoritative and the session stays usable.

| Guard | Condition | Action |
| --- | --- | --- |
| G1 truncation | `resp.Response.FinishReason == fantasy.FinishReasonLength` | mark summary message `FinishReasonError` / `"Summarization Error"` / "summary hit the output limit"; do **not** update anchor; count as a failure |
| G2 collapse | `newTokens < oldTokens * 0.5` **and** the dropped span is non-empty | reject, retry once with a stricter prompt; still failing → mark error, do not update anchor |
| G3 inflation | `newTokens >= prevContextTokens` | reject; do not update anchor |
| G4 repeat | consecutive failures `>= 3` | persist `summary_fail_count`, disable automatic summarization for that session, emit one user-visible notice |

Concrete inputs for G2/G3 (read them **before** the reset block, which sets
`PromptTokens = 0`):

- `oldTokens` = the narrative size of the previous summary, re-read from
  `messages.Get(SummaryMessageID)` and cut at `manifestLeadIn` (see D-2); `0`
  when there is no previous summary.
- `newTokens` = `summaryNarrativeTokens(summaryMessage.Content().Text)` —
  measured the same way as `oldTokens`, on the narrative only (everything
  before `manifestLeadIn`). Provider usage is deliberately **not** used:
  a usage figure and a byte-derived estimate are not comparable, and the
  manifest is appended after the guard runs, so the two would describe
  different strings. `summaryCompletionTokens` still supplies the stored
  session counter; the guard and the counter are allowed to differ because
  they answer different questions.
- `prevContextTokens` = `currentSession.PromptTokens + currentSession.CompletionTokens`
  (the size of the context being replaced; `0` on the first compaction, in
  which case G3 does not apply).

G2's threshold and the reason it exists: a documented case collapsed an
18,282-token context at 66.7 accuracy into 122 tokens at 57.1 — below the
no-adaptation baseline of 63.7. The guard's job is to notice "the summary
lost 99% of its volume" before it becomes the only context.

No new column is needed for any guard, but **`CompletionTokens` is not the
previous summary's size**: it is overwritten by every step, so after one
conversational turn it holds that turn's output. G2 must re-read the previous
summary message and measure its narrative (everything before the manifest
lead-in) with `approxTokenCount`. The replaced context size is
`PromptTokens + CompletionTokens`. PR-3 adds `summary_fail_count` for G4
only. See D-2 in §7.3 for the failure this causes if implemented as written
above.

---

## 5. Data Model Changes

Three additive, back-compatible changes. Each belongs to exactly one PR so
migrations do not collide.

| Change | PR | Purpose |
| --- | --- | --- |
| `sessions.summary_fail_count INTEGER NOT NULL DEFAULT 0` | PR-3 | circuit breaker |
| `sessions.summary_anchor_message_id TEXT` | PR-9 | id of the oldest message the current summary replaces; the start of the next merge span |
| `sessions.summary_prompt_prefix_len INTEGER NOT NULL DEFAULT 0` | PR-9 | byte length of the model-written narrative inside the summary content, so later code can split narrative from the code-appended manifest/transcript blocks without re-parsing |
| `file_events(session_id, path, kind, at)` (new table) | PR-12 | write/exec ledger; `read_files` is left untouched because `require-read-before-write` depends on its semantics |

`summary_prompt_prefix_len` is the decided mechanism (not an open question):
storing the narrative length is one integer and removes an entire class of
"where does the manifest start" parsing bugs. PR-9 must set it.

Do **not** add a `session_pins` table. The retrieval design deliberately has
no cross-compaction pin registry; see D6 and the retrieval-loop mitigation in
§12.

Migration file naming follows the existing convention
`internal/db/migrations/<YYYYMMDDHHMMSS>_<snake_name>.sql` with goose
markers:

```sql
-- +goose Up
ALTER TABLE sessions ADD COLUMN summary_fail_count INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE sessions DROP COLUMN summary_fail_count;
```

Query changes must be added to `internal/db/sql/*.sql` and regenerated with
`sqlc generate` (config at repo root `sqlc.yaml`). Generated files under
`internal/db/` are committed.

**Dual-driver constraint.** The build can use `modernc.org/sqlite` or
`github.com/ncruces/go-sqlite3`. Do not use FTS5, virtual tables, generated
columns, or `json_each` in this PRD. Plain columns and `LIKE` only.

---

## 6. Config Surface

Exactly one new option ships in this PRD; a second is named for a deferred
PR. Follow `internal/shellconfig/options.go` conventions: a `jsonKey`, a
`kind`, and `inverted` only when the stored field is a negation.

| Option | JSON field | Type | Default | PR |
| --- | --- | --- | --- | --- |
| `compaction-elide` | `compaction_elide` | string: `off` \| `shadow` \| `on` | `shadow` | PR-7 |
| `compaction-summary-model` | `compaction_summary_model` | string: `large` \| `small` | `large` | PR-13 (deferred, do not implement) |

`shadow` means: compute the elision, log the byte savings and the tool names
that would be elided, but send the **full** content. This is how we measure
before changing behavior; it must be the default on first ship.

Do not add string-enum options to `optionSpecs` as `optString` without also
adding validation — an unknown value must be a usage error, not a silent
fallback.

---

## 7. Pull Request Plan

Each PR is independently reviewable, independently testable, and leaves
`main` working. Dependencies are stated explicitly; anything not listed as a
dependency can be implemented in parallel.

Ordering rationale: correctness first (PR-1..PR-3), then the cheap quality
wins that need no new machinery (PR-4, PR-5), then the reversible-eviction
stack (PR-6 → PR-7 → PR-8), then long-run stability (PR-9, PR-10). PR-11
and PR-12 are follow-ups.

### 7.1 At a glance

| PR | Title | Depends on | Touches DB? | Default-off? |
| --- | --- | --- | --- | --- |
| 1 | Truncation guard | — | no | — |
| 2 | Collapse/inflation guards | 1 | no | — |
| 3 | Failure circuit breaker | 1 | yes (column) | — |
| 4 | Summary prompt + template | — | no | — |
| 5 | Context manifest | 4 | no | — |
| 6 | `recall(tool_call_id)` | — | no (one query) | tool always present |
| 7 | Elision + tombstones | 6 | no | **shadow** |
| 8 | Transcript export | 4 | no | — |
| 9 | Incremental merge | 2, 5, 8 | yes (2 columns) | — |
| 10 | Real todo + recitation | — | no | — |
| 11 | `fetch`/search spill handles | 6 | no | — |
| 12 | Write/exec ledger + recent files | — | yes (new table) | — |
| 20 | ~~Make published anchors retrievable~~ (superseded: anchors removed) | 5, 6 | no | — |

Three PRs (3, 9, 12) touch the schema. Each carries its own migration so two
can be in flight without a conflict; if they land in the same window, merge
them in numeric order.

If only three PRs can be done: **1, 4, 5**. They fix the correctness bug,
the highest-value prompt gap, and the weakest summary dimension, and none of
them requires new machinery.

### 7.2 Implementation status

Snapshot taken on branch `summary-refactor`, against base commit `fe756b83`.
Update this table when a PR lands; it is the only place that answers "what is
actually done" without reading the tree.

| PR | Status | Files |
| --- | --- | --- |
| 1 | **done**, reviewed | `agent.go`, `summary_guard.go` |
| 2 | **done** | `summary_guard.go`, `agent.go` |
| 3 | **done** | `summary_guard.go`, `agent.go`, `session.go`, migration + sqlc |
| 4 | **done**, reviewed | `templates/summary.md`, `summary_prompt.go`, `agent.go` |
| 5 | **done**, reviewed, amended (§7.3) | `summary_manifest.go`, `summary_worktree.go`, `agent.go` |
| 6 | **done** | `tools/recall.go`, `message.go`, `messages.sql`, `config.go`, `coordinator.go` |
| 10 | **done**, reviewed | `todo_reminder.go`, `agent.go` |
| 7 | **done**, reviewed | `tombstone.go`, `agent.go`, `message.go`, `config.go`, `shellconfig/options.go` |
| 8 | **done**, reviewed | `transcript.go`, `agent.go` |
| 9 | **done**, reviewed | `summary_merge.go`, `agent.go`, `session.go`, migration + sqlc |
| 11 | **done**, reviewed | `tools/fetch.go`, `tools/search.go`, `tools/recall.go`, `tools/spill.go`, `shell/truncate.go` |
| 12 | **done**, reviewed | `filetracker/service.go`, `summary_recent.go`, `agent.go`, tools, migration + sqlc |
| 20 | **superseded**, anchors removed (see §4.4 amendment) | `tools/recall.go`, `tools/recall.md`, `summary_manifest.go`, PRD §4.4/§4.6 |

This is the §7.1 minimum viable subset (1, 4, 5) plus PR-2, PR-3, PR-6, PR-7,
PR-8, PR-9, PR-10, PR-11 and PR-12. All twelve §7 PRs are now implemented.
PR-20 was superseded: the anchors it made retrievable were removed (see the
§4.4 amendment), because the handles duplicated facts the summary already
carries. PR-13..PR-19 remain deferred (§8). PR-3 adds
one migration; PR-6 adds one generated query; PR-7 adds
the first user-facing configuration option in this PRD
(`compaction-elide`, §6).
"Reviewed" means the
two-axis review recorded below has been run and its findings applied; it does
not mean the PRDs' acceptance commands all ran, since `task lint` cannot
execute here.

**PR-1 — truncation guard.** `summaryMaxOutputTokens = 20_000` is attached to
the summarizer via `fantasy.WithMaxOutputTokens` (fixes P2). The pure helper
`summaryOutcome(*fantasy.AgentResult) error` returns
`errSummaryTruncated` on `FinishReasonLength`; nil, empty and unknown finish
reasons pass, because some providers report none and treating "unknown" as
truncation would discard usable summaries (fixes P1). On rejection the summary
message is finalized as `FinishReasonError` / `"Summarization Error"` /
`"summary hit the output limit"` and execution returns before the anchor
update, so `SummaryMessageID` keeps pointing at the previous complete
summary. The helper returns no token count: size comparisons belong to PR-2,
which measures the persisted narrative so the two sides of its ratio are
directly comparable.

**PR-2 — collapse and inflation guards.** `validateSummary(summarySizes) error`
is pure and returns `errSummaryCollapsed` below `summaryCollapseRatio = 0.5` of
the previous summary, and `errSummaryInflated` when the new document is at
least as large as the context it replaces. Both checks are inert without their
baseline (`PrevTokens == 0`, `PrevContext == 0`), so a first compaction can
never be rejected. `PrevTokens` is **measured from the stored previous summary
row** (D-2 in §7.3), not taken from `CompletionTokens`; the narrative is cut at
`manifestLeadIn` first so the code-appended blocks do not inflate the baseline.
`PrevContext` is `PromptTokens + CompletionTokens`, captured before the reset
block. `NewTokens` is measured with the same `summaryNarrativeTokens` helper as
`PrevTokens`: both sides of the ratio must be the same quantity, and a provider
usage count is not comparable to a byte-derived estimate of a different string.

A collapse gets `summaryCollapseRetries = 1` retry with
`summaryCollapseRetrySuffix` appended to the prompt; a second collapse, or any
inflation, is rejected without a retry. Every rejection — the retried attempt,
the final attempt, and a truncation — persists the attempt as
`FinishReasonError` / `"Summarization Error"`, and the only path that reaches
the anchor update is the one that passed every guard.

Two shapes were chosen while implementing and are worth recording.

- **Each attempt gets its own summary message.** `Summarize`'s stream setup
  moved into a closure, so a retry does not rewrite text the UI has already
  streamed; the rejected attempt keeps its failed row. Rejected rows are then
  excluded by `getSessionMessages`, which would otherwise feed a previous
  attempt's partial prose into the next compaction as a second version of the
  session's state (D6).
- **`rejectSummary` returns only the persistence error.** The guard error is
  returned by the caller, because the collapse path has to retry instead of
  surfacing it. The first draft returned the cause from the helper and the
  retry never ran; the two integration tests exist because of that. The
  stream-error path uses the same helper, so the `"Summarization Error"`
  label and the failed-row shape have exactly one definition.

Also fixed while implementing, because PR-2 was the first PR to exercise the
rejection path end to end: `manifestAnchors` skipped summary rows only by
checking `Role == User`, but `getSessionMessages` rewrites the previous
summary's role to `user`, so a summary id could be offered as a `recall`
handle it cannot resolve. §7.2's PR-5 paragraph already claimed the exclusion;
the code now does it, with a regression test. And `stripAnalysisBlock` now
runs before the guards rather than after them, so a *rejected* row — which is
still persisted and still visible — cannot carry the `<analysis>` scratchpad
either.

Two smaller decisions are worth recording:

- **The dropped-span input is not wired yet.** §4.8's G2 condition is
  `newTokens < oldTokens * 0.5` **and** the dropped span is non-empty. The
  second half only becomes meaningful once PR-9 knows whether a previous
  summary is part of the span, which is why §7 defers it there. Until then
  `PrevTokens > 0` stands in for it; a previous summary always means a
  non-empty span. §12's risk row still describes the end state, not today's.
- **A dangling anchor skips the collapse guard, not the compaction.** When
  `messages.Get(SummaryMessageID)` fails the row is gone, which is not the
  same as "no previous summary", so it is logged at `Warn` under its own
  message rather than folded into the inert case. The compaction still runs:
  `ListFromSummary` falls back to the whole session in that situation, so
  G3 still bounds the new document against the context actually being
  replaced, and refusing would wedge a session that is already over its
  window. The alternative — treat an unreadable baseline as a failure — was
  rejected because it would make a missing row permanently disable
  compaction.

Known gaps: rejected attempts' provider usage is not added to the session's
cost or cumulative totals, because the rejection paths do not save the session
at all (adding it means deciding what a failed compaction costs, which belongs
with PR-3's failure accounting). Guard rejections also do not yet increment a
counter, so the circuit breaker still has nothing to read.

**PR-6 — `recall` for archived tool results.** `recall(tool_call_id, offset,
limit)` reads back a tool result that compaction dropped from the prompt,
from the same SQLite row, byte for byte. It is additive: nothing elides a
payload yet, so the tool is usable on its own, which is what makes PR-7
reversible when it lands.

The query takes no summary anchor — that is the point, since
`ListFromSummary` is exactly what hides archived results — and filters on
`session_id` rather than the `(role, created_at)` index, as §4.6 anticipated.
No index was added.

Implementation notes worth keeping:

- **The header carries `exit=` and `bytes=`.** §4.6's example shows `exit`
  for bash; `bytes` is borrowed from §4.3's tombstone, for the same reason:
  it is what prices a retrieval. Both are the first thing a caller needs to
  decide whether the body is worth fetching, and reading them off the body
  would defeat the purpose of a header. `exit=` is gated on the tool being
  bash, mirroring the manifest: a `view` result whose tail quotes
  "Exit code 3" must not be reported as a failure.
- **The exit-code parser moved into `internal/agent/tools`.** It was
  `parseManifestExitCode` in the agent package, but `tools/bash.go` is what
  *writes* the `Exit code N` line, so the inverse belongs beside it. Both the
  manifest and `recall` now call `tools.ParseExitCode`, and the duplicated
  "is this bash" predicate moved with it as `tools.IsBashToolName`. The
  alternative — a second regexp and a second name check — would have been
  two definitions that can drift apart, and the un-gated first draft did
  exactly that: it attached `exit=` to non-bash results.
- **`spill_path` is deliberately absent.** §7 says not to advertise a
  capability PR-11 has not implemented. The params struct has only
  `tool_call_id`, `offset` and `limit`. §4.6 asks the empty-argument error to
  "explain both forms"; with one form shipped, it names that one rather than
  describing a parameter the model cannot pass, and §7's "both parameters set"
  case had no second parameter to set. PR-11 added the second form and PR-20
  the third, so the error now names all three.
- **The empty-result notice and the `is_error` omission are small
  deviations.** §4.6 does not specify a response for a zero-byte result, and
  returning a header with nothing after it would read as a retrieval that
  failed; it says the result is empty instead. The `is_error` field in the
  first draft was dropped from the header, because §4.6's header does not
  carry it and the archived body already contains whatever the tool said.
- **Slicing is rune-safe at both ends**, via `runeStartAtOrAfter` /
  `runeStartAtOrBefore`, which now also back `truncateUTF8` — the rune walk
  existed and is shared rather than re-implemented. Concatenating pages can
  drop a rune but can never produce invalid UTF-8. `recallSlice` is pure and
  table-tested.
- **The tool is registered for sub-agents too**, per §4.6's "read-only"
  note: it reads only the caller's own session, which a sub-agent owns
  separately. It had to be added to `allToolNames()` as well, or
  `AllowedTools` filtering would have removed it from every agent;
  `resolveReadOnlyTools` now lists it so the task agent keeps it. The plan
  agent does not get it: `resolvePlanTools` is a deliberately narrow
  allow-list of planning aids, and an archived payload is only useful when
  resuming work, which the plan agent does not do.

**PR-4 — prompt and template.** `templates/summary.md` now carries the seven
model-authored headings of §4.7 in order; `## Full Transcript` is absent by
design, it is injected by code in PR-8. The preamble states that the summary
is the only surviving context, forbids tool use with the "will be rejected
and will waste this turn" wording, permits an `<analysis>` scratchpad, and
replaces `**Length**: No limit` with "every section mandatory, write `none`
if empty, never shorten paths/commands/quotes". `stripAnalysisBlock` removes
only a leading block (case-insensitive, first occurrence only); it is applied
by `setSummaryContent` on completion, so the UI still streams the deltas live.
`buildSummaryPrompt` keeps its PR-1-era signature, adds `active_form` to the
injected todo block, instructs the model to restate the list under
`## Current State`, and states explicitly when the list is empty. `## Artifacts`
also forbids restating or counting the generated file list: a count written
into the prose goes stale on the next write, and a merged summary keeps it.

**PR-5 — context manifest.** `buildContextManifest(manifestInput) string` is
pure and renders `archived / failures / files / anchors / coverage`, appended
by code after the model's prose (the narrative is never rewritten).
Failures are `IsError` results or bash results with a non-zero exit parsed
from the **last** `^Exit code (\d+)$` match in the final 512 bytes, so a test
log quoting the phrase cannot register as a failure. Files come from tool
call input, deduped per path with read/edit counts. The 1500-token budget is
enforced by dropping files first, then failures, then anchors. A previous
summary row is excluded from `anchors` even though `getSessionMessages`
rewrites its role to `user`.

> **Amendment (2026-10-28).** The record above is the first cut. Since then
> `anchors` is gone, `files` comes from the write ledger merged with git (and
> has no structural cap), and every code-appended block shares one appendix
> marker: see the three §4.4 amendments. The budget now trims `files` and
> `failures` only.

**Deliberate deviations from this document** (per §14: "the code wins for
facts, the document wins for intent", recorded here rather than silently):

- §4.4 describes only the budget drop. `failures` also has a structural cap
  (10) and reports it as `- (truncated: N more)`, because a silent cap has the
  same "not there vs not indexed" problem the `coverage` line exists to solve.
  `files` had one too (20) until the third §4.4 amendment removed it: the
  budget trims files first, so the extra cap only hid real paths.

**Review outcome.** A two-axis review of this work (standards against
`AGENTS.md`, spec against §4.4/§4.7/§7) found three real defects in the first
draft. All three are fixed and each has a regression test:

1. `- id=... tool_call_id=... tool=...` inserted an undeclared field into the
   normative failure line of §4.4. The line went back to
   `id, tool, exit, cmd, key` — which it kept until PR-20 re-added
   `tool_call_id` at the end, deliberately and with §4.4 amended to match
   (§7.3 D-3).
2. `anchors` kept the **oldest** ten handles when trimming, dropping the
   current request — the one §4.7 calls the most important thing to carry.
   It now keeps the newest ten and still renders oldest-first.
3. Failure ordering was nondeterministic: results batched into one assistant
   message share both `created_at` (one-second resolution) and the message id,
   and arrived in map order, so the manifest could differ between two runs
   over the same session. Ties now break on tool call id.

Also taken from the review: the §4.4 requirement that the manifest be
labelled data is now met by `manifestLeadIn`, emitted by `Summarize` before
the block; the template's opening line now carries the "only context that
will survive" statement §4.7 requires of it; `summaryOutcome` returns only its
error (the two guards it fed returned a token count nobody used); and
`setSummaryContent` moved to `message.Message.SetContent`, where the package
already owns `AppendContent`.

**PR-3 — failure circuit breaker.** `sessions.summary_fail_count` is
incremented atomically by a targeted `UPDATE ... RETURNING` query rather than
by a full-row `Save`: `Summarize` holds a session snapshot taken before a
potentially minutes-long provider round-trip, so saving that snapshot would
clobber a concurrent rename or usage update, and two concurrent failures could
both read the same count. `session.Session` still maps the column, so a
fetch-modify-save round trip preserves it. `summaryBreakerTripped` is checked
both at the top of `Summarize` (so no provider request is made once open) and
in `Run`'s `StopWhen`, which is what makes the trips a *skip* rather than a
turn failure: a session that cannot be summarized finishes its turn with the
context it already has instead of being cut short and re-queued forever.
Provider errors count toward the breaker (excluding context cancellation); the
in-turn collapse retry does not, so one rejected compaction counts once.

The notice is a new `notify.TypeSummarizeDisabled`, published exactly on the
trip that reaches the threshold. It is not `TypeAgentError` because it is not a
failed run, and the client/server path preserves that distinction: the wire
event is `proto.AgentEventTypeSummarizeDisabled` and it carries the reason in
`Progress` with `Error` cleared, so `crush run` (which aborts on a non-nil
`Error`) ignores it instead of failing.

**PR-10 — real todo injection and tail recitation.** The head reminder is now
computed from the session's stored list, with the historical "your todo list is
currently empty" text kept byte-for-byte for the empty case. `preparePrompt`
takes a `promptOptions{Todos, Recite, Goal, NoHead}` rather than a bare
`todos` slice, because three states have to stay distinguishable: no list to
report, a real empty list, and no reminder wanted at all.

Three findings from review changed the shape:

- **The recitation names the wrong goal if it reads the message history.**
  `Run` snapshots the session before it stores the new user message, so the
  newest stored message belongs to the *previous* turn — and, right after a
  compaction, `getSessionMessages` re-roles the summary to a user message, so
  the tail would recite the summary narrative. `promptOptions.Goal` carries
  the turn's own prompt; the history is only a fallback. Two Run-level tests
  fail without the fix.
- **The summarizer must not carry the head reminder.** It renders `todoReminder(nil)`
  while `buildSummaryPrompt` embeds the real list, so the same request claimed
  both "the list is empty" and a populated list. `NoHead` suppresses it; the
  summarizer already has the list in the right place.
- **A whitespace-only assistant row used to convert to a turn with no parts**,
  because the skip filter compared `Content().Text == ""` while `ToAIMessage`
  trims. That blank turn sat between the conversation and the recitation. The
  filter now also rejects a converted assistant turn with no content.

Sub-agents get neither the head reminder nor the recitation: a sub-agent owns
its own task prompt and has no todo list of its own. `clipToBytes` reserves the
ellipsis inside the byte budget rather than adding three bytes past it.

**PR-7 — tool-result elision with tombstones.** `compaction_elide` has three
values: `shadow` (the default) computes the decision, logs it, and sends the
full payload; `on` replaces eligible results with a tombstone; `off` leaves
the prompt exactly as it was. `internal/agent/tombstone.go` holds the whole
layer: `elidePolicy` (mode, `KeepRecent` 3, `KeepBytes` 2048, `view` and
`lsp_diagnostics` exempt), `shouldElide`, and `renderTombstone`. The decision
and the rendering are pure and table-tested; the only I/O in the file is the
shadow-mode log line.

The prompt change is confined to `preparePrompt`: the tool-result collection
now iterates `message.ToolResults()` instead of converting whole rows and
re-parsing the parts, so the policy sees `Name`, `Content`, `Data` and can
carry each result's `CreatedAt` into the tombstone. Ordering is unchanged —
the orphan filter still runs first and the results are still re-emitted next
to the assistant message that requested them — because elision replaces the
content of a result, never its position. `message.ToolResult` gained
`ToAIToolResultPart()` so the shared conversion has one definition; the
tool-role branch of `ToAIMessage` now calls it.

Decisions worth recording:

- **The turn-local exemption travels in the context.** `Run` fills a set from
  the stream's `OnToolResult` callbacks and attaches it with
  `withElideExempt` before calling `Summarize`; the compaction's prompt is the
  one prompt built after the turn's own results exist. It is not another
  `Summarize` parameter because it is read by exactly one prompt builder and
  describes the turn, not the compaction.
- **`saved_bytes` is the payload size.** The log line reports what would be
  removed, matching PR-7's own `slog.Info(... "saved_bytes", n)` wording; the
  tombstone that replaces it is a bounded few hundred bytes, and subtracting
  it per call would make the shadow-mode total disagree with the per-call
  numbers a reviewer checks it against.
- **Elision runs on every prompt, not only above the threshold.** §4.2 places
  step ① after the threshold is crossed, but `preparePrompt` has no threshold
  to test: the ladder's trigger lives in `Run`'s `StopWhen`, and the prompt is
  built before any step has run. The policy is inert for short sessions by
  construction (`KeepRecent` and `KeepBytes` between them mean a session with
  a handful of small results elides nothing), and `shadow` is the default, so
  this cannot change behavior on first ship. Moving the trigger into
  `preparePrompt` would mean a session that elides nothing on a turn and then
  does on the next, which is the same decision with a worse boundary.
- **Media results are never elided.** `recall` serves text, so eliding a
  binary payload would be a permanent loss rather than a reversible one;
  `shouldElide` requires `Data == ""`. §4.3 does not mention this case.
- **Previews are ANSI-stripped.** §4.3 does not say, but a tombstone exists to
  be short and readable; copying escape sequences into it would spend the
  400-byte budget on styling and render as garbage in the archived position.
  The recalled body is still returned unmodified.
- **`optEnum` was added to the shell config parser** so `compaction-elide`
  can reject an unknown value at parse time instead of falling back
  silently, and `config.Load` validates the same value for hand-edited JSON.
  The values themselves are typed constants in the config package, which the
  agent aliases rather than re-declaring, so the three spellings have one
  definition.

**PR-8 — transcript export.** Every successful compaction writes the span it
is replacing to `<DataDirectory>/sessions/<session-id>/transcript-<unix>.jsonl`
(one JSON object per line, `0o600` in a `0o700` directory) and appends a
`## Full Transcript` block naming the absolute path, the message count and the
timestamp range. `internal/agent/transcript.go` holds the writer, the block
renderer and the pruner; `Summarize` calls `appendTranscript` after the
manifest and before the anchor update. A failed export is logged at `Warn` and
the compaction continues.

Decisions worth recording:

- **The exported slice is the one already fetched.** `Summarize` reads the
  archived span through `getSessionMessages`, so the transcript is written
  from that slice rather than from a second query. There is no "the new
  anchor" to compute against at that point: the anchor has not moved yet.
- **The stored role is restored for summary rows.** `getSessionMessages`
  rewrites the previous summary's role to `user` so the prompt reads it as
  context. The transcript records the row, not the prompt, so
  `transcriptRole` puts `assistant` back — otherwise the JSONL would claim the
  model wrote the summary as a user message.
- **Pruning sorts by the filename, not by mtime.** The filename carries the
  write timestamp, which survives a copy or a restore; mtime does not.
  Protected paths, not age, decide what survives: the file just written and
  the one the *previous* summary names (`transcriptPathFromContent` on the row
  read for the size guards). §4.5's "never delete one that a live summary
  points to" only bites on the previous summary — the new file is always the
  newest and therefore never a deletion candidate — which is exactly why the
  path is recovered from that row and not assumed.
- **The transcript file is created with `O_EXCL`.** `os.WriteFile` applies the
  mode only when it creates the file, so two compactions in the same second
  would overwrite the earlier transcript and leave a summary pointing at the
  wrong bytes. A collision now takes the next free timestamp.
- **The path component is validated in `writeTranscript` itself.** There is no
  shared safe-path helper in this repo, and `filepath.Join` cleans `..`
  instead of refusing it, which would silently write outside the data
  directory. The check rejects any id whose `filepath.Base` differs from the
  id, any id containing `..`, `/`, `\` or NUL, and any id with a control
  character.
- **Encoding cannot fail today, and the caller is told when it does.**
  `transcriptMessage` is all strings, so `json.Marshal` has no error path;
  returning it rather than logging and skipping keeps a future field from
  silently dropping messages out of the only surviving record.

**PR-9 — incremental merge.** The second and later compactions now merge into
the previous summary instead of rewriting it. Two columns carry the state it
needs: `sessions.summary_anchor_message_id` (the id of the oldest message the
current summary replaces) and `sessions.summary_prompt_prefix_len` (the byte
length of the model-written narrative inside the summary content). Both are
set on every successful compaction; `internal/agent/summary_merge.go` holds
the split, the merge instruction and the anchor helper.

How the pieces fit:

- **The summarization input is unchanged.** `getSessionMessages` already
  returns exactly `[previous summary .. now]`, which is the span the new
  summary replaces, so `buildSummaryPrompt(todos, previous)` only has to add
  an instruction. `previous` is the narrative split out of the stored row.
- **The prefix length is the decided mechanism, not an open question.**
  `summaryPrefixLen` measures everything before the first code-appended block
  (the manifest lead-in, or the transcript heading when the manifest was
  empty) and stores it, so `splitPreviousSummary` never parses. A summary
  written before the column existed has length 0 and falls back to the same
  string search the guards already used.
- **The guard and the prompt measure the same string.** `PrevTokens` is
  `approxTokenCount` of the narrative returned by `splitPreviousSummary`, not
  a second measurement with a different boundary. `summaryNarrativeTokens` is
  still what `NewTokens` uses, and the two agree because the new summary has
  no appended block at the moment it is measured.
- **The merge instruction carries a size floor, not a countdown.** It names the
  previous document's approximate token count so "never write less" is
  actionable. That is an output floor (D9 forbids telling the model how much
  window remains, which is a different number).
- **`SummaryAnchorMessageID` is written but not yet read.** Nothing in this
  PRD consumes it: `getSessionMessages` already provides the merge span, and
  the manifest is built from that same slice. It exists because the follow-up
  partial-compaction work (§8, PR-15) needs "where does this summary start",
  and recomputing it later would mean a query over the whole session. It is
  populated now so the data is there when it is read.

Findings from review, applied:

- **The prefix length is captured, not re-derived.** The first draft scanned
  the final content for the appended-block markers, which is the parsing §5
  says the column exists to remove. `Summarize` now takes
  `len(strings.TrimSpace(...))` of the content at the moment it is still only
  the model's prose — before the manifest and transcript blocks are appended —
  and the value stored is exactly the offset `splitPreviousSummary` slices at.
- **The first draft protected the wrong transcript from pruning.** It passed
  the file just written, which is always the newest and therefore never a
  deletion candidate, so the guard was a no-op and a summary that outlived
  four later compactions kept a pointer to a deleted file. The previous
  summary's path is now recovered from the row read for the size guards and
  protected as well.
- **A summary with only a transcript block had no recoverable boundary.** The
  marker scan (the fallback for rows stored before the column existed) looked
  for the manifest lead-in alone, so the transcript heading became part of the
  "narrative" and inflated the next compaction's `PrevTokens`. Both markers
  are now boundaries, in one shared list, which matters whenever the archived
  span has no failures and no changed files.
- **The merge wording gained the spec's qualifier.** "Never shrink without
  cause" was implemented as "never shrink", which would tell a model whose
  work was genuinely resolved to pad. The instruction now says a shorter merge
  is acceptable only when facts were resolved or superseded.
- **The merge state travels over the wire.** A client/server save replaces
  the stored row from a proto struct that had neither column, so any remote
  save would have silently reset the span and prefix length. Both are now on
  `proto.Session` and both directions of the conversion, with a round-trip
  assertion alongside the existing ones.
- **The migration follows the repo's form.** `StatementBegin/End` markers, an
  explanatory comment and a `CHECK (>= 0)` on the counter, matching the
  token-totals migration it sits next to; a round-trip test applies it up and
  down against the embedded set.

**PR-11 — spill handles for `fetch` and web search.** A truncated `fetch` body
and an over-long search snippet are now written to the spill directory in full
and named in the result, so neither is a permanent loss any more (§1's table).
`recall(spill_path=...)` reads them back in byte pages with the same rune-safe
slicing as the archived-result form. Nothing elides a page or a snippet today;
the handle exists so the truncation notice can point somewhere.

Implementation notes worth keeping:

- **The read root is a list, not one directory.** A tool is constructed with
  the data directory, but a confined call spills into the policy's private
  temp directory — the only place a sub-agent's read-only fence allows a
  write. `spillRoots` accepts both, so a file written under either is
  readable. `shell.SpillDir` owns the `<dir>/shell-output` layout so the tool
  does not reconstruct it.
- **Validation and the open are one step.** `resolveSpillPath` resolves
  symlinks, checks containment on path segments, then opens the file and
  confirms the descriptor is the path that passed. A `strings.HasPrefix`
  check would accept a sibling named `shell-output-evil`; a bare
  validate-then-open would leave a swap window. Non-regular files are
  refused so a FIFO cannot block the turn.
- **The research profile gets `recall`.** PR-11 names spill handles for the
  web tools, but those tools only exist in the research sub-agent. Without the
  tool registered there, every path the notice names would be unreadable, so
  the sub-agent now carries `recall` too: it reads its own session and only
  Crush's spill directories.
- **`fetch` converts before it truncates.** PR-9's delivery point is the
  converted text, so the spill holds that, not the raw response. The buffered
  read grows to `maxFetchReadSize` (5MB) only so a page that exceeds the
  100KB prompt cap can still be converted; the model-facing result is still
  capped and still names the file.
- **A snippet below the spill threshold still says it was cut.** The spec asks
  for spill handles on snippets over the cap; spilling a 600-rune snippet
  would create a file that saves nothing. Instead the result reports the full
  length, so "this is a fragment" stays distinguishable from "this is the
  snippet", which is the same reason the manifest carries a `coverage` line.
- **`SpillOutput` deletes a file it failed to write.** A partial spill whose
  path was never returned would otherwise sit until the retention sweep.
- **Known gap: spilled files are shared across sessions.** The spill directory
  is per-project, not per-session, so a sub-agent can read a file another
  session spilled by naming its path. The paths are unguessable
  (`output-<random>.log`) and contain no session content beyond what the tool
  produced, but this is a wider boundary than §4.6's "only the current
  session is searched" claims for archived payloads. Per-session spill
  subdirectories would close it and belong with PR-12's `file_events` work.

**PR-12 — write/exec ledger and recently-touched files.**

> **Amendment (2026-10-28).** The `## Recently Modified Files` block was
> removed; the write ledger now feeds the manifest's `files:` section. The
> notes below describe the earlier shape and are kept as history.

`file_events` records
what a session wrote and ran, and each successful compaction appends a
`## Recently Modified Files` list of the five newest writes. The read side is
unchanged: `read_files` still answers "did this session see the file before
writing it", because `require-read-before-write` depends on its exact
semantics, and a write event cannot answer that question.

How the pieces fit:

- **Writes are recorded where the file is actually written.** `edit`, `write`,
  `multiedit`, `lsp_replace_symbol` and `lsp_rename` each call `RecordWrite`
  next to the existing `RecordRead`. `lsp_rename` is beyond the four tools §7
  enumerates: it rewrites every affected file, and leaving it out would make
  the ledger claim a rename changed nothing.
- **Execs are recorded after approval, not before.** The first draft wrote the
  ledger entry before the permission gate, so a refused command entered the
  ledger as something the session ran. It now runs after every approval and
  before execution, so a command that fails is still recorded (the resumption
  question is "what was run", not "what succeeded") and a denied one is not.
- **Timestamps are milliseconds.** `strftime('%s', 'now')` has one-second
  resolution, and a multi-file edit lands in one burst: five writes in the
  same second would order alphabetically and drop the newest, which is the one
  a resuming agent needs. The caller supplies the millisecond stamp.
- **A write is stored as an absolute path.** `ListReadFiles` re-absolutises
  relative rows against `os.Getwd()`, which is right for the read guard but
  wrong for the summary: the block shortens against the session's workspace,
  which need not be the process working directory. Storing the tool's absolute
  path lets each reader do its own shortening.
- **The block is a narrative boundary.** `recentlyModifiedBlockLeadIn` joined
  the marker list in `summary_merge.go`, and `summaryNarrativeTokens` now cuts
  with `narrativeCut` instead of searching for the manifest alone. A
  compaction with no failures and no changed files produces no manifest but
  does produce this list, so the old single-marker cut would have counted it
  as model prose and inflated the next guard comparison.
- **One row per `(session, path, kind)`.** Repeating a write refreshes `at`
  rather than appending, so the ledger stays bounded to the files and commands
  a session actually touched. The tradeoff is that history is not kept: PR-12
  asks for "the newest five", not for a replayable edit log.
- **The `read` kind is reserved, not written.** §5's table gives the column as
  `kind`, and §7's prose names `'read'`; SQLite cannot widen a CHECK constraint
  without rebuilding the table, so the value is admitted now and left unused.
  `read_files` stays authoritative for reads.
- **Exec rows are not in the summary.** The spec asks for a top-five list of
  *files*, and a command is not a modified file; listing one under that
  heading would claim an edit that may not have happened. The exec ledger
  remains available to any future consumer (and is what the manifest's bash
  call count is a weaker substitute for), but nothing reads it today. That is
  recorded as a known gap, not presented as a working feature.

**PR-12's block versus the manifest's `files:` section.** *(Superseded by the
2026-10-28 amendment: the block is gone and its ledger is merged into
`files:`.)* They overlap and are
not redundant. `files:` answers "what is changed in the working tree right
now" and is merged with git, so it survives a session that edited through a
shell command. The recently-modified list answers "what did *this session*
write", which git cannot attribute and which still works when git is
unavailable or the tree has been reverted since. A resuming agent needs both
facts, and the manifest's `src=` field already distinguishes the sources on its
side.

**PR-20 — make the published anchors retrievable.** PR-5 published ids under
`anchors:` and PR-6 shipped `recall`, and the two did not meet: the manifest
listed message ids while `recall` accepted only tool call ids. Inspecting a
real compacted session (565 archived messages, six anchors) and calling the
tool with each one produced six "no archived tool result" errors, so every
handle the summary offered was dead on arrival. The document carried the same
contradiction (§4.4 said "`message_id`s the agent can pass to `recall`", §4.6
listed no such parameter), which is why it survived review.

Two changes close it, one on each side:

- **`recall` accepts `message_id`.** A user request is worth keeping verbatim
  — §4.7 calls that section the highest-value one — and compaction otherwise
  keeps it only as the model's paraphrase. The reader fetches the message,
  requires the caller's own session, refuses a summary row (it is the previous
  compaction's output; reading it back would offer a stale copy of the state
  being replaced), and pages the text with the same rule as a tool result. The
  alternative — teaching the model to call `recall` with an id it was never
  given a parameter for — was not an option, and stuffing user text into the
  manifest would recreate the bloat compaction removes.
- **Failure anchors and failure lines carry `tool_call_id`.** The id a
  `kind=failure` anchor publishes is now the tool call id, because that is
  what fetches the failing output; the assistant message that requested the
  call is not what a resuming agent needs to read. The same handle is appended
  to each `failures:` line, after every field §4.4 fixes, because anchors are
  capped at ten shared with the user messages — the eleventh failure would
  otherwise be listed but unreadable.

Deduplication now keys on the handle rather than the message: results batched
into one assistant message share its id, and each failed call is a separate
payload worth its own anchor.

**Old summaries still parse.** A summary written before this change lists a
failure anchor as the assistant message that requested the failing call. That
id now reaches `recall` as `message_id`, and answering "0 bytes" would read as
"the record is empty" when the payload simply lives under a different key, so
an assistant row is refused with the tool call ids from that message. Anchors
are not re-read from disk, so the fix must be kind to the ids already written.

Two smaller facts recorded while fixing it. `manifestAnchor` gained nothing —
the renderer formats `id` and `kind`, and `kind` is what tells the caller
which `recall` parameter to pass. And the two failure list ids are now
deliberately different fields: `id=` names the carrying message (stable across
the batch, useful for grouping), `tool_call_id=` names the payload.

**Inspection findings left as documented gaps, not code changes.** A full
read of one real compaction against the PRD produced two more observations
that do not justify behaviour changes on their own:

- **A session that edits only through `bash` produces no recently-modified
  list.** `file_events` gets `exec` rows from shell commands but no `write`
  rows, so PR-12's block is empty exactly in the case D-1 built the git half
  for. The block is not wrong — it claims only what file tools wrote, and the
  manifest's `src=git` entries still name the changed paths — but the two
  sections then disagree, and a future reader will ask why. Fixing it means
  deriving paths from commands (rejected in D-1: `sed -i`, heredocs and shell
  functions are not reliably parseable) or reading git for this block too
  (then it duplicates `files:`). Left as is, and named here so the empty list
  is not mistaken for a bug in the ledger.
- **Manifest `files:` paths are absolute while PR-12's list is
  workspace-relative.** §4.4's example shows relative paths. The manifest
  keeps whatever the tool call recorded, which is absolute for the file tools;
  `recentlyModifiedBlock` shortens against the session's workspace. Harmless
  today — both spellings resolve — but the inconsistency is deliberate only in
  the sense that nobody chose it, so a future pass should pick one and apply
  it to both.

**Known gaps, in dependency order.** PR-12's exec ledger has no reader: the
summary lists writes only, and nothing else queries `kind='exec'` yet. The
rows are correct and bounded, but they are written for a consumer that does
not exist; a future "commands run" section, or the bash-call substitute the
manifest currently infers, is where they would land. The cross-cutting fixture
in §9 (`internal/agent/testdata/summary/`) is also not created yet, so
PR-1/2/4/5 are covered by synthetic in-test histories instead.
`manifestInput.Undecodable` is implemented and unit-tested but unreachable in
production: `ListFromSummary` returns an error on a decode failure rather than
a partial slice, so `Summarize` has no count to pass. Wiring it needs a change
to the message service and belongs with a later PR.

### 7.3 Defects found after implementation

> **Amendment (2026-10-28).** The `anchors` section and
> `recall(message_id=...)` described in PR-5, PR-20 and D-3 below were
> removed; see §4.4. The notes are kept as the record of what was built and
> why, not as the current specification.

Three defects were found by inspecting a real session rather than the test
suite. All are recorded here with the reasoning, because each one invalidates
a claim this document makes elsewhere, and a future implementer will otherwise
re-derive the same wrong design.

**D-1 — `files:` was blind to shell edits, so §10's artifact metric was 0%.**

The manifest derived its file list from tool-call input. That covers `view`,
`edit`, `write`, `multiedit` and `lsp_replace_symbol`, and nothing else. The
current implementation uses the session write ledger for the tool half, which
also covers `lsp_rename`; the git half below is unchanged. An
agent that edits through a shell command — `sed -i`, a heredoc, a
redirection, `python3 - <<EOF` — leaves **no path in any tool input**, so
`files:` came out empty while the working tree was full of changes. §10 claims
artifact accuracy is "100% by construction (code-generated); a mismatch is a
bug". The construction was sound; the *input* was wrong, and the metric would
have read 0% against `git status --porcelain`.

Parsing the command string for paths was rejected: `sed -i`, `tee`, `git mv`,
shell functions and heredocs are not reliably parseable, and a wrong match is
worse than a missing entry, because it makes the whole section untrustworthy
again — exactly the prose-quality problem the manifest exists to remove.

The fix is to ask git, which is what §10 already used as ground truth. At
compaction the working tree is queried once
(`git status --porcelain=v1 -z`, 5s timeout, bounded to 50 entries), and the
result is merged into the tool-derived list by path, with provenance marked:

```
files:
- path=internal/agent/agent.go ops=read,edit reads=4 edits=2 last=... src=tool
- path=internal/agent/summary_manifest.go ops=none src=git state=modified
```

A path can be both: `src=git` says git confirms it changed, `ops=`/`reads=`/
`edits=` still carry what the file tools recorded, because git cannot report
reads. When git could not be consulted, the section says so rather than
rendering nothing, which is the same "not there vs not indexed" distinction
the `coverage` line exists for:

```
- (working tree not checked; 3 bash call(s) may have modified files)
```

This is a deliberate widening of the section's meaning: `files:` now answers
"what is changed right now" (the resumption question) rather than "what was
touched in the archived range" (a history question git cannot answer by
itself). §4.4's field list is unchanged apart from `src=`/`state=`.

**D-2 — G2's `oldTokens` reads the wrong counter, so the guard misfires.**

§4.8 defines `oldTokens = currentSession.CompletionTokens`, described as "the
previous summary's output size", and adds "no new column is needed for any
guard". That second claim does not hold. `updateSessionTokenCounters`
**overwrites** both counters on every step:

```go
if usage.OutputTokens != 0 { session.CompletionTokens = usage.OutputTokens }
```

So after a compaction, the first completed turn rewrites
`CompletionTokens` to that turn's output. The next compaction then compares
the new summary against one conversational reply, not against the summary it
is replacing — and G2, whose entire job is catching summary-of-summary
collapse, is measuring the wrong quantity in exactly the scenario it exists
for. Both directions misfire: a small intermediate turn lowers the threshold
and admits a genuinely collapsed summary; a large one raises it and rejects a
healthy summary.

The fix measures the previous summary directly, at the point of use:

```go
prev, err := a.messages.Get(ctx, currentSession.SummaryMessageID)
narrative, _, _ := strings.Cut(prev.Content().Text, manifestLeadIn)
prevTokens = approxTokenCount(strings.TrimSpace(narrative))
```

Three details matter. The manifest and transcript blocks must be cut off
before measuring — they are code-appended, and counting them would inflate
`PrevTokens` and make G2 more permissive. `approxTokenCount` is sufficient
because G2 is a ratio guard, not a billing check; a stored usage figure would
make it exact but needs a column. And the guard stays inert when there is no
previous summary (`prevTokens == 0`), matching §4.8's own condition.

This is a documentation correction as much as a code one: **§4.8's "no new
column is needed" is only true if you re-read the previous summary row**, and
that row must be parsed for the narrative boundary. PR-9's
`summary_prompt_prefix_len` makes the same measurement cheaper once it lands.

**Also added: `tool_result_bytes` on the `archived:` line** (§4.4 did not ask
for it). The model was told `tool_calls=7` with no way to distinguish a
session carrying four kilobytes of tool output from one carrying four
hundred. §4.3 argues the tombstone needs a byte count so the model can price a
retrieval; the same argument applies one level up, where the decision is
whether the archive is worth searching at all.

**D-3 — `anchors:` published ids that no `recall` parameter accepted.**

The manifest offered up to ten `message_id`s "the agent can pass to `recall`",
and `recall` had no `message_id` parameter: its params were `tool_call_id`,
`offset` and `limit` from PR-6, plus `spill_path` from PR-11. Neither review
axis caught it because each PR was internally consistent — PR-5 emitted ids
its own tests could match, PR-6 filtered the ids it was handed — and the
contract between them lived only in prose.

Probing a real compacted session made it unambiguous. Its manifest listed six
anchors; calling the tool with each returned:

```
no archived tool result with tool_call_id "c210f989-..." in this session;
ids from other sessions, parent sessions or sub-agent sessions are not visible here
```

That error is itself misleading, which is the second half of the defect: the
id *is* in this session, it is simply not a tool call id, so the message
points the model at a scoping problem that does not exist. The same six ids
were all resolvable the moment `message_id` was added.

The fix has no schema or query cost: `messages.Get` already existed, and the
session check reuses the field it returns. The design question was what
`kind=failure` should publish, and the answer follows from what the section is
for — a resuming agent wants the failing output, and only `tool_call_id`
reaches it, so failure anchors switched from the message id to the call id.
Both ids still appear in `failures:` under distinct names, because the message
id is the stable key when several results share one assistant row.

The general lesson, since this document is the contract between PRs: **a
handle is not a fact until a caller with a name and a test resolves it.** Two
PRs that both look right can still disagree about a string, and only an
end-to-end probe against real data finds that.

---

**Partly not verifiable in this environment.** `golangci-lint` (both v1 and
v2 on `PATH`) refuses to load the config because it was built with Go 1.24
against a Go 1.27 target, so `task lint` cannot run here; PR-3 and later must
not be merged on a green test run alone. `sqlc` was the other blocker and is
now solved — see the install recipe in §13, which is what let PR-6 add a
query.
Separately, `./internal/sandbox` fails five tests on this machine (a
locale-dependent "read-only filesystem" message and
`landlock: function not implemented`); the same five fail on a pristine
`fe756b83` worktree, so they are environmental and not caused by this work.

---

### PR-1 — Guard against truncated summaries

**Depends on:** none.
**Fixes:** P1, P2.
**Estimated size:** ~80 lines including tests.

**Changes**

- `internal/agent/agent.go`
  - Add `const summaryMaxOutputTokens = 20_000` next to the existing
    summarization threshold constants (`largeContextWindowThreshold` etc.),
    with a comment explaining the number is an output cap, not a context
    cap.
  - In `Summarize`, add `fantasy.WithMaxOutputTokens(summaryMaxOutputTokens)`
    to the `fantasy.NewAgent(...)` call that builds the summarizer.
  - After `agent.Stream` returns successfully, check
    `resp.Response.FinishReason`. On `fantasy.FinishReasonLength`, mark the
    summary message with `FinishReasonError`, title `"Summarization Error"`,
    detail `"summary hit the output limit"`, persist it, and return a
    wrapped error (`fmt.Errorf("summary truncated at output limit: %w", ...)`
    or a sentinel `errSummaryTruncated`).
  - The anchor update block (`currentSession.SummaryMessageID = ...` through
    `sessions.Save`) must execute only on the complete path.
- New file `internal/agent/summary_guard.go` (keeps `agent.go` from growing):
  - `func summaryOutcome(resp *fantasy.AgentResult) error` — or an equivalent
    small helper that both PR-1 and PR-2 can extend. Keep it pure so it is
    unit-testable without a provider. (As shipped it returns only the error;
    nothing consumed a token count, and PR-2 measures the persisted narrative
    instead.)

**Tests**

- `internal/agent/summary_guard_test.go`
  - table test: `FinishReasonStop` → no error; `FinishReasonLength` → error
    identifying truncation; nil/unknown reason → no error (some providers do
    not report one).
- `internal/agent/agent_test.go` (existing summarize tests)
  - a test asserting `SummaryMessageID` is unchanged when the summarizer
    returns `Length`. Use the existing mock-provider scaffolding in that
    package rather than a live provider.

**Acceptance**

- `go test ./internal/agent/ -run 'Summary|Summarize'`
- A truncated summary can no longer be observed with
  `FinishReasonEndTurn`.

**Rollback:** revert; no schema, no config.

---

### PR-2 — Reject collapsed and inflated summaries

**Depends on:** PR-1 (extends the outcome helper).
**Fixes:** P1 (second half).

**Changes**

- `internal/agent/summary_guard.go`
  - `type summarySizes struct { PrevTokens, NewTokens, PrevContext int64 }`
    and `func validateSummary(s summarySizes) error` returning
    `errSummaryCollapsed` / `errSummaryInflated` / nil. A struct beats four
    bare `int64`s here because two of them are token counts and two are
    context sizes, and swapping them is a silent bug.
  - Field meanings are exactly §4.8's bullets (`oldTokens`, `newTokens`,
    `prevContextTokens`). `NewTokens` comes from `summaryNarrativeTokens`,
    the same measurement as `PrevTokens`; never from `len(content)/4`
    computed separately, and not from provider usage.
  - Thresholds as named constants: `summaryCollapseRatio = 0.5` and
    `summaryCollapseRetries = 1`. Collapse requires `s.PrevTokens > 0`;
    inflation is `s.NewTokens >= s.PrevContext` and requires
    `s.PrevContext > 0`. Keep the function pure — no session, no clock.
- `internal/agent/agent.go`
  - Call `validateSummary` before persisting the anchor.
  - On collapse: retry once with an appended prompt suffix asking the model
    to preserve detail and not to summarize the previous summary away. One
    retry only — do not loop.
  - On inflation: reject immediately (retrying a summarizer that produced a
    longer context is not productive).

**Tests**

- Table test covering: no previous summary (`PrevTokens == 0`) → allowed;
  equal sizes → allowed; 60% of previous → allowed; 40% → collapsed; output
  larger than the replaced context → inflated; `PrevContext == 0` on a first
  compaction → G3 does not fire.
- One integration-flavoured test with the mock provider: first summarize
  succeeds, second returns a tiny body, anchor is unchanged and the error is
  surfaced.

**Acceptance**

- `go test ./internal/agent/ -run 'Summary|Summarize|Guard'`

**Rollback:** revert.

---

### PR-3 — Circuit breaker for repeated summary failures

**Depends on:** PR-1 (failure path must exist).
**Fixes:** P3.

**Changes**

- Migration: `sessions.summary_fail_count INTEGER NOT NULL DEFAULT 0`.
  Regenerate sqlc. Extend `session.Session` with `SummaryFailCount int` and
  map it in `fromDBItem`/`ToDBParams` (see `internal/session/session.go`
  around the existing `SummaryMessageID` mapping).
- `internal/agent/agent.go`
  - On a guarded failure, increment and `sessions.Save`.
  - On success, reset to 0.
  - At the top of `Summarize`, if `Session.SummaryFailCount >= maxSummaryFailures`
    (`const maxSummaryFailures = 3`), return `errSummaryDisabled` without
    calling the provider.
  - In `Run`'s `shouldSummarize` branch, treat `errSummaryDisabled` as
    "skip summarization and continue the turn" — **not** as a turn failure.
    The turn must still complete; only the compaction is skipped.
- User-visible notice: on the trip that crosses the threshold, emit exactly
  one notice saying automatic summarization is paused for this session.
  Mechanism: `internal/agent/notify` defines the agent-facing event types
  (`TypeAgentFinished`, `TypeAgentError`, ...). Add
  `TypeSummarizeDisabled Type = "summarize_disabled"` there, carry the reason
  in `Notification.Message`, and publish via `a.notify.Publish(...)` exactly
  when `SummaryFailCount` becomes `maxSummaryFailures`. Do not reuse
  `TypeAgentError` (it is wired to error surfaces and a RunID). Do not emit
  from the TUI layer — the agent also runs headless under `crush run`. If
  the TUI wants to render it, that is a separate, optional UI change.

**Tests**

- `internal/session/session_test.go`: round-trip the new column.
- `internal/agent/agent_test.go`: three consecutive failures → fourth call
  returns `errSummaryDisabled` without invoking the provider mock
  (assert call count 0); a success in between resets the counter.
- `Run` test: `errSummaryDisabled` does not fail the turn.

**Acceptance**

- `go test ./internal/session/ ./internal/agent/ -run 'Summary|Summarize|Fail'`
- `task test` green (migration ordering matters).

**Rollback:** revert code; the column is additive and can stay.

---

### PR-4 — Rewrite the summary prompt and template

**Depends on:** none (parallel with PR-1..PR-3).
**Fixes:** P4 (and part of P6, via `## Artifacts`).

**Changes**

- `internal/agent/templates/summary.md`: replace with the seven-heading
  structure in §4.7 (items 1–7; item 8 is injected by code in PR-8). Keep the file pure prose; no template variables in this
  PR (PR-9 introduces conditional text via `buildSummaryPrompt`, not the
  template).
- `internal/agent/agent.go`
  - `buildSummaryPrompt(todos []session.Todo) string`:
    - keep the todo block, and add the instruction to restate todos inside
      `## Current State`;
    - when `len(todos) == 0`, say explicitly that the todo list is empty so
      the model does not invent one.
  - Add `<analysis>` stripping after streaming: a small helper
    `stripAnalysisBlock(s string) string` that removes a leading
    `<analysis>...</analysis>` (case-insensitive, first occurrence only) and
    trims surrounding whitespace. Call it before `messages.Update` and before
    the final persisted content is set, so the analysis never reaches the
    anchor. Implementation detail: `OnTextDelta` appends live (the UI streams
    it); strip only on completion, in the final update.
- Do **not** change the prompt to ban tool calls here — that belongs with the
  no-tools preamble in the same template edit; do it in this PR since it is
  template-only. The summarizer agent keeps its (currently empty) tool list;
  the preamble is extra insurance for providers that auto-inject tools.

**Tests**

- `internal/agent/agent_test.go`
  - `TestSummaryPromptContainsRequiredSections`: the seven model-authored
    `## ` headings from §4.7 (items 1–7) appear in the embedded template, in
    order; item 8 (`## Full Transcript`) must **not** appear.
  - `TestBuildSummaryPromptWithTodos`: todo content and status appear; the
    "restate inside Current State" instruction appears.
  - `TestBuildSummaryPromptWithoutTodos`: explicit empty notice appears.
  - `TestStripAnalysisBlock`: leading block removed; absent block untouched;
    inline mention not stripped; only the first block removed.
- Golden-ish test is unnecessary; assert substrings.

**Acceptance**

- `go test ./internal/agent/ -run 'Summary|Summarize'`
- Manual: trigger `Summarize` against a mock provider and inspect the
  persisted summary message parts for all seven headings and no
  `<analysis>`.

**Rollback:** revert template + helpers. This PR changes behavior more than
any other, so it is the one to gate behind a review of the rendered prompt.

---

### PR-5 — Code-generated context manifest

**Depends on:** PR-4 (the summary content is where the manifest is appended).
**Fixes:** P6.

**Changes**

- New file `internal/agent/summary_manifest.go`:
  - `type manifestInput struct { Messages []message.Message; AnchorAt int64 }`
  - `func buildContextManifest(in manifestInput) string` — pure, returns the
    `<context_manifest>` block from §4.4, or `""` when there is nothing to
    report.
  - Internal helpers (each independently testable):
    - `manifestFailures(msgs) []manifestFailure` — `IsError` results, plus
      `bash` results whose content signals a non-zero exit. The exact shape
      produced by `tools/bash.go` (`formatOutput`, lines ~336-356) is a
      trailing line `Exit code N` (capital `E`, own line, appended after a
      blank line), and `Command was aborted before completion` for
      interrupts. Parse the **last** matching line only:
      `regexp.MustCompile(`(?m)^Exit code (\d+)$`)` against the final 512
      bytes of the content; a missing match means "not a failure" unless
      `IsError` is set. Do not scan the whole body — a test log may quote
      the phrase.
    - `manifestFiles(msgs) []manifestFile` — from tool call names
      `view|edit|write|multiedit|lsp_replace_symbol`, extracting the path
      from the **tool call input JSON**, not from the result. Decode into a
      `struct{ Path string `json:"path"` }` and skip on decode failure.
    - `manifestAnchors(msgs) []string`
    - escaping helper `manifestEscape(s string) string` per §4.4.
  - Budget enforcement: measure with `approxTokenCount`, truncate `files`
    first, then `failures`, appending `- (truncated: N more)`.
- `internal/agent/agent.go`: after a successful summary update, append the
  manifest to the summary message content via a second `messages.Update`
  (append, do not rewrite the model's text), then persist the anchor. If the
  manifest is empty, skip the update.

**Tests**

- `internal/agent/summary_manifest_test.go` — table tests over synthetic
  `message.Message` values:
  - empty input → `""`
  - two failures, one error result, one non-zero bash → both listed, newest
    first
  - files merged by path from the ledger and git, newest `last` first, git
    winning provenance; a list longer than the old twenty-entry cap keeps
    every path while the block fits
  - `coverage` reflects undecodable parts
  - escaping: a command containing `<`, `&`, and a newline
  - budget: enough long paths to overrun the block → truncated with a count
    line, block stays under 1500 `approxTokenCount`
- `internal/agent/agent_test.go`: summarize with a synthetic history and
  assert the persisted parts contain `<context_manifest>` and that the
  model's prose precedes it.

**Acceptance**

- `go test ./internal/agent/ -run 'Manifest|Summary|Summarize'`

**Rollback:** revert; the manifest is additive.

---

### PR-6 — `recall` tool for archived tool results

**Depends on:** none (works even before tombstones exist).
**Fixes:** P5, first half.

**Changes**

- `internal/db/sql/messages.sql`: add `ListToolMessagesBySession` (§4.6) and
  run `sqlc generate`.
- `internal/message/message.go`: add to the `Service` interface and
  implementation:
  `ListToolMessages(ctx context.Context, sessionID string) ([]Message, error)`.
- New `internal/agent/tools/recall.go` + `internal/agent/tools/recall.md`
  (embed the description, matching `todos.md` style).
  - `NewRecallTool(messages message.Service) fantasy.AgentTool`.
  - Session id from `GetSessionFromContext(ctx)`; empty → error.
  - Search the decoded results for `ToolCallID`; first match wins.
  - Byte-slice with `Offset`/`Limit`; default `Limit = 8192`, clamp to
    `65536`, clamp negative to 0. Slice on a rune boundary (reuse
    `truncateUTF8`'s rune-walk logic; do not split a multi-byte rune).
  - Header + body + remainder notice per §4.6.
- `internal/agent/coordinator.go`: register in the base tool block
  (`allTools = append(...)` near `tools.NewTodosTool`). Available to
  sub-agents too (read-only).

**Tests**

- `internal/agent/tools/recall_test.go` (use `t.TempDir()`-backed message
  service as the other tool tests do):
  - hit returns header + full body for a small result
  - hit with `Limit` smaller than body returns a remainder notice with the
    correct byte counts
  - `Offset` past the end → empty body + notice
  - unknown id → error mentioning the id
  - more than one selector set / none set → error naming each form (PR-20
    raises the count from two to three)
  - id belonging to a different session → error
  - multi-byte rune at the slice boundary is not split
- `internal/message/message_test.go`: `ListToolMessages` returns only
  `role='tool'`, ascending by `created_at`.

**Acceptance**

- `go test ./internal/agent/tools/ ./internal/message/ -run 'Recall|ToolMessages'`

**Notes**

- Do not add an index for this query yet. If a profile shows it dominating,
  the follow-up is a `(session_id, role)` index in its own PR.
- Do not implement `spill_path` here; leave the field out of the params until
  PR-11 so the tool schema does not advertise an unimplemented capability.

**Rollback:** remove the tool registration; the SQL/message-service additions
are inert.

---

### PR-7 — Tool-result elision with tombstones (shadow by default)

**Depends on:** PR-6 (the tombstone points at `recall`).
**Fixes:** P5, second half — the actual reversibility.

**Changes**

- New file `internal/agent/tombstone.go`:
  - `type elidePolicy struct { Mode elideMode; KeepRecent int; KeepBytes int; ExemptTools map[string]bool }`
  - `func renderTombstone(r message.ToolResult, at int64) string` — pure,
    produces §4.3 exactly, including the 400-byte cap.
  - `func shouldElide(msgs []message.Message, idx int, policy elidePolicy) bool`
    — pure: false for the last `KeepRecent` tool results, false for exempt
    tool names, false when the content is under `KeepBytes`, true otherwise.
  - `elideMode`: `elideOff` / `elideShadow` / `elideOn`.
- `internal/agent/agent.go`
  - `preparePrompt` gains an `elidePolicy` parameter (prefer passing it in
    over reading it off the agent, so tests do not need a whole agent).
  - **Exact insertion point.** Today `preparePrompt` builds
    `toolResultsByCall map[string][]fantasy.MessagePart` by calling
    `m.ToAIMessage()` on each `message.Tool` row. That conversion is
    all-at-once and loses the per-result metadata (`Name`, `IsError`,
    `Content`) that a tombstone needs. Two acceptable shapes:
    - **(preferred)** replace that loop with an iteration over
      `m.ToolResults()` (which yields `message.ToolResult` with
      `ToolCallID`, `Name`, `IsError`, `Content`, `Data`, `MIMEType`), decide
      elision there, and build the `fantasy.ToolResultPart` from the result
      — either the real content or the tombstone text — with a small helper
      mirroring `message.ToAIMessage`'s Tool branch;
    - or keep `ToAIMessage` and post-process the produced parts, which
      requires re-parsing the text out of the part and therefore cannot tell
      a `bash` result from a `view` result after the fact. Do not do this.
  - The orphan-tool-result `continue` (unknown `ToolCallID`) must run **before**
    elision, so an orphan is dropped as today, not replaced by a tombstone
    pointing at an id the model cannot resolve.
  - Compute elision over the **flattened tool-result order**, not per message,
    so `KeepRecent` means "the last N results" regardless of how results are
    packed into rows. Build the ordered slice of result ids once at the top of
    `preparePrompt`, then index into it.
  - Shadow mode renders the full content and logs
    `slog.Info("Would elide tool result", "tool", name, "bytes", n, "tool_call_id", id, "saved_bytes", n)`.
- Config: `Options.CompactionElide string` (`""`/`off`/`shadow`/`on`, empty
  means shadow) + `option compaction-elide` in `shellconfig/options.go` with
  value validation (usage error on unknown). Document in
  `docs/config/README.md` next to `auto-summarize`.
- Defaults: `KeepRecent = 3`, `KeepBytes = 2048`,
  `ExemptTools = {view, diagnostics}`. The "actively debugging" exemption is
  not automatic in this PR — the model gets no special treatment for the
  most recent failures beyond `KeepRecent`.
- **Turn-local exemption (required, not optional).** `Run` keeps a per-run
  `map[string]struct{}` of tool-result ids produced *this turn*; those are
  never elided. `preparePrompt` receives it. This is the only mitigation for
  the retrieval loop: a `recall` result produced in turn N must not be
  elided during the same turn. It is cheap (one map, one lookup) and it
  makes PR-7 safe to enable without waiting for PR-19.

**Tests**

- `internal/agent/tombstone_test.go`:
  - format: header field order, `exit` omitted for non-bash, footer line
    count correct
  - two preview lines: first non-empty + first failure marker, deduped when
    identical
  - 400-byte cap: previews dropped, header+footer retained
  - preview clipping on a multi-byte rune boundary
  - `shouldElide`: last 3 kept; small results kept; exempt tools kept;
    everything else elided
- `internal/agent/agent_test.go`:
  - shadow mode: rendered prompt contains the full content and the log path
    is exercised (assert via the returned messages, not logs)
  - on mode: rendered prompt contains `[elided]` and `recall(tool_call_id=`
  - `off` mode: unchanged output (regression guard — compare against the
    pre-PR output for a fixed fixture)
  - turn-local exemption: a tool result in the current run's exempt set is
    never elided even when it is older than `KeepRecent`
- `internal/shellconfig/options_test.go`: `option compaction-elide bogus` is
  a usage error; `on`/`shadow`/`off` map to the JSON field.

**Acceptance**

- `go test ./internal/agent/ ./internal/shellconfig/ -run 'Elide|Tombstone|Option'`
- Shadow mode is byte-identical to today for every non-log code path.

**Risks / notes**

- Do not touch persisted rows. If any test asserts that
  `messages.Update` is called during elision, that is a bug in the test.
- Order of transforms in `preparePrompt` matters: elision must run **after**
  orphan-tool-result filtering (PR-6 does not change that; the existing
  `continue` for orphaned results stays) and **before**
  `supportsImages`/`filterFileParts`.

**Rollback:** set the option to `off`; or revert. Shadow mode is safe to ship
alone.

---

### PR-8 — Export the transcript and point the summary at it

**Depends on:** PR-4 (summary content shape).
**Fixes:** coarse retrieval — the "I remember seeing an error somewhere"
case that a `tool_call_id` handle cannot serve.

**Changes**

- New file `internal/agent/transcript.go`:
  - `func writeTranscript(dir, sessionID string, msgs []message.Message, at time.Time) (string, int, error)`
    producing JSONL per §4.5, `0o600`.
  - Path validation: reject a `sessionID` containing `..`, `/`, `\`, or
    control characters (`filepath.Clean` + `strings.Contains` is not
    sufficient — validate the component set explicitly).
  - `func pruneTranscripts(dir, sessionID, keepPath string, keep int) error`
    — delete oldest `transcript-*.jsonl` beyond `keep` (5), never deleting
    `keepPath`.
- `internal/agent/agent.go`: in `Summarize`, after success and before the
  anchor update, write the transcript. Append the `## Full Transcript` block
  (§4.5) to the summary content. On error: log `Warn` and continue.
- **Which messages to export.** `Summarize` already fetched exactly the span
  being replaced (`getSessionMessages` returns the previous summary plus
  everything after it). Export **that slice**. Do not issue a new query and
  do not try to compute "messages before the new anchor" — the new anchor
  does not exist yet at that point. Note the slice's first element may be the
  *previous* summary with its role already rewritten to `user` by
  `getSessionMessages`; export the row's stored role, or export the slice
  before the role mutation (see §13 Gotchas).

**Tests**

- `internal/agent/transcript_test.go` (`t.TempDir()`):
  - JSONL: one line per message, valid JSON, tool calls and results
    serialized, text preserved
  - file mode is `0o600`, parent dir `0o700`
  - invalid session id (`../../etc`) → error, no file written
  - pruning keeps 5, deletes oldest, never deletes `keepPath`
- `internal/agent/agent_test.go`: summary content ends with the
  `## Full Transcript` block containing an existing path.

**Acceptance**

- `go test ./internal/agent/ -run 'Transcript|Summary'`
- Manual: compact a session, `rg` the printed path, confirm hits. Confirm
  `view` on a large transcript returns the size error — the prompt already
  tells the model to use `rg`, and PR-11 adds a `recall(spill_path)` path if
  the file is small enough not to trip it.

**Risks**

- A large session produces a large file; that is intended (it replaces an
  index). Pruning bounds it at 5 files.
- Never write the transcript inside the workspace — it would show up in
  `git status` and in `rg --glob` results for the agent's own searches. Use
  `DataDirectory`.

**Rollback:** revert; orphan transcript files are harmless and pruned by the
next compaction.

---

### PR-9 — Incremental merge for repeated compactions

**Depends on:** PR-2 (guards), PR-5 (manifest), PR-8 (pointer).
**Fixes:** summary-of-summary drift.

**Changes**

- Migration: `sessions.summary_anchor_message_id TEXT` and
  `sessions.summary_prompt_prefix_len INTEGER`
  (`NOT NULL DEFAULT 0`). Regenerate sqlc. Extend `session.Session`.
- On a successful summary, set `SummaryAnchorMessageID` to the id of the
  **oldest message being dropped** (the first element of the span exported in
  PR-8; if that element is the previous summary message itself, use it — it
  is being replaced), and set `SummaryPromptPrefixLen` to the byte length of
  the model-written narrative *before* the manifest/transcript blocks are
  appended. Both fields are decided; do not re-open this. The prefix length
  is what `splitPreviousSummary` uses to recover the narrative without
  parsing.
- `internal/agent/agent.go`
  - `getSessionMessages` keeps its contract.
  - New `func splitPreviousSummary(msgs []message.Message) (narrative string, ok bool)`
    — returns the previous summary's model-written text; when the length
    column is set, slice by it, else use the whole content.
  - `buildSummaryPrompt` gains the previous narrative: signature becomes
    `buildSummaryPrompt(todos []session.Todo, previous string) string`
    (update call sites and tests). When `previous != ""`, append the merge
    instruction from §4.7 (merge, do not rewrite; keep still-relevant facts;
    mark superseded decisions; never shrink below the previous's token
    count without cause).
  - The summarization input does not change. `getSessionMessages` already
    returns exactly `[previous anchor .. now]`, so the only change is passing
    `previous` into the prompt and instructing a merge. Verify this by
    reading `getSessionMessages` before writing code; if the span turns out
    to include more than that, fix it here rather than adding a new query.
  - `validateSummary`'s "dropped span non-empty" input becomes: true whenever
    `previous != ""` (the previous summary message is always part of the span
    being replaced).

**Tests**

- `splitPreviousSummary`: with a prefix length, with none, with a manifest
  appended, with a transcript block appended.
- `buildSummaryPrompt`: merge instruction present only when `previous != ""`.
- Guard interaction: two compactions in a row, the second returns 30% of the
  first → rejected (G2) even though the span was small.
- Migration round-trip test in `internal/session`.

**Acceptance**

- `go test ./internal/agent/ ./internal/session/ -run 'Summary|Split|Merge'`

**Risks**

- This is the most behavior-changing PR after PR-4. Ship it with the guards
  already in place (PR-2) so a bad merge is rejected rather than adopted.

**Rollback:** revert code; dropping the two columns is not necessary.

---

### PR-10 — Real todo injection and tail recitation

**Depends on:** none. Independent of the summary path.
**Fixes:** long-session drift; a currently stale hard-coded reminder.

**Changes**

- `internal/agent/agent.go`, `preparePrompt`: the hard-coded
  `<system_reminder>` that says "your todo list is currently empty" is
  replaced by a computed block:
  - when the session has todos: list them with status and `active_form`, and
    say to keep using the `todos` tool;
  - when empty: keep today's text verbatim (it is correct in that case).
- Append a **tail recitation** as a final synthetic user message: the current
  goal (last non-empty user message, clipped to 400 bytes) and the
  in-progress todo (`Status == in_progress`, else the first pending one).
  This is intentionally redundant with the head block.
- `preparePrompt` needs the session's todos: it currently takes
  `([]message.Message, bool, ...Attachment)`. Add a parameter
  (`todos []session.Todo`) rather than reading the session inside — keep it
  pure and testable. Two call sites:
  - `Run`: pass `currentSession.Todos`.
  - `Summarize`: pass `nil`. The summary prompt already embeds the todo list
    via `buildSummaryPrompt`, and the summarizer is not resuming work, so a
    recitation there is noise. The recitation is a *turn* feature, not a
    compaction feature.
- The recitation must be the **last** element of the returned history and
  must not be preceded by a blank assistant message, or providers that
  require alternation will reject the request. Use the existing
  `fantasy.NewUserMessage` shape rather than fabricating parts.

**Tests**

- `preparePrompt` with todos → head block lists them, tail recitation has the
  goal + in-progress item.
- `preparePrompt` without todos → head block is the existing empty-list text,
  tail recitation still present with the goal only.
- Ordering: the recitation is the last element of the returned history.
- Regression: `Summarize`'s history does not gain a duplicate todo block.

**Acceptance**

- `go test ./internal/agent/ -run 'PreparePrompt|Recitation|Todo'`

**Rollback:** revert; no data changes.

---

### PR-11 — Spill handles for `fetch` and web search

**Depends on:** PR-6 (`recall` exists; adds `spill_path`).
**Fixes:** two permanent-loss sites in §1.

**Changes**

- `internal/agent/tools/fetch.go`: when content is truncated, write the full
  body with `shell.SpillOutput(content, spillDir)` and replace the notice
  with `[Content truncated to N bytes; full content: <path>]`. The tool needs
  a `spillDir` — thread it from the coordinator the same way `NewBashTool`
  gets it (`c.cfg.Config().Options.DataDirectory`), and prefer
  `spillDirForContext(ctx)` when a policy is attached, exactly as
  `formatOutput` does, so sandboxed runs spill into the permitted temp dir.
- `internal/agent/tools/search.go`: same for snippets over `maxSnippetRunes`,
  but only when the full snippet exceeds a minimum worth spilling
  (e.g. 4× the cap) to avoid file spam.
- `internal/agent/tools/recall.go`: implement `spill_path`:
  - accept only a path whose cleaned absolute form is inside the spill
    directory that the tool was constructed with (`<spillDir>/shell-output/`),
    checked with `filepath.Abs` + `filepath.Clean` + a path-prefix check that
    compares `filepath.Dir`-segments, not a raw `strings.HasPrefix` (a
    sibling directory named `shell-output-evil` must not pass);
  - reject symlinks pointing outside that directory (`os.Lstat` + reject
    non-regular files);
  - chunked read with the same `Offset`/`Limit`/remainder semantics as the
    payload path;
  - do not shell out and do not depend on `view` (it rejects files over
    `MaxTextViewSize`).

**Tests**

- `fetch`/`search` tests: truncated output contains a path that exists, and
  the content is complete on disk.
- `recall` tests: `spill_path` outside the spill dir → error; traversal
  (`../../etc/passwd`) → error; chunked read matches the file.
- Sub-agent sandbox interaction: `SubagentPolicy` narrows writes to the
  private temp dir but keeps reads; assert a read-only sub-agent can read a
  spill file it produced, without any write permission.

**Acceptance**

- `go test ./internal/agent/tools/ -run 'Fetch|Search|Recall|Spill'`

**Rollback:** revert.

---

### PR-12 — Record writes/execs and refresh recently-touched files

**Depends on:** none.
**Fixes:** the one place where a storage change is genuinely required
(`filetracker` records reads only, so "re-read the files we just changed" is
impossible).

**Changes**

- `internal/filetracker`: add `RecordWrite(ctx, sessionID, path)` and
  `RecordExec(ctx, sessionID, command string)`. Store them in a **new**
  `file_events(session_id, path, kind, at)` table with
  `kind IN ('read','write','exec')`; do not touch `read_files`, because
  `require-read-before-write` depends on its exact semantics.
- Wire `RecordWrite` into `edit`, `write`, `multiedit`,
  `lsp_replace_symbol`; `RecordExec` into `bash`.
- After a successful compaction, append to the summary a
  `Recently modified files` list (top 5 by write time, with paths only) —
  this is what lets the resuming agent `view` the right things without
  guessing. Do **not** auto-read the files in this PR; measure first.

**Tests**

- filetracker: new table round-trip, ordering by time, per-session isolation.
- tools: one write/edit test asserts a write event; a bash test asserts an
  exec event.
- summary: the recently-modified list appears only when there are write
  events, capped at 5, paths only.

**Acceptance**

- `go test ./internal/filetracker/ ./internal/agent/tools/ ./internal/agent/ -run 'File|Recent|Summary'`

**Rollback:** revert code; drop the table in a follow-up if unused.

---

### PR-20 — Make the published anchors retrievable

> **Amendment (2026-10-28).** Superseded. `anchors:` and the
> `recall(message_id=...)` selector it added were removed (§4.4 and §4.6
> amendments), so `recall` takes only `tool_call_id` and `spill_path`. What
> survives from this PR is the failure row's trailing `tool_call_id=`, which
> is the whole of the retrieval handle now.

**Depends on:** PR-5 (publishes `anchors:`), PR-6 (`recall` exists).
**Fixes:** D-3 in §7.3 — every anchor the manifest published was unusable.

**Changes**

- `internal/agent/tools/recall.go`: add `MessageID` to `RecallParams`, and
  replace the two-selector check with a count over all three selectors so the
  error names each form. `recallMessage` fetches the message, requires its
  session to match the caller's, refuses a summary row with an explanation,
  and pages `Content().Text` through the existing `recallSlice`.
  `messageResponse` renders `message_id`, `role=user`, `bytes` and
  `recorded_at`, so an archived request is as labelled as an archived result.
- `internal/agent/summary_manifest.go`: failure anchors publish
  `ToolCallID` instead of the carrying message id; deduplicate anchors by
  handle rather than by message so batched failures each get one; append
  `tool_call_id=` to each `failures:` line after the fields §4.4 fixes.
- `internal/agent/tools/recall.md`: document `message_id` and state which
  `kind` maps to which parameter.

**Tests**

- `recall`: an archived user message round-trips; it pages; an unknown id, a
  foreign session's id and a summary row are each refused with a message that
  says which; `message_id` + `spill_path` is refused as "not several".
- manifest: `kind=failure` anchors carry the call id; the `failures:` line
  keeps its declared order and gains `tool_call_id` last; batched failures
  produce one anchor each.

**Acceptance**

- `go test ./internal/agent/ ./internal/agent/tools/ -run 'Recall|Manifest|Anchor'`

**Rollback:** revert; a manifest written by the new code still parses, since
only trailing fields were added.

---

## 8. Deferred Work (named, not scheduled)

Listed so the PR numbers referenced elsewhere in this document have a home.
None of these block the PRs above.

- **PR-13 — Optional small model for summarization.** Config
  `compaction-summary-model = large|small` (default `large`). Blocked on
  evidence: providers differ in how they handle structured content, and
  switching models can degrade exactly the sections this PRD adds. Ship only
  with an A/B on the probe set. (Referenced by §6 and D10.)
- **PR-14 — `view` long-line spill.** Replace the `MaxLineLength` cut
  (`tools/view.go`) with a spill handle so a 3000-byte line is recoverable.
  Needs a new `view` parameter surface; separate PRD.
- **PR-15 — Partial compaction.** "Summarize from here / up to here" over a
  message id, mirroring session-rewind UX.
- **PR-16 — Handoff command.** A human-editable draft in a new session, as a
  complement to compaction rather than a replacement (see D1).
- **PR-17 — `preparePrompt` budget accounting.** Give each context layer
  (pinned / narrative / manifest / tail) an explicit token budget instead of
  whichever one grows. Depends on PR-10 (which introduces the layers).
- **PR-18 — FTS5 / `searchable_text` retrieval.** Only if `rg` over the
  exported transcript demonstrably fails the "I remember seeing an error"
  case, and only after confirming FTS5 on both SQLite drivers. See D4.
- **PR-19 — Retrieval-loop pinning.** If measurements show recall results
  being elided and re-recalled, add a `session_pins` table. Deliberately not
  in this PRD (D6).

---

## 9. Test Strategy

**Per-PR requirements**

- Unit tests for every pure helper (`renderTombstone`, `buildContextManifest`,
  `validateSummary`, `stripAnalysisBlock`, `writeTranscript`,
  `splitPreviousSummary`).
- `preparePrompt`/`Summarize` tests use the package's existing mock-provider
  scaffolding, never a live endpoint.
- Table-driven where the input space is enumerable; `require` (not `assert`);
  `t.Parallel()`; `t.TempDir()` for any filesystem work; `t.Setenv()` for
  env-dependent behavior.
- Every PR must keep `go test ./...` green, not just its own package.

**Cross-cutting regression fixtures**

Add a small fixture session (5 user messages, 12 tool calls including a
failing `go test`, 3 file reads, 1 edit) under
`internal/agent/testdata/summary/` and assert, per PR, on the rendered
history size and on the presence/absence of markers:

| PR | Assertion on the fixture |
| --- | --- |
| PR-4 | all seven model-authored headings present in the summary text |
| PR-5 | manifest lists exactly the failing command and the edited file |
| PR-7 off | rendered history identical to the pre-PR baseline |
| PR-7 shadow | identical to baseline (full content) |
| PR-7 on | history shrinks; every elided result has a resolvable handle |
| PR-8 | transcript path exists and contains the failing command |
| PR-9 | second compaction's prompt contains the first narrative |

Baseline files live next to the fixture; regenerate deliberately, never
blindly.

**Commands**

```
task fmt                 # gofumpt
task lint                # includes lint:log (capitalized log messages)
go test ./internal/agent/ -run 'Summary|Summarize' -race
task test                # full suite with -race -failfast
```

---

## 10. Evaluation

Metrics that decide whether this work paid off. Instrument first (PR-5 and
PR-7 shadow mode give the data), change behavior second.

| Metric | How | Target |
| --- | --- | --- |
| Repeated tool call rate | After a compaction, count `(tool, normalized args)` pairs seen **before** the anchor and repeated within the next 5 turns | decreases after PR-7 ships as `on` |
| Byte savings from elision | Shadow-mode log totals | ≥ 40% of tool-result bytes on long sessions |
| Resumption probe accuracy | Force a compaction at 50% of a recorded session, ask 4 probes (original error message / which files changed / what is next / what was decided about X), score against the un-compacted arm | within 10 points of the un-compacted arm |
| Artifact accuracy | Manifest file list vs `git status --porcelain` at the same point | 100% by construction (code-generated); a mismatch is a bug. Only holds because `files:` is merged with git — see D-1 in §7.3 |
| Tokens per completed task | End-to-end on a fixed task set, before/after | must not increase |
| `recall` usage | Calls per compacted session; hit/empty/stale-misuse counts | measure; misuse < 5% |
| Cache hit ratio around compaction | `cache_read / (cache_read + cache_creation)` | no material drop |
| Summary failure rate | Guard rejections per 1,000 compactions | trending down after PR-4 |

Reference magnitude for "how much accuracy compaction can cost": published
benchmarks report post-compaction accuracy around 56.7% average / 68.5% best
on tasks that were 100% solvable before compaction. If a change drops us
below that band, something is systematically wrong.

---

## 11. Decision Log

Recorded so the next session does not re-open settled questions. Each entry:
decision, alternatives considered, why they lost.

**D1 — Summarization is kept; it is not replaced by "start a new thread".**
Some products removed compaction in favor of a human-reviewed handoff
document. Rejected: Crush's usage includes long investigative sessions where
the alternative is losing the work. Handoff may be added later as a
complement (a `/handoff` command), not as a replacement.

**D2 — Facts are generated by code; judgements are written by the model.**
Alternative: a richer prompt asking the model for a structured file table.
Rejected: published artifact-tracking scores are the weakest dimension for
every summarizer (2.19–2.45 / 5), which is a structural failure, not a prompt
failure. Prompts encode judgement; plumbing encodes guarantees.

**D3 — Eviction is display-layer only; no idempotent replay.**
Alternative considered and dropped: intercepting a repeated tool call and
serving the archived result (requires mtime/hash checks, an idempotency
whitelist, and a `force` escape hatch). Rejected because an archived payload
is immutable history and needs no freshness judgement — only *re-execution*
does. The handle (`tool_call_id`) gives the model the choice, at a cost it
can see (bytes), without the harness guessing.

**D4 — Retrieval is `rg` over an exported transcript, not a search index.**
Alternatives: a `searchable_text` column + `LIKE`; FTS5. Rejected for now:
the build can use either `modernc` or `ncruces` SQLite, and `parts` is JSON,
so FTS5 needs trigger work plus driver verification, while an exported JSONL
gives `rg` (regex, context lines, multi-file) for the cost of one writer.
Revisit only if usage shows `rg`-over-transcript failing.

**D5 — No cross-session recall.**
Crush creates per-task sub-sessions. Searching the parent from a sub-agent
would import unrelated content and blur the delegation boundary. Parent
context must be passed explicitly.

**D6 — Consistency beats completeness.**
A summary is a single authoritative view. Anything that can present two
versions of the same fact (old summary vs new, old decision vs superseded)
is rejected or explicitly superseded. This is why eviction tombstones point
at *immutable* payloads and never at decision state.

**D7 — Elision ships in shadow mode first.**
Alternative: ship enabled. Rejected: tombstone quality (preview-line choice)
determines whether the model retrieves or re-runs, and we have no data yet.
Shadow mode measures the savings and the would-be-affected tools with zero
behavior change.

**D8 — Keep `## Artifacts` prose *and* the manifest.**
They look redundant but answer different questions: the manifest is *what
changed* (authoritative, code-generated), the prose is *why it mattered*
(model-generated). Merging them would force the model to author facts.

**D9 — Do not feed the model a token countdown.**
Reported failure mode: a model that knows its remaining budget takes
shortcuts and leaves work incomplete ("context anxiety"). No
`<total_tokens>` reminders.

**D10 — No automatic summarizer-model downgrade in this PRD.**
Tempting (title generation already uses the small model), but summarization
quality is the one place where the large model earns its cost, and providers
differ on structured-content handling. Left as follow-up PR-13, opt-in.

**D11 — Failure is a skip, not a turn failure.**
When the breaker trips or a guard rejects, `Run` continues the turn without
compaction. The user gets one notice. Turning a compaction problem into a
conversation failure multiplies the damage.

---

## 12. Risks

| Risk | Severity | Mitigation |
| --- | --- | --- |
| Tombstone preview quality decides the whole feature | High | PR-7 ships in shadow; measure retrieval vs re-execution; the format is one constant to tune |
| Retrieval loop (recall → next compaction elides it again) | High | Recalled content is a tool result and will be elided; keep `KeepRecent` large enough to cover a recall followed by a compaction, and treat a `recall` result as exempt for the current turn. Implement the turn-local exemption in PR-7 (cheap: mark the message id in a per-run set) |
| Model ignores tombstones | Medium | Keep them short, uniform, and in the *original position*; never batch them at the end |
| Stale-content misuse | Medium | Every retrieval states `recorded_at` and "may not reflect current state"; the tool description says to use `view` for current content |
| Prompt injection through archived payloads | Medium | Manifest and transcript blocks are fenced and labelled as data; manifest values are escaped; `recall` wraps the body in an explicit archived-record header |
| Transcript files accumulate | Low | Cap at 5 per session, never delete the live one |
| Guard false positives (legitimately tiny summaries) | Medium | G2 requires both a previous summary and a non-empty dropped span; a single retry, then keep the old anchor (session still works) |
| Migration ordering conflicts across parallel PRs | Low | One migration per PR, timestamped at implementation time |
| `preparePrompt` signature churn breaks other callers | Low | Only two call sites today (`Run`, `Summarize`); update them in the same PR |

---

## 13. Implementation Notes

**Repo conventions that matter here**

- CGO disabled; `GOEXPERIMENT=greenteagc`.
- Formatting: `gofumpt` (`task fmt`). Imports grouped stdlib/external/internal.
- Log messages start with a capital letter (`task lint:log` enforces it).
- Comments on their own line start with a capital and end with a period; wrap
  at 78 columns.
- Errors: wrap with `fmt.Errorf("...: %w", err)`.
- Tests: testify `require`, `t.Parallel()`, `t.TempDir()`, `t.Setenv()`.
- Tool descriptions live in `<tool>.md` next to `<tool>.go`, embedded with
  `//go:embed`.
- Generated DB code is committed; regenerate with `sqlc generate` after
  editing `internal/db/sql/*.sql`.
- `internal/agent/templates/*.md.tpl` are Go templates; `summary.md` is *not*
  a template today and must stay plain text unless a PR explicitly templates
  it (PR-9 adds conditional text through `buildSummaryPrompt`, not the
  template).

**Where to start reading to get oriented** (in order)

1. `internal/agent/agent.go` — `Summarize`, then `getSessionMessages`, then
   `preparePrompt`, then the `Run` `StopWhen` block.
2. `internal/agent/templates/summary.md`.
3. `internal/message/message.go` — `ListFromSummary`, `Create`, `Update`
   debounce semantics (a read can race the debounce; use `Flush` before
   asserting on freshly written state in tests).
4. `internal/db/sql/messages.sql` + `sqlc.yaml` (repo root).
5. `internal/agent/tools/todos.go` + `todos.md` as the model for a new tool.

**Gotchas**

- There is no shared "safe path" helper in this repo. Any new path handling
  (transcript dir, `spill_path`) must validate explicitly; see §4.5.
- `message.Service.Update` is debounced (33ms) for streaming deltas and
  synchronous for terminal updates. Any test that writes then reads must
  `Flush`/`FlushAll` first, or assert after a terminal update.
- `getSessionMessages` mutates `msgs[0].Role` on the returned slice. If you
  add code that reuses that slice for serialization (PR-8), serialize
  **before** the role mutation or work from the unmutated copy.
- `preparePrompt` emits tool results adjacent to their assistant message
  because several providers reject non-adjacent results. Any change to
  elision ordering must preserve that invariant — elide the result content,
  never the result's position.
- `ListFromSummary` falls back to the whole session when the anchor is
  missing. That fallback is deliberate; do not "fix" it into an empty list.
- Sub-agents run read-only (`sandbox.SubagentPolicy`); any file access this
  PRD adds must work under a read-only policy, or be skipped for sub-agents.
- `recall` and the transcript pointer must not leak absolute paths outside
  `DataDirectory`.
- A synthetic tool name that ends in `_bash` is still a bash tool: MCP tools
  are namespaced `<server>_<tool>`. Use `tools.IsBashToolName` rather than
  comparing to `"bash"`, or an MCP bash wrapper will report no exit status.

**Toolchain: regenerating `internal/db`**

`sqlc` is not vendored and `go install` fails in this environment for two
avoidable reasons — the module cache is read-only, and checksum verification
wants to write to `sum.golang.org`'s cache. Both are fixed by pointing at a
writable cache and disabling the checksum DB for the install only:

```sh
mkdir -p /tmp/gomodcache /tmp/gobin
env GOMODCACHE=/tmp/gomodcache GOSUMDB=off GOFLAGS=-mod=mod \
    GOBIN=/tmp/gobin \
    go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0
```

`GOPROXY` may need setting too: `proxy.golang.org` timed out from this
machine while a regional mirror worked, so add `GOPROXY=<mirror>,direct` if
the download stalls. The `GOSUMDB=off` is for the install only — it works
around the read-only sumdb cache, and nothing about the project's own
dependency verification changes.

Verified as written: with the cache warm the recipe installs without
`GOPROXY`, and `/tmp/gobin/sqlc generate` on a clean checkout produces no
diff. Remove the temp directories afterwards if disk matters — the sandbox
may refuse to delete them, in which case leave them; they are outside the
repo.

Pin the version: the committed files carry a `sqlc v1.30.0` marker, and a
different generator reformats unrelated code, which buries the real change in
the diff. `/tmp/gobin/sqlc generate` then matches `internal/db` exactly (see
`24d7563`, which cleared a pre-existing drift of that kind before PR-6 could
add to it).

Two consequences worth knowing before the next schema PR:

- **Verify the regeneration is empty first.** Run `sqlc generate` before
  editing any query. If `git status` shows `internal/db` changes, the
  checkout was already out of sync: commit that separately, so the schema PR
  contains only its own query.
- **`golangci-lint` still cannot run here** (see the note above
  §7 "PR-1"). `task lint`, including `task lint:log`, is not a check this
  environment can perform; review new log calls by hand.

**Definition of done for the whole PRD**

- All twelve PRs merged (PR-11/PR-12 may be deferred without blocking).
  PR-20 no longer applies: the anchors it made retrievable were removed, and
  the failure row's `tool_call_id` is the only handle left to keep working.
- `task fmt && task lint && task test` green.
- Shadow-mode data reviewed; a decision recorded on whether to flip
  `compaction-elide` to `on` by default.
- Evaluation harness (probe set) exists and has a first baseline recorded,
  even if the numbers are not yet where we want them.

---

## 14. Appendix A — New Session Checklist

Follow this literally; it assumes no memory of the design discussion.

1. **Read §1–§4 of this document.** Then read, in this order,
   `internal/agent/agent.go` `Summarize` → `getSessionMessages` →
   `preparePrompt`, and `internal/agent/templates/summary.md`.
2. **Verify the anchors in §3 still match.** Line numbers drift; symbol names
   should not. If a symbol moved, update §3 in the same PR that moves it.
3. **Pick the next PR from §7.2's status table, not from this list.** PR-1,
   PR-2, PR-4, PR-5 and PR-6 have landed; the next one by dependency order is
   PR-3 (circuit breaker). Look at the table before starting anything: it is
   the only place that says what is actually done.
4. **If the PR touches the schema, set up `sqlc` first** using the recipe in
   §13, and confirm `sqlc generate` produces no diff *before* editing any
   query. A dirty baseline means a drifted checkout, not your change.
5. **One PR at a time.** Do not bundle. Each PR in §7 lists its own tests and
   its own acceptance command; run exactly those, then `task test` before
   opening the PR.
6. **Never regenerate the baseline fixtures blindly.** If a PR is supposed to
   change rendered history, assert the change explicitly in the test rather
   than accepting a golden diff.
7. **When the document and the code disagree, the code wins for facts and
   the document wins for intent.** Record the discrepancy in the PR
   description and fix the document in the same PR.

If you are asked to "just do the summary improvement" and none of it has
landed, the answer is PR-1, PR-4, PR-5 in that order (see §7.1). On this
branch those are done; check §7.2 and continue from PR-3.

---

## 15. Appendix B — Discussion Traceability

Each finding that drove the design, and where it lands. Kept so that a future
reader can tell which claims are load-bearing and which were discarded.

### Observations that became requirements

| Finding | Consequence | Where |
| --- | --- | --- |
| Tool output dominates tokens and is mostly noise | elide tool results before summarizing | PR-7 ladder step ① |
| Simple masking matches or beats LLM summarization on coding tasks at half the cost | eviction is worth doing *before* paying for a summary | §4.2, D7 |
| Summaries systematically lose exact paths, commands and exit codes | facts generated by code, not prose | PR-5, D2 |
| User messages are the least derivable content in the session | verbatim section, mandatory | PR-4 |
| Failures are evidence; deleting them causes rework | `## Rejected Approaches` mandatory; `failures` in the manifest | PR-4, PR-5 |
| Re-summarizing a summary causes collapse | merge instead of rewrite, plus a collapse guard | PR-9, PR-2 |
| Truncation is silently persisted | hard guard before the anchor moves | PR-1 |
| Retry storms burn API budget | circuit breaker, skip-not-fail | PR-3 |
| The pre-compaction transcript is the cheapest retrieval index available | export JSONL, use `rg` | PR-8, D4 |
| An evicted payload is immutable, so retrieving it needs no freshness check | handle (`tool_call_id`), not replay | PR-6, D3 |
| Models do not go looking for memory on their own | put the handle in the discarded content's own position | §4.3, D7 |
| Compaction happens at the model's least capable moment | (not fixed here; mitigations are user-driven) | §8, PR-15 |

### Ideas considered and deliberately dropped

| Idea | Why not | Where |
| --- | --- | --- |
| Idempotent replay of `view`/`rg`/`glob` | needs mtime/hash/force machinery to answer "did the world change"; the archived payload needs no such judgement | D3 |
| `searchable_text` column + `LIKE`, or FTS5 | dual SQLite driver, JSON `parts`, and `rg` over an exported file is strictly more capable for the same effort | D4, PR-18 |
| Embedding/semantic search over history | unpredictable, extra dependency, worse than exact matching for code identifiers | D4 |
| Cross-session recall from sub-agents | blurs the delegation boundary; parent context must be explicit | D5, Risk table |
| Pinning previous summaries forever | creates a second authoritative view; contradicts D6 | §5, §12 |
| Feeding the model its remaining token budget | reported to induce shortcuts and incomplete work | D9 |
| Replacing compaction with handoff | long investigative sessions would lose work | D1, PR-16 |
| Tree-sitter folded definitions, repo maps | cost, and LSP tooling already covers symbol lookup | §2 Non-Goals |
| Provider-side compaction API | opaque payloads are unauditable; revisit if fantasy exposes it | §2 Non-Goals |
| Auto-downgrading the summarizer to the small model | quality risk in exactly the sections this PRD adds | D10, PR-13 |

### Claims that were checked and found weaker than expected

Recorded so nobody re-derives them as "obviously right":

- **"Replay is free."** It is not: an archived payload is free to retrieve,
  but *substituting* it for a fresh tool call requires an answer to "has the
  world changed", which is not free. Dropped.
- **"A retrieval layer is the main gap."** It is a narrow gap: code tasks
  recover well from disk alone; only investigative tasks (grep/bash findings
  that exist nowhere else) need it. Hence a handle plus an exported file, not
  an index.
- **"More compaction = more savings."** Over-compression forces re-reads and
  re-exploration; the target is tokens per completed task, not per request.
  Hence `KeepRecent`, `KeepBytes`, and shadow-first rollout.
- **"A bigger context window makes this moot."** Long-context retrieval
  degrades well before the window fills, so compaction still matters; but the
  urgency is lower than the raw window size suggests.
