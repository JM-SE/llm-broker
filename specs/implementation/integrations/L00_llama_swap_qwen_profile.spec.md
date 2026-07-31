# Integration Phase L00: Sanitized llama-swap And Qwen Profile

## Status

- Phase: L00.
- State: `pending`.
- Prerequisite: accepted P09.
- Runtime impact: none; repository assets only.

## Objective

Prepare a fully sanitized, dormant integration profile for one Qwen Instruct
model behind llama-swap without installing, starting, stopping, or reconfiguring
any local service.

## In Scope

- Documented mapping from generic broker interfaces to llama-swap behavior.
- Parameterized llama-swap config template.
- Parameterized broker config/profile and systemd/Compose notes.
- Optional llama-swap runtime-controller design/tests against fixtures.
- Qwen Instruct workload/capability profile and limits.
- Local private-value inventory template kept out of Git.
- Compatibility and rollback prerequisites for later phases.

## Out Of Scope

- Downloading/building/installing llama-swap, models, or Go tools on the runtime
  host.
- Modifying systemd, listeners, firewall, database, or current llama-server.
- Starting the broker against a real backend.
- Editing or deploying YTPipe.
- Real inference.
- Committing personal paths, actual ports, tokens, database names, or service
  identities.

## Deliverables

- `deploy/examples/llama-swap-qwen/` sanitized templates and README.
- `docs/integrations/llama-swap.md` compatibility/recovery documentation.
- Optional compiled llama-swap runtime controller plus fixture tests if its API
  is stable and sufficient; otherwise an explicit `none`-controller decision.
- Local-values checklist naming required private inputs without values.
- L02 rehearsal and restoration checklist.

## Sanitization Requirements

- Use placeholders such as `<MODEL_PATH>`, `<BROKER_API_ADDR>`,
  `<LLAMA_SWAP_URL>`, `<DATABASE_URL>`, and `<BACKEND_API_KEY>`.
- No personal home path, live occupied port, private hostname, token, database
  credential, client credential, or production payload appears in Git.
- The exact private mapping is stored only in a Git-ignored local file or host
  secret/config manager.
- Examples are safe but not assumed to match the target host unchanged.

## llama-swap Requirements

- Bind its listener to loopback/private interface selected by operator.
- Use a broker-only API key; clients never receive it.
- Set `concurrencyLimit: 1`.
- Set `captureBuffer: 0` to disable in-memory request/response capture.
- Set `sendLoadingState: false`.
- Configure one-model-resident behavior; do not use a concurrency `matrix`.
- Keep model process command/path entirely operator-side.
- Document shutdown, idleness, model load, and error semantics actually supported
  by the tested llama-swap version.

## Qwen Profile Requirements

- Capability is a logical operator-defined name such as `instruct`.
- Client still cannot submit `Qwen`, model file, alias, or backend URL.
- The concrete model alias is mapped in operator config only.
- Initial profile assumes 16k context, one active request, and conservative output
  defaults/ranges compatible with the selected Instruct model.
- Prompt construction and domain validation remain client-owned.
- Thinking/reasoning model routing is excluded from this phase.

## Procedure

1. Pin and review a tested llama-swap release and its config/status APIs.
2. Map its OpenAI-compatible behavior to P06 conformance expectations.
3. Decide whether a controller can prove idle/fence safely; do not infer this from
   HTTP disconnect alone.
4. Add sanitized templates and exact private-value checklist.
5. Add fixture/contract tests for any controller code without contacting runtime.
6. Prepare L02 preflight, synthetic requests, failure tests, and restoration plan.
7. Search the complete repository for private/local values.
8. Confirm no file has been installed or service touched.

## Automated Acceptance

- Templates parse after substituting documented fake values.
- Any controller passes fixture tests for active, idle, unavailable, malformed,
  and ambiguous states.
- OpenAI-compatible profile validates through `llm-broker config validate`.
- Repository secret/private-path/live-value scans pass.
- Product tests remain independent of profile assets.
- Full Go checks and `git diff --check` pass.

## Manual Acceptance

- Reviewer confirms all files are public-safe and dormant.
- Reviewer confirms llama-swap capture/concurrency/privacy settings.
- Reviewer confirms a complete restoration checklist exists before L02.
- Current runtime, model service, database, systemd, and YTPipe are unchanged.

## Stop Conditions

- Required llama-swap behavior is undocumented or cannot be tested safely.
- Controller claims idle without authoritative evidence.
- A template requires a private value to be committed.
- Any command would modify/start/stop a live service.
- Qwen-specific fields leak into product API/schema.

## Handoff

Include tested version/docs, compatibility matrix, controller decision, template
validation, sanitization scan, explicit no-runtime-change evidence, Git status,
and a suggested commit message. Stop for approval before L01.
