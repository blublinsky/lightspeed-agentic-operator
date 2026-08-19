# Inter-Operator Configuration Handoff — Design

OLS-3685 / OLS-3572

## Problem Statement

The agentic operator currently receives its sandbox configuration through CLI flags (`--sandbox-mode`, `--agentic-sandbox-image`) and reads OTEL connectivity from a dedicated ConfigMap (`lightspeed-otel-collector-client`). This approach has several issues:

- The sandbox image and mode are hardcoded at operator startup and cannot be updated without restarting the operator.
- OTEL configuration is split between a ConfigMap (endpoints, CA) and operator flags (image), with no single source of truth.
- As both operators move toward a single OLM bundle, there is no startup ordering guarantee. The classic operator may not have reconciled yet when the agentic operator starts.

## Design

### ConfigMap Contract

The classic operator (lightspeed-operator) produces a `lightspeed-agentic-configuration` ConfigMap in the shared namespace once `OLSConfig` is reconciled through Phase 2.

| Key | Type | Description |
|-----|------|-------------|
| `sandbox-mode` | string | `bare-pod` or `sandbox-claim` |
| `sandbox-pod-spec` | JSON string | Serialized `corev1.PodSpec` — image, resources, tolerations, nodeSelector, emptyDir volumes |
| `otel-collector-endpoint` | string | OTLP gRPC endpoint (`host:port`) |
| `otel-admin-endpoint` | string | HTTPS admin API base URL |
| `otel-ca-secret` | string | Name of the Secret holding OTEL client CA cert |
| `mcp-endpoint` | string | OpenShift MCP HTTPS endpoint URL (present when introspection enabled) |
| `mcp-ca-secret` | string | Name of the Secret holding MCP client CA cert (present when introspection enabled) |

Additionally, the classic operator owns two CA Secrets:
- `lightspeed-agentic-otel-ca` (key: `otel-ca.crt`)
- `lightspeed-agentic-mcp-ca` (key: `mcp-ca.crt`)

### Lifecycle: No Blocking, Graceful Degradation

The ConfigMap is not available at operator startup. It is created only when an admin creates an `OLSConfig` CR and the classic operator reconciles it. On a fresh install, this could take minutes, hours, or may never happen until the admin acts.

The agentic operator must **not** block startup on the ConfigMap. Instead:

1. **Operator starts normally.** Health and readiness probes pass. The manager runs. Controllers register.
2. **ConfigMap watch is registered at startup.** The `configwatch.Watcher` watches `lightspeed-agentic-configuration` via informers.
3. **Cached config starts as nil.** The sandbox config cache is initially empty.
4. **Individual AgenticRuns are skipped** when the config is nil. The reconciler checks `Cache.Available()` before processing; if false, it logs and returns without updating the run's status. The run is retried on every subsequent reconcile until the config appears.
5. **ConfigMap appears.** The watcher fires, the handler parses and caches the config. Subsequent reconciles proceed normally.
6. **ConfigMap updates.** Cache is refreshed. Next reconcile picks up the new base PodSpec, mode, or OTEL config.
7. **ConfigMap deleted.** Cache returns to nil. New reconciles fail with the same clear error. In-flight runs are not affected.

This is the standard Kubernetes pattern: the controller is always running, individual reconciles fail when prerequisites are missing.

### What This Replaces

| Before | After |
|--------|-------|
| `--sandbox-mode` CLI flag | `sandbox-mode` key in ConfigMap |
| `--agentic-sandbox-image` CLI flag | Image from `sandbox-pod-spec` in ConfigMap |
| `lightspeed-otel-collector-client` ConfigMap (startup blocking + runtime watch) | `otel-collector-endpoint`, `otel-admin-endpoint`, `otel-ca-secret` keys in ConfigMap |
| Inline CA PEM in OTEL ConfigMap | CA loaded from Secret referenced by `otel-ca-secret` |

### What This Does NOT Change

- **MCP endpoint/CA wiring** from the ConfigMap is deferred. The keys are parsed and cached but not consumed yet.

### Config Cache Design

The `pkg/configuration/` package provides:

- **`Config` struct** with `Sandbox` (`Mode`, `PodSpec`), `OTEL` (`CollectorEndpoint`, `AdminEndpoint`, `CASecretName`), and `MCP` (`Endpoint`, `CASecretName`) sub-configs.
- **`Cache`** — thread-safe holder using `atomic.Pointer[Config]`. Starts nil. Populated by `OnConfigMapChange` handler.
- **`Cache.Available() bool`** — returns whether the config has been loaded.
- **`Cache.OnConfigMapChange`** — `configwatch.Handler`-compatible callback. Parses the ConfigMap, updates the cache, and reconfigures the OTEL provider.

The cache is created at startup (empty) and passed to the `SandboxManager`, reconciler, and telemetry provider. The `configwatch.Watcher` populates it when the ConfigMap is first seen.

### Sandbox Manager (OLS-3686)

PodSpec construction is unified into a single `SandboxManager` with two public methods:

- **`Create(ctx, run, step, agent, llm, tools, deadline, query, agentCtx)`** — fully encapsulates sandbox setup: creates a per-step ServiceAccount (`sandboxSAName` → `ls-{step}-{namespace}-{runUID}`, using the run's UID for collision-resistant fixed-length naming), adds the SA to reader ClusterRoleBindings, creates execution RBAC for the execution step, builds and creates the input ConfigMap, reads the base PodSpec from the config cache, overlays agent-specific configuration via `PodSpecBuilder`, then creates either a bare Pod or SandboxClaim+SandboxTemplate depending on `cfg.Sandbox.Mode`. Sets SA owner reference to pod/claim for GC.
- **`Release(ctx, run, step)`** — deletes the pod/claim (GC cascades to SA, ConfigMap, result RBAC via owner refs), removes per-step SA from reader CRBs, and for execution: explicitly deletes cross-namespace Roles/ClusterRoles. Routes by `cfg.Sandbox.Mode`. Idempotent (NotFound is not an error).

Both bare pods and sandbox-claim resources carry OwnerReferences to the AgenticRun for garbage collection. `WaitReady` was removed — the operator watches for pod completion and Result CR creation via `Owns()` watches.

The `SandboxLifecycle` interface (`Create`/`Release`) decouples the `SandboxAgentCaller` from the concrete `SandboxManager` for testability.

### OTEL Migration

The telemetry provider currently reads from `lightspeed-otel-collector-client` with inline CA PEM. With this change:

- The telemetry provider reads OTEL endpoints from the sandbox config cache.
- The CA certificate is loaded from the Secret named in `otel-ca-secret`, not from inline ConfigMap data.
- The startup `WaitFor` call on the old ConfigMap is removed. Telemetry starts disabled and activates when the config cache is populated with valid OTEL endpoints and the CA Secret is available. No startup blocking, no timeout, no fatal error on missing OTEL config.
- If OTEL config is incomplete or the CA Secret is unavailable, telemetry remains disabled. This replaces the old behavior where `audit-logging.md` rule 26 and `templog.md` rule 4 mandated blocking startup on `lightspeed-otel-collector-client`. Both specs should be updated to reference `lightspeed-agentic-configuration` as the OTEL source.

### Changes by File

| File | Change |
|------|--------|
| `cmd/main.go` | Remove `--sandbox-mode`, `--agentic-sandbox-image`, `--image-pull-policy` flags. Create `configuration.Cache`, register ConfigMap watcher. Wire `SandboxManager` → `SandboxAgentCaller` → reconciler. Inline `lightspeed-agent` SA creation (unconditional). |
| `pkg/configuration/` (new) | `Config` struct, `Cache` with `atomic.Pointer`, `OnConfigMapChange` handler, ConfigMap parser. Replaces old `pkg/sandboxconfig/`. |
| `pkg/configwatch/` (new) | Generic ConfigMap watcher utility using controller-runtime informers. |
| `pkg/telemetry/provider.go` | Renamed to `pkg/configuration/provider.go`. Accepts config updates from `Cache.OnConfigMapChange`. |
| `controller/agenticrun/sandbox_manager.go` (new) | Unified `SandboxManager` with `Create`/`Release`. Fully encapsulates SA, RBAC, ConfigMap, and pod lifecycle. Replaces `bare_pod_manager.go` and `sandbox.go`. |
| `controller/agenticrun/sandbox_agent.go` | `SandboxLifecycle` interface (`Create`/`Release`). `SandboxAgentCaller` uses `Sandbox` field via `launchSandbox`. |
| `controller/agenticrun/podspec_builder.go` | `Build` takes base `*PodSpec` + agent config, returns overlaid `*PodSpec`. Internal to `SandboxManager`. Shared constants/helpers moved here from deleted `sandbox_templates.go`. |
| `controller/agenticrun/reconciler.go` | Config guard: skips reconciliation when `Cache.Available()` is false. |
| Deleted `controller/setup.go` | Logic inlined into `cmd/main.go`. |
| Deleted `controller/agenticrun/bare_pod_manager.go` | Replaced by `sandbox_manager.go`. |
| Deleted `controller/agenticrun/sandbox.go` | Replaced by `sandbox_manager.go`. |
| Deleted `controller/agenticrun/sandbox_templates.go` | Dead code — `EnsureAgentTemplate` replaced by inline template creation in `SandboxManager`. Shared helpers moved to `podspec_builder.go`. |
