Launch a sub-agent to work on a task in its own context window, so raw findings never fill yours.

Profiles:
- `context` (default): searches the codebase for context and implementation details. Use it when you are looking for a keyword or file and are not confident you will find the right match on the first try.
- `research`: searches the web and answers with citations. Use it for anything that needs current information from outside the repository. Pass `url` to read one specific page instead of searching.
