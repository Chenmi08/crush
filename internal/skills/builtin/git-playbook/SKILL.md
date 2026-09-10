---
name: git-playbook
description: Use when the user asks to create or update a git commit, amend, or create a pull request. Contains the required commit message quality rules, commit/PR workflow, and attribution requirements.
---

# Git Commit & PR Playbook

## Commit Message Quality

These rules apply whenever creating or updating commit messages, PR titles, or PR bodies:

- Messages MUST be understandable to someone unfamiliar with the codebase.
- Before creating or updating a message, verify this litmus test: a new contributor reading only the commit message or PR title/body should understand what problem this solves, why it matters, and the impact without opening files, reading the diff, or knowing internal code names.
- Avoid code identifiers, filenames, function names, and implementation details unless they are necessary for understanding the user-facing impact.
- Bad: "Add NameFromHex with sync.Once lazy init"
- Good: "Improve color name lookup performance while keeping startup fast"

## Commit Messages

Commit messages are for future readers scanning history. Before committing:

- Follow the commit message quality rules above.
- Draft a concise 1-2 sentence message focusing on why the change exists and what outcome it enables, not a list of files or implementation details.
- Use clear, accurate verbs ("add"=new capability, "update"=enhancement, "fix"=bug fix) and avoid generic messages.
- The first line MUST be under 72 characters.
- Add a body only when it is needed to explain the reasoning, tradeoffs, or important context; wrap body lines at 72 characters.
- If the change is internal-only, still describe the benefit or maintenance outcome rather than naming private code.
- Bad: "fix: nil pointer in session.go"
- Good: "fix: prevent session loading from crashing on missing metadata"
- Bad: "refactor: move PromptBuilder into internal/agent"
- Good: "refactor: make prompt assembly easier to maintain"

## Creating a Commit

1. Single message with three tool_use blocks (IMPORTANT for speed):
   - git status (untracked files)
   - git diff (staged/unstaged changes)
   - git log (recent commit message style)
2. Add relevant untracked files to staging. Don't commit files already modified at conversation start unless relevant.
3. Analyze staged changes in <commit_analysis> tags:
   - List changed/added files, summarize nature (feature/enhancement/bug fix/refactoring/test/docs)
   - Brainstorm purpose/motivation, assess project impact, check for sensitive info
   - Don't use tools beyond the context of git
4. Draft a commit message following the rules above; review the draft against the litmus test before committing.
5. Create the commit using HEREDOC, appending the attribution trailer shown in the bash tool description (omit attribution if none is configured):
   git commit -m "$(cat <<'EOF'
   Commit message here.
   EOF
   )"
6. If a pre-commit hook fails, retry ONCE. If it fails again, the hook is preventing the commit. If it succeeds but files were modified, MUST amend.
7. Run git status to verify.

Notes: Use "git commit -am" when possible, don't stage unrelated files, NEVER update config, don't push, no -i flags, no empty commits, return empty response, when rebasing always use -m.

## Creating a Pull Request

When the user asks to create or update a PR:

1. Single message with multiple tool_use blocks (VERY IMPORTANT for speed):
   - git status (untracked files)
   - git diff (staged/unstaged changes)
   - Check if branch tracks remote and is up to date
   - git log and 'git diff main...HEAD' (full commit history from main divergence)
2. Create new branch if needed
3. Commit changes if needed
4. Push to remote with -u flag if needed.
5. Analyze changes in <pr_analysis> tags:
   - List commits since diverging from main
   - Summarize nature of changes
   - Brainstorm purpose/motivation
   - Assess project impact
   - Don't use tools beyond git context
   - Check for sensitive information
6. Draft a PR message:
   - Follow the commit message quality rules
   - Draft concise (1-2 bullet points) PR summary focusing on "why"
   - Ensure the summary reflects ALL changes since main divergence
   - Use clear, concise language and avoid generic summaries
   - Review the draft against the litmus test before creating or updating the PR
7. Create the PR with gh pr create using HEREDOC, including the configured attribution if any:
   gh pr create --title "title" --body "$(cat <<'EOF'
   <summary>
   EOF
   )"

Important: return an empty response after creating the PR (the user sees gh output). Never update git config.
