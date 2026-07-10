# Rename Proposal to AgenticRun — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rename the `Proposal` CRD kind to `AgenticRun` and `ProposalApproval` to `AgenticRunApproval` across the entire codebase, aligning with the updated spec.

**Architecture:** Mechanical rename via ordered find-replace (longest identifiers first to avoid partial matches), directory/file renames via `git mv`, then `make manifests` to regenerate CRD YAML and RBAC. The code must compile and pass all unit tests after completion.

**Tech Stack:** Go, kubebuilder/controller-gen, perl, Kubernetes CRDs

## Global Constraints

- Replacement ordering: always process longer identifiers before shorter ones to prevent partial matches.
- `Proposal` (8 chars) does NOT match `Proposed` (8 chars, differs at position 7) — bare `Proposal` → `AgenticRun` is safe.
- `ProposalResult` → `RemediationPlan` (not `AgenticRunResult`).
- `LabelProposal` → `LabelRun` (not `LabelAgenticRun`) — must be handled before the bare `Proposal` replacement.
- Phase value string `"Proposed"` stays unchanged (workflow state name).
- `SandboxStep` enum values, Result CRD kinds, SA naming pattern, finalizer string — all stay unchanged.
- Design spec: `docs/superpowers/specs/2026-07-07-rename-proposal-to-agenticrun-design.md`

---

### Task 1: Rename directories and files

**Files:**
- Rename: `controller/proposal/` → `controller/agenticrun/` (35 Go files)
- Rename: `cli/proposal/` → `cli/run/` (20 Go files)
- Rename: 4 API type files, 2 RBAC files, 2 sample files

**Interfaces:**
- Consumes: nothing
- Produces: all files at new paths, ready for content replacement

- [ ] **Step 1: Rename directories**

```bash
git mv controller/proposal controller/agenticrun
git mv cli/proposal cli/run
```

- [ ] **Step 2: Rename API type files**

```bash
git mv api/v1alpha1/proposal_types.go api/v1alpha1/agenticrun_types.go
git mv api/v1alpha1/proposal_status_types.go api/v1alpha1/agenticrun_status_types.go
git mv api/v1alpha1/proposal_analysis_types.go api/v1alpha1/agenticrun_analysis_types.go
git mv api/v1alpha1/proposalapproval_types.go api/v1alpha1/agenticrunapproval_types.go
```

- [ ] **Step 3: Rename config files**

```bash
git mv config/rbac/proposal_approver_role.yaml config/rbac/run_approver_role.yaml
git mv config/rbac/proposal_approver_binding.yaml config/rbac/run_approver_binding.yaml
git mv config/samples/agentic_v1alpha1_proposal.yaml config/samples/agentic_v1alpha1_agenticrun.yaml
git mv config/samples/agentic_v1alpha1_proposalapproval.yaml config/samples/agentic_v1alpha1_agenticrunapproval.yaml
```

- [ ] **Step 4: Verify renames**

```bash
git status --short | head -30
```

Expected: ~60 file renames shown, no deleted-without-added files.

---

### Task 2: All content replacements

This is the core task. Replacements are organized in phases that MUST execute in order. Each phase's output is the input for the next.

**Files:**
- Modify: all `.go`, `.yaml`, `.sh`, `.md`, `Dockerfile` files in the repo

**Interfaces:**
- Consumes: files at new paths from Task 1
- Produces: all identifiers, strings, and config values renamed

- [ ] **Step 1: Define the file set**

```bash
cd "$(git rev-parse --show-toplevel)"
ALL_FILES=$(find . -type f \( -name '*.go' -o -name '*.yaml' -o -name '*.yml' -o -name '*.sh' -o -name '*.md' -o -name 'Dockerfile' \) ! -path './.git/*' ! -path './vendor/*' ! -path './bin/*' ! -path './docs/superpowers/*')
```

- [ ] **Step 2: Phase 1 — Targeted field rename for RemediationOption.Proposal**

Must happen BEFORE any `ProposalResult` or `Proposal` replacements:

```bash
perl -pi -e 's/Proposal(\s+)ProposalResult(\s+`json:"proposal,omitzero"`)/RemediationPlan$1ProposalResult$2/g' $ALL_FILES
```

After `ProposalResult` is renamed later, this becomes `RemediationPlan RemediationPlan \`json:"remediationPlan,omitzero"\``. Note: the JSON tag `"proposal,omitzero"` → `"remediationPlan,omitzero"` is part of this same line replacement. Verify in `api/v1alpha1/agenticrun_analysis_types.go`:

```bash
perl -pi -e 's/`json:"proposal,omitzero"`/`json:"remediationPlan,omitzero"`/g' api/v1alpha1/agenticrun_analysis_types.go
```

- [ ] **Step 3: Phase 2 — PascalCase compound identifiers (longest first)**

```bash
perl -pi -e '
  s/ProposalApprovalMutator/AgenticRunApprovalMutator/g;
  s/ProposalApprovalStatus/AgenticRunApprovalStatus/g;
  s/ProposalApprovalSpec/AgenticRunApprovalSpec/g;
  s/ProposalApprovalList/AgenticRunApprovalList/g;
  s/ProposalApproval/AgenticRunApproval/g;
  s/ProposalReconciler/AgenticRunReconciler/g;
  s/ProposalIDGenerator/AgenticRunIDGenerator/g;
  s/ProposalConditionEmergencyStopped/AgenticRunConditionEmergencyStopped/g;
  s/ProposalConditionEscalated/AgenticRunConditionEscalated/g;
  s/ProposalConditionVerified/AgenticRunConditionVerified/g;
  s/ProposalConditionExecuted/AgenticRunConditionExecuted/g;
  s/ProposalConditionAnalyzed/AgenticRunConditionAnalyzed/g;
  s/ProposalConditionDenied/AgenticRunConditionDenied/g;
  s/DefaultMaxConcurrentProposals/DefaultMaxConcurrentRuns/g;
  s/MaxConcurrentProposals/MaxConcurrentRuns/g;
  s/ProposalResult/RemediationPlan/g;
  s/ProposalStatus/AgenticRunStatus/g;
  s/ProposalPhase/AgenticRunPhase/g;
  s/ProposalSpec/AgenticRunSpec/g;
  s/ProposalStep/AgenticRunStep/g;
  s/ProposalList/AgenticRunList/g;
  s/ProposalName/AgenticRunName/g;
  s/LabelProposal/LabelRun/g;
' $ALL_FILES
```

- [ ] **Step 4: Phase 3 — Bare PascalCase `Proposal` → `AgenticRun`**

Catches remaining: type `Proposal`, `*Proposal`, `&Proposal{}`, `EmitProposalReceived`, `ContextWithProposalTraceID`, etc. Safe because `Proposed` ≠ `Proposal` (differ at char 7).

```bash
perl -pi -e 's/Proposal/AgenticRun/g' $ALL_FILES
```

- [ ] **Step 5: Phase 4 — camelCase identifiers and JSON tags**

```bash
GO_FILES=$(find . -name '*.go' ! -path './.git/*' ! -path './vendor/*' ! -path './bin/*' ! -path './docs/superpowers/*')

perl -pi -e '
  s/maxConcurrentProposals/maxConcurrentRuns/g;
  s/proposalName/agenticRunName/g;
  s/proposalGVR/agenticRunGVR/g;
  s/proposalOwnerRef/agenticRunOwnerRef/g;
' $GO_FILES
```

Also handle hack scripts with JSON field names:

```bash
HACK_FILES=$(find hack -type f \( -name '*.sh' -o -name '*.yaml' -o -name '*.md' \))
perl -pi -e 's/maxConcurrentProposals/maxConcurrentRuns/g' $HACK_FILES
```

- [ ] **Step 6: Phase 5 — Package declarations**

```bash
perl -pi -e 's/^package proposal$/package agenticrun/' controller/agenticrun/*.go
perl -pi -e 's/^package proposal$/package run/' cli/run/*.go
```

- [ ] **Step 7: Phase 6 — Import paths**

```bash
perl -pi -e 's|controller/proposal|controller/agenticrun|g' $GO_FILES
perl -pi -e 's|cli/proposal|cli/run|g' $GO_FILES
```

- [ ] **Step 8: Phase 7 — Import aliases in test files and root.go**

```bash
# test/agent files: change alias from proposal to agenticrun
perl -pi -e 's/proposal ("github\.com)/agenticrun $1/g' test/agent/main.go test/agent/cmd/schemadump/main.go
perl -pi -e 's/proposal\.(ExecutionOutputSchema|VerificationOutputSchema|EscalationOutputSchema)/agenticrun.$1/g' test/agent/main.go test/agent/cmd/schemadump/main.go

# cli/root.go: the import "cli/run" gives package name "run"
perl -pi -e 's/proposal\.New/run.New/g' cli/root.go
```

- [ ] **Step 9: Phase 8 — Audit events, OTEL spans, attribute keys**

These MUST be replaced BEFORE the broad variable rename in Phase 10.

```bash
# Audit event prefixes
perl -pi -e 's/audit\.proposal\./audit.agenticrun./g' controller/agenticrun/audit.go controller/agenticrun/audit_test.go

# OTEL span names
perl -pi -e '
  s/proposal\.lifecycle/agenticrun.lifecycle/g;
  s/proposal\.human_approval/agenticrun.human_approval/g;
  s/proposal\.analyze/agenticrun.analyze/g;
  s/proposal\.execute/agenticrun.execute/g;
  s/proposal\.verify/agenticrun.verify/g;
  s/proposal\.escalate/agenticrun.escalate/g;
  s/proposal\.terminal/agenticrun.terminal/g;
' controller/agenticrun/audit.go controller/agenticrun/audit_test.go

# OTEL attribute keys
perl -pi -e '
  s/"proposal\.name"/"agenticrun.name"/g;
  s/"proposal\.namespace"/"agenticrun.namespace"/g;
  s/"proposal\.uid"/"agenticrun.uid"/g;
  s/"proposal\.request"/"agenticrun.request"/g;
' controller/agenticrun/audit.go controller/agenticrun/audit_test.go

# Audit payload map key "proposal" → "agenticrun"
perl -pi -e 's/"proposal":\s*serialized/"agenticrun": serialized/g' controller/agenticrun/audit.go
```

- [ ] **Step 10: Phase 9 — Controller name, tracer name**

```bash
perl -pi -e 's/Named\("proposal"\)/Named("agenticrun")/g' controller/agenticrun/reconciler.go
```

The tracer name string `"github.com/openshift/lightspeed-agentic-operator/controller/proposal"` was already updated by Phase 6 import path replacement.

- [ ] **Step 11: Phase 10 — Label value**

```bash
perl -pi -e 's|agentic\.openshift\.io/proposal|agentic.openshift.io/run|g' $ALL_FILES
```

- [ ] **Step 12: Phase 11 — JSON schema property `"proposal"` → `"remediationPlan"` in schemas.go and test mock**

```bash
perl -pi -e 's/"proposal"/"remediationPlan"/g' controller/agenticrun/schemas.go controller/agenticrun/schemas_test.go
perl -pi -e 's/"proposal"/"remediationPlan"/g' test/agent/main.go
```

- [ ] **Step 13: Phase 12 — CEL validation messages**

```bash
perl -pi -e 's/proposal is required when diagnosis is present/remediationPlan is required when diagnosis is present/g' api/v1alpha1/agenticrun_analysis_types.go
perl -pi -e 's/diagnosis is required when proposal is present/diagnosis is required when remediationPlan is present/g' api/v1alpha1/agenticrun_analysis_types.go
```

- [ ] **Step 14: Phase 13 — CLI command names and help text**

```bash
# The cobra Use field and aliases (these are lowercase strings, not Go identifiers)
perl -pi -e 's/Use:\s*"proposal"/Use:   "run"/g' cli/run/*.go
perl -pi -e 's/"proposals", "prop"/"runs"/g' cli/run/*.go

# Help text
perl -pi -e 's/agentic proposals/agentic runs/g' cli/run/*.go cli/root.go
perl -pi -e 's/Agentic proposals/Agentic runs/g' cli/run/*.go cli/root.go
perl -pi -e 's/AgenticRun resources/AgenticRun resources/g' cli/root.go

# User-facing output: "proposal/NAME" → "run/NAME"
perl -pi -e 's|proposal/%s|run/%s|g' cli/run/*.go

# Suspend/status messages
perl -pi -e 's/proposals will be terminated/runs will be terminated/g' cli/system/suspend.go
perl -pi -e 's/proposals emergency-stopped/runs emergency-stopped/g' cli/system/status_test.go
perl -pi -e 's/proposals to terminate/runs to terminate/g' cli/system/status_test.go
```

- [ ] **Step 15: Phase 14 — Controller log messages**

```bash
perl -pi -e 's/list proposals/list runs/g' controller/agenticolsconfig/reconciler.go
perl -pi -e 's/proposals to terminate/runs to terminate/g' controller/agenticolsconfig/reconciler.go
perl -pi -e 's/proposals emergency-stopped/runs emergency-stopped/g' controller/agenticolsconfig/reconciler.go
```

- [ ] **Step 16: Phase 15 — Broad variable rename `proposal` → `run`**

Now that all specific string patterns are handled, the remaining lowercase `proposal` references are Go variable/parameter names.

```bash
# Controller Go files
perl -pi -e 's/\bproposal\b/run/g' controller/agenticrun/*.go

# CLI Go files
perl -pi -e 's/\bproposal\b/run/g' cli/run/*.go

# E2E test files
perl -pi -e 's/\bproposal\b/run/g' test/e2e/*.go

# cmd/main.go and controller/setup.go use proposal as import reference
perl -pi -e 's/\bproposal\b/agenticrun/g' cmd/main.go controller/setup.go

# agenticolsconfig reconciler
perl -pi -e 's/\bproposal\b/run/g' controller/agenticolsconfig/reconciler.go
```

- [ ] **Step 17: Phase 16 — Kubernetes resource plural names**

Order matters: `proposalapprovals` before `proposals` before `proposal`.

```bash
# kubebuilder RBAC markers (Go comments)
perl -pi -e 's/resources=proposalapprovals/resources=agenticrunapprovals/g' controller/agenticrun/reconciler.go
perl -pi -e 's/resources=proposals/resources=agenticruns/g' controller/agenticrun/reconciler.go controller/agenticolsconfig/reconciler.go

# CLI GVR
perl -pi -e 's/WithResource\("proposals"\)/WithResource("agenticruns")/g' cli/run/*.go

# RBAC YAML
perl -pi -e 's/proposalapprovals/agenticrunapprovals/g' config/rbac/run_approver_role.yaml
perl -pi -e 's/proposals/agenticruns/g' config/rbac/run_approver_role.yaml

# Webhook YAML
perl -pi -e 's/proposalapproval-mutator/agenticrunapproval-mutator/g' config/webhook/manifests.yaml
perl -pi -e 's|/mutate-proposalapproval|/mutate-agenticrunapproval|g' config/webhook/manifests.yaml controller/setup.go
perl -pi -e 's/proposalapprovals/agenticrunapprovals/g' config/webhook/manifests.yaml

# RBAC role/binding names
perl -pi -e 's/agentic-proposal-approver/agentic-run-approver/g' config/rbac/run_approver_role.yaml config/rbac/run_approver_binding.yaml

# Kustomization file references
perl -pi -e 's/proposal_approver_role\.yaml/run_approver_role.yaml/g' config/rbac/kustomization.yaml
perl -pi -e 's/proposal_approver_binding\.yaml/run_approver_binding.yaml/g' config/rbac/kustomization.yaml
perl -pi -e 's/agentic_v1alpha1_proposal\.yaml/agentic_v1alpha1_agenticrun.yaml/g' config/samples/kustomization.yaml
perl -pi -e 's/agentic_v1alpha1_proposalapproval\.yaml/agentic_v1alpha1_agenticrunapproval.yaml/g' config/samples/kustomization.yaml
```

- [ ] **Step 18: Phase 17 — Hack scripts**

```bash
HACK_FILES=$(find hack -type f \( -name '*.sh' -o -name '*.yaml' -o -name '*.md' \))

perl -pi -e 's/proposalapprovals/agenticrunapprovals/g' $HACK_FILES
perl -pi -e 's/proposalapproval/agenticrunapproval/g' $HACK_FILES
perl -pi -e 's/proposals/agenticruns/g' $HACK_FILES

perl -pi -e 's/agentic-proposal-approver/agentic-run-approver/g' $HACK_FILES

perl -pi -e 's/agentic\.openshift\.io_proposalapprovals/agentic.openshift.io_agenticrunapprovals/g' $HACK_FILES
perl -pi -e 's/agentic\.openshift\.io_proposals/agentic.openshift.io_agenticruns/g' $HACK_FILES

perl -pi -e 's/proposalapproval-mutator/agenticrunapproval-mutator/g' $HACK_FILES
perl -pi -e 's|/mutate-proposalapproval|/mutate-agenticrunapproval|g' $HACK_FILES

# Remaining lowercase "proposal" in hack scripts
perl -pi -e 's/\bproposal\b/run/g' $HACK_FILES
```

- [ ] **Step 19: Phase 18 — Dockerfile labels**

```bash
perl -pi -e 's/agentic proposal workflow/agentic run workflow/g' Dockerfile
perl -pi -e 's/agentic proposal workflows/agentic run workflows/g' Dockerfile
```

- [ ] **Step 20: Phase 19 — Tekton pipeline**

```bash
perl -pi -e 's/proposalapprovals/agenticrunapprovals/g; s/proposals/agenticruns/g' .tekton/integration-tests/pipelines/agentic-operator-e2e-pipeline.yaml
```

- [ ] **Step 21: Verify — no remaining `proposal` references**

```bash
echo "=== Go files ==="
grep -rn -i 'proposal' --include='*.go' . | grep -v '.git/' | grep -v 'vendor/' | grep -v 'docs/superpowers/' | grep -v 'Proposed' | grep -v 'ProposedAction' | head -20 || echo "Clean"

echo "=== YAML files ==="
grep -rn -i 'proposal' --include='*.yaml' --include='*.yml' . | grep -v '.git/' | grep -v 'docs/superpowers/' | head -20 || echo "Clean"

echo "=== Scripts ==="
grep -rn -i 'proposal' --include='*.sh' . | grep -v '.git/' | head -20 || echo "Clean"

echo "=== Dockerfile ==="
grep -i 'proposal' Dockerfile || echo "Clean"
```

Fix any remaining references. `Proposed` and `ProposedAction` are expected survivors.

---

### Task 3: Regenerate manifests and build

**Files:**
- Regenerate: `config/crd/bases/agentic.openshift.io_agenticruns.yaml` (new)
- Regenerate: `config/crd/bases/agentic.openshift.io_agenticrunapprovals.yaml` (new)
- Regenerate: `config/rbac/role.yaml`
- Delete: `config/crd/bases/agentic.openshift.io_proposals.yaml` (old)
- Delete: `config/crd/bases/agentic.openshift.io_proposalapprovals.yaml` (old)
- Modify: `config/crd/kustomization.yaml`

**Interfaces:**
- Consumes: all source renames from Tasks 1-2
- Produces: generated CRDs with new Kind names, clean build

- [ ] **Step 1: Run `make manifests`**

```bash
make manifests
```

Expected: generates new CRD files and RBAC role.yaml.

- [ ] **Step 2: Delete old CRD YAML files**

```bash
git rm -f config/crd/bases/agentic.openshift.io_proposals.yaml 2>/dev/null || true
git rm -f config/crd/bases/agentic.openshift.io_proposalapprovals.yaml 2>/dev/null || true
```

- [ ] **Step 3: Update CRD kustomization**

```bash
perl -pi -e 's/agentic\.openshift\.io_proposals\.yaml/agentic.openshift.io_agenticruns.yaml/g' config/crd/kustomization.yaml
perl -pi -e 's/agentic\.openshift\.io_proposalapprovals\.yaml/agentic.openshift.io_agenticrunapprovals.yaml/g' config/crd/kustomization.yaml
```

- [ ] **Step 4: Verify CRD content**

```bash
grep 'kind: AgenticRun' config/crd/bases/agentic.openshift.io_agenticruns.yaml | head -3
grep 'kind: AgenticRunApproval' config/crd/bases/agentic.openshift.io_agenticrunapprovals.yaml | head -3
grep 'agenticruns\|agenticrunapprovals' config/rbac/role.yaml | head -10
```

- [ ] **Step 5: Build**

```bash
make build
```

If compilation fails, read each error and fix. The most common issues will be:
- Missed variable or function renames — grep the error message to find the remaining `proposal` reference and fix it
- Import alias mismatches — verify the import alias matches the package name
- Re-run `make build` after each fix

- [ ] **Step 6: Vet and format check**

```bash
make vet
make fmt-check
```

Fix any issues. `gofmt -w .` for formatting, then re-check.

---

### Task 4: Run unit tests

**Interfaces:**
- Consumes: clean build from Task 3
- Produces: all unit tests passing

- [ ] **Step 1: Run unit tests**

```bash
make test
```

- [ ] **Step 2: Fix test failures**

If tests fail, examine each failure. Common causes:
- Test assertion strings still containing `"proposal"` where `"run"` or `"agenticrun"` is expected
- JSON field names in test fixtures using old names
- Mock data with old schema field names
- Test helper function names

Fix each failure and re-run `make test`.

- [ ] **Step 3: Final comprehensive audit**

```bash
grep -rn 'proposal' --include='*.go' --include='*.yaml' --include='*.sh' . \
  | grep -vi 'proposed\|ProposedAction' \
  | grep -v '.git/' | grep -v 'vendor/' | grep -v 'docs/superpowers/' \
  | head -30 || echo "All clean"
```

Any remaining references should be fixed.

- [ ] **Step 4: Commit**

```bash
git add -A
git status
git commit -m "OLS-3475: rename Proposal CRD to AgenticRun

Mechanical rename across the codebase:
- Proposal → AgenticRun, ProposalApproval → AgenticRunApproval
- ProposalResult → RemediationPlan
- CLI: oc-agentic proposal → oc-agentic run
- RBAC: agentic-proposal-approver → agentic-run-approver
- Labels: agentic.openshift.io/proposal → agentic.openshift.io/run
- Audit: audit.agenticrun.* prefix
- OTEL: agenticrun.* span prefix
- JSON fields: proposalName → agenticRunName, maxConcurrentProposals → maxConcurrentRuns
- RemediationOption.Proposal → RemediationOption.RemediationPlan (json: remediationPlan)"
```
