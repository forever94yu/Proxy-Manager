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

	scenario_api_traffic_policy || return 1

	log "Scenario api: passed"
}

# Usage limits: per-user policy lines, enforcement by leaving disabled users out
# of the active configuration, counter re-allocation on a new accounting
# period, and traffic reports.
scenario_api_traffic_policy() {
	local name password output index_before index_after
	name="$(unique_client_name quota)"
	password="QuotaPass_123!"
	log "Scenario: api traffic policy (${name})"

	output=$(run_api_with_input "${password}"$'\n'"enabled 100 0" user-add "${name}")
	grep -q $'PM\tresult\tcreated' <<<"${output}" || return 1
	grep -q $'PM\ttraffic_report\tv1' <<<"${output}" || return 1
	grep -q $'PM\tnode_user\t'"${name}" <<<"${output}" || return 1
	grep -qE $'^PM\ttraffic\t'"${name}"$'\tenabled\t100\t0\t[0-9]+\t[0-9]+$' <<<"${output}" || return 1
	container_grep -q "^countall [0-9]*/${name} N 100 ${name}$" /etc/3proxy/3proxy.cfg || return 1
	container_grep -q '^counter /etc/3proxy/3proxy.counters$' /etc/3proxy/3proxy.cfg || return 1

	output=$(run_api_with_input "" traffic)
	grep -qE $'^PM\ttraffic\t'"${name}"$'\tenabled\t100\t0\t' <<<"${output}" || return 1
	index_before=$(awk -F'\t' -v user="${name}" '$2 == "traffic" && $3 == user { print $7 }' <<<"${output}")

	# Disabling keeps the account but removes it from the active configuration.
	output=$(run_api_with_input "${name} disabled 100 0" policy-apply)
	grep -q $'PM\tresult\tapplied' <<<"${output}" || return 1
	user_exists_in_container "${name}" || return 1
	if container_grep -q "^users ${name}:" /etc/3proxy/3proxy.cfg; then
		log_error "Disabled user is still active: ${name}"
		return 1
	fi
	stub_is_active_in_container || return 1

	output=$(run_api_with_input "${name} disabled 100 0" policy-apply)
	grep -q $'PM\tresult\tunchanged' <<<"${output}" || return 1

	# A new accounting period re-enables the account on a fresh counter pair.
	output=$(run_api_with_input "${name} enabled 200 1" policy-apply)
	grep -q $'PM\tresult\tapplied' <<<"${output}" || return 1
	container_grep -q "^users ${name}:" /etc/3proxy/3proxy.cfg || return 1
	index_after=$(awk -F'\t' -v user="${name}" '$2 == "traffic" && $3 == user { print $7 }' <<<"${output}")
	if [[ -z ${index_before} || -z ${index_after} || ${index_before} == "${index_after}" ]]; then
		log_error "New period did not allocate a new counter (${index_before} -> ${index_after})"
		return 1
	fi
	grep -qE $'^PM\ttraffic\t'"${name}"$'\tenabled\t200\t1\t'"${index_after}"$'\t0$' <<<"${output}" || return 1

	# Deleting the account reports the final value of its counter.
	output=$(run_api_with_input "" user-delete "${name}")
	grep -q $'PM\tresult\tdeleted' <<<"${output}" || return 1
	grep -qE $'^PM\ttraffic\t'"${name}"$'\tremoved\t200\t1\t'"${index_after}"$'\t[0-9]+$' <<<"${output}" || return 1
	if container_grep -q "${name}" /etc/3proxy/3proxy.cfg.policy; then
		log_error "Policy entry survived account deletion: ${name}"
		return 1
	fi

	log "Scenario api traffic policy: passed"
}

if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
	check_prerequisites
	if [[ -z "${CONTAINER_NAME:-}" ]]; then
		log_error "CONTAINER_NAME must be set"
		exit 1
	fi
	scenario_api
fi
