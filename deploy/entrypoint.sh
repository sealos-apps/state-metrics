#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

RELEASE_NAME=${RELEASE_NAME:-"sealos-state-metrics"}
RELEASE_NAMESPACE=${RELEASE_NAMESPACE:-"sealos"}
CHART_PATH=${CHART_PATH:-"${SCRIPT_DIR}/charts/sealos-state-metrics"}
HELM_OPTS=${HELM_OPTS:-""}
PACKAGED_APP_VALUES_FILE=${PACKAGED_APP_VALUES_FILE:-${CHART_APP_VALUES_FILE:-"${CHART_PATH}/sealos-state-metrics-values.yaml"}}
USER_VALUES_FILE=${USER_VALUES_FILE:-"/root/.sealos/cloud/values/core/sealos-state-metrics-values.yaml"}

[ -d "${CHART_PATH}" ] || {
  echo "chart directory not found: ${CHART_PATH}" >&2
  exit 1
}

[ -f "${PACKAGED_APP_VALUES_FILE}" ] || {
  echo "packaged values file not found: ${PACKAGED_APP_VALUES_FILE}" >&2
  exit 1
}

mkdir -p "$(dirname "${USER_VALUES_FILE}")"

if [ ! -f "${USER_VALUES_FILE}" ]; then
  cp "${PACKAGED_APP_VALUES_FILE}" "${USER_VALUES_FILE}"
  echo "Generated default user values at ${USER_VALUES_FILE}"
fi

echo "Using user values from ${USER_VALUES_FILE}"

cleanup_grafana_dashboard() {
  local dashboard_name="${RELEASE_NAME}-dashboard"

  command -v kubectl >/dev/null 2>&1 || return 0

  if kubectl api-resources --api-group=grafana.integreatly.org --namespaced=true -o name 2>/dev/null | grep -qx grafanadashboards.grafana.integreatly.org; then
    kubectl delete grafanadashboard.grafana.integreatly.org "${dashboard_name}" -n "${RELEASE_NAMESPACE}" --ignore-not-found || true
  fi

  kubectl delete configmap "${dashboard_name}" -n "${RELEASE_NAMESPACE}" --ignore-not-found || true
}

cleanup_grafana_dashboard

# HELM_OPTS intentionally accepts additional Helm flags supplied by the installer.
# shellcheck disable=SC2086
helm upgrade -i "${RELEASE_NAME}" "${CHART_PATH}" \
  -n "${RELEASE_NAMESPACE}" --create-namespace \
  -f "${USER_VALUES_FILE}" \
  ${HELM_OPTS}
