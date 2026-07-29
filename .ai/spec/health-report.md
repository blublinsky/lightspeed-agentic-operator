# Spec health report

Last evaluated: 2026-07-27
Trigger: post OLS-3685/OLS-3686 implementation (SandboxManager unification, config cache)
Layout: software (.ai/spec/)

## Stale

1. **what/crd-api.md rule 18** — States "Agent — `status.conditions`: Observed readiness; `Ready` condition documents whether referenced provider resources are accessible (see operator reconcile behavior)." No Agent reconciler exists in the codebase; the operator only reconciles `AgenticRun` CRs. Rule 18 should be marked `[PLANNED]` or reworded to clarify this is aspirational rather than implemented behavior.

## Missing

1. **Console plugin behavioral rules** — `controller/console/` deploys a console plugin (Deployment, Service, ConfigMap, ConsolePlugin CR, Console activation), but no what/ file defines behavioral rules for this component. It is only documented in how/reconciler.md as implementation detail. Consider adding a `what/console-plugin.md` if the console deployment has rules worth specifying (idempotency, image absence handling, activation semantics).

2. **`pkg/configuration/` spec** — The new configuration cache package (`Config`, `Cache`, `OnConfigMapChange`) has no dedicated spec file. Covered in `docs/inter-operator-handoff-design.md` but not in `.ai/spec/`. Consider adding `how/configuration.md` if the package grows.

## Structural concerns

1. **how/reconciler.md size** — At 180 lines, this file covers both `controller/agenticrun/` (large) and `controller/console/` (small). This is acceptable given the console section is only ~10 lines, but if the console component grows it should be split into `how/console.md`.

2. **how/reconciler.md entry point section** — The `cmd/main.go` description (lines 9-15) partially duplicates the new `how/project-structure.md` entry points section. The reconciler.md section should reference project-structure.md for the main binary and focus only on the controller setup flow.

## Findability issues

None. The cross-reference table in README.md provides clear mapping between what/ and how/ files. The quick-start table covers all common entry points.

## No issues

- All spec files have real content (no empty templates or placeholders).
- All `controller/agenticrun/` source files listed in how/reconciler.md module map exist on disk. Deleted files (`sandbox.go`, `bare_pod_manager.go`, `sandbox_templates.go`) removed from map.
- All `cli/run/` source files listed in how/cli.md module map exist on disk.
- All template files (`*.tmpl`) listed in how/reconciler.md exist.
- All CRD types in `api/v1alpha1/*_types.go` are covered by what/crd-api.md.
- Behavioral rules are numbered sequentially in all what/ files.
- `[PLANNED: OLS-XXXX]` markers are used consistently across all what/ files.
- Constraints sections present in all what/ files.
- `CLAUDE.md` has spec pointer.
- `ARCHITECTURE.md` exists at project root.
- Layer READMEs removed; content absorbed into main README.
