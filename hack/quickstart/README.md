# Quickstart

Deploy Agentic OLS onto an OpenShift cluster using pre-built Konflux images.
No building, no cloning required.

## Prerequisites

- `oc` CLI on PATH
- Logged into the target OpenShift cluster
- cluster-admin privileges

## Install

```bash
bash <(curl -sL https://raw.githubusercontent.com/openshift/lightspeed-agentic-operator/main/hack/quickstart/install.sh)
```

The script installs CRDs, deploys the operator, and creates an ApprovalPolicy.
After completion it prints instructions for configuring an LLM provider and
submitting a test run.

## Uninstall

```bash
bash <(curl -sL https://raw.githubusercontent.com/openshift/lightspeed-agentic-operator/main/hack/quickstart/uninstall.sh)
```

Skip the confirmation prompt with `QUICKSTART_FORCE=1`.

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `NAMESPACE` | `openshift-lightspeed` | Target namespace |
| `OPERATOR_IMAGE` | Konflux `:main` | Operator container image |
| `SANDBOX_IMAGE` | Konflux `:main` | Agent sandbox container image |
| `SANDBOX_MODE` | `bare-pod` | Sandbox mode (`bare-pod` or `sandbox-claim`) |
| `IMAGE_PULL_POLICY` | *(empty — Kubernetes default)* | Image pull policy for operator and sandbox pods (`Always`, `IfNotPresent`, `Never`) |

Example with overrides:

```bash
NAMESPACE=my-ns SANDBOX_MODE=sandbox-claim \
  bash <(curl -sL https://raw.githubusercontent.com/openshift/lightspeed-agentic-operator/main/hack/quickstart/install.sh)
```

For dev environments with floating tags like `:main`, force fresh pulls:

```bash
IMAGE_PULL_POLICY=Always \
  bash <(curl -sL https://raw.githubusercontent.com/openshift/lightspeed-agentic-operator/main/hack/quickstart/install.sh)
```

## LLM Provider Examples

The [`examples/`](examples/) directory contains LLMProvider + Agent templates:

| File | Provider |
|---|---|
| [`openai.yaml`](examples/openai.yaml) | OpenAI (direct API) |
| [`vertex-anthropic.yaml`](examples/vertex-anthropic.yaml) | Vertex AI with Claude |
| [`vertex-google.yaml`](examples/vertex-google.yaml) | Vertex AI with Gemini |

## CLI Plugin

Install the `oc-agentic` CLI plugin to manage agenticruns from the command line
([install instructions](../../README.md#install)).

Verify installation:

```bash
oc agentic version
```

## Example AgenticRun

[`deploy-test-workload.yaml`](examples/deploy-test-workload.yaml) submits a
run that analyzes the target namespace and deploys a test workload
(nginx Deployment + Service). Execution requires manual approval via
`AgenticRunApproval`.

### Using the CLI

Instead of applying YAML, you can create and manage agenticruns with the CLI:

```bash
# Create a run
oc agentic run create --request="Deploy a test nginx workload" --target-namespaces=default

# Watch it progress
oc agentic run list
oc agentic run get <name>

# Approve analysis, then execution
oc agentic run approve <name> --stage=analysis
oc agentic run approve <name> --stage=execution --option=0

# Stream sandbox logs
oc agentic run logs <name> -f

# Check system status
oc agentic status
```

See the [CLI reference](../../README.md#cli-plugin-oc-agentic) for all commands and flags.
