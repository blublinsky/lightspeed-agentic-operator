# AgenticRun controller — architecture (how)

Audience: AI agents. Behavioral rules and phase semantics live in **what/** specs (e.g. `what/run-lifecycle.md`, `what/crd-api.md`, `what/approval.md`, `what/sandbox-execution.md`). This document maps **structure, call graph, and implementation mechanics** only.

---

## Entry point: `cmd/main.go`

- Parses flags: `metrics-bind-address`, `health-probe-bind-address`, `namespace` (falls back to `POD_NAMESPACE`).
- Builds controller-runtime `Manager` with core + `agenticv1alpha1` scheme.
- Creates `configuration.Cache` (starts nil). Eagerly attempts `configwatch.TryLoad` for the `lightspeed-agentic-configuration` ConfigMap. Registers `configwatch.Watcher` for runtime changes.
- Wires **dependency injection** directly (no `controller/setup.go`):
  - `agenticrun.NewSandboxManager(mgr.GetClient(), cfgCache, namespace)` → `SandboxLifecycle`.
  - `&agenticrun.SandboxAgentCaller{Sandbox, K8sClient, ClientFactory, Namespace, Audit}` → satisfies `agenticrun.AgentCaller`.
  - `agenticrun.AgenticRunReconciler{Client, Agent, Config, Namespace, Audit, TempLog}` → `SetupWithManager(mgr)`.
  - `agenticolsconfig.Reconciler` → `SetupWithManager(mgr)` — maintains `AgenticOLSConfig` `Suspended` condition.
- Ensures `lightspeed-agent` ServiceAccount unconditionally (idempotent create).
- Registers health/readiness probes and webhook.

---

## Module map: `controller/agenticrun/`

| File | Types / primary responsibilities | Key functions / methods |
|------|----------------------------------|-------------------------|
| `reconciler.go` | `AgenticRunReconciler` (embeds `client.Client`, `Agent AgentCaller`, `Log`) | `Reconcile`, `SetupWithManager` |
| `handlers.go` | (methods on `AgenticRunReconciler`) | `handleAnalysis`, `handleRevision`, `handleExecution`, `handleVerification`, `handleEscalation`, `handleFailed`, `denyAgenticRun`, `conditionTime`, `hasMutationSuccess`, `isObservationAction` |
| `helpers.go` | `revisionData`, `analysisQuery`, `executionQuery`, `verificationQuery`, `escalationData`; embedded templates via `//go:embed templates/*.tmpl` | `renderTemplate`, `failStep`, `statusPatch`, `hasSandboxClaims`, `isTerminal`, `setVerificationSkipped`, `getLatestAnalysisResult`, `selectedOption`, `trimNonSelectedOptions`, `resetExecutionAndVerification`, `maxAttempts`, `buildEscalationRequest`, `needsRevision`, `buildRevisionContext`, `buildAnalysisQuery`, `buildExecutionQuery`, `buildVerificationQuery`, `prettyJSON` |
| `approval.go` | — | `getApprovalPolicy`, `getAgenticRunApproval`, `ensureAgenticRunApproval`, `isStageApproved`, `isStageDenied`, `getStageOverrideAgent`, `getStageOption` |
| `resolve.go` | `resolvedStep`, `resolvedWorkflow` | `resolveAgenticRun`, `stepAgentName` |
| `agent.go` | `AgentCaller`, `StubAgentCaller`; `AnalysisOutput`, `ExecutionOutput`, `VerificationOutput`, `EscalationOutput` | Interface methods on `StubAgentCaller` |
| `sandbox_manager.go` | `SandboxManager` | `NewSandboxManager`, `Create`, `WaitReady`, `Release`, `createBarePod`, `createSandboxClaim`, `releaseBarePod`, `releaseSandboxClaim`, `waitPodReady`, `waitSandboxClaimReady`, `podSpecToUnstructured` |
| `sandbox_agent.go` | `SandboxLifecycle` interface; `SandboxAgentCaller`; private JSON DTOs for unmarshaling agent responses | `Analyze`, `Execute`, `Verify`, `Escalate`, `ReleaseSandboxes`, `callWithSandbox`, `patchSandboxInfo`, `buildAgentContext`, `collectFailedResults`, `stepString` |
| `podspec_builder.go` | `PodSpecBuilder`; label constants (`LabelManaged`, `LabelRun`, etc.); MCP env DTOs (`mcpServerEnvEntry`, `mcpHeaderEnvEntry`) | `Build`, `buildSkills`, `buildMCPServers`, `buildRequiredSecrets`, `addProviderSpecificEnv`, `credentialsSecretName`, `providerURL`, `providerTypeString` |
| `client.go` | `AgentHTTPClientInterface`, `AgentHTTPClient`; `agentRunRequest`, `agentContext`, `agentExecutionResult`, `agentPreviousAttempt`, `agentRunResponse` | `NewAgentHTTPClient`, `(*AgentHTTPClient).Run`, `executionOutputToAgentResult` |
| `schemas.go` | Package vars: default/minimal analysis schemas, execution/verification/escalation schemas; `defaultOutputSchemas`, `builtInPropertyJSON` | `init` (precompute property JSON), `injectBuiltInProperty`, `outputSchemaForStep` |
| `rbac.go` | `readerBindings atomic.Value` (cached CRB names) | `ensureExecutionRBAC`, `cleanupExecutionRBAC`, `resolveReaderBindings`, `addReaderSubject`, `removeReaderSubject`, `addSubjectToBinding`, `removeSubjectFromBinding`, `annotatedRBACNamespaces`, `deleteIfExists`, `rbacTargetNamespaces`, `truncateK8sName`, `executionRoleName`, `clusterRoleName`, `rbacLabels`, `rbacRulesToPolicyRules`, `normalizeCoreAPIGroup` |
| `results.go` | `statusHolder` interface (defined; no references elsewhere in this package) | `resultCRName`, `agenticRunOwnerRef`, `resultLabels`, `executionRetryIndex`, `resultConditions`, `createAnalysisResult`, `createExecutionResult`, `createVerificationResult`, `createEscalationResult`, `createIdempotent` |
| `templates/*.tmpl` | Text templates | Names: `analysis_query.tmpl`, `execution_query.tmpl`, `verification_query.tmpl`, `revision_context.tmpl`, `escalation_request.tmpl` |
| `reconciler_test.go` | `testAgentCaller`, fixtures | `testScheme`, `testDefaultAgent`, `testAgenticRun`, `reconcileOnce`, `getAgenticRun`, … |
| `state_machine_test.go` | Policy/combo tests | Helpers: `testManualPolicy`, `newManualReconciler`, `approveStage`, `denyStage`, `assertPhase`, … |
| `approval_test.go` | Tests for approval helpers | — |
| `client_test.go` | HTTP client tests | — |
| `handlers_test.go` | Handler-focused tests | — |
| `helpers_test.go` | Helper tests | — |
| `results_test.go` | Result CR tests | — |
| `resolve_test.go` | Resolution tests | — |
| `revision_test.go` | Revision flow tests | — |
| `rbac_test.go` | RBAC ensure/cleanup tests | — |
| `sandbox_manager_test.go` | SandboxManager Create/WaitReady/Release, name prefix routing, truncation | — |
| `sandbox_agent_test.go` | Agent caller tests | — |
| `schemas_test.go` | Output schema assembly tests | — |

---


## Module map: `controller/agenticolsconfig/`

| File | Types | Key functions |
|------|-------|----------------|
| `reconciler.go` | `Reconciler` (embeds `client.Client`, `EventRecorder`) | `Reconcile`, `SetupWithManager`, `handleActivation`, `handleDeactivation` |
| `reconciler_test.go` | — | Activation/deactivation, event emission, non-terminal run requeue |

**Integration note:** Registered in `cmd/main.go`. Watches the cluster `AgenticOLSConfig` named `cluster` and **Watches** `AgenticRun` objects to requeue the config when run phases change.

---

## Module map: `controller/console/`

| File | Types | Key functions |
|------|-------|----------------|
| `reconciler.go` | `AgenticConsoleConfig` (Image, Namespace); constants for plugin name, cert, nginx config string | `EnsureAgenticConsole` (orchestrates ordered ensures), `labels`, `ensureConfigMap`, `ensureServiceAccount`, `ensureService`, `ensureDeployment`, `ensureConsolePlugin`, `ensureConsoleActivation` |
| `reconciler_test.go` | — | Tests for idempotency, image updates, skip when no image |

**Integration note:** `EnsureAgenticConsole` is registered in `cmd/main.go` as a `manager.RunnableFunc` — it runs once at manager start, not as a reconcile loop. It mutates OpenShift `Console` cluster CR `spec.plugins` via retry-on-conflict.

---

## Data flow: reconcile loop

1. **Watch / enqueue:** controller-runtime delivers `ctrl.Request` for a `AgenticRun` namespaced name. `SetupWithManager` also `Owns` child CRs (`AgenticRunApproval`, `AnalysisResult`, `ExecutionResult`, `VerificationResult`, `EscalationResult`) and **Watches** cluster `ApprovalPolicy` and `AgenticOLSConfig` to enqueue all non-terminal runs when either changes.
2. **`Reconcile` load:** `Get` `AgenticRun`; ignore not-found.
3. **Deletion path:** If `DeletionTimestamp` set and finalizer `agentic.openshift.io/execution-rbac-cleanup` present: `Agent.ReleaseSandboxes`, `cleanupExecutionRBAC`, remove finalizer, return.
4. **Suspension check:** Fetch `AgenticOLSConfig` singleton via `isSuspended()`. If `spec.suspended == true` and run is non-terminal: `handleSuspension` releases sandboxes (best-effort), cleans up RBAC (best-effort), sets `EmergencyStopped=True` condition, status patch, return. If CR not found, treat as not suspended. See **what/system-config.md**.
5. **Phase:** `agenticv1alpha1.DerivePhase(proposal.Status.Conditions)` — see **what/** for semantics. Now includes `EmergencyStopped` as highest-precedence terminal phase.
6. **Finalizer add:** If not terminal and finalizer missing, add RBAC cleanup finalizer (re-fetch proposal after patch).
7. **Terminal / failed shortcuts:** Completed/Denied/Escalated/EmergencyStopped/NoActionRequired → optional sandbox release via `Agent.ReleaseSandboxes`. `AgenticRunPhaseFailed` → `handleFailed` (RBAC cleanup if annotation set).
8. **Shared prelude:** `getApprovalPolicy` (cluster singleton name `cluster`), `ensureAgenticRunApproval`, `resolveAgenticRun`. Resolution failure → set `AgenticRunConditionAnalyzed=False` with `reasonWorkflowFailed`, status patch, return (no requeue).
9. **Phase switch:** Routes to `handleRevision` (if `needsRevision`) before analysis/execution/escalation arms; otherwise `handleAnalysis`, `handleExecution`, `handleVerification`, `handleEscalation`, or no-op.
10. **Handlers** set step conditions (`Unknown` → agent call → `True`/`False`), create result CRs, append `Status.Steps.*.Results`, `statusPatch` proposal.
11. **Agent path:** All agent steps go through `r.Agent.*` which (in production) is `SandboxAgentCaller`: `callWithSandbox` calls `Sandbox.Create` (reads config cache, builds PodSpec via `PodSpecBuilder`, creates bare Pod or SandboxClaim+Template based on mode, name pattern `ls-{step}-{run}`) → `patchSandboxInfo` on proposal → `Sandbox.WaitReady` (routes by `cfg.Sandbox.Mode`) → normalize URL → `outputSchemaForStep` → `ClientFactory(endpoint).Run` → JSON unmarshal into outputs.

---

## Handler dispatch pattern

- **Single `Reconcile`** dispatches on **derived phase** and **revision predicate** (`needsRevision`: non-empty `Spec.RevisionFeedback` and `Generation > ObservedGeneration` on `AgenticRunConditionAnalyzed`).
- **Revision** clears downstream conditions and step sandboxes for execution/verification, resets analyzed condition to `Unknown`, appends revision context to request text, re-runs analysis path logic.
- **In-progress idempotency:** Each handler checks existing condition status (`Unknown` / `True`) to avoid duplicate agent invocations on requeue.
- **Approval gates:** Handlers call `isStageDenied` / `isStageApproved` before progressing; waiting states return `(Result{}, nil)` without error.

---

## `SandboxManager`

Unified sandbox lifecycle manager. Decides bare-pod vs sandbox-claim mode based on the configuration cache.

- **`Create`:** Reads base PodSpec from config cache, overlays agent config via `PodSpecBuilder.Build`, then creates either a bare Pod or SandboxClaim+SandboxTemplate. Name pattern `ls-{step}-{run}` truncated to 63 chars — both modes use the same `ls-` prefix. Both paths set OwnerReferences to the AgenticRun. Idempotent via `AlreadyExists`.
- **`WaitReady`:** Routes by `cfg.Sandbox.Mode`. For bare-pod: polls Pod conditions until `Ready=True`, returns `status.podIP`. For sandbox-claim: polls claim → `status.sandbox.name` → Sandbox `Ready=True` → `status.serviceFQDN`. Treats NotFound/terminating as terminal errors.
- **`Release`:** Routes by `cfg.Sandbox.Mode`. For bare-pod: deletes Pod. For sandbox-claim: deletes both SandboxClaim and SandboxTemplate (same name). Idempotent (NotFound ignored).

### `PodSpecBuilder` (internal to `SandboxManager`)

- **Build:** Takes base `*corev1.PodSpec` (from config cache) and overlays agent-specific configuration: LLM env vars, credential mounts, skills volumes, MCP config, required secrets, readiness/liveness probes, SA.
- Also defines label constants (`LabelManaged`, `LabelRun`, etc.) and shared helpers (`credentialsSecretName`, `providerURL`, `providerTypeString`).

**No log streaming in controller:** logs are cluster-side (`kubectl` / CLI); manager only waits for endpoint.

---

## `SandboxAgentCaller` and HTTP

- **Constructor:** Struct literal with `Sandbox SandboxLifecycle`, `K8sClient`, `ClientFactory`, `Namespace`, `Audit`. `Timeout` defaults to `defaultSandboxTimeout` const.
- **`callWithSandbox` order:** `Sandbox.Create(ctx, run, step, agent, llm, tools, serviceAccount)` → `patchSandboxInfo` (status subresource merge) → `Sandbox.WaitReady` → normalize URL (`http://{endpoint}:8080` if no scheme) → `outputSchemaForStep` → inject trace headers → `ClientFactory(endpoint).Run(ctx, "", query, schema, agentCtx, headers)`.
- **`Run` contract:** Empty `systemPrompt`; full payload in POST body per `client.go` (`query`, `outputSchema`, `context`). Path constant `/v1/agent/run`.
- **`buildAgentContext`:** `TargetNamespaces`, `ApprovedOption` / `ExecutionResult` per step, `PreviousAttempts` from failed `StepResultRef` outcomes across analysis/execution/verification result lists.
- **`ReleaseSandboxes`:** Iterates `Status.Steps.{Analysis,Execution,Verification,Escalation}.Sandbox.ClaimName` and calls `Sandbox.Release` for each non-empty.

---

## `AgentHTTPClient` / `AgentHTTPClientInterface`

- **`AgentHTTPClientInterface`:** `Run(ctx, systemPrompt, query, outputSchema, agentCtx) (*agentRunResponse, error)`.
- **`NewAgentHTTPClient`:** Returns concrete type with long HTTP timeout, TLS `InsecureSkipVerify` for in-cluster calls.
- **`Run`:** Marshals `agentRunRequest`, POSTs, reads capped body size, non-200 → error with truncated body; 200 → raw JSON in `agentRunResponse.Response` for caller to unmarshal phase-specific structs.

---

## Template system

- **Embed:** `helpers.go` embeds `templates/*.tmpl` into `templateFS`; `template.Must(ParseFS(...))`.
- **Query builders:** `buildAnalysisQuery` (`analysis_query.tmpl` + `analysisQuery`), `buildExecutionQuery` (`execution_query.tmpl` + pretty-printed option JSON), `buildVerificationQuery` (`verification_query.tmpl` + option + execution JSON via `executionOutputToAgentResult`).
- **Revision:** `buildRevisionContext` → `revision_context.tmpl`.
- **Escalation:** `buildEscalationRequest` → `escalation_request.tmpl` with run identity, request, and slices of `StepResultRef` from status (`Name`, `Outcome` per API — verify template field names match; `StepResultRef` has no `Success` field).

---

## Result CR creation

- **Naming:** `resultCRName(agenticRunName, step, len(existingResults)+1)` with K8s name truncation.
- **`createIdempotent`:** `Create` object (API server drops status on create), then `Status().Patch(MergeFrom)` from a deep copy that retained full status — required because status subresource is separate.
- **Owner:** Controller ref to `AgenticRun`; labels `LabelRun`, `LabelStep`.
- **Execution/Verification result CRs:** `Spec.RetryIndex` from `executionRetryIndex` ( ties to verification retry semantics in **what/** specs).

---

## RBAC resource lifecycle

- **Creation:** `handleExecution`, when selected option has non-empty `RBAC` rules, calls `ensureExecutionRBAC(ctx, Client, proposal, &selectedOption.RBAC, defaultSandboxSA, proposal.Namespace)`. Creates namespaced `Role`/`RoleBinding` per target namespace (from `Spec.TargetNamespaces` or rule namespace fields), persists comma-joined namespaces in annotation `agentic.openshift.io/rbac-namespaces`, and cluster `ClusterRole`/`ClusterRoleBinding` when cluster rules present. Sandbox SA name constant `defaultSandboxSA` (`lightspeed-agent` in `helpers.go`).
- **Cleanup:** `cleanupExecutionRBAC` reads annotation to delete bindings/roles; deletes cluster RBAC by derived name. Invoked on: run deletion (finalizer), `handleFailed` if annotation set, after successful escalation completion, and terminal phases via sandbox release path is separate.
- **`normalizeCoreAPIGroup`:** Maps LLM-facing `"core"` to `""` in K8s `PolicyRule.APIGroups`.
- **Read RBAC (multi-binding):** `resolveReaderBindings` discovers **all** `ClusterRoleBinding`s where `lightspeed-agent` is a subject (e.g. `cluster-reader`, `cluster-monitoring-view`), caches the list in `readerBindings` (`atomic.Value` storing `[]string`), and returns it. `addReaderSubject`/`removeReaderSubject` iterate all discovered bindings. Individual binding updates are in `addSubjectToBinding`/`removeSubjectFromBinding` with conflict retry loops. [OLS-3712]
- **Execution outcome override:** In `handleExecution`, when the agent reports `success=false`, the controller calls `hasMutationSuccess(actions)` to check whether all mutating actions actually succeeded. If yes, it overrides `execResult.Success = true` and proceeds to verification (or trust-mode completion). `isObservationAction(type)` classifies non-mutating action types (`pre-check`, `post-check`, `verification`, `check`, `wait`); everything else is treated as a mutation. [OLS-3558]

---

## Key abstractions

- **`AgentCaller`:** Boundary between reconciler and runtime (stub vs sandbox+HTTP). Methods mirror workflow steps plus `ReleaseSandboxes`.
- **`SandboxLifecycle`:** Interface (`Create`/`WaitReady`/`Release`) for swappable sandbox management (tests can fake). Production implementation: `SandboxManager`. All resources use the `ls-` name prefix; `WaitReady` and `Release` dispatch by reading `cfg.Sandbox.Mode` from the config cache.
- **`PodSpecBuilder`:** Internal to `SandboxManager`. Takes base `*corev1.PodSpec` from config cache and overlays agent config. Produces typed `corev1.PodSpec`; the mode then determines delivery (bare Pod or SandboxTemplate conversion).
- **`resolveAgenticRun`:** Produces `resolvedWorkflow` with cached `Agent` + `LLMProvider` per name; applies per-stage agent overrides from `AgenticRunApproval` via `getStageOverrideAgent`; `Execution`/`Verification` steps nil when corresponding spec sections are zero.

---

## Integration points (who calls whom)

```text
cmd/main.go
  ├─ configuration.NewCache → cfgCache (starts nil)
  ├─ configwatch.Watcher → populates cfgCache on ConfigMap event
  ├─ NewSandboxManager(client, cfgCache, namespace) → SandboxLifecycle
  ├─ SandboxAgentCaller{Sandbox, K8sClient, ClientFactory, Namespace}
  ├─ AgenticRunReconciler{Client, Agent, Config: cfgCache, Namespace}.SetupWithManager
  ├─ agenticolsconfig.Reconciler.SetupWithManager
  └─ inline lightspeed-agent SA creation (manager.RunnableFunc)

AgenticRunReconciler.Reconcile
  ├─ config guard: cfgCache.Available() → false: fail with clear error
  ├─ approval.go, resolve.go
  ├─ handlers.go → results.go, rbac.go, helpers.go (status, option trim)
  └─ Agent (SandboxAgentCaller)
        └─ Sandbox.Create → config cache → PodSpecBuilder.Build → bare Pod / SandboxClaim+Template (ls- prefix)
        └─ Sandbox.WaitReady (routes by cfg.Sandbox.Mode)
        └─ Sandbox.Release (routes by cfg.Sandbox.Mode)
        └─ helpers.go (query templates), schemas.go (outputSchemaForStep)
        └─ client.go (HTTP Run)
```

---

## Implementation notes (gotchas)

- **`cmd/main.go` scheme:** Registers core + `agenticv1alpha1` + `consolev1` + `openshiftv1`. No separate `controller/setup.go` — all wiring is inline in `main.go`. Watching or applying arbitrary CRDs from tests may need extended schemes (see `reconciler_test.go`).
- **Max concurrent reconciles:** `SetupWithManager` reads cluster `ApprovalPolicy` via API reader for `MaxConcurrentRuns`, else `DefaultMaxConcurrentRuns` from API package.
- **Policy watch:** Enqueues **all** non-terminal runs on any `ApprovalPolicy` event — can be chatty.
- **AgenticOLSConfig watch:** Same pattern as policy watch — enqueues all non-terminal runs on any `AgenticOLSConfig` change. When `suspended` flips to `true`, all re-queued runs hit the suspension guard and get terminated.
- **Workflow resolution errors:** Patched onto `AgenticRunConditionAnalyzed` false — see API for exact condition ordering vs `DerivePhase`.
- **`selectedOption` vs trim:** Verification uses latest analysis result’s **first** option (`Options[0]`) when resolving; execution path uses `trimNonSelectedOptions` which respects `AgenticRunApproval` execution option index when multiple options exist.
- **`maxAttempts`:** Combines `ApprovalPolicy.Spec.MaxAttempts` ceiling with per-approval execution override (`helpers.go`); retry semantics interact with verification failure branch in `handleVerification` (see **what/run-lifecycle.md**).
- **Sandbox FQDN:** Agent URL assumes port `8080` unless endpoint already has `http` prefix.
- **Logs CLI vs status:** CLI `logs` uses `SandboxInfo.ClaimName` as **pod name** in `GetLogs`; ensure cluster layout matches (if claim name ≠ pod name, logs command would need revision — operational detail for agents touching `logs.go`).
- **Tests:** `state_machine_test.go` is the primary lifecycle matrix; `testAgentCaller` implements `AgentCaller` with injectable errors/results; fake client uses `WithStatusSubresource` for run and result types.
