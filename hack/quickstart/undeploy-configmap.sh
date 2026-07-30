#!/usr/bin/env bash
#
# Remove the agentic configuration ConfigMap and OTEL CA secret.
# Can be called from uninstall.sh or run independently.
#
# Usage:
#   bash hack/quickstart/undeploy-configmap.sh

set -euo pipefail

NAMESPACE="${NAMESPACE:-openshift-lightspeed}"

info()  { echo "  ✓ $*"; }
step()  { echo "[configmap] $*"; }

step "Deleting agentic configuration resources"

oc delete configmap lightspeed-agentic-configuration -n "${NAMESPACE}" --ignore-not-found
oc delete secret lightspeed-agentic-otel-ca -n "${NAMESPACE}" --ignore-not-found

info "Configuration resources deleted"
