#!/usr/bin/env bash
#
# Quickstart installer for Agentic OLS.
# Deploys all required components directly — no OLM bundle needed.
#
# Usage:
#   bash hack/quickstart/install.sh
#   bash hack/quickstart/install.sh --operator-image=quay.io/... --sandbox-image=quay.io/...
#
# Prerequisites:
#   - oc CLI on PATH, logged into an OpenShift 4.22+ cluster
#   - cluster-admin privileges
#
# Flags:
#   --operator-image=IMAGE          Agentic operator image
#   --sandbox-image=IMAGE           Sandbox image for the ConfigMap PodSpec
#   --console-image=IMAGE           Console plugin image
#   --alerts-adapter-image=IMAGE    Alerts adapter image
#   --otel-image=IMAGE              OTEL collector image
#   --postgres                      Deploy Postgres backend for OTEL audit logs
#   -h, --help                      Show this help and exit

set -euo pipefail

NAMESPACE="${NAMESPACE:-openshift-lightspeed}"

OPERATOR_IMAGE=""
SANDBOX_IMAGE=""
CONSOLE_IMAGE=""
ALERTS_ADAPTER_IMAGE=""
OTEL_IMAGE=""
WITH_POSTGRES=0

usage() {
  cat <<'EOF'
Usage: bash install.sh [options]

Options:
  --operator-image=IMAGE          Agentic operator image (default: Konflux :main)
  --sandbox-image=IMAGE           Sandbox image (default: Konflux :main)
  --console-image=IMAGE           Console plugin image (default: Konflux :main)
  --alerts-adapter-image=IMAGE    Alerts adapter image (default: Konflux :main)
  --otel-image=IMAGE              OTEL collector image (default: Konflux :main)
  --postgres                      Deploy Postgres backend for OTEL audit logs
  -h, --help                      Show this help and exit
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --operator-image=*)        OPERATOR_IMAGE="${1#*=}"; shift ;;
    --operator-image)          [ $# -lt 2 ] && { echo "Missing value for $1" >&2; exit 1; }; OPERATOR_IMAGE="$2"; shift 2 ;;
    --sandbox-image=*)         SANDBOX_IMAGE="${1#*=}"; shift ;;
    --sandbox-image)           [ $# -lt 2 ] && { echo "Missing value for $1" >&2; exit 1; }; SANDBOX_IMAGE="$2"; shift 2 ;;
    --console-image=*)         CONSOLE_IMAGE="${1#*=}"; shift ;;
    --console-image)           [ $# -lt 2 ] && { echo "Missing value for $1" >&2; exit 1; }; CONSOLE_IMAGE="$2"; shift 2 ;;
    --alerts-adapter-image=*)  ALERTS_ADAPTER_IMAGE="${1#*=}"; shift ;;
    --alerts-adapter-image)    [ $# -lt 2 ] && { echo "Missing value for $1" >&2; exit 1; }; ALERTS_ADAPTER_IMAGE="$2"; shift 2 ;;
    --otel-image=*)            OTEL_IMAGE="${1#*=}"; shift ;;
    --otel-image)              [ $# -lt 2 ] && { echo "Missing value for $1" >&2; exit 1; }; OTEL_IMAGE="$2"; shift 2 ;;
    --postgres)                WITH_POSTGRES=1; shift ;;
    -h|--help)                 usage; exit 0 ;;
    *)                         echo "Unknown flag: $1 (try --help)" >&2; exit 1 ;;
  esac
done

info()  { echo "  ✓ $*"; }
step()  { echo ""; echo "=== $* ==="; }
fail()  { echo "  ✗ $*" >&2; exit 1; }

# Locate the script directory (for calling sibling scripts).
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# --- Prerequisites -----------------------------------------------------------

step "1/7 Checking prerequisites"

command -v oc >/dev/null 2>&1 || fail "oc CLI not found. Install it first."
command -v python3 >/dev/null 2>&1 || fail "python3 not found. Required by deploy scripts."
command -v openssl >/dev/null 2>&1 || fail "openssl not found. Required by deploy scripts."
info "Required CLI tools found"

for script in deploy-otel.sh deploy-alerts-adapter.sh deploy-configmap.sh deploy-operator.sh deploy-console.sh; do
  [ -f "${SCRIPT_DIR}/${script}" ] || fail "Missing script: ${SCRIPT_DIR}/${script}. Run from a repo checkout."
done
info "All deploy scripts present"

oc whoami >/dev/null 2>&1 || fail "Not logged into a cluster. Run: oc login ..."
info "Logged in as $(oc whoami)"

if ! oc auth can-i create clusterrolebindings >/dev/null 2>&1; then
  fail "Current user lacks cluster-admin privileges."
fi
info "cluster-admin privileges confirmed"

# --- Namespace ---------------------------------------------------------------

step "2/7 Ensuring namespace ${NAMESPACE}"

if oc get namespace "${NAMESPACE}" >/dev/null 2>&1; then
  info "Namespace already exists"
else
  oc create namespace "${NAMESPACE}"
  info "Namespace created"
fi

# --- OTEL collector ----------------------------------------------------------

step "3/7 Deploying OTEL collector"

OTEL_ARGS=""
if [ -n "${OTEL_IMAGE}" ]; then
  OTEL_ARGS="--image=${OTEL_IMAGE}"
fi
if [ "${WITH_POSTGRES}" = "1" ]; then
  OTEL_ARGS="${OTEL_ARGS} --postgres"
fi
bash "${SCRIPT_DIR}/deploy-otel.sh" ${OTEL_ARGS}

# --- Configuration ConfigMap -------------------------------------------------

step "4/7 Creating agentic configuration"

if [ -n "${SANDBOX_IMAGE}" ]; then
  bash "${SCRIPT_DIR}/deploy-configmap.sh" --sandbox-image="${SANDBOX_IMAGE}"
else
  bash "${SCRIPT_DIR}/deploy-configmap.sh"
fi

# --- Agentic operator (installs CRDs — must precede alerts adapter) ----------

step "5/7 Deploying agentic operator"

if [ -n "${OPERATOR_IMAGE}" ]; then
  bash "${SCRIPT_DIR}/deploy-operator.sh" --image="${OPERATOR_IMAGE}"
else
  bash "${SCRIPT_DIR}/deploy-operator.sh"
fi

# --- Alerts adapter ----------------------------------------------------------

step "6/7 Deploying alerts adapter"

if [ -n "${ALERTS_ADAPTER_IMAGE}" ]; then
  bash "${SCRIPT_DIR}/deploy-alerts-adapter.sh" --image="${ALERTS_ADAPTER_IMAGE}"
else
  bash "${SCRIPT_DIR}/deploy-alerts-adapter.sh"
fi

# --- Console plugin ----------------------------------------------------------

step "7/7 Deploying console plugin"

if [ -n "${CONSOLE_IMAGE}" ]; then
  bash "${SCRIPT_DIR}/deploy-console.sh" --image="${CONSOLE_IMAGE}"
else
  bash "${SCRIPT_DIR}/deploy-console.sh"
fi

# --- Done --------------------------------------------------------------------

GITHUB_RAW="https://raw.githubusercontent.com/openshift/lightspeed-agentic-operator/main"

# Prefer local examples when running from a checkout.
if [ -d "${SCRIPT_DIR}/../../hack/quickstart/examples" ]; then
  EXAMPLES_BASE="$(cd "${SCRIPT_DIR}/../.." && pwd)/hack/quickstart/examples"
else
  EXAMPLES_BASE="${GITHUB_RAW}/hack/quickstart/examples"
fi

cat <<DONE

════════════════════════════════════════════════════════════════
  Agentic OLS installed successfully!

  Namespace: ${NAMESPACE}

  NEXT: Configure your agentic LLM provider. Pick one:

  ── Vertex AI / Claude ─────────────────────────────────────
  export GOOGLE_APPLICATION_CREDENTIALS=/path/to/your/service-account-key.json
  oc create secret generic llm-creds-vertex -n ${NAMESPACE} \\
    --from-file=GOOGLE_APPLICATION_CREDENTIALS="\$GOOGLE_APPLICATION_CREDENTIALS"
  oc apply -f ${EXAMPLES_BASE}/vertex-anthropic.yaml

  ── Vertex AI / Gemini ─────────────────────────────────────
  export GOOGLE_APPLICATION_CREDENTIALS=/path/to/your/service-account-key.json
  oc create secret generic llm-creds-vertex -n ${NAMESPACE} \\
    --from-file=GOOGLE_APPLICATION_CREDENTIALS="\$GOOGLE_APPLICATION_CREDENTIALS"
  oc apply -f ${EXAMPLES_BASE}/vertex-google.yaml

  ── OpenAI ─────────────────────────────────────────────────
  oc create secret generic llm-creds-openai -n ${NAMESPACE} \\
    --from-literal=OPENAI_API_KEY=sk-...
  oc apply -f ${EXAMPLES_BASE}/openai.yaml

  ── Then submit an example run ────────────────────────
  oc apply -f ${EXAMPLES_BASE}/namespace-inventory.yaml
  oc get agenticruns -n ${NAMESPACE} -w

  ── To uninstall ───────────────────────────────────────────
  bash hack/quickstart/uninstall.sh

════════════════════════════════════════════════════════════════
DONE
