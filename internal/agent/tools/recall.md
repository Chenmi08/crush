Retrieve an archived tool result that is no longer in the conversation.

Compaction removes tool results from the prompt to keep the conversation
within the model's context window, but the payload is still stored. This tool
returns the original bytes by the tool call id that produced them, or a range
of them when the result is large.

The content is a **historical record**: it is what the tool reported at the
time. State may have changed since — files may have been edited and commands
may now succeed. Use `view` for current file contents and re-run a command
when you need the current state rather than the archived one.

Only the current session is searched. Tool calls from a parent session or a
sub-agent session are not visible here.

Pass `tool_call_id` to read an archived result. Use `offset` and `limit` to
page through a large one; the response states how many bytes remain.

Pass `spill_path` instead to read a file named in a truncation notice, such
as the full body of a page larger than `fetch` returns or a search snippet
that was cut. Only files inside Crush's spill directory are accepted; any
other path is refused. Use `view` for a normal file.
