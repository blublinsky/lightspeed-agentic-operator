# Run lifecycle (state machine)

Behavioral specification for the `AgenticRun` resource lifecycle. **Approval gates, sandbox calls, and RBAC** are defined in `approval.md` and `sandbox-execution.md`. **Field semantics** are in `crd-api.md`.

## Behavioral Rules

1. **Source of truth**: `status.conditions` (Kubernetes conditions keyed by `type`) is authoritative. The **phase** is a derived display value only; it is not persisted as its own field.
2. **Phases**: The system MUST derive exactly one phase label from `status.conditions` using the algorithm in rule 9 (and precedence rules 10–11). Valid labels: `Pending`, `Analyzing`, `Proposed`, `Executing`, `Verifying`, `Completed`, `Failed`, `Denied`, `Escalating`, `Escalated`, `EmergencyStopped`, `NoActionRequired`.
3. **Condition types (run-level)**: The workflow uses `Analyzed`, `Executed`, `Verified`, `Denied`, `Escalated`, `EmergencyStopped` (string values as defined on the API). Status values are `True`, `False`, or `Unknown`.
4. **Terminal phases**: `Completed`, `Denied`, `Escalated`, `Failed`, `EmergencyStopped`, and `NoActionRequired` are terminal for reconciliation progression. After `Completed`, `Denied`, `Escalated`, `EmergencyStopped`, or `NoActionRequired`, the controller MUST stop active work and MAY release sandbox claims when present. `Failed` triggers failure cleanup behaviors (see `sandbox-execution.md` for RBAC cleanup interactions). `EmergencyStopped` indicates the run was terminated by the system kill switch (see `system-config.md`). `NoActionRequired` indicates the analysis agent determined no remediation is needed (see rule 9).
5. **Workflow shape**: `spec.analysis` is always required. `spec.execution` and `spec.verification` MAY be omitted; omission skips those steps subject to rules 20–22.
6. **Revision loop**: If `spec.revisionFeedback` is non-empty AND `metadata.generation` is greater than `Analyzed.observedGeneration`, the system MUST treat the run as needing **re-analysis** before continuing downstream steps. Re-analysis MUST append revision context to the user-visible request text (after `spec.request`), then reset execution/verification/escalation progress as implemented for revision handling, and MUST NOT advance execution until the new analysis completes. Revision feedback is supported from the `NoActionRequired` terminal phase — patching `spec.revisionFeedback` resets conditions and re-runs analysis. **Exception**: When the controller internally writes `spec.ttlAfterTerminal` (rule 23) on a terminal run, it bumps `metadata.generation`; the controller MUST simultaneously advance `Analyzed.observedGeneration` to match so this operator-internal mutation is never misread by the revision detector as a new revision request. This ensures terminal runs (advisory-only `Completed`, execution-less `Failed`, and `NoActionRequired`) do not re-enter analysis when only TTL stamping causes a generation bump.
7. **Verification failure → escalation**: When `spec.verification` is present, after a successful execution the verification step MAY fail **objectively** if the agent reports failure **or** any verification check records a non-pass outcome (even when a coarse success flag might otherwise read true). On verification failure, the system MUST NOT retry execution. The system MUST set `Verified` to `False` with reason `VerificationFailed` and MUST set `Escalated` to `Unknown`, entering the escalating phase. The escalation summary includes the execution result and failed verification result so a human operator can assess what happened.
8. **No execution retries**: The operator does not re-execute remediation after verification failure. Convergence-dependent checks (alerts, metrics, pod readiness) are handled within the verification agent's single sandbox call via prompt-guided wait-and-retry. This avoids the risk of re-executing non-idempotent remediations against a cluster in an unknown intermediate state.
9. **DerivePhase — precedence (first match in order)**:
   - If `EmergencyStopped` exists with status `True` → phase `EmergencyStopped`.
   - Else if `Escalated` exists with status `True` → phase `Escalated`.
   - Else if `Denied` exists with status `True` → phase `Denied`.
   - Else if `Escalated` exists → if status is `Unknown` → phase `Escalating`; otherwise → phase `Failed`.
   - Else evaluate `Verified` if present:
     - If `Verified` is `True` → phase `Completed`.
     - If `Verified` is `Unknown` → phase `Verifying`.
     - If `Verified` is `False` → phase `Failed` (unless `Escalated` is set, which takes precedence per rule ordering).
   - Else evaluate `Executed` if present:
     - If `Executed` is `True` → phase `Verifying`.
     - If `Executed` is `Unknown` → phase `Executing`.
     - If `Executed` is `False` → phase `Failed`.
   - Else evaluate `Analyzed` if present:
     - If `Analyzed` is `True` AND reason is `NoActionRequired` → phase `NoActionRequired`.
     - If `Analyzed` is `True` → phase `Proposed`.
     - If `Analyzed` is `Unknown` → phase `Analyzing`.
     - If `Analyzed` is `False` → phase `Failed`.
   - Else → phase `Pending`.
10. **EmergencyStopped vs other terminals in derivation**: `EmergencyStopped=True` MUST win over all other conditions because derivation checks it first. `Escalated=True` MUST win over `Denied=True` if both are present because derivation checks complete escalation before denial. Otherwise `Denied=True` MUST win over non-terminal progress (`Analyzed`, `Executed`, `Verified` combinations).
11. **Advisory completion**: If execution is absent and verification is absent, after successful analysis the controller MAY set `Executed` and `Verified` to `True` with skip reasons such that the derived phase is `Completed`.
12. **Trust mode completion**: If execution is present and verification is absent, after successful execution the controller MUST set `Verified` to `True` with a skip reason such that the derived phase is `Completed`.
13. **Skipped steps**: `Executed=True` with skip reason and `Verified=True` with skip reason together MUST derive `Completed` when that is the intended advisory outcome per tests and valid condition combinations.
14. **Step phases (display vocabulary)**: The API defines per-step display phases `PendingApproval`, `Running`, `Completed`, `Failed`, `Skipped` (see `crd-api.md`). A conforming implementation SHOULD map: `Running` ↔ corresponding run-level step condition `Unknown` with in-progress reason; `Completed` ↔ `True` with complete/passed/skipped reason as applicable; `Failed` ↔ `False`; `Skipped` ↔ `True` with skipped reason on execution/verification where applicable; `PendingApproval` ↔ step not yet active while run phase waits on approval for that step (see `approval.md`).
14a. **[OLS-3066] Step-level conditions**: The controller MUST populate `status.steps.<step>.conditions` for each step. These conditions serve both observability (console/CLI can show step progress) and re-entry logic (controller uses them to determine what to do next in the async reconcile pattern — see `sandbox-execution.md` rules 43–43e). Condition reasons:

| Status | Reason | Meaning |
|---|---|---|
| `Unknown` | `WaitingForSandbox` | Pod/SandboxClaim created, waiting for pod to start |
| `Unknown` | `Running` | Pod is running, agent is working |
| `True` | `Succeeded` | Result CR exists with `success: true` |
| `False` | `AgentFailed` | Result CR exists with `success: false` |
| `False` | `SandboxTimeout` | Per-step timeout fired, pod killed (see `sandbox-execution.md` rule 40) |
| `False` | `SandboxFailed` | Pod exited without creating Result CR |
| `False` | `ImagePullFailed` | Pod stuck in ImagePullBackOff |

15. **Success**: `Verified=True` MUST yield `Completed` once rule 9 reaches the `Verified` branch, unless an earlier branch already returned `Escalated` or `Denied` per rules 9–10.
16. **Step failure**: Any of `Analyzed`, `Executed`, or `Verified` with status `False` MUST yield `Failed` when reached by the derivation order in rule 9 (unless superseded by `Escalated` / `Denied` per rules 9–10).
16a. **[OLS-3666] Failure condition message**: When the controller sets a step condition to `False` (reason `Failed`) because the agent returned `success: false`, the condition `message` MUST include context from the agent response rather than a generic string. The controller MUST use the first available source from this fallback chain: (1) the sandbox response `summary` field (which contains the error message for sandbox-level failures or the raw agent output when the output schema has no top-level `summary` property); (2) for analysis: the top-level or per-option `diagnosis.summary`; for execution: the first failed action's `description` and `error`; (3) a properly-cased generic fallback (`"Analysis agent reported failure"` / `"Execution agent reported failure"`). The message MUST use sentence casing.
17. **Escalation failure**: `Escalated` with status `False` MUST yield `Failed` once rule 9 evaluates the `Escalated` presence branch (non-`True`, non-`Unknown`).
18. **Result CR linkage**: Each analysis/execution/verification/escalation attempt SHOULD append a `status.steps.*.results[]` entry naming the corresponding result resource with an outcome matching agent success/failure for that attempt. **Exception:** when the execution agent reports `success=false` but all mutating actions succeeded (only observation actions failed), the controller MUST override the outcome to `Succeeded` and proceed to the verification step. Observation action types (`pre-check`, `post-check`, `verification`, `check`, `wait`) are not considered when determining mutation success.
19. **Observed generation**: Conditions SHOULD carry `observedGeneration` aligned with `metadata.generation` when the controller updates them for the current spec generation, except revision completion MAY pin the analyzed condition to the generation that triggered the revision, per existing reconciliation behavior.
20. **Immutable spec (excluding revision and TTL)**: Once set, `spec.request`, `spec.targetNamespaces`, `spec.analysisOutput`, `spec.tools`, `spec.analysis`, `spec.execution`, and `spec.verification` MUST NOT change; CEL on the CRD enforces this. `spec.revisionFeedback` (iterative feedback) and `spec.ttlAfterTerminal` (terminal-run TTL override, rule 23) are the two mutable spec fields; see rule 24 for how the controller keeps its own `ttlAfterTerminal` writes from being misread as a revision request.
21. **Option trim after analysis**: When multiple remediation options exist, execution MUST use the option selected through the approval resource; non-selected options MAY be removed from the stored analysis result before execution (see `approval.md`).
22. **Selected option for verification**: Verification MUST use the same selected remediation option as execution (latest trimmed analysis result).
23. **[OLS-3566] Terminal-run TTL / auto-deletion**: On every reconcile of a run in a terminal phase (rule 4), the controller MUST call a `handleTerminalTTL` step, integrated into all terminal branches (`Completed`, `Failed`, `Denied`, `Escalated`, `EmergencyStopped`, `NoActionRequired`):
   - If `status.terminalTime` is unset, stamp it to the current time (set once per terminal episode, not updated again while still terminal — see `crd-api.md` rule 6b). When a terminal run re-enters revision (rule 6), `handleRevision` MUST clear `status.terminalTime` so a later terminal phase gets a fresh timestamp rather than computing TTL expiry off the earlier terminal event.
   - If `spec.ttlAfterTerminal` is unset, stamp it from `AgenticOLSConfig.spec.lifecycle.terminalTTL` (rule 48 in `crd-api.md`) when that cluster-wide default exists; a pre-set `spec.ttlAfterTerminal` (by adapter or admin) MUST NOT be overwritten. When no cluster config exists, or the config has no `lifecycle.terminalTTL`, no default is stamped, and the run's `spec.ttlAfterTerminal` stays unset unless it was already pre-set. This absence of a cluster default only controls whether a *default* gets stamped — it does NOT suppress deletion of a run whose `spec.ttlAfterTerminal` was already pre-set independently of the cluster config; any non-zero pre-set value is still honored per the next two bullets regardless of `AgenticOLSConfig`'s presence.
   - If `spec.ttlAfterTerminal` is unset, or explicitly `0`, the controller MUST NOT delete the run.
   - Otherwise, once `status.terminalTime + spec.ttlAfterTerminal` has elapsed, the controller MUST delete the `AgenticRun` CR (Kubernetes garbage collection cascades to owned result CRs and sandbox resources via owner references per `crd-api.md` rule 40); before elapsing, the controller MUST requeue with `RequeueAfter` set to the remaining duration.
   - The `AgenticOLSConfig`/`ApprovalPolicy`/watched-`ConfigMap` fan-out (see `system-config.md`) MUST re-enqueue terminal runs still missing `status.terminalTime` (this stamping is unconditional). It MUST re-enqueue terminal runs missing `spec.ttlAfterTerminal` only when an effective cluster-wide `terminalTTL` is currently configured, so a cluster TTL added after runs have already gone terminal is retroactively applied — and MUST NOT do so when no cluster default exists, since there would be nothing to stamp and re-enqueuing every such run on every config-adjacent change would be pure churn.
24. **[OLS-3566] Generation sync on internal TTL stamp**: Stamping `spec.ttlAfterTerminal` (rule 23) is a spec write and therefore advances `metadata.generation`, the same signal the revision loop (rule 6) uses to detect user-initiated feedback. Because `spec.revisionFeedback` is never cleared once processed, the controller MUST advance `Analyzed.observedGeneration` to match the post-stamp `metadata.generation` in the same operation that stamps `ttlAfterTerminal`, so this internal, operator-driven mutation is never misread by the revision loop as a new revision request. This generation sync runs unconditionally (outside the `TTLAfterTerminal == nil` conditional) so that a crash between the spec patch and the status patch is self-healing on the next reconcile. External `spec.ttlAfterTerminal` patches (by adapters or admins) also advance `metadata.generation`; the same idempotent sync handles these without triggering spurious revision re-entry.

## Configuration Surface

- `spec.request`
- `spec.revisionFeedback`
- `spec.ttlAfterTerminal`
- `spec.targetNamespaces`
- `spec.analysisOutput` / `spec.analysisOutput.mode` / `spec.analysisOutput.schema`
- `spec.tools` and per-step `spec.analysis.tools`, `spec.execution.tools`, `spec.verification.tools`
- `spec.analysis`, `spec.execution`, `spec.verification`
- `metadata.generation` (revision detection vs `status.conditions`)
- `status.conditions[*].type`, `status.conditions[*].status`, `status.conditions[*].reason`, `status.conditions[*].observedGeneration`
- `status.steps.*.results`, `status.steps.*.sandbox`
- `status.terminalTime`

## Constraints

- Derivation MUST be a pure function of `status.conditions` for phase display (same conditions → same phase).
- Downstream steps MUST NOT run before approval and precondition rules in `approval.md` are satisfied.
- Execution runs exactly once per analysis iteration. Verification failure escalates, never re-executes.

## Planned Changes

- ~~[PLANNED: OLS-2913]~~ [PLANNED: OLS-3066] Populate `status.steps.<step>.conditions` for observability and async re-entry. See rule 14a. Subsumes the original OLS-2913 step-conditions intent.
- [PLANNED: OLS-2894] **Per-run approval overrides** (e.g. annotations) and **namespace-scoped approval policy** if product requires policy resolution beyond cluster singleton `ApprovalPolicy` named `cluster` (current code: cluster singleton only; see `approval.md`).
- [DONE: OLS-3018] `EmergencyStopped` phase and condition type added to run lifecycle. See `system-config.md` for full kill switch specification.
- [DONE: OLS-3268] `NoActionRequired` terminal phase: when analysis returns `actionRequired=false`, the operator sets `Analyzed=True` with reason `NoActionRequired` and the run auto-completes, bypassing approval/execution/verification.
- [DONE: OLS-3295] Renamed `Proposal` CRD kind to `AgenticRun`, `ProposalApproval` to `AgenticRunApproval`, and updated all associated API surface (labels, RBAC resources, CLI commands, audit events, OTEL spans).
- [DONE: OLS-3558] Execution outcome override — controller no longer hard-fails when `success=false` but all mutating actions succeeded; defers outcome to the verification step. See `sandbox-execution.md` rule 21b.
- [DONE: OLS-3566] Terminal-run TTL / auto-deletion (Kubernetes Jobs `ttlSecondsAfterFinished` pattern): `AgenticOLSConfig.spec.lifecycle.terminalTTL` cluster-wide default plus `AgenticRun.spec.ttlAfterTerminal` per-run override, stamped by `handleTerminalTTL` on terminal runs. See rules 23–24 and `crd-api.md` rules 6a, 6b, 48.
