# Dependencies considered

Libraries evaluated for gilda and the decision on each. Written 2026-09-23. None is adopted.

## Method

Each library was added to a copy of `go.mod` with one blank import, then `go mod tidy`. "Modules added" counts modules in `go list -m all` that gilda does not already list. gilda listed 111. `go list -m all` includes test-only modules, so the counts are upper bounds on what the binary compiles in. Binary size was not measured.

| Library | Latest release | Modules added | Decision |
|-|-|-|-|
| `google.golang.org/genai` | v1.71.0, 2026-08-31 | 35 | no; use OpenRouter |
| `github.com/tmc/langchaingo` | v0.1.14, 2025-10-20; last push 2026-01-11 | 236 | no |
| `github.com/moby/moby/client` | v0.6.0 (`docker-v29.8.1`, 2026-09-15) | 30 | no |

## google.golang.org/genai

Gemini's native SDK. It speaks Google's own API, through the Gemini Developer API or Vertex AI, so it cannot be used with OpenRouter. Not needed: OpenRouter serves Gemini models, and that is the supported route.

- **OpenRouter.** OpenRouter documents a `google-gemini-v1` format for `reasoning_details` and recommends passing reasoning back during tool use ([reasoning tokens](https://openrouter.ai/docs/use-cases/reasoning-tokens)). The adapter already replays `reasoning_details`: `mergeReasoning` in `llm/openrouter/openrouter.go` joins the text and summary fragments it knows and passes any other entry through unchanged. Not yet run against a Gemini model.

- **`-P compat` against Gemini's own OpenAI-compatible endpoint.** Only for direct billing. The endpoint supports thought signatures ([openai.md](https://ai.google.dev/gemini-api/docs/openai)), but `compat` replays nothing provider-specific. Gemini says signatures are "required to maintain reasoning continuity across multi-turn interactions" ([thinking guide](https://ai.google.dev/gemini-api/docs/thinking#signatures)). So compat probably loses reasoning between tool calls. Whether Gemini then rejects the request or reasons worse is untested.

A native adapter would add direct billing without OpenRouter's fee, Gemini's context caching, and exact signature and safety-stop handling. It costs 35 modules, mostly `cloud.google.com/go/...`, and a fourth adapter with its own stop-reason mapping and tests.

Revisit only if Gemini must be billed directly rather than through OpenRouter. Then first try `-P compat`; if signatures break it, replaying them in `compat` may be enough before taking on the SDK.

## github.com/tmc/langchaingo

No.

- No release for 11 months.

- 236 modules, more than twice gilda's total.

- Its common interface across providers hides what gilda depends on: `llm.Native` reasoning replay, prompt cache keys, and per-provider stop reasons such as refusals.

- Its chains and agents duplicate `agent.Run`.

## github.com/moby/moby/client

No. The `docker` CLI covers both uses:

- **Docker as a model tool.** The model runs `docker` through `bash`.

- **Docker as a `bash` sandbox.** `docs/dev/permissions.md` chooses Landlock and Seatbelt: no daemon, one extra `exec` per call. A container backend would isolate more, at the cost of a daemon, an image, bind mounts that mirror the root, and slower calls. It could still run `docker run` and `docker exec` through `os/exec`.

Revisit only if gilda manages container lifecycles in depth, such as attaching streams or watching events.
