# Unifying Approach to RBAC Generation

**Status:** Accepted  
**Jira:** OLS-3794

## Problem Statement

The current implementation of the agentic sandbox is structured specifically for oc/kubectl command execution. In reality, agentic systems are expected to use MCP servers — in addition to pure oc/kubectl — to interact with the cluster and external services.

When adding MCP servers, one of the most complex questions is how to generate proper RBAC for tool execution. This document describes a unifying approach that keeps RBAC derivation grounded in the well-understood kubectl command → API mapping, regardless of the execution mechanism.

## Current State

The current analysis prompt derives RBAC by walking through each kubectl/oc command in the remediation script and mapping it to Kubernetes API verbs and resources. This works well for kubectl-based remediations but does not account for MCP tools:

- When MCP tools are available, the analysis agent may propose MCP-based remediations
- MCP tool names (e.g., `patch_resource`, `scale_deployment`) don't appear in the kubectl → API mapping table
- The agent has no instruction to derive RBAC from MCP tool calls

Without guidance, the agent either skips RBAC derivation for MCP-based steps or guesses — both leading to runtime permission failures.

## The Two Languages of Cluster Operations

OpenShift/Kubernetes exposes a single control plane: the API server. Every mutation — scaling a deployment, patching a ConfigMap, restarting a rollout — is an API call authenticated and authorized through RBAC.

There are two ways to speak to this API:

- **kubectl/oc** — the low-level language. Each command maps directly to one or a small number of API calls. It is the *assembler* of OpenShift operations: explicit, granular, and fully transparent in what permissions it requires.

- **MCP tools** — a high-level language for the same purpose. A single MCP tool call translates to one or more kubectl-equivalent operations. Regardless of how an MCP server is implemented internally (Go client, REST calls, shell commands), it ultimately makes the same API calls to the same API server. **Constraint:** this model assumes the MCP server authenticates to the API server using the sandbox pod's projected ServiceAccount token (e.g. an in-pod sidecar or a process using the mounted token). A shared network-service MCP server that authenticates with its own identity would bypass the per-step RBAC boundary; such MCP servers require their own authorization model and are out of scope for this approach.

This distinction is purely syntactic. The underlying operations — and therefore the RBAC requirements — are identical.

RBAC is a property of *what you do*, not which language you use to describe the actions. A deployment scale operation requires `get` and `update` on `deployments/scale` whether the agent runs `kubectl scale deployment/foo --replicas=3` or calls an MCP tool named `scale_deployment(name="foo", replicas=3)`.

Since every MCP tool that modifies OpenShift resources must go through the Kubernetes API — there is no other path — every MCP operation can be expressed as equivalent kubectl commands.

## The Approach

Add one step to the existing analysis workflow. The current flow is unchanged; MCP is layered on top:

### Step 1: Derive remediation and RBAC (similar to current implementation)

The analysis agent does exactly what it does today:

1. Inspect the cluster using MCP tools, kubectl/oc (read-only). If an appropriate MCP tool is available, prefer it over raw oc/kubectl commands
2. Diagnose the root cause
3. Write a remediation script as ordered kubectl/oc commands
4. Derive RBAC from those commands using the command → API mapping rules
5. Produce a verification plan

RBAC derivation remains anchored to kubectl commands. This is the proven, well-understood path.

### Step 2: Rewrite to MCP tools (new, conditional)

If MCP tools are available, the analysis agent rewrites the remediation script using available MCP tools where they provide a clearer or more reliable execution path.

Rules for rewriting:

- **Prefer MCP over raw commands.** If MCP tools are available, rewrite the remediation script preferring MCP tool calls over raw oc/kubectl commands. If a kubectl command has no MCP equivalent, keep it as-is.
- **RBAC is already complete.** The RBAC derived in step 1 covers all underlying API calls. The MCP rewrite does not add or remove RBAC rules.

The rewritten script is returned for execution. The purely kubectl-based script is an intermediate representation required for defining RBAC and creating the final script.

### Why This Works

| Property | Explanation |
|----------|-------------|
| **Same API server** | MCP tools hit the same k8s API as kubectl. No bypass. |
| **Same SA token** | MCP servers in the sandbox authenticate with the execution ServiceAccount token. Same identity, same RBAC evaluation. |
| **RBAC is complete before rewrite** | Step 1 derives RBAC from the full kubectl script covering all API operations. Step 2 only changes syntax. |
| **No over- or under-provisioning** | A single MCP call replacing three kubectl commands doesn't change RBAC — all three are already covered. A kubectl command replaced by one MCP call doesn't remove RBAC — the permission is still needed. |
| **Backward compatible** | Without MCP tools, the flow is identical to today. |

## Execution

The execution agent receives a remediation script that may contain MCP tool calls, oc/kubectl commands, or both. It follows the same ordered execution rules regardless of how each step is expressed.

The execution SA has been granted the RBAC derived in the analysis step. The permissions are evaluated by the API server at call time, regardless of whether the call originates from an MCP tool or a kubectl command.

## Example

**Scenario:** A deployment in namespace `production` has the wrong image tag.

### Step 1 output (kubectl + RBAC)

```bash
kubectl set image deployment/web-app container=registry.example.com/web:v2.1 -n production
kubectl rollout status deployment/web-app -n production --timeout=120s
```

Derived RBAC:
```yaml
- apiGroups: ["apps"]
  resources: ["deployments"]
  verbs: ["get", "patch"]
  resourceNames: ["web-app"]
- apiGroups: ["apps"]
  resources: ["deployments"]
  verbs: ["list", "watch"]
- apiGroups: ["apps"]
  resources: ["replicasets"]
  verbs: ["get", "list"]
```

> **Note:** `list` and `watch` on a collection cannot be restricted by `resourceNames`, which is why they require a separate rule.

### Step 2 output (MCP rewrite)

```json
[
  {
    "tool": "openshift.patch_resource",
    "args": {
      "kind": "Deployment",
      "name": "web-app",
      "namespace": "production",
      "patch": {"spec": {"template": {"spec": {"containers": [{"name": "container", "image": "registry.example.com/web:v2.1"}]}}}}
    }
  },
  {
    "tool": "openshift.wait_for_rollout",
    "args": {
      "kind": "Deployment",
      "name": "web-app",
      "namespace": "production",
      "timeout": "120s"
    }
  }
]
```

RBAC: unchanged from step 1. The MCP tools make the same API calls.

## Relationship to Architecture Spec

This document describes the **current** RBAC derivation strategy: the analysis agent maps kubectl commands to API verbs at analysis time. `docs/architecture-redesign-spec.md` describes a **planned future** approach (Phase B deterministic execution) where RBAC is derived from real 403 responses at execution time rather than LLM reasoning. The two approaches are complementary, not contradictory — this document covers the current implementation; the architecture spec covers the target state. When Phase B lands, this document should be updated or superseded.

## Scope and Limitations

- **OpenShift/Kubernetes MCP tools only.** This approach applies to MCP servers that operate on the cluster API using the sandbox pod's projected ServiceAccount token. MCP servers that authenticate with their own identity bypass per-step RBAC and are out of scope. MCP servers that interact with external systems (PagerDuty, Slack, monitoring APIs) have their own authentication and do not require Kubernetes RBAC.
- **MCP tool fidelity.** The agent must correctly map kubectl commands to MCP tool calls. This depends on the quality of MCP tool descriptions provided in the prompt context.
- **No RBAC reduction.** The approach intentionally keeps RBAC at the kubectl-derived level. Even if an MCP tool combines multiple operations into one call, the full set of permissions is granted. This is safer (no under-provisioning) at the cost of minimal over-provisioning.
