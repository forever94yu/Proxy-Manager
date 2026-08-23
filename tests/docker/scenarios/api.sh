#!/bin/bash
# Scenario: exercise the non-interactive control-plane API.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=tests/docker/lib/common.sh
source "${SCRIPT_DIR}/../lib/common.sh"

scenario_api() {
	local old_name new_name password output
	old_name="$(unique_client_name api)"
	new_name="$(unique_client_name api_updated)"
	password="ApiPass_123!"

	log "Scenario: api (${old_name} -> ${new_name})"
	output=$(run_api_with_input "" deploy "${DOCKER_TEST_SERVER_IP}" 3128 1080 1.1.1.1 1.0.0.1)
	grep -q $'PM\tresult\tdeployed' <<<"${output}" || return 1
	grep -q $'PM\tservice\tactive' <<<"${output}" || return 1

	output=$(run_api_with_input "${password}" user-add "${old_name}")
	grep -q $'PM\tresult\tcreated' <<<"${output}" || return 1
	user_exists_in_container "${old_name}" || return 1

	output=$(run_api_with_input "" inspect)
	grep -q $'PM\tinstalled\ttrue' <<<"${output}" || return 1
	grep -q $'PM\tservice\tactive' <<<"${output}" || return 1
	grep -q $'PM\tuser\t'"${old_name}" <<<"${output}" || return 1

	output=$(run_api_with_input "${password}" user-update "${old_name}" "${new_name}")
	grep -q $'PM\tresult\tupdated' <<<"${output}" || return 1
	if user_exists_in_container "${old_name}"; then
		log_error "Old user still exists after update: ${old_name}"
		return 1
	fi
	user_exists_in_container "${new_name}" || return 1

	output=$(run_api_with_input "" service stop)
	grep -q $'PM\tservice\tinactive' <<<"${output}" || return 1
	output=$(run_api_with_input "" service start)
	grep -q $'PM\tservice\tactive' <<<"${output}" || return 1

	output=$(run_api_with_input "" user-delete "${new_name}")
	grep -q $'PM\tresult\tdeleted' <<<"${output}" || return 1
	if user_exists_in_container "${new_name}"; then
		log_error "User still exists after API deletion: ${new_name}"
		return 1
	fi

	log "Scenario api: passed"
}

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
	check_prerequisites
	if [[ -z "${CONTAINER_NAME:-}" ]]; then
		log_error "CONTAINER_NAME must be set"
		exit 1
	fi
	scenario_api
fi
