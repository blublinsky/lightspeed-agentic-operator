#!/usr/bin/env bash
#
# Deploy the agentic console plugin as a standalone workload.
# Can be called from install.sh or run independently.
#
# Usage:
#   bash hack/quickstart/deploy-console.sh
#   bash hack/quickstart/deploy-console.sh --image=quay.io/my-org/my-console:tag
#
# Prerequisites:
#   - oc CLI on PATH, logged into an OpenShift 4.22+ cluster
#   - Namespace openshift-lightspeed exists
#
# Flags:
#   --image=IMAGE   Console plugin image (default: Konflux :main)

set -euo pipefail

NAMESPACE="${NAMESPACE:-openshift-lightspeed}"
CONSOLE_IMAGE="quay.io/redhat-user-workloads/crt-nshift-lightspeed-tenant/lightspeed-agentic-console:main"

while [ $# -gt 0 ]; do
  case "$1" in
    --image=*) CONSOLE_IMAGE="${1#*=}"; shift ;;
    --image)   [ $# -lt 2 ] && { echo "Missing value for $1" >&2; exit 1; }; CONSOLE_IMAGE="$2"; shift 2 ;;
    *) echo "Unknown flag: $1" >&2; exit 1 ;;
  esac
done

PLUGIN_NAME="lightspeed-agentic-console-plugin"
PLUGIN_PORT=9443
CERT_SECRET="${PLUGIN_NAME}-cert"

info()  { echo "  ✓ $*"; }
step()  { echo "[console] $*"; }

step "Deploying agentic console plugin to ${NAMESPACE}"
step "Image: ${CONSOLE_IMAGE}"

oc apply -f - <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: ${PLUGIN_NAME}
  namespace: ${NAMESPACE}
  labels:
    app.kubernetes.io/name: ${PLUGIN_NAME}
    app.kubernetes.io/component: console
data:
  nginx.conf: |
    pid /tmp/nginx/nginx.pid;
    error_log /dev/stdout info;
    events {}
    http {
      client_body_temp_path /tmp/nginx/client_body;
      proxy_temp_path       /tmp/nginx/proxy;
      fastcgi_temp_path     /tmp/nginx/fastcgi;
      uwsgi_temp_path       /tmp/nginx/uwsgi;
      scgi_temp_path        /tmp/nginx/scgi;
      include               /etc/nginx/mime.types;
      default_type          application/octet-stream;
      keepalive_timeout     65;
      server {
        listen              ${PLUGIN_PORT} ssl;
        listen              [::]:${PLUGIN_PORT} ssl;
        ssl_certificate     /var/cert/tls.crt;
        ssl_certificate_key /var/cert/tls.key;
        root                /usr/share/nginx/html;
        access_log          /dev/stdout;
      }
    }
---
apiVersion: v1
kind: ServiceAccount
metadata:
  name: ${PLUGIN_NAME}
  namespace: ${NAMESPACE}
  labels:
    app.kubernetes.io/name: ${PLUGIN_NAME}
    app.kubernetes.io/component: console
---
apiVersion: v1
kind: Service
metadata:
  name: ${PLUGIN_NAME}
  namespace: ${NAMESPACE}
  labels:
    app.kubernetes.io/name: ${PLUGIN_NAME}
    app.kubernetes.io/component: console
  annotations:
    service.beta.openshift.io/serving-cert-secret-name: ${CERT_SECRET}
spec:
  selector:
    app.kubernetes.io/name: ${PLUGIN_NAME}
  ports:
    - name: https
      port: ${PLUGIN_PORT}
      targetPort: ${PLUGIN_PORT}
      protocol: TCP
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ${PLUGIN_NAME}
  namespace: ${NAMESPACE}
  labels:
    app.kubernetes.io/name: ${PLUGIN_NAME}
    app.kubernetes.io/component: console
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: ${PLUGIN_NAME}
  template:
    metadata:
      labels:
        app.kubernetes.io/name: ${PLUGIN_NAME}
    spec:
      serviceAccountName: ${PLUGIN_NAME}
      securityContext:
        runAsNonRoot: true
        seccompProfile:
          type: RuntimeDefault
      containers:
      - name: console
        image: ${CONSOLE_IMAGE}
        imagePullPolicy: Always
        ports:
        - containerPort: ${PLUGIN_PORT}
          protocol: TCP
        securityContext:
          allowPrivilegeEscalation: false
          capabilities:
            drop:
            - ALL
        resources:
          requests:
            cpu: 10m
            memory: 50Mi
          limits:
            memory: 100Mi
        volumeMounts:
        - name: cert
          mountPath: /var/cert
          readOnly: true
        - name: nginx-conf
          mountPath: /etc/nginx/nginx.conf
          subPath: nginx.conf
          readOnly: true
        - name: nginx-tmp
          mountPath: /tmp/nginx
      volumes:
      - name: cert
        secret:
          secretName: ${CERT_SECRET}
      - name: nginx-conf
        configMap:
          name: ${PLUGIN_NAME}
      - name: nginx-tmp
        emptyDir: {}
---
apiVersion: console.openshift.io/v1
kind: ConsolePlugin
metadata:
  name: ${PLUGIN_NAME}
  labels:
    app.kubernetes.io/name: ${PLUGIN_NAME}
    app.kubernetes.io/component: console
spec:
  displayName: "OpenShift Lightspeed Agentic Console Plugin"
  backend:
    type: Service
    service:
      name: ${PLUGIN_NAME}
      namespace: ${NAMESPACE}
      port: ${PLUGIN_PORT}
      basePath: "/"
  i18n:
    loadType: Preload
EOF

info "Console plugin resources applied"

step "Activating console plugin"
existing_plugins="$(oc get console.operator.openshift.io cluster -o jsonpath='{.spec.plugins[*]}' 2>/dev/null || echo "")"
if echo " ${existing_plugins} " | grep -q " ${PLUGIN_NAME} "; then
  info "Plugin already registered — skipping"
else
  if ! oc patch console.operator.openshift.io cluster --type=json \
    -p "[{\"op\": \"add\", \"path\": \"/spec/plugins/-\", \"value\": \"${PLUGIN_NAME}\"}]" 2>/dev/null; then
    oc patch console.operator.openshift.io cluster --type=json \
      -p "[{\"op\": \"add\", \"path\": \"/spec/plugins\", \"value\": [\"${PLUGIN_NAME}\"]}]"
  fi
  info "Console plugin activated"
fi

step "Waiting for rollout..."
oc rollout status deployment/${PLUGIN_NAME} -n "${NAMESPACE}" --timeout=120s

info "Console plugin deployed successfully"
info "Note: Console plugin requires OpenShift 4.22+"
