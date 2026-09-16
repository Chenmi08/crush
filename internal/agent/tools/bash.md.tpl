Execute shell commands; long-running commands automatically move to background and return a shell ID.

Banned commands ({{ .BannedCommands }}) return an error - explain to the user instead of retrying. Safe read-only commands execute without prompts. Output is truncated beyond {{ .MaxOutputLength }} characters.

- Use the dedicated View/Edit tools instead of `cat`/`sed` for reading and editing files.
- Chain commands with ';' or '&&'; avoid newlines except in quoted strings. Each call runs in an independent shell (no state persists between calls).
- Use absolute paths instead of `cd`.
- Never run interactive commands; use non-interactive variants (e.g., `npm init -y` not `npm init`).
- For servers, watchers, or any process that does not exit on its own, use run_in_background=true and never append `&`; read output with job_output and stop it with job_kill. Do not background builds, test suites, git operations, or short-lived scripts.
{{- if .RgAvailable }}
- Ripgrep (`rg`) is available and preferred over `grep` for content search.
{{- end }}
{{- if .FdCommand }}
- `{{ .FdCommand }}` is available and preferred over `find` for locating files by name.
{{- end }}

<git_workflow>
When the user asks to commit, amend, or create a PR, first load the git-playbook skill and follow it exactly. Never commit or push unless the user explicitly asked.
{{ if .Attribution.GeneratedWith }}
Attribution line for commit messages and PR bodies:
💘 Generated with Crush
{{ end }}
{{- if eq .Attribution.TrailerStyle "assisted-by" }}
Attribution trailer:
Assisted-by: Crush:{{ .ModelID }}
{{- else if eq .Attribution.TrailerStyle "co-authored-by" }}
Attribution trailer:
Co-Authored-By: Crush <crush@charm.land>
{{- end }}
</git_workflow>

<examples>
Good: pytest /foo/bar/tests
Bad: cd /foo/bar && pytest tests
</examples>
