# Quickstart

Deploy Agentic OLS onto an OpenShift cluster. All components are deployed
directly — no OLM bundle or `operator-sdk` required.

## Prerequisites

- A checkout of this repository (scripts reference each other via relative paths)
- `oc` CLI on PATH, logged into an OpenShift 4.22+ cluster
- cluster-admin privileges
- `python3` and `curl` on PATH (for automatic image resolution from Quay)
- `openssl` on PATH (for Postgres password generation, only with `--postgres`)

## Install

Run the all-in-one installer:

```bash
bash hack/quickstart/install.sh
```

Override any image:

```bash
bash hack/quickstart/install.sh \
  --operator-image=quay.io/.../lightspeed-agentic-operator:on-pr-<sha> \
  --sandbox-image=quay.io/.../lightspeed-agentic-sandbox:main
```

Deploy with Postgres backend for OTEL audit logs:

```bash
bash hack/quickstart/install.sh --postgres
```

Or deploy components individually (run from the repo root — scripts
call each other via `hack/quickstart/`):

```bash
bash hack/quickstart/deploy-otel.sh
bash hack/quickstart/deploy-alerts-adapter.sh
bash hack/quickstart/deploy-configmap.sh
bash hack/quickstart/deploy-operator.sh
bash hack/quickstart/deploy-console.sh
```

## Uninstall

```bash
bash hack/quickstart/uninstall.sh
```

Skip the confirmation prompt:

```bash
bash hack/quickstart/uninstall.sh --force
```

Or remove components individually (run from the repo root):

```bash
bash hack/quickstart/undeploy-console.sh
bash hack/quickstart/undeploy-operator.sh
bash hack/quickstart/undeploy-configmap.sh
bash hack/quickstart/undeploy-alerts-adapter.sh
bash hack/quickstart/undeploy-otel.sh
```

## CLI flags

### install.sh

| Flag | Description |
|------|-------------|
| `--operator-image=IMAGE` | Agentic operator image (default: Konflux `:main`) |
| `--sandbox-image=IMAGE` | Sandbox image for the ConfigMap PodSpec (default: Konflux `:main`) |
| `--console-image=IMAGE` | Console plugin image (default: Konflux `:main`) |
| `--alerts-adapter-image=IMAGE` | Alerts adapter image (default: resolved from Quay) |
| `--otel-image=IMAGE` | OTEL collector image (default: resolved from Quay) |
| `--postgres` | Deploy Postgres backend for OTEL audit logs |

### Individual scripts

| Script | Flag | Default |
|--------|------|---------|
| `deploy-operator.sh` | `--image=IMAGE` | Konflux `:main` |
| `deploy-console.sh` | `--image=IMAGE` | Konflux `:main` |
| `deploy-alerts-adapter.sh` | `--image=IMAGE` | Resolved from Quay (newest git SHA tag) |
| `deploy-otel.sh` | `--image=IMAGE` | Resolved from Quay (newest `on-pr-*` tag) |
| `deploy-otel.sh` | `--postgres` | Deploy Postgres backend for audit logs |
| `deploy-configmap.sh` | `--sandbox-image=IMAGE` | Konflux `:main` |

Images with a Konflux `:main` tag use it directly. Images without a `:main`
tag (alerts adapter, OTEL collector) are resolved from Quay at runtime by
finding the most recent build tag.

## Image resolution

The **operator**, **sandbox**, and **console** images are built by this repo's
Konflux push pipeline and have a floating `:main` tag.

The **alerts adapter** and **OTEL collector** are built by the same Konflux
tenant (`crt-nshift-lightspeed-tenant`). They do not have `:main` tags, so the
deploy scripts query the Quay API to find the latest build automatically.

To use a specific PR build of the agentic operator:

```bash
bash hack/quickstart/deploy-operator.sh \
  --image=quay.io/redhat-user-workloads/crt-nshift-lightspeed-tenant/lightspeed-agentic-operator:on-pr-<commit-sha>
```

## Components

| Component | Script | What it deploys |
|-----------|--------|-----------------|
| OTEL collector | `deploy-otel.sh` | Collector accepting OTLP with TLS via service-ca; optionally `--postgres` for audit log storage |
| Postgres | `deploy-postgres.sh` | Minimal Postgres instance (emptyDir), auto-deployed by `deploy-otel.sh --postgres` |
| Alerts adapter | `deploy-alerts-adapter.sh` | Polls Alertmanager, creates AgenticRuns, RBAC for agenticruns + alertmanager |
| Configuration | `deploy-configmap.sh` | `lightspeed-agentic-configuration` ConfigMap + OTEL CA secret |
| Operator | `deploy-operator.sh` | CRDs, SA, cluster-admin, Deployment, agent RBAC, ApprovalPolicy, webhook |
| Console | `deploy-console.sh` | Console plugin with nginx + TLS, registers with OpenShift Console |

## LLM Provider Examples

The [`examples/`](examples/) directory contains LLMProvider + Agent templates:

| File | Provider |
|------|----------|
| [`openai.yaml`](examples/openai.yaml) | OpenAI (direct API) |
| [`vertex-anthropic.yaml`](examples/vertex-anthropic.yaml) | Vertex AI with Claude |
| [`vertex-google.yaml`](examples/vertex-google.yaml) | Vertex AI with Gemini |

## CLI Plugin

Install the `oc-agentic` CLI plugin to manage agenticruns from the command line
([install instructions](../../README.md#install)).

```bash
oc agentic version
oc agentic run create --request="Deploy a test nginx workload" --target-namespaces=default
oc agentic run list
```
