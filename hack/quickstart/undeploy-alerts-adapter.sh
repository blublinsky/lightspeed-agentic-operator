#!/usr/bin/env bash
#
# Remove the agentic alerts adapter standalone workload.
# Can be called from uninstall.sh or run independently.
#
# Usage:
#   bash hack/quickstart/undeploy-alerts-adapter.sh

set -euo pipefail

NAMESPACE="${NAMESPACE:-openshift-lightspeed}"
ADAPTER_NAME="lightspeed-agentic-alerts-adapter"

info()  { echo "  ✓ $*"; }
step()  { echo "[alerts-adapter] $*"; }

step "Deleting alerts adapter resources"

oc delete deployment "${ADAPTER_NAME}" -n "${NAMESPACE}" --ignore-not-found
oc delete configmap alerts-adapter-config -n "${NAMESPACE}" --ignore-not-found
oc delete sa "${ADAPTER_NAME}" -n "${NAMESPACE}" --ignore-not-found
oc delete clusterrolebinding "${ADAPTER_NAME}-agenticruns" --ignore-not-found ||
  echo "  ! Warning: could not delete ClusterRoleBinding (managed cluster?)"
oc delete clusterrole "${ADAPTER_NAME}-agenticruns" --ignore-not-found ||
  echo "  ! Warning: could not delete ClusterRole (managed cluster?)"
oc delete rolebinding "${ADAPTER_NAME}-alertmanager" -n openshift-monitoring --ignore-not-found ||
  echo "  ! Warning: could not delete RoleBinding in openshift-monitoring (managed cluster?)"

info "Alerts adapter resources deleted"
