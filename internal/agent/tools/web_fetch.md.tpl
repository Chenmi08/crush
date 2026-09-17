Read one or more web pages and return their content as markdown.

Pass every URL you need in a single call. One call costs one request no matter how many URLs it carries, while separate calls are slower and can hit rate limits. Set `max_characters` for long documents. URLs on local or private addresses are read directly and never sent to a third party.
{{- if .GhAvailable }} For GitHub content when an exact repo, issue, or PR link is provided, use `gh` CLI in bash instead.{{- end }}
