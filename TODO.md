# TODO

## Show the change when approving `write` and `edit`

Done: `edit` and `write`, on by default, off with `diff = false` in `settings.toml` (`tool.Previewer`, rendered by `tui.ApprovalLines`).

Remaining:

- `-p` asks on `/dev/tty` (`cmd/gila/headless.go`, `ttyAsk`) and shows neither the diff nor escaped control characters.
- Decide whether `diff` stays on by default after trying it.
- `askVia` computes the preview before the TUI checks "always allow", so a call allowed with `a` still reads its file once for a preview nobody sees.
- Refuse an edit or write when the file changed after the preview. `Run` recomputes the edit from the file at run time, so a change made while the user decides (an editor save, a background job) means the edit applied is not the diff approved. Keep a hash of `before` from the preview and have `Run` refuse on a mismatch. This needs the hash passed from approval to `Run`, which the `tool.Tool` interface does not carry today.

Constraints:

- A large diff fills the terminal history. Show a limited diff inline, say how many lines are hidden, and bind a key that opens the full diff in a pager. Hiding lines without a way to see them repeats the bug that `tui.ApprovalLines` fixed.
- No token cost. The diff is computed locally and never sent to the model.

Options:

- Send the full diff to an external viewer, set by the user as with git's `core.pager`:
  - [delta](https://github.com/dandavison/delta): a pager for git and diff output, with syntax highlighting.
  - [hunk](https://github.com/modem-dev/hunk): a terminal diff viewer for reviewing changes written by agents, with split and unified layouts and pager support.

## Tools from the environment

See `docs/dev/tools.md`. Next step: count tool calls per model with `scripts/tally` over `--json` runs before building `[[tools.command]]` declarations.

## Open findings from the self-review

- A response cut at `max_tokens` that has text but no calls counts as a success. `--json` reports `"outcome": "complete"` and the exit code is 0. Decide whether it is an error or a separate `"truncated"` outcome.
- `-p` in plain-text mode prints a partial answer, retries, and then prints the full answer after it on stdout. Decide whether to hold output until the attempt finishes, or to print a marker on stdout.
- The TUI's agent goroutine sends on the event channel with no way out. If the TUI exits while the buffer is full, the goroutine blocks forever. Every send must also watch a context that lives as long as the TUI, not the per-prompt one, because Esc cancels that one and `doneMsg` must still be delivered. This only matters when gila is embedded.
