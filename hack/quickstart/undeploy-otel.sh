#!/usr/bin/env bash
#
# Remove the quickstart OTEL collector.
# Can be called from uninstall.sh or run independently.
#
# Usage:
#   bash hack/quickstart/undeploy-otel.sh

set -euo pipefail

NAMESPACE="${NAMESPACE:-openshift-lightspeed}"
COLLECTOR_NAME="lightspeed-otel-collector"

info()  { echo "  ✓ $*"; }
step()  { echo "[otel] $*"; }

step "Deleting OTEL collector resources"

oc delete deployment "${COLLECTOR_NAME}" -n "${NAMESPACE}" --ignore-not-found
oc delete service "${COLLECTOR_NAME}" -n "${NAMESPACE}" --ignore-not-found
oc delete configmap "${COLLECTOR_NAME}-config" -n "${NAMESPACE}" --ignore-not-found
oc delete sa "${COLLECTOR_NAME}" -n "${NAMESPACE}" --ignore-not-found
oc delete secret "${COLLECTOR_NAME}-cert" -n "${NAMESPACE}" --ignore-not-found
oc delete secret "${COLLECTOR_NAME}-postgres-dsn" -n "${NAMESPACE}" --ignore-not-found

info "OTEL collector resources deleted"

if oc get deployment lightspeed-postgres-server -n "${NAMESPACE}" >/dev/null 2>&1; then
  step "Removing Postgres backend..."
  SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  bash "${SCRIPT_DIR}/undeploy-postgres.sh"
fi
