Ask the user a structured question and wait for their response. Use this when you need clarification, confirmation, or a choice before proceeding.

Every question MUST include:
- `type`: `yes_no`, `single_choice`, `multi_choice`, or `free_text`
- `question`: a short, direct one-line question
- `description`: markdown context with details, tradeoffs, or examples (required, under 300 chars)

Hard limits (enforced; violations return an error and waste a round trip):
- Max 5 questions per batch; max 5 choices per question, each choice description under 100 chars
- `choices` required for `single_choice` and `multi_choice`; each choice has `id` (unique identifier) and `label` (display text)
- `yes_no` is only for propositions the user affirms or rejects; for A-vs-B choices use `single_choice` even with exactly two options
- `free_text` for open-ended answers; do not add an "Other"/"Custom" choice manually (single/multi choice questions get a free-text fill-in automatically)

Multiple questions render as a tabbed form with a confirmation screen:
- `label`: optional 3-word tab header (defaults to the first 3 words of `question`)
- `confirm_title` / `confirm_description`: shown on the final confirmation tab; describe what will happen based on the expected answers

When to use: confirm destructive or ambiguous actions, multiple valid interpretations of the request, gather several related answers at once.
When NOT to use: questions answerable by reading code or docs, information obtainable via other tools, asking permission (use the permission system).
