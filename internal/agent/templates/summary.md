**This summary is the only context that will survive.** Every message before it
will be gone when the conversation resumes: no tool results, no file contents,
no earlier reasoning. Write for the teammate who reads this document instead.
Be thorough.

You are briefing a teammate who is taking over your work mid-task.

**Tool use is forbidden.** You have no tools worth calling for this task and
none of them will be executed. Tool calls will be rejected and will waste this
turn: nothing you write afterwards will be saved. Read the conversation above
and write the summary.

You may open a single `<analysis>` block at the very start of your reply to
think before writing. It is stripped before the summary is stored, so nothing
in it survives — put every fact that matters in the sections below.

**Required sections**, in this order. Every section is mandatory; write `none`
if a section has nothing to report, so "nothing happened" stays
distinguishable from "the summarizer skipped it".

## Current State

- The exact current user request, quoted verbatim.
- Progress so far: what is completed and verified, what is half done, what has
  not been started.
- What was in flight when this summary was taken.
- The todo list, restated with each item's status. If there is no todo list,
  say so.

## User Messages

Every user message from the conversation, in order, verbatim. Do not
paraphrase, merge, shorten or reorder. Include messages that consist only of a
shell command, annotated as such. This is the highest-value section: mid-task
corrections and added constraints survive only here.

## Constraints

Explicit limits the user stated, quoted verbatim: files not to touch,
libraries to use or avoid, style rules, budgets, deadlines, things the user
already rejected.

## Decisions

Each decision, its rationale, and the evidence that supports it (file path,
command, error string, benchmark). Mark decisions that were later reversed as
`superseded by: <the later decision>`.

## Rejected Approaches

What was tried, why it failed, and the evidence it failed. Keep failures
intact; a reader who does not know the dead ends will walk into them again.

## Artifacts

Breadcrumbs the resuming assistant can query immediately: file path, symbol,
line number. One item per line, with a note on why it matters. Do not guess a
path or line number you have not seen. (A separate machine-generated index
carries the authoritative list of changed files; this section carries the why.)

## Exact Next Steps

A numbered list of concrete actions. Not "implement authentication" but
"add the token check to internal/auth/middleware.go:112, then run
go test ./internal/auth/". End with a verbatim quote of the most recent
exchange showing where the work stopped.

**Do not shorten paths, commands, error strings or user quotes.** They are the
reason the summary exists. Length itself is not capped, but every section must
be populated: a short summary that drops a fact is worse than a long one that
repeats one.

**Tone**: briefing a teammate who starts in five minutes from this document
alone. No emojis.
