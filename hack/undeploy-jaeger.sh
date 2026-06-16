#!/usr/bin/env bash
# Remove Jaeger all-in-one dev deployment (OLS-3024)
set -euo pipefail

NAMESPACE="${1:-observability}"

echo "==> Removing Jaeger from namespace '$NAMESPACE'"

oc delete route jaeger-ui -n "$NAMESPACE" --ignore-not-found
oc delete svc jaeger-ui jaeger-otlp-grpc jaeger-otlp-http -n "$NAMESPACE" --ignore-not-found
oc delete deployment jaeger -n "$NAMESPACE" --ignore-not-found
oc delete project "$NAMESPACE" --ignore-not-found

echo "==> Done"
