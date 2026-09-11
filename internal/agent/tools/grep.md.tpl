Search file contents by regex or literal text; returns matching file paths sorted by modification time (max {{ .MaxResults }} results, context lines included); respects .gitignore. Use glob to filter by filename, not contents.

- Set context to include lines before and after each match.
- Set type to restrict the search to a ripgrep file type (e.g. "go", "js", "py").
- Set ignore_case for case-insensitive matching, or multiline to let the pattern span lines.
{{- if not .RgAvailable }}
- type and multiline require ripgrep (rg) on $PATH.
{{- end }}
