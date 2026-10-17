package tools

// sandboxDescriptionNote is appended to every confined tool's description. It
// states that the sandbox is active and how to escalate, so the model asks for
// permission instead of guessing or trying another route.
const sandboxDescriptionNote = `

<sandbox>
Writes are confined to a sandbox: the workspace, a private temp directory, and
the user cache directory by default. A write outside those roots is refused
with a message starting "sandbox-denied" that names the refused path and the
setting that would allow it. Paths that grant future code execution (Crush's
config, instruction files, .git/hooks and .git/config) are refused even inside
the workspace and cannot be allowed.

If a refused operation is genuinely required, retry once with
sandbox_permissions set to a mode strictly wider than the current one
(workspace-write, or danger-full-access) together with a justification
sentence. The user is asked to approve leaving the sandbox for that one call;
approval never carries to another call. If the user declines, stop and explain
what is needed instead of trying another tool. Reads are not confined.
</sandbox>`

// withSandboxNote appends the shared sandbox note to a tool description.
func withSandboxNote(description string) string {
	return description + sandboxDescriptionNote
}
