#!/bin/bash

HELM_OPTS=${HELM_OPTS:-""}
NAMESPACE=${NAMESPACE:-sealos}
RELEASE_NAME=${RELEASE_NAME:-sealos-state-metrics}
DASHBOARD_NAME="${RELEASE_NAME}-dashboard"

if kubectl api-resources --api-group=grafana.integreatly.org --namespaced=true -o name | grep -qx grafanadashboards.grafana.integreatly.org; then
    kubectl delete grafanadashboard.grafana.integreatly.org "${DASHBOARD_NAME}" -n "${NAMESPACE}" --ignore-not-found
fi

kubectl delete configmap "${DASHBOARD_NAME}" -n "${NAMESPACE}" --ignore-not-found

helm upgrade -i "${RELEASE_NAME}" \
    -n "${NAMESPACE}" --create-namespace \
    ./charts/sealos-state-metrics \
    ${HELM_OPTS}
