# Changelog

## Unreleased

### Added

- Permission modes: `--permissions auto|ask|all|read-only`, `GILA_PERMISSIONS`, or `/permissions` in the REPL. The REPL asks inline, `-p` asks on the terminal, and `--json` refuses what would ask. A refused or declined call goes back to the model with the reason. No mode confines `bash`; only a kernel sandbox could.

- `~/.config/gila/settings.toml`. Under `[permissions]`, `mode` sets the default mode, below `--permissions` and `GILA_PERMISSIONS`. `secrets` and `protected` add paths that need approval. `commands` and `hosts` allowlist `bash` commands and network hosts. An unknown key or a malformed entry stops gila, so a typo never drops a protection silently.

  ```toml
  [permissions]
  mode = "ask"
  secrets = ["*.pem", "!public.pem"]
  protected = ["migrations"]
  commands = ["go test", "git status$"]
  hosts = ["pkg.go.dev", "*.githubusercontent.com"]
  ```

- Built-in secret and protected paths, which nothing can lift. Secrets ask before any read or write: dotenv files, SSH private keys, key and key-store files, credential files such as `.netrc`, and Terraform state. Protected paths ask before a write: `.git`, `.hg`, `.svn` and `.jj`. Only names that nearly always mean credentials are built in, since a false positive could never be switched off.

- Path patterns follow `.gitignore`: a slash anchors to the working directory and covers what is under it, and `!` exempts an earlier pattern. The built-in rules, the settings file and an embedding app's rules are separate layers, and `!` exempts only within its own layer. A merged list would have let an app's `!*.pem` undo the user's `*.pem`.

- `commands` allowlists `bash` in `ask` mode by word prefix: `go test` allows `go test ./...`. An entry ending in `$` must match the whole command. A command that chains, substitutes, redirects or expands a variable never matches. `auto` does not use the list: there it would turn every other command into a prompt, and into a refusal under `--json`.

- Tools declare their effect, and permissions follow the declaration rather than the tool's name. `tool.ReadOnly` marks a tool that only reads local files. `tool.Paths` names the files a call touches. `tool.Hosts` names the hosts a call contacts. A tool that declares nothing is treated as modifying anything. Deciding by name would have let a custom tool called `read` inherit that built-in's policy.

- Network tools, declared by `tool.Hosts`. A call to listed hosts that writes no files runs in every mode; an unlisted host asks, or is refused in `read-only`. A network tool is never read-only, since a fetch can carry out what the model has read. `bash` is not checked against the list.

- `app.Options.Tools` adds custom tools. `tool.New` builds one from functions and declares only what its `Def` sets. Names must be unique and valid for every provider. `tool.HostOf` and `tool.Env.Abs` help implementations resolve hosts and paths as gila does.

- `-P compat` talks to any OpenAI-compatible Chat Completions server, such as LM Studio or vLLM, at `--base-url` or `COMPAT_BASE_URL`, with an optional `COMPAT_API_KEY`. A named provider was chosen over accepting `--base-url` alone, which would have to guess the wire format.

- Retries show. The provider SDKs retry a failed request on their own and say nothing: Anthropic and OpenAI up to 4 times, the local providers twice, OpenRouter for up to a minute. The REPL's status bar and a `[retry]` line, `-p`'s stderr and a `retry` record in `--json` now report each retry and why the previous attempt failed. One HTTP transport counts the attempts each request makes, which covers all four adapters without hooking each SDK.

- `GILA_LOG=FILE` appends each request's endpoint and status and the raw response stream, for diagnosing a provider. Request bodies and headers, keys among them, are never written.

- For embedding apps: `agent.Config.Approve` is asked before each call, and `permission.Approver` builds one from a mode. `agent.Record` maps events to JSON-ready records. `app.Options.Keys` takes vendor keys ahead of the environment, which a GUI app does not inherit. `StateDir`, `CacheDir` and `ConfigDir` keep an app's state apart from the CLI's.

### Fixed

- A response cut off mid-stream ended the prompt with "stream ended without a finish reason", even with the timeout below fixed. gila now sends the round-trip again, up to twice in a row, shown as a `[retry]` line; the cut response never enters the history. The SDKs retry a request that fails, not a response that stops partway. The error also names the read error that cut the stream, which the OpenRouter SDK's reader discards.

- OpenRouter streams longer than 60 seconds were cut off, and a prompt with a large context could time out before its first token and retry silently. The SDK's default HTTP client has a 60-second limit on the whole request, and its event reader drops the read error, so a cut stream reported only "stream ended without a finish reason". gila now gives the SDK a client that bounds connecting and waiting for headers but not the stream.

- A model the provider does not list is refused, at startup and on `/model`, with a hint. `-m deepseek/deepseek-v4.1-flash` with `openai` suggests `openrouter:deepseek/deepseek-v4.1-flash`; before, it was accepted and failed on the first request. OpenAI's model list no longer offers models that cannot chat, such as `babbage-002` or `tts-1`.

- A tool-call chunk from OpenRouter with a negative index crashed gila; it is now ignored.

- `-p -` with empty input and `--json` exited 1; it exits 2, like every other usage error.

- A custom tool whose schema was decoded from JSON lost its `required` list, because `ToolSpec.Required` accepted only `[]string`.

- A llama.cpp model id, which is a file path, got the hint to use `openrouter:`. The hint now applies only to cloud providers.

- A cost estimate uses the highest long-prompt tier the prompt passes. It took the last matching tier in list order, so a price list with tiers out of order priced a long prompt at a lower tier's rate.

### Changed

- By default gila asks before a write outside the working directory, under version-control metadata or to a secret, and before reading a secret. 0.1.0 ran every call. With no one to ask, as under `--json`, such a call is refused. `--permissions all` restores the old behaviour.

- `--json` `tool_call` and `tool_result` records carry a `label` field.

- Tool lines in the REPL and on `-p`'s stderr start with `[tool] `, as in myra, so they stand apart from the answer when read without colour. They stay one line, cut at the label so the outcome remains.

- `--help` wraps at 80 columns, or the terminal width if narrower, and names flag values (`--model ID`) in place of their Go types.

- The module path is `github.com/shakfu/gila`.

## 0.1.0

### Added

- A coding agent and Go library with four tools: `read`, `write`, `edit` and `bash`. Providers `anthropic`, `openai` and `openrouter` go through their vendors' SDKs. `llamacpp` and `ollama` go through openai-go's Chat Completions.

- History is neutral, but each assistant message keeps the provider's own payload and replays it to the model that produced it. Thinking signatures, encrypted reasoning and `reasoning_details` survive the tool calls of a turn, and a mid-session switch still works. See `docs/dev/design.md`.

- A REPL on Bubble Tea v2, inline so output stays in scrollback. It streams markdown, shows one line per tool call, and pins an input box and a status bar with model, context used and session cost. `/model` and `/provider` open a filterable picker.

- Headless `-p`, and `--json` for one record per line, ending in a `result` record.

- Cost: reported by OpenRouter; estimated for OpenAI and Anthropic from OpenRouter's public price list, with cache rates and long-prompt tiers. Prompt caching on every provider that supports it.

- `AGENTS.md` from the config directory and from the repository root down to the working directory, and skills from the config directory.
