---
name: git-commit-message
description: How to write git commit messages in this repository. Use whenever creating a commit, amending a commit message, or drafting a squash/merge message.
---

# Git commit messages

This file is the single place that controls how commit messages are written
here. Edit the rules below; Claude follows them whenever it commits.

## Before writing

1. Run `git diff --staged` (or `git diff` if nothing is staged) and read it.
   Describe what the diff actually does, not what the conversation said it
   would do.
2. Run `git log --oneline -20` and match the style already in use. If the
   history conflicts with the rules below, the rules below win.
3. If the staged changes cover more than one unrelated concern, say so and
   suggest splitting the commit instead of writing a vague message.

## Subject line

- Format: `<type>(<scope>): <summary>`
- Imperative mood: "add retry to statement polling", not "added" or "adds".
- At most 50 characters where possible, 72 as a hard limit.
- Lowercase after the colon, no trailing full stop.

### Types

| type       | use for                                              |
|------------|------------------------------------------------------|
| `feat`     | new user-visible behaviour                           |
| `fix`      | a bug fix                                            |
| `refactor` | restructuring with no behaviour change               |
| `test`     | adding or changing tests only                        |
| `docs`     | README, CLAUDE.md, comments                          |
| `build`    | go.mod, build scripts, generated parser              |
| `chore`    | anything else that does not touch behaviour          |

### Scopes

Use the package that changed. Leave the scope out if the change spans several.

`flinksql`, `testspec`, `config`, `ccloud`, `engine`, `runner`, `cli`
(`cli` is `main.go`), `parser`

## Body

- Optional for a trivial change; otherwise include one.
- Leave a blank line after the subject, and wrap at 72 columns.
- Explain **why** the change was needed and any non-obvious decision. The diff
  already shows what changed.
- Mention behaviour a reviewer might not expect, such as a changed default or a
  new error.
- feat commits should summarise the feature
- Add security, design, reliability and performance related concerns as sepearte lines. Prefix them with `sec`, `design`, `rel` and `perf` respectively. 

## Trailers

- Reference issues as `Refs: #123` or `Fixes: #123`, only when one exists.
- Mark a breaking change with `BREAKING CHANGE: <what breaks>`.
- Keep the `Co-Authored-By` attribution trailer that Claude Code adds. Delete
  this line to say it should be left off.


## Do not

- Do not use vague summaries such as "fix stuff", "updates" or "wip".
- Do not put secrets, run ids, account ids or environment ids in a message.
- Do not rewrite or amend commits that have already been pushed unless asked.
- Do not commit unless asked to.

## Example

```
fix(runner): drop tables after pipelines are deleted

Dropping a table that a running INSERT still wrote to failed with a
generic "in use" error, which left tables behind for `ccflink clean`.
Delete the pipelines first and wait for them to stop.
```
