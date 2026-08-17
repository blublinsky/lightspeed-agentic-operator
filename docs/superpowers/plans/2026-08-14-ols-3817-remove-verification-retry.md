# OLS-3817 — Remove retry mechanism from operator controller and CRDs — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When verification fails, the operator escalates directly instead of re-executing remediation, and all retry-related CRD fields are removed.

**Architecture:** The AgenticRun lifecycle is driven by Kubernetes conditions; `DerivePhase` maps conditions to a display phase. Today a verification failure can loop back to execution up to `maxAttempts`. This plan collapses that loop: on any verification failure the controller sets `Verified=False` (reason `VerificationFailed`) and `Escalated=Unknown` (reason `VerificationFailed`), and the retry-tracking fields (`maxAttempts`, `retryCount`, `retryIndex`) are removed from the API.

**Tech Stack:** Go, kubebuilder/controller-runtime, controller-gen (v0.19.0), envtest-free fake-client unit tests, `make` targets.

**Spec:** The behavioral contract is already merged. This plan argues from:
- `lightspeed-agentic-operator/.ai/spec/what/run-lifecycle.md` (rules 7–9, 76)
- `lightspeed-agentic-operator/.ai/spec/what/approval.md` (rule 18)
- `lightspeed-agentic-operator/.ai/spec/what/crd-api.md` (rules 25, 32–33)
- `lightspeed-agentic-operator/.ai/spec/what/sandbox-execution.md` (retryIndex rule removed)

Jira: [OLS-3817](https://redhat.atlassian.net/browse/OLS-3817), under epic [OLS-3816](https://redhat.atlassian.net/browse/OLS-3816).

## Global Constraints

- **Verification failure MUST escalate, never re-execute.** Execution runs exactly once per analysis iteration (run-lifecycle rules 7–8, 76).
- **Verified=False → Failed** in `DerivePhase`, unless `Escalated` is set (which is checked earlier and wins). No reason-string special-casing (run-lifecycle rule 9).
- **Condition reasons for the failure path:** `Verified=False` reason `VerificationFailed`; `Escalated=Unknown` reason `VerificationFailed`.
- **Removed API fields (must not remain anywhere, incl. generated YAML):** `ApprovalPolicy.spec.maxAttempts`, `AgenticRunApproval.spec.stages[].execution.maxAttempts` (+ its immutability CEL rule), `AgenticRun.status.steps.execution.retryCount`, `ExecutionResult.spec.retryIndex`, `VerificationResult.spec.retryIndex` (+ the `Retry` printer column on both).
- **Always regenerate, never hand-edit generated files:** after any `api/v1alpha1` marker/field change run `make manifests` (CRD YAML + RBAC) and regenerate deepcopy (see Task 3/4 for the exact command).
- **Run tests via `make test`** (covers main + api + cli modules; includes `fmt-check` and `vet`). Never `go test` directly. Run `make api-lint` after API type changes.
- **Commit style:** first line `OLS-3817 <imperative summary>` under 72 chars.
- **Out of scope for this plan (belongs to OLS-3819):** removing the `EmitVerificationRetry` audit method, the `audit.verification.retry` structured event, and the `agenticrun.verification.retry` span event. This plan only removes the `retry_index` span *attribute* reads that would otherwise fail to compile once `retryCount` is gone (Task 4).

---

## File structure & decomposition

Tasks are ordered so the tree **compiles and `make test` passes after every commit**. Each task removes a coherent slice together with all of its readers.

| Task | Responsibility | Key files |
|---|---|---|
| 1 | Controller behavior: escalate on verification failure (fields still present) | `controller/agenticrun/handlers.go`, `state_machine_test.go`, `handlers_test.go` |
| 2 | `DerivePhase` simplification + reason constants | `api/v1alpha1/agenticrun_types.go`, `derive_phase_test.go`, `controller/agenticrun/helpers.go` |
| 3 | Remove `maxAttempts` (fields, CEL, helper, `NewApprovalStage` param, samples) | `api/v1alpha1/{approvalpolicy,agenticrunapproval,approval_stage}_types*.go`, `controller/agenticrun/helpers.go`, `cli/run/*.go`, `controller/agenticrun/approval.go`, samples, generated YAML |
| 4 | Remove `retryCount` / `retryIndex` (fields, result-CR wiring, span attrs) | `api/v1alpha1/{agenticrun_status,executionresult,verificationresult}_types.go`, `controller/agenticrun/results.go`, `controller/agenticrun/audit.go`, samples, generated YAML/deepcopy |
| 5 | e2e: verification-failure → escalation, single execution | `test/e2e/` |
| 6 | Docs + final full regen & verification sweep | `CLAUDE.md`, `AGENTS.md`, final `make` sweep |

---

### Task 1: Controller escalates on verification failure

Rewrite the `!allPassed` branch of `handleVerification` so a verification failure escalates directly. This removes the only *writer* of `RetryCount`, the `EmitVerificationRetry` call, the `maxAttempts()` usage, and the two retry error constants. The `RetryCount` field itself and `maxAttempts()`/`executionRetryIndex()` helpers stay in place (removed in Tasks 3–4), so everything still compiles.

**Files:**
- Modify: `controller/agenticrun/handlers.go` (`handleVerification`, ~lines 493–544; const block lines 34–35)
- Test: `controller/agenticrun/state_machine_test.go` (replace the three retry tests, ~lines 362–481)
- Test: `controller/agenticrun/handlers_test.go` (`TestReconcile_VerificationObjectiveFailure_RetriesExecution` ~297–362, `TestReconcile_VerificationOutcomeFailed_RetriesExecution` ~1241+)

**Interfaces:**
- Consumes: `agent.verifyResult *VerificationOutput`; helpers `approveAnalysis/approveExecution/approveVerification/reconcileOnce/assertPhase/getAgenticRun` (existing in the test package).
- Produces: after a verification failure the run has conditions `Verified=False/VerificationFailed` and `Escalated=Unknown/VerificationFailed`; derived phase `Escalating`.

- [ ] **Step 1: Replace the retry unit tests with escalation tests.**

In `state_machine_test.go`, delete `TestManualApproval_VerificationFailRetry`, `TestManualApproval_FullRetryExhaustion`, and `TestManualApproval_RetryThenSucceed` (~lines 362–481) and the section banner above them. Add in their place:

```go
// ---------------------------------------------------------------------------
// Verification failure escalates directly (no execution retries) — OLS-3817
// ---------------------------------------------------------------------------

func TestManualApproval_VerificationFailEscalates(t *testing.T) {
	run := testAgenticRun()
	agent := newTestAgentCaller()
	r, fc := newManualReconciler(t, run, agent)

	// Analysis → Proposed → Executing → Verifying
	approveAnalysis(t, fc, "fix-crash")
	reconcileOnce(r, "fix-crash")
	approveExecution(t, fc, "fix-crash", 0)
	reconcileOnce(r, "fix-crash")
	assertPhase(t, r, "fix-crash", agenticv1alpha1.AgenticRunPhaseVerifying)

	// Objective verification failure
	agent.verifyResult = &VerificationOutput{
		Success: false,
		Summary: "Pod still crashing",
		Checks:  []agenticv1alpha1.VerifyCheck{{Name: "pod-running", Result: agenticv1alpha1.CheckResultFailed}},
	}
	approveVerification(t, fc, "fix-crash")
	reconcileOnce(r, "fix-crash")

	// Escalates directly — never returns to Executing.
	assertPhase(t, r, "fix-crash", agenticv1alpha1.AgenticRunPhaseEscalating)

	p, _ := getAgenticRun(r, "fix-crash")
	verified := meta.FindStatusCondition(p.Status.Conditions, agenticv1alpha1.AgenticRunConditionVerified)
	if verified == nil || verified.Status != metav1.ConditionFalse || verified.Reason != "VerificationFailed" {
		t.Fatalf("expected Verified=False/VerificationFailed, got %+v", verified)
	}
	escalated := meta.FindStatusCondition(p.Status.Conditions, agenticv1alpha1.AgenticRunConditionEscalated)
	if escalated == nil || escalated.Status != metav1.ConditionUnknown {
		t.Fatalf("expected Escalated=Unknown, got %+v", escalated)
	}

	// Exactly one ExecutionResult — proof of no re-execution.
	var execResults agenticv1alpha1.ExecutionResultList
	if err := fc.List(context.Background(), &execResults); err != nil {
		t.Fatalf("list execution results: %v", err)
	}
	if len(execResults.Items) != 1 {
		t.Fatalf("expected exactly 1 ExecutionResult, got %d", len(execResults.Items))
	}
}
```

- [ ] **Step 2: Run the new test to confirm it fails against current behavior.**

Run: `make test 2>&1 | tail -30`
Expected: FAIL — current code retries, so phase is `Executing`, not `Escalating` (and `ExecutionResult` count may differ). Also expect compile errors from the two `handlers_test.go` retry tests still referencing retry expectations; that's fine, they're rewritten in Step 4.

- [ ] **Step 3: Rewrite the `!allPassed` branch in `handleVerification`.**

In `controller/agenticrun/handlers.go`, replace the whole retry/exhaustion block (from `if !allPassed {` through its closing `}`, ~lines 493–544) with:

```go
	if !allPassed {
		log.Info("verification failed, escalating", LogKeySummary, verifyResult.Summary)
		if r.Audit != nil {
			r.Audit.EmitVerificationCompleted(spanCtx, run, verifyCR)
		}
		meta.SetStatusCondition(&run.Status.Conditions, metav1.Condition{
			Type:               agenticv1alpha1.AgenticRunConditionVerified,
			Status:             metav1.ConditionFalse,
			Reason:             "VerificationFailed",
			Message:            fmt.Sprintf("Verification failed: %s", verifyResult.Summary),
			ObservedGeneration: run.Generation,
		})
		meta.SetStatusCondition(&run.Status.Conditions, metav1.Condition{
			Type:               agenticv1alpha1.AgenticRunConditionEscalated,
			Status:             metav1.ConditionUnknown,
			Reason:             "VerificationFailed",
			Message:            "Verification failed, escalating",
			ObservedGeneration: run.Generation,
		})
		if err := r.statusPatch(ctx, run, base); err != nil {
			return ctrl.Result{}, fmt.Errorf("%s: %w", ErrUpdateVerificationFailed, err)
		}
		return ctrl.Result{}, nil
	}
```

- [ ] **Step 4: Update the two `handlers_test.go` retry reconcile tests.**

Rename `TestReconcile_VerificationObjectiveFailure_RetriesExecution` → `..._Escalates` and `TestReconcile_VerificationOutcomeFailed_RetriesExecution` → `..._Escalates`. In each, drop the `defaultObjectsWithMaxAttempts(3)` fixture in favour of the default objects (see the existing non-retry tests in the same file for the default constructor, e.g. `defaultObjects()`), remove all `RetryCount` assertions and multi-attempt reconcile loops, and assert a single reconcile after the failing verification yields phase `Escalating` with `Verified=False/VerificationFailed`. Model the assertions on `TestManualApproval_VerificationFailEscalates` from Step 1.

- [ ] **Step 5: Swap the retry error constants for a single escalation constant.**

In `handlers.go` const block (~lines 34–35) remove:

```go
	ErrUpdateForExecRetry        = "update for execution retry"
	ErrUpdateRetriesExhausted    = "update (retries exhausted)"
```

and add (keep the block's alignment/gofmt):

```go
	ErrUpdateVerificationFailed  = "update (verification failed, escalating)"
```

- [ ] **Step 6: Run tests.**

Run: `make test 2>&1 | tail -30`
Expected: PASS. If a rewritten `handlers_test.go` case still references `defaultObjectsWithMaxAttempts`, keep that fixture for now (it is removed in Task 3) — it must still compile at this step.

- [ ] **Step 7: Commit.**

```bash
git add controller/agenticrun/handlers.go controller/agenticrun/state_machine_test.go controller/agenticrun/handlers_test.go
git commit -m "OLS-3817 escalate on verification failure instead of retrying"
```

---

### Task 2: Simplify DerivePhase and drop retry reasons

Remove the reason-sensitive `Executing` branch so `Verified=False` always derives `Failed` (escalation still wins, checked earlier). Delete the now-orphaned reason constants in both the api module and their controller aliases.

**Files:**
- Modify: `api/v1alpha1/agenticrun_types.go` (reason consts ~47–51; `DerivePhase` ~95–100)
- Modify: `controller/agenticrun/helpers.go` (aliases ~61–62)
- Test: `api/v1alpha1/derive_phase_test.go`

**Interfaces:**
- Produces: `DerivePhase` returns `Failed` for `Verified=False` (any reason); `ReasonRetryingExecution`/`ReasonRetriesExhausted` no longer exist. `ReasonNoActionRequired` is retained.

- [ ] **Step 1: Update the derivation tests.**

In `api/v1alpha1/derive_phase_test.go`:
- Delete the case `"verification failed - retrying execution"` (~109–115).
- Delete the case `"verification failed - retries exhausted (without escalated condition)"` (~117–123) — the surviving `"verification failed - terminal"` case (Verified=False/`Failed` → `AgenticRunPhaseFailed`) already covers the collapsed behavior.
- In `"escalating takes priority over verified retries exhausted"` (~158–165), keep the case (it proves escalation precedence) but change the reason strings from `"RetriesExhausted"` to `"VerificationFailed"` and rename to `"escalating takes priority over verified false"`.
- In the two `"escalated..."` cases, change the reason literal `"MaxAttemptsExhausted"` to `"VerificationFailed"` (cosmetic; DerivePhase does not branch on it).

- [ ] **Step 2: Run tests to confirm failure.**

Run: `make test 2>&1 | tail -30`
Expected: FAIL — `DerivePhase` still maps `Verified=False/VerificationFailed` via the removed-reason path; and the api module will still compile (test uses string literals, not the consts).

- [ ] **Step 3: Simplify `DerivePhase`.**

In `api/v1alpha1/agenticrun_types.go`, replace the `Verified` `default` arm (~95–100):

```go
		default:
			if c.Reason == ReasonRetryingExecution {
				return AgenticRunPhaseExecuting
			}
			return AgenticRunPhaseFailed
		}
```

with:

```go
		default:
			return AgenticRunPhaseFailed
		}
```

- [ ] **Step 4: Delete the orphaned reason constants.**

In `api/v1alpha1/agenticrun_types.go` reason block (~47–51), remove `ReasonRetryingExecution` and `ReasonRetriesExhausted`, keeping `ReasonNoActionRequired`:

```go
// Condition reasons used by DerivePhase for state transitions.
// SYNC: must match derivePhaseFromConditions in lightspeed-agentic-console/src/models/agenticrun.ts
const (
	ReasonNoActionRequired = "NoActionRequired"
)
```

In `controller/agenticrun/helpers.go`, remove the two aliases (~61–62):

```go
	reasonRetryingExecution = agenticv1alpha1.ReasonRetryingExecution
	reasonRetriesExhausted  = agenticv1alpha1.ReasonRetriesExhausted
```

- [ ] **Step 5: Run tests + api-lint.**

Run: `make test 2>&1 | tail -30 && make api-lint 2>&1 | tail -20`
Expected: PASS / no lint errors.

- [ ] **Step 6: Commit.**

```bash
git add api/v1alpha1/agenticrun_types.go api/v1alpha1/derive_phase_test.go controller/agenticrun/helpers.go
git commit -m "OLS-3817 map Verified=False to Failed, drop retry reasons"
```

---

### Task 3: Remove maxAttempts

Delete `maxAttempts` from both CRDs, its immutability CEL rule, the `maxAttempts()` controller helper (now unused after Task 1), and the `maxAttempts` parameter of `NewApprovalStage`; update every caller and sample; regenerate.

**Files:**
- Modify: `api/v1alpha1/approvalpolicy_types.go` (field ~62–68)
- Modify: `api/v1alpha1/agenticrunapproval_types.go` (field ~99–105; CEL rule ~183)
- Modify: `api/v1alpha1/approval_stage.go` (`NewApprovalStage` signature + setter ~7–25)
- Modify: `controller/agenticrun/helpers.go` (delete `maxAttempts()` ~237–254)
- Modify callers: `cli/run/approve.go:141`, `cli/run/deny.go:95`, `controller/agenticrun/approval.go:65,71,77`
- Modify tests: `api/v1alpha1/agenticrunapproval_types_test.go`, `controller/agenticrun/approval_test.go`, `controller/agenticrun/state_machine_test.go` (`testPolicyWithMaxAttempts`), `controller/agenticrun/reconciler_test.go`/`handlers_test.go` (`defaultObjectsWithMaxAttempts`, `testAutoApprovePolicyWithMaxAttempts`), `helpers_test.go` (`TestMaxAttempts`)
- Modify samples: `config/samples/agentic_v1alpha1_approvalpolicy.yaml`, `examples/setup/02-approval-policy.yaml`
- Regenerate: `config/crd/bases/agentic.openshift.io_approvalpolicies.yaml`, `..._agenticrunapprovals.yaml`

**Interfaces:**
- Produces: `NewApprovalStage(typ, decision, agent, option)` — 4 params, no `maxAttempts`. No `MaxAttempts` field on `ApprovalPolicySpec` or `ExecutionApproval`.

- [ ] **Step 1: Delete `TestMaxAttempts` and update fixtures/tests first (TDD: prove the helper is gone).**

- In `controller/agenticrun/helpers_test.go`, delete `TestMaxAttempts` (~207–250).
- In `state_machine_test.go`, collapse `testPolicy`/`testPolicyWithMaxAttempts`: make `testPolicy` build the policy directly (drop `MaxAttempts`), and delete `testPolicyWithMaxAttempts`:

```go
func testPolicy(analysis, execution, verification agenticv1alpha1.ApprovalMode) *agenticv1alpha1.ApprovalPolicy {
	return &agenticv1alpha1.ApprovalPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "cluster"},
		Spec: agenticv1alpha1.ApprovalPolicySpec{
			Stages: []agenticv1alpha1.ApprovalPolicyStage{
				{Name: agenticv1alpha1.SandboxStepAnalysis, Approval: analysis},
				{Name: agenticv1alpha1.SandboxStepExecution, Approval: execution},
				{Name: agenticv1alpha1.SandboxStepVerification, Approval: verification},
			},
		},
	}
}
```

- In `reconciler_test.go`/`handlers_test.go`, replace `defaultObjectsWithMaxAttempts(n)` / `testAutoApprovePolicyWithMaxAttempts(n)` usages with the non-`MaxAttempts` equivalents (`defaultObjects()` / the auto-approve policy without the field) and delete the now-unused `...WithMaxAttempts` constructors. Grep to be exhaustive: `grep -rn "MaxAttempts\|maxAttempts" controller/ cli/ api/`.
- In `agenticrunapproval_types_test.go` and `approval_test.go`, drop the trailing `0`/`maxAttempts` argument from every `NewApprovalStage(...)` call.

- [ ] **Step 2: Run tests to confirm compile failure (fields/params still present in non-test code).**

Run: `make test 2>&1 | tail -30`
Expected: FAIL to compile — tests now call `NewApprovalStage` with 4 args but the function still takes 5. Good; drives Step 3.

- [ ] **Step 3: Change `NewApprovalStage` and delete the `maxAttempts()` helper.**

In `api/v1alpha1/approval_stage.go`, remove the `maxAttempts int32` parameter and the `if maxAttempts > 0 { e.MaxAttempts = maxAttempts }` setter:

```go
func NewApprovalStage(typ ApprovalStageType, decision ApprovalDecision, agent string, option *int32) ApprovalStage {
```

(delete the `e.MaxAttempts = maxAttempts` lines inside; leave the rest of the constructor intact).

In `controller/agenticrun/helpers.go`, delete the entire `maxAttempts(...)` function (~237–254).

- [ ] **Step 4: Update non-test callers of `NewApprovalStage`.**

Drop the trailing `0` argument at `cli/run/approve.go:141`, `cli/run/deny.go:95`, and the three calls in `controller/agenticrun/approval.go` (~65, 71, 77).

- [ ] **Step 5: Remove the `maxAttempts` fields and CEL rule.**

- `api/v1alpha1/approvalpolicy_types.go`: delete the `MaxAttempts` field and its doc comment + `+kubebuilder` markers (~62–68).
- `api/v1alpha1/agenticrunapproval_types.go`: delete the `MaxAttempts` field on `ExecutionApproval` (~99–105) **and** the `+kubebuilder:validation:XValidation` line enforcing `"maxAttempts once set cannot be changed"` (~183).

- [ ] **Step 6: Clean the YAML samples.**

Remove the `maxAttempts:` line from `config/samples/agentic_v1alpha1_approvalpolicy.yaml` and `examples/setup/02-approval-policy.yaml`.

- [ ] **Step 7: Regenerate manifests and verify no `maxAttempts` remains.**

```bash
make manifests
grep -rn "maxAttempts\|MaxAttempts" api/ controller/ cli/ config/ examples/ | grep -v _test.go
```
Expected: `make manifests` rewrites the two CRD YAMLs; the grep returns **nothing**.

- [ ] **Step 8: Run full tests + api-lint.**

Run: `make test 2>&1 | tail -30 && make api-lint 2>&1 | tail -20`
Expected: PASS.

- [ ] **Step 9: Commit.**

```bash
git add -A
git commit -m "OLS-3817 remove maxAttempts from CRDs, helper, and CLI"
```

---

### Task 4: Remove retryCount and retryIndex

Delete `retryCount` (AgenticRun status) and `retryIndex` (execution/verification results) with their printer columns, the `executionRetryIndex()` helper and its call sites, and the `retry_index` span-attribute reads in `audit.go` (a compile-fix; the rest of retry audit is OLS-3819). Regenerate deepcopy + manifests.

**Files:**
- Modify: `api/v1alpha1/agenticrun_status_types.go` (`RetryCount` ~187–193)
- Modify: `api/v1alpha1/executionresult_types.go` (`RetryIndex` ~62–68; `Retry` printer column ~75)
- Modify: `api/v1alpha1/verificationresult_types.go` (`RetryIndex` ~69–73; `Retry` printer column ~80)
- Modify: `controller/agenticrun/results.go` (`executionRetryIndex` ~44–49; call sites ~159, 211)
- Modify: `controller/agenticrun/audit.go` (`StartExecutionSpan` ~231–238, `StartVerificationSpan` ~239–246)
- Modify samples: `config/samples/agentic_v1alpha1_executionresult.yaml`, `..._verificationresult.yaml`
- Regenerate: `api/v1alpha1/zz_generated.deepcopy.go`, the execution/verification/agenticrun CRD YAMLs

**Interfaces:**
- Produces: no `RetryCount`/`RetryIndex` fields; `createExecutionResult`/`createVerificationResult` build result specs without `RetryIndex`; execution/verification spans carry no `retry_index` attribute.

- [ ] **Step 1: Remove `RetryIndex` from result-CR creation and delete the helper.**

In `controller/agenticrun/results.go`:
- Delete the `executionRetryIndex` function (~44–49).
- Remove `RetryIndex: ptr.To(executionRetryIndex(run)),` from `createExecutionResult` (~159) and `createVerificationResult` (~211). If `ptr` becomes an unused import, remove it (check other uses first).

- [ ] **Step 2: Remove `retry_index` span reads in `audit.go`.**

In `StartExecutionSpan` and `StartVerificationSpan`, delete the two blocks (~233–234 and ~241–242) that read `run.Status.Steps.Execution.RetryCount` and append the `retry_index` attribute, so each becomes a plain `return l.startPhaseSpan(ctx, run, "agenticrun.execute"/"agenticrun.verify")`. If `extra` / `attribute` become unused in these functions, simplify accordingly (leave the wider `audit.go` retry method for OLS-3819).

- [ ] **Step 3: Remove the fields.**

- `api/v1alpha1/agenticrun_status_types.go`: delete `RetryCount *int32` and its doc/markers (~187–193).
- `api/v1alpha1/executionresult_types.go`: delete `RetryIndex *int32` (+ markers) and the `+kubebuilder:printcolumn:name="Retry"...` line.
- `api/v1alpha1/verificationresult_types.go`: same removal (field + `Retry` printer column).

- [ ] **Step 4: Clean result-CR samples.**

Remove the `retryIndex:` line from `config/samples/agentic_v1alpha1_executionresult.yaml` and `..._verificationresult.yaml`.

- [ ] **Step 5: Regenerate deepcopy and manifests.**

```bash
make manifests
bin/controller-gen object paths=./api/v1alpha1/...
```
(The repo has no `make generate` target; `bin/controller-gen` is installed by any `controller-gen`-dependent make target such as `make manifests`. If `bin/controller-gen` is missing, run `make manifests` first — it installs it.)

Then verify:
```bash
grep -rn "retryCount\|RetryCount\|retryIndex\|RetryIndex" api/ controller/ config/ | grep -v _test.go
```
Expected: **nothing** (including `zz_generated.deepcopy.go`).

- [ ] **Step 6: Run full tests + api-lint.**

Run: `make test 2>&1 | tail -30 && make api-lint 2>&1 | tail -20`
Expected: PASS. Fix any result-CR unit tests that asserted `RetryIndex` by removing those assertions.

- [ ] **Step 7: Commit.**

```bash
git add -A
git commit -m "OLS-3817 remove retryCount and retryIndex fields"
```

---

### Task 5: e2e — verification failure escalates with a single execution

Add a black-box e2e case (build tag `e2e`) asserting the full-cluster behavior. Follow the existing patterns in `test/e2e/` for driving the mock agent to a failing verification.

**Files:**
- Modify/Create: `test/e2e/` (add to the existing verification/escalation e2e file; match the package's helper style)

**Interfaces:**
- Consumes: existing e2e harness helpers (client, mock-agent programming, phase-wait utilities).

- [ ] **Step 1: Read the existing e2e verification test(s).**

Run: `ls test/e2e && grep -rln "Verif\|Escalat" test/e2e`
Read the matched file(s) to learn the harness: how a run is created, how the mock agent is told to fail verification, and how phase transitions are awaited.

- [ ] **Step 2: Write the e2e test.**

Add a test that: creates an `AgenticRun` with execution + verification; programs the mock agent to return a failing verification check; approves through execution and verification; then asserts the run reaches `Escalating`→`Escalated` (approving escalation if the harness requires it) and that **exactly one** `ExecutionResult` exists for the run. Use the existing phase-wait helper rather than fixed sleeps. Mirror assertion style from `TestManualApproval_VerificationFailEscalates` (Task 1) for the single-`ExecutionResult` check.

- [ ] **Step 3: Run e2e (requires a live cluster + running operator).**

Run: `make test-e2e 2>&1 | tail -40`
Expected: PASS. (See `test/e2e/` README prereqs. If no cluster is available in the execution environment, mark this step for the manual cluster-verification phase and note it in the commit.)

- [ ] **Step 4: Commit.**

```bash
git add test/e2e
git commit -m "OLS-3817 e2e: verification failure escalates, single execution"
```

---

### Task 6: Docs and final verification sweep

**Files:**
- Modify: `CLAUDE.md` (Run lifecycle phases note), `AGENTS.md` (same note if duplicated)

- [ ] **Step 1: Fix the lifecycle docs.**

In `CLAUDE.md` (and `AGENTS.md` if it carries the same text), change the `Executing` bullet:

```text
- **Executing** — in flight (Executed=Unknown) or retry (Verified=False / RetryingExecution).
```

to:

```text
- **Executing** — execution in flight (Executed=Unknown). Verification failure escalates; it never returns to Executing.
```

- [ ] **Step 2: Final residual-reference sweep.**

Run:
```bash
grep -rn "maxAttempts\|MaxAttempts\|retryCount\|RetryCount\|retryIndex\|RetryIndex\|RetryingExecution\|RetriesExhausted" . | grep -v vendor | grep -v "docs/superpowers" | grep -v "\.ai/spec"
```
Expected: **nothing** outside this plan and (for OLS-3819's scope) the audit `EmitVerificationRetry` method / `verification.retry` events, which are intentionally left for that story. If anything else appears, remove it in the task where it belongs and amend.

- [ ] **Step 3: Full build, manifests-clean, tests, lint.**

Run:
```bash
make manifests && git diff --exit-code config/ && make build && make test && make api-lint
```
Expected: `git diff --exit-code config/` passes (manifests already committed and stable), build/tests/lint all green.

- [ ] **Step 4: Commit.**

```bash
git add CLAUDE.md AGENTS.md
git commit -m "OLS-3817 update lifecycle docs for escalate-on-verification-failure"
```

---

## Self-Review

**Spec coverage:**
- run-lifecycle rule 7/8/76 (escalate, no retry, exactly-once) → Task 1 (behavior) + Task 5 (e2e).
- run-lifecycle rule 9 (Verified=False → Failed, escalation wins) → Task 2.
- approval rule 18 / crd-api rule 25 (maxAttempts removed) → Task 3.
- crd-api rules 32–33 + sandbox-execution retryIndex removal → Task 4.
- Reason strings `VerificationFailed` → Task 1.
- Console SYNC comment note → not this repo's story; delivered by OLS-3915.

**Placeholder scan:** No TBD/TODO; every code step has concrete code or an exact edit + grep verification. Two steps (Task 1 Step 4, Task 3 Step 1, Task 5 Step 2) reference reading neighboring existing tests to match fixture names that vary by file — these are exact-named symbols (`defaultObjects`, `NewApprovalStage`, `TestManualApproval_VerificationFailEscalates`) with grep commands, not vague "similar to" hand-waves.

**Type consistency:** `NewApprovalStage` reduced to 4 params consistently across Task 3 (definition + all callers). Condition reasons `VerificationFailed` used identically in Task 1 code and Task 1/Task 2 tests. `ErrUpdateVerificationFailed` defined (Task 1 Step 5) and used (Task 1 Step 3). `RetryCount`/`RetryIndex`/`maxAttempts` removals each pair the field deletion with all reader deletions in the same task, preserving compile-green per commit.

**Cross-story boundary:** `audit.go` `retry_index` attribute reads are removed here (compile necessity); the `EmitVerificationRetry` method, `audit.verification.retry` structured event, and `agenticrun.verification.retry` span event are explicitly deferred to OLS-3819 and called out in Global Constraints + Task 6 Step 2.
