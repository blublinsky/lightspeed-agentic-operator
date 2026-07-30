#!/usr/bin/env bash
#
# Deploy a minimal Postgres instance for the OTEL collector audit backend.
# Uses emptyDir for data — sufficient for dev/test, not for production.
#
# Usage:
#   bash hack/quickstart/deploy-postgres.sh
#
# Prerequisites:
#   - oc CLI on PATH, logged into an OpenShift cluster
#   - Namespace openshift-lightspeed exists

set -euo pipefail

NAMESPACE="${NAMESPACE:-openshift-lightspeed}"
POSTGRES_IMAGE="registry.redhat.io/rhel9/postgresql-16@sha256:42f385ac3c9b8913426da7c57e70bc6617cd237aaf697c667f6385a8c0b0118b"

PG_NAME="lightspeed-postgres-server"
PG_SECRET="lightspeed-postgres-secret"
PG_BOOTSTRAP_SECRET="lightspeed-postgres-bootstrap"
PG_CONFIGMAP="lightspeed-postgres-conf"
PG_CERTS_SECRET="lightspeed-postgres-certs"
PG_DATA_DIR="/var/lib/pgsql/data/userdata"

info()  { echo "  ✓ $*"; }
step()  { echo "[postgres] $*"; }

step "Deploying Postgres to ${NAMESPACE}"

# --- Generate random password ------------------------------------------------

PG_PASSWORD="$(openssl rand -base64 16)"

# --- Apply all resources ------------------------------------------------------

oc apply -f - <<EOF
apiVersion: v1
kind: ServiceAccount
metadata:
  name: ${PG_NAME}
  namespace: ${NAMESPACE}
  labels:
    app: ${PG_NAME}
    app.kubernetes.io/name: ${PG_NAME}
    app.kubernetes.io/component: postgres
---
apiVersion: v1
kind: Secret
metadata:
  name: ${PG_SECRET}
  namespace: ${NAMESPACE}
  labels:
    app: ${PG_NAME}
    app.kubernetes.io/name: ${PG_NAME}
    app.kubernetes.io/component: postgres
type: Opaque
stringData:
  password: "${PG_PASSWORD}"
---
apiVersion: v1
kind: Secret
metadata:
  name: ${PG_BOOTSTRAP_SECRET}
  namespace: ${NAMESPACE}
  labels:
    app: ${PG_NAME}
    app.kubernetes.io/name: ${PG_NAME}
    app.kubernetes.io/component: postgres
type: Opaque
stringData:
  create-extensions.sh: |
    #!/bin/bash
    cat /var/lib/pgsql/data/userdata/postgresql.conf
    echo "attempting to create extensions and schemas if they do not exist"
    _psql () { psql --set ON_ERROR_STOP=1 "\$@" ; }
    echo "CREATE EXTENSION IF NOT EXISTS pg_trgm;" | _psql -d \$POSTGRESQL_DATABASE
    echo "CREATE SCHEMA IF NOT EXISTS quota;" | _psql -d \$POSTGRESQL_DATABASE
    echo "CREATE SCHEMA IF NOT EXISTS conversation_cache;" | _psql -d \$POSTGRESQL_DATABASE
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: ${PG_CONFIGMAP}
  namespace: ${NAMESPACE}
  labels:
    app: ${PG_NAME}
    app.kubernetes.io/name: ${PG_NAME}
    app.kubernetes.io/component: postgres
data:
  postgresql.conf.sample: |
    huge_pages = off
    ssl = on
    ssl_cert_file = '/etc/certs/tls.crt'
    ssl_key_file = '/etc/certs/tls.key'
    ssl_ca_file = '/etc/certs/cm-olspostgresca/service-ca.crt'
---
apiVersion: v1
kind: Service
metadata:
  name: ${PG_NAME}
  namespace: ${NAMESPACE}
  labels:
    app: ${PG_NAME}
    app.kubernetes.io/name: ${PG_NAME}
    app.kubernetes.io/component: postgres
  annotations:
    service.beta.openshift.io/serving-cert-secret-name: ${PG_CERTS_SECRET}
spec:
  selector:
    app: ${PG_NAME}
  type: ClusterIP
  ports:
  - name: server
    port: 5432
    targetPort: server
    protocol: TCP
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: ${PG_NAME}
  namespace: ${NAMESPACE}
  labels:
    app: ${PG_NAME}
    app.kubernetes.io/name: ${PG_NAME}
    app.kubernetes.io/component: postgres
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app: ${PG_NAME}
  template:
    metadata:
      labels:
        app: ${PG_NAME}
        app.kubernetes.io/name: ${PG_NAME}
        app.kubernetes.io/component: postgres
    spec:
      serviceAccountName: ${PG_NAME}
      terminationGracePeriodSeconds: 60
      securityContext:
        runAsNonRoot: true
        seccompProfile:
          type: RuntimeDefault
      containers:
      - name: ${PG_NAME}
        image: ${POSTGRES_IMAGE}
        imagePullPolicy: IfNotPresent
        ports:
        - name: server
          containerPort: 5432
          protocol: TCP
        securityContext:
          allowPrivilegeEscalation: false
          capabilities:
            drop:
            - ALL
        env:
        - name: POSTGRESQL_USER
          value: postgres
        - name: POSTGRESQL_DATABASE
          value: postgres
        - name: POSTGRESQL_ADMIN_PASSWORD
          valueFrom:
            secretKeyRef:
              name: ${PG_SECRET}
              key: password
        - name: POSTGRESQL_PASSWORD
          valueFrom:
            secretKeyRef:
              name: ${PG_SECRET}
              key: password
        - name: POSTGRESQL_SHARED_BUFFERS
          value: "256MB"
        - name: POSTGRESQL_MAX_CONNECTIONS
          value: "2000"
        resources:
          requests:
            cpu: 30m
            memory: 300Mi
        lifecycle:
          preStop:
            exec:
              command:
              - /bin/sh
              - -c
              - "if [ -f ${PG_DATA_DIR}/PG_VERSION ]; then pg_ctl stop -D ${PG_DATA_DIR} -m fast -w -t 55; fi"
        volumeMounts:
        - name: tls-certs
          mountPath: /etc/certs
          readOnly: true
        - name: bootstrap
          mountPath: /opt/app-root/src/postgresql-start/create-extensions.sh
          subPath: create-extensions.sh
          readOnly: true
        - name: config
          mountPath: /usr/share/pgsql/postgresql.conf.sample
          subPath: postgresql.conf.sample
        - name: data
          mountPath: /var/lib/pgsql/data
        - name: run
          mountPath: /var/run/postgresql
        - name: tmp
          mountPath: /tmp
        - name: postgres-ca
          mountPath: /etc/certs/cm-olspostgresca
          readOnly: true
      volumes:
      - name: tls-certs
        secret:
          secretName: ${PG_CERTS_SECRET}
          defaultMode: 0640
      - name: bootstrap
        secret:
          secretName: ${PG_BOOTSTRAP_SECRET}
          defaultMode: 0640
      - name: config
        configMap:
          name: ${PG_CONFIGMAP}
          defaultMode: 0644
      - name: data
        emptyDir: {}
      - name: run
        emptyDir: {}
      - name: tmp
        emptyDir: {}
      - name: postgres-ca
        configMap:
          name: openshift-service-ca.crt
          defaultMode: 0644
EOF

info "Postgres resources applied"

step "Waiting for rollout..."
oc rollout status deployment/${PG_NAME} -n "${NAMESPACE}" --timeout=120s

info "Postgres deployed successfully"
info "Service: ${PG_NAME}.${NAMESPACE}.svc:5432"
info "Password stored in secret: ${PG_SECRET}"
