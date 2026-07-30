#!/usr/bin/env bash
#
# Remove the Postgres instance deployed by deploy-postgres.sh.

set -euo pipefail

NAMESPACE="${NAMESPACE:-openshift-lightspeed}"

echo "[postgres] Removing Postgres from ${NAMESPACE}..."

oc delete deployment lightspeed-postgres-server -n "${NAMESPACE}" --ignore-not-found
oc delete service lightspeed-postgres-server -n "${NAMESPACE}" --ignore-not-found
oc delete configmap lightspeed-postgres-conf -n "${NAMESPACE}" --ignore-not-found
oc delete secret lightspeed-postgres-bootstrap -n "${NAMESPACE}" --ignore-not-found
oc delete secret lightspeed-postgres-secret -n "${NAMESPACE}" --ignore-not-found
oc delete secret lightspeed-postgres-certs -n "${NAMESPACE}" --ignore-not-found
oc delete sa lightspeed-postgres-server -n "${NAMESPACE}" --ignore-not-found

echo "  ✓ Postgres removed"
