#!/bin/bash

# Secure 3proxy server installer
# https://github.com/a0s/3proxy-install

umask 077

readonly PROXY3_SOURCE_COMMIT="7c1bc48c853f99f2574deb61fb9347a5d3056ad0"
readonly PROXY3_SOURCE_URL="https://github.com/z3apa3a/3proxy/archive/${PROXY3_SOURCE_COMMIT}.tar.gz"
readonly PROXY3_SOURCE_DIR="3proxy-${PROXY3_SOURCE_COMMIT}"
readonly DEFAULT_HTTP_PORT=3128
readonly DEFAULT_SOCKS_PORT=1080
readonly PROXY3_BINARY="/usr/local/bin/3proxy"
readonly PROXY3_CONFIG="/etc/3proxy/3proxy.cfg"
readonly PROXY3_SERVICE="/etc/systemd/system/3proxy.service"
readonly PROXY3_PARAMS="/etc/3proxy/params"
readonly PROXY3_CONFIG_DIR="/etc/3proxy"
readonly PROXY3_LOCK="/run/lock/3proxy-manager.lock"
# Per-user traffic policy, one "NAME INDEX STATE CAP_MB PERIOD" line per user.
# INDEX selects the counter pair (2*INDEX-1 = upload, 2*INDEX = total), STATE is
# enabled|disabled, CAP_MB is the node-local total-traffic cap in MiB and PERIOD
# is the control-plane accounting period token. A new PERIOD allocates a fresh,
# zeroed counter pair, which is how traffic is reset.
readonly PROXY3_POLICY="/etc/3proxy/3proxy.cfg.policy"
# Binary 3proxy counter store (see `counter` in 3proxy.cfg(3)).
readonly PROXY3_COUNTERS="/etc/3proxy/3proxy.counters"
# 1 PiB expressed in MiB: the cap used for "no limit" counter rules.
readonly TRAFFIC_UNLIMITED_MB=1073741824
# Long-lived connections report their traffic to the counters every 64 MiB.
readonly TRAFFIC_LOGDUMP_BYTES=67108864
# Layout of the 3proxy counter file with a 64-bit time_t: a 16-byte header
# followed by 24-byte records (uint64 traffic, time_t cleared, time_t updated).
readonly COUNTER_HEADER_BYTES=16
readonly COUNTER_RECORD_BYTES=24

declare -A POLICY_INDEX=()
declare -A POLICY_STATE=()
declare -A POLICY_CAP=()
declare -A POLICY_PERIOD=()
declare -A POLICY_RESERVED=()
NEW_POLICY_INDEXES=()
ALLOCATED_POLICY_INDEX=""
REMOVED_POLICY_NAME=""
REMOVED_POLICY_INDEX=""
REMOVED_POLICY_CAP=""
REMOVED_POLICY_PERIOD=""

RED='\033[0;31m'
GREEN='\033[0;32m'
ORANGE='\033[0;33m'
NC='\033[0m'

function isRoot() {
	if [ "${EUID}" -ne 0 ]; then
		echo "You need to run this script as root"
		exit 1
	fi
}

function checkVirt() {
	local VIRT=""
	if command -v virt-what &>/dev/null; then
		VIRT=$(virt-what)
	elif command -v systemd-detect-virt &>/dev/null; then
		VIRT=$(systemd-detect-virt 2>/dev/null || true)
	fi
	case "${VIRT}" in
	"" | none | docker | container | podman | wsl | kvm | qemu | xen | microsoft | vmware | oracle | amazon | google | ibm | bhyve | vz | parallels | uml | systemd-nspawn)
		return 0
		;;
	openvz)
		echo "OpenVZ is not supported"
		exit 1
		;;
	lxc)
		echo "LXC is not supported (yet)."
		exit 1
		;;
	esac
}

function checkIptables() {
	if command -v iptables &>/dev/null; then
		IPTABLES_AVAILABLE=true
	else
		IPTABLES_AVAILABLE=false
	fi
}

function runSystemctl() {
	# A direct-launch test stub (or another service manager wrapper) may spawn a
	# daemon itself. Never let that long-lived process inherit the config lock.
	command systemctl "$@" 9>&-
}

function isValidPort() {
	local port=${1:-}
	[[ ${port} =~ ^[0-9]+$ ]] && [ "${port}" -ge 1 ] && [ "${port}" -le 65535 ]
}

function isValidAddress() {
	local address=${1:-}
	[[ -n ${address} && ${#address} -le 253 && ${address} =~ ^[a-zA-Z0-9._:%-]+$ ]]
}

function isValidIPv4() {
	local address=${1:-}
	local octet
	local -a octets
	[[ ${address} =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] || return 1
	IFS='.' read -r -a octets <<<"${address}"
	for octet in "${octets[@]}"; do
		[[ ${octet} =~ ^[0-9]+$ ]] && [ "${octet}" -ge 0 ] && [ "${octet}" -le 255 ] || return 1
	done
}

function isValidClientName() {
	local name=${1:-}
	[[ ${name} =~ ^[a-zA-Z0-9_-]+$ && ${#name} -le 64 ]]
}

function isValidClientPassword() {
	local password=${1:-}
	[[ ${#password} -ge 8 && ${#password} -le 128 && ${password} =~ ^[a-zA-Z0-9_@%+=,.!?-]+$ ]]
}

function isValidPolicyState() {
	[[ ${1:-} == "enabled" || ${1:-} == "disabled" ]]
}

function isValidPolicyCap() {
	local value=${1:-}
	[[ ${value} =~ ^[1-9][0-9]{0,9}$ ]] && [ "${value}" -le "${TRAFFIC_UNLIMITED_MB}" ]
}

function isValidPolicyPeriod() {
	[[ ${1:-} =~ ^(0|[1-9][0-9]{0,11})$ ]]
}

function isValidPolicyIndex() {
	[[ ${1:-} =~ ^[1-9][0-9]{0,6}$ ]]
}

function isValidPolicyEntry() {
	isValidClientName "${1:-}" && isValidPolicyIndex "${2:-}" && isValidPolicyState "${3:-}" &&
		isValidPolicyCap "${4:-}" && isValidPolicyPeriod "${5:-}"
}

function validateRuntimeParams() {
	isValidAddress "${SERVER_PUB_IP:-}" || return 1
	isValidPort "${HTTP_PORT:-}" || return 1
	isValidPort "${SOCKS_PORT:-}" || return 1
	isValidIPv4 "${DNS1:-}" || return 1
	isValidIPv4 "${DNS2:-}" || return 1
	[[ -z ${IPTABLES_AVAILABLE:-} || ${IPTABLES_AVAILABLE} == "true" || ${IPTABLES_AVAILABLE} == "false" ]]
}

function checkOS() {
	source /etc/os-release
	OS="${ID}"
	if [[ ${OS} == "debian" || ${OS} == "raspbian" ]]; then
		if [[ ${VERSION_ID} -lt 10 ]]; then
			echo "Your version of Debian (${VERSION_ID}) is not supported. Please use Debian 10 Buster or later"
			exit 1
		fi
		OS=debian
	elif [[ ${OS} == "ubuntu" ]]; then
		RELEASE_YEAR=$(echo "${VERSION_ID}" | cut -d'.' -f1)
		if [[ ${RELEASE_YEAR} -lt 18 ]]; then
			echo "Your version of Ubuntu (${VERSION_ID}) is not supported. Please use Ubuntu 18.04 or later"
			exit 1
		fi
	elif [[ ${OS} == "fedora" ]]; then
		if [[ ${VERSION_ID} -lt 32 ]]; then
			echo "Your version of Fedora (${VERSION_ID}) is not supported. Please use Fedora 32 or later"
			exit 1
		fi
	elif [[ ${OS} == 'centos' ]] || [[ ${OS} == 'almalinux' ]] || [[ ${OS} == 'rocky' ]]; then
		if [[ ${VERSION_ID} == 7* ]]; then
			echo "Your version of CentOS (${VERSION_ID}) is not supported. Please use CentOS Stream 8 or later"
			exit 1
		fi
	elif [[ -e /etc/oracle-release ]]; then
		source /etc/os-release
		OS=oracle
	elif [[ -e /etc/arch-release ]]; then
		OS=arch
	elif [[ -e /etc/alpine-release ]]; then
		OS=alpine
		if ! command -v virt-what &>/dev/null; then
			if ! (apk update && apk add virt-what); then
				echo -e "${RED}Failed to install virt-what. Continuing without virtualization check.${NC}"
			fi
		fi
	else
		echo "Looks like you aren't running this installer on a Debian, Ubuntu, Fedora, CentOS, AlmaLinux, Oracle or Arch Linux system"
		exit 1
	fi
}

function initialCheck() {
	isRoot
	checkOS
	checkVirt
}

function getHomeDirForClient() {
	local CLIENT_NAME=$1

	if [ -z "${CLIENT_NAME}" ]; then
		echo "Error: getHomeDirForClient() requires a client name as argument"
		exit 1
	fi

	if [ -e "/home/${CLIENT_NAME}" ]; then
		HOME_DIR="/home/${CLIENT_NAME}"
	elif [ "${SUDO_USER}" ]; then
		if [ "${SUDO_USER}" == "root" ]; then
			HOME_DIR="/root"
		else
			HOME_DIR="/home/${SUDO_USER}"
		fi
	else
		HOME_DIR="/root"
	fi

	echo "$HOME_DIR"
}

function installPackages() {
	if ! "$@"; then
		echo -e "${RED}Failed to install packages.${NC}"
		echo "Please check your internet connection and package sources."
		exit 1
	fi
}

function installQuestions() {
	echo "Welcome to the 3proxy installer!"
	echo "The git repository is available at: https://github.com/a0s/3proxy-install"
	echo ""
	echo "I need to ask you a few questions before starting the setup."
	echo "You can keep the default options and just press enter if you are ok with them."
	echo ""

	SERVER_PUB_IP=$(ip -4 addr | sed -ne 's|^.* inet \([^/]*\)/.* scope global.*$|\1|p' | awk '{print $1}' | head -1)
	if [[ -z ${SERVER_PUB_IP} ]]; then
		SERVER_PUB_IP=$(ip -6 addr | sed -ne 's|^.* inet6 \([^/]*\)/.* scope global.*$|\1|p' | head -1)
	fi
	while true; do
		read -rp "IPv4, IPv6 or public hostname: " -e -i "${SERVER_PUB_IP}" SERVER_PUB_IP
		if isValidAddress "${SERVER_PUB_IP}"; then
			break
		fi
		echo "Please enter a valid public address or hostname."
	done

	echo ""
	echo "HTTP proxy port:"
	echo "   1) Default: ${DEFAULT_HTTP_PORT}"
	echo "   2) Custom"
	echo "   3) Random [49152-65535]"
	until [[ ${HTTP_PORT_CHOICE} =~ ^[1-3]$ ]]; do
		read -rp "Port choice [1-3]: " -e -i 1 HTTP_PORT_CHOICE
	done
	case $HTTP_PORT_CHOICE in
	1)
		HTTP_PORT="${DEFAULT_HTTP_PORT}"
		;;
	2)
		until [[ ${HTTP_PORT} =~ ^[0-9]+$ ]] && [ "${HTTP_PORT}" -ge 1 ] && [ "${HTTP_PORT}" -le 65535 ]; do
			read -rp "Custom HTTP port [1-65535]: " -e -i ${DEFAULT_HTTP_PORT} HTTP_PORT
		done
		;;
	3)
		HTTP_PORT=$(shuf -i 49152-65535 -n1)
		echo "Random HTTP Port: $HTTP_PORT"
		;;
	esac

	echo ""
	echo "SOCKS proxy port:"
	echo "   1) Default: ${DEFAULT_SOCKS_PORT}"
	echo "   2) Custom"
	echo "   3) Random [49152-65535]"
	until [[ ${SOCKS_PORT_CHOICE} =~ ^[1-3]$ ]]; do
		read -rp "Port choice [1-3]: " -e -i 1 SOCKS_PORT_CHOICE
	done
	case $SOCKS_PORT_CHOICE in
	1)
		SOCKS_PORT="${DEFAULT_SOCKS_PORT}"
		;;
	2)
		until [[ ${SOCKS_PORT} =~ ^[0-9]+$ ]] && [ "${SOCKS_PORT}" -ge 1 ] && [ "${SOCKS_PORT}" -le 65535 ]; do
			read -rp "Custom SOCKS port [1-65535]: " -e -i ${DEFAULT_SOCKS_PORT} SOCKS_PORT
		done
		;;
	3)
		SOCKS_PORT=$(shuf -i 49152-65535 -n1)
		echo "Random SOCKS Port: $SOCKS_PORT"
		;;
	esac

	echo ""
	echo "What DNS resolvers do you want to use with the proxy?"
	echo "   1) Cloudflare (1.1.1.1, 1.0.0.1)"
	echo "   2) Google (8.8.8.8, 8.8.4.4)"
	echo "   3) Quad9 (9.9.9.9, 149.112.112.112)"
	echo "   4) Quad9 uncensored (9.9.9.10, 149.112.112.10)"
	echo "   5) FDN (80.67.169.40, 80.67.169.12)"
	echo "   6) DNS.WATCH (84.200.69.80, 84.200.70.40)"
	echo "   7) OpenDNS (208.67.222.222, 208.67.220.220)"
	echo "   8) Yandex (77.88.8.8, 77.88.8.1)"
	echo "   9) AdGuard (94.140.14.14, 94.140.15.15)"
	echo "  10) NextDNS (45.90.28.167, 45.90.30.167)"
	echo "  11) Custom"
	until [[ ${DNS_CHOICE} =~ ^[1-9]$|^1[01]$ ]]; do
		read -rp "DNS choice [1-11]: " -e -i 1 DNS_CHOICE
	done
	case $DNS_CHOICE in
	1)
		DNS1="1.1.1.1"
		DNS2="1.0.0.1"
		;;
	2)
		DNS1="8.8.8.8"
		DNS2="8.8.4.4"
		;;
	3)
		DNS1="9.9.9.9"
		DNS2="149.112.112.112"
		;;
	4)
		DNS1="9.9.9.10"
		DNS2="149.112.112.10"
		;;
	5)
		DNS1="80.67.169.40"
		DNS2="80.67.169.12"
		;;
	6)
		DNS1="84.200.69.80"
		DNS2="84.200.70.40"
		;;
	7)
		DNS1="208.67.222.222"
		DNS2="208.67.220.220"
		;;
	8)
		DNS1="77.88.8.8"
		DNS2="77.88.8.1"
		;;
	9)
		DNS1="94.140.14.14"
		DNS2="94.140.15.15"
		;;
	10)
		DNS1="45.90.28.167"
		DNS2="45.90.30.167"
		;;
	11)
		until [[ ${DNS1} =~ ^((25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.){3}(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)$ ]]; do
			read -rp "Primary DNS: " -e DNS1
		done
		until [[ ${DNS2} =~ ^((25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.){3}(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)$ ]] || [[ ${DNS2} == "" ]]; do
			read -rp "Secondary DNS (optional): " -e DNS2
		done
		if [[ ${DNS2} == "" ]]; then
			DNS2="${DNS1}"
		fi
		;;
	esac

	echo ""
	echo "Okay, that was all I needed. We are ready to setup your 3proxy server now."
	echo "You will be able to generate a user at the end of the installation."
	read -n1 -r -p "Press any key to continue..."
}

function install3proxy() {
	installQuestions
	install3proxyCore || exit 1
	newClient
}

function install3proxyCore() {
	checkIptables

	echo ""
	echo "Installing build dependencies..."
	if [[ ${OS} == 'ubuntu' ]] || [[ ${OS} == 'debian' ]]; then
		apt-get update
		installPackages apt-get install -y build-essential curl tar libssl-dev
	elif [[ ${OS} == 'fedora' ]] || [[ ${OS} == 'centos' ]] || [[ ${OS} == 'almalinux' ]] || [[ ${OS} == 'rocky' ]] || [[ ${OS} == 'oracle' ]]; then
		local -a rhel_build_deps=(gcc make tar openssl openssl-devel)
		if ! command -v curl &>/dev/null; then
			rhel_build_deps+=(curl)
		fi
		installPackages dnf install -y "${rhel_build_deps[@]}"
	elif [[ ${OS} == 'arch' ]]; then
		installPackages pacman -S --needed --noconfirm base-devel curl tar openssl
	elif [[ ${OS} == 'alpine' ]]; then
		apk update
		installPackages apk add gcc make curl tar openssl-dev
	fi

	echo ""
	echo "Downloading 3proxy source..."
	cd /tmp || exit 1
	if ! curl -fL --retry 5 -o 3proxy.tar.gz "${PROXY3_SOURCE_URL}"; then
		echo -e "${RED}Failed to download 3proxy source.${NC}"
		exit 1
	fi

	echo "Extracting 3proxy source..."
	if ! tar -xzf 3proxy.tar.gz; then
		echo -e "${RED}Failed to extract 3proxy source.${NC}"
		exit 1
	fi

	echo "Building 3proxy..."
	cd "${PROXY3_SOURCE_DIR}" || exit 1
	if ! ln -sf Makefile.Linux Makefile; then
		echo -e "${RED}Failed to create Makefile symlink.${NC}"
		exit 1
	fi
	if ! make; then
		echo -e "${RED}Failed to build 3proxy.${NC}"
		exit 1
	fi

	echo "Installing 3proxy binary..."
	if ! cp bin/3proxy "${PROXY3_BINARY}"; then
		echo -e "${RED}Failed to install 3proxy binary.${NC}"
		exit 1
	fi
	chmod 755 "${PROXY3_BINARY}"

	echo "Cleaning up build files..."
	cd /tmp || exit 1
	rm -rf 3proxy.tar.gz "${PROXY3_SOURCE_DIR}"

	echo "Creating 3proxy configuration directory..."
	mkdir -p "${PROXY3_CONFIG_DIR}"

	echo "Generating 3proxy configuration file..."
	generateConfig || return 1

	echo "Creating systemd service..."
	mkdir -p "$(dirname "${PROXY3_SERVICE}")"
	generateService || return 1

	echo "Enabling and starting 3proxy service..."
	runSystemctl daemon-reload || return 1
	runSystemctl enable 3proxy || return 1
	runSystemctl start 3proxy || return 1

	if ! runSystemctl is-active --quiet 3proxy; then
		echo -e "${ORANGE}WARNING: 3proxy service is not running.${NC}"
		echo "You can check the status with: systemctl status 3proxy"
	else
		echo -e "${GREEN}3proxy service is running.${NC}"
	fi

	saveParams || return 1

	echo ""
	echo -e "${GREEN}3proxy installation completed!${NC}"
}

function generateConfig() {
	local config_tmp line name index state cap period extra
	local -A active_state=()
	local -a count_rules=()
	config_tmp=$(mktemp "${PROXY3_CONFIG}.tmp.XXXXXX") || return 1
	chmod 600 "${config_tmp}" || {
		rm -f "${config_tmp}"
		return 1
	}

	if [ -f "${PROXY3_POLICY}" ]; then
		while read -r name index state cap period extra || [[ -n ${name} ]]; do
			[[ -z ${name} ]] && continue
			if [[ -n ${extra} ]] || ! isValidPolicyEntry "${name}" "${index}" "${state}" "${cap}" "${period}"; then
				echo "Invalid traffic policy file: ${PROXY3_POLICY}" >&2
				rm -f "${config_tmp}"
				return 1
			fi
			active_state[${name}]=${state}
			# countout only exists so 3proxy also adds uploaded bytes to countall.
			count_rules+=("countout $((index * 2 - 1))/${name} N ${TRAFFIC_UNLIMITED_MB} ${name}")
			count_rules+=("countall $((index * 2))/${name} N ${cap} ${name}")
		done <"${PROXY3_POLICY}"
	fi

	if ! cat >"${config_tmp}" <<EOF
nserver ${DNS1}
nserver ${DNS2}

log
logformat "L%t%. L%t.%. %N.%p %E %U %C:%c %R:%r %O %I %h %T"
logdump ${TRAFFIC_LOGDUMP_BYTES} ${TRAFFIC_LOGDUMP_BYTES}
counter ${PROXY3_COUNTERS}

EOF
	then
		rm -f "${config_tmp}"
		return 1
	fi

	# Disabled, expired or exhausted users are left out of the active user list.
	# On reload 3proxy re-authenticates established sessions, so this also
	# terminates their open connections.
	if [ -f "${PROXY3_CONFIG}.users" ]; then
		while IFS= read -r line || [[ -n ${line} ]]; do
			if [[ ${line} =~ ^users[[:space:]]+([a-zA-Z0-9_-]+): ]] &&
				[[ ${active_state[${BASH_REMATCH[1]}]:-enabled} == "disabled" ]]; then
				continue
			fi
			printf '%s\n' "${line}"
		done <"${PROXY3_CONFIG}.users" >>"${config_tmp}" || {
			rm -f "${config_tmp}"
			return 1
		}
	fi

	{
		printf '\nauth strong\n'
		if [[ ${#count_rules[@]} -gt 0 ]]; then
			printf '%s\n' "${count_rules[@]}"
		fi
		printf 'allow *\nproxy -p%s\nsocks -p%s\nflush\n' "${HTTP_PORT}" "${SOCKS_PORT}"
	} >>"${config_tmp}" || {
		rm -f "${config_tmp}"
		return 1
	}

	mv -f "${config_tmp}" "${PROXY3_CONFIG}" || return 1
	chmod 600 "${PROXY3_CONFIG}" || return 1
}

function generateService() {
	local service_tmp render_status
	service_tmp=$(mktemp "${PROXY3_SERVICE}.tmp.XXXXXX") || return 1
	if [[ "${IPTABLES_AVAILABLE}" == "true" ]]; then
		cat >"${service_tmp}" <<EOF
[Unit]
Description=3proxy proxy server
Documentation=man:3proxy(1)
After=network.target

[Service]
ExecStartPre=/bin/bash -c 'iptables -C INPUT -p tcp --dport ${HTTP_PORT} -j ACCEPT 2>/dev/null || iptables -I INPUT -p tcp --dport ${HTTP_PORT} -j ACCEPT'
ExecStartPre=/bin/bash -c 'iptables -C INPUT -p tcp --dport ${SOCKS_PORT} -j ACCEPT 2>/dev/null || iptables -I INPUT -p tcp --dport ${SOCKS_PORT} -j ACCEPT'
ExecStart=${PROXY3_BINARY} ${PROXY3_CONFIG}
ExecReload=/bin/kill -USR1 \$MAINPID
ExecStopPost=/bin/bash -c 'iptables -D INPUT -p tcp --dport ${HTTP_PORT} -j ACCEPT 2>/dev/null || true'
ExecStopPost=/bin/bash -c 'iptables -D INPUT -p tcp --dport ${SOCKS_PORT} -j ACCEPT 2>/dev/null || true'
KillMode=process
Restart=on-failure
LimitNOFILE=65536
LimitNPROC=32768

[Install]
WantedBy=multi-user.target
EOF
	else
		cat >"${service_tmp}" <<EOF
[Unit]
Description=3proxy proxy server
Documentation=man:3proxy(1)
After=network.target

[Service]
ExecStart=${PROXY3_BINARY} ${PROXY3_CONFIG}
ExecReload=/bin/kill -USR1 \$MAINPID
KillMode=process
Restart=on-failure
LimitNOFILE=65536
LimitNPROC=32768

[Install]
WantedBy=multi-user.target
EOF
	fi
	render_status=$?
	if [[ ${render_status} -ne 0 ]]; then
		rm -f "${service_tmp}"
		return 1
	fi
	chmod 644 "${service_tmp}" || {
		rm -f "${service_tmp}"
		return 1
	}
	mv -f "${service_tmp}" "${PROXY3_SERVICE}" || return 1
}

function saveParams() {
	local params_tmp
	params_tmp=$(mktemp "${PROXY3_PARAMS}.tmp.XXXXXX") || return 1
	chmod 600 "${params_tmp}" || {
		rm -f "${params_tmp}"
		return 1
	}
	if ! cat >"${params_tmp}" <<EOF
SERVER_PUB_IP=${SERVER_PUB_IP}
HTTP_PORT=${HTTP_PORT}
SOCKS_PORT=${SOCKS_PORT}
DNS1=${DNS1}
DNS2=${DNS2}
IPTABLES_AVAILABLE=${IPTABLES_AVAILABLE}
EOF
	then
		rm -f "${params_tmp}"
		return 1
	fi
	mv -f "${params_tmp}" "${PROXY3_PARAMS}" || return 1
	chmod 600 "${PROXY3_PARAMS}" || return 1
}

function loadParams() {
	if [ -f "${PROXY3_PARAMS}" ]; then
		local key value
		SERVER_PUB_IP=""
		HTTP_PORT=""
		SOCKS_PORT=""
		DNS1=""
		DNS2=""
		IPTABLES_AVAILABLE=""
		while IFS='=' read -r key value; do
			case "${key}" in
			SERVER_PUB_IP)
				SERVER_PUB_IP=${value}
				;;
			HTTP_PORT)
				HTTP_PORT=${value}
				;;
			SOCKS_PORT)
				SOCKS_PORT=${value}
				;;
			DNS1)
				DNS1=${value}
				;;
			DNS2)
				DNS2=${value}
				;;
			IPTABLES_AVAILABLE)
				IPTABLES_AVAILABLE=${value}
				;;
			esac
		done <"${PROXY3_PARAMS}"

		if ! validateRuntimeParams; then
			echo "Invalid parameters file: ${PROXY3_PARAMS}"
			exit 1
		fi
		# If IPTABLES_AVAILABLE is not set (old installations), detect it
		if [[ -z "${IPTABLES_AVAILABLE}" ]]; then
			checkIptables
			# Update params file with detected value
			saveParams
		fi
	else
		echo "Parameters file not found: ${PROXY3_PARAMS}"
		exit 1
	fi
}

function generatePassword() {
	openssl rand -base64 16 | tr -d "=+/" | cut -c1-16
}

function acquireConfigLock() {
	if ! command -v flock &>/dev/null; then
		echo "flock is required for safe configuration updates" >&2
		return 1
	fi
	mkdir -p "$(dirname "${PROXY3_LOCK}")"
	exec 9>"${PROXY3_LOCK}"
	flock -x 9
}

function restartProxyService() {
	if runSystemctl is-active --quiet 3proxy; then
		runSystemctl restart 3proxy || return 1
	else
		runSystemctl start 3proxy || return 1
	fi
	runSystemctl is-active --quiet 3proxy
}

function ensureServiceReloadable() {
	[ -f "${PROXY3_SERVICE}" ] || return 1
	grep -q '^ExecReload=' "${PROXY3_SERVICE}" && return 0
	# Units written by older installer versions cannot be reloaded in place.
	generateService || return 1
	runSystemctl daemon-reload
}

function proxyListenerInodes() {
	local port_hex
	printf -v port_hex ':%04X' "${HTTP_PORT}"
	awk -v port="${port_hex}" '$4 == "0A" && substr($2, length($2) - 4) == port { print $10 }' \
		/proc/net/tcp /proc/net/tcp6 2>/dev/null | sort | tr '\n' ' '
}

# Reload 3proxy in place (SIGUSR1). Unlike a restart this keeps established
# sessions of unaffected users, and 3proxy writes its traffic counters to disk
# before re-reading the configuration. 3proxy opens the new listener before it
# closes the old one; the reload is complete once only new listeners remain.
# Connections accepted by the old listener in between are reset, so this runs
# only when the configuration actually changed.
function reloadProxyService() {
	local before after attempt inode stale
	ensureServiceReloadable || return 1
	before=$(proxyListenerInodes)
	runSystemctl reload 3proxy || return 1
	for ((attempt = 0; attempt < 50; attempt++)); do
		sleep 0.2
		after=$(proxyListenerInodes)
		[[ -n ${after} ]] || continue
		stale=false
		for inode in ${before}; do
			if [[ " ${after} " == *" ${inode} "* ]]; then
				stale=true
				break
			fi
		done
		if [[ ${stale} == "false" ]]; then
			runSystemctl is-active --quiet 3proxy
			return
		fi
	done
	return 1
}

function applyProxyConfig() {
	if ! runSystemctl is-active --quiet 3proxy; then
		runSystemctl start 3proxy || return 1
		runSystemctl is-active --quiet 3proxy
		return
	fi
	if reloadProxyService; then
		return 0
	fi
	echo "3proxy did not confirm the in-place reload; restarting the service" >&2
	restartProxyService
}

# 3proxy only persists counters once a minute and on reload, not on SIGTERM.
function flushTrafficCounters() {
	[ -f "${PROXY3_POLICY}" ] || return 0
	runSystemctl is-active --quiet 3proxy || return 0
	reloadProxyService >/dev/null 2>&1 || true
}

function loadPolicy() {
	local name index state cap period extra
	POLICY_INDEX=()
	POLICY_STATE=()
	POLICY_CAP=()
	POLICY_PERIOD=()
	POLICY_RESERVED=()
	NEW_POLICY_INDEXES=()
	[ -f "${PROXY3_POLICY}" ] || return 0
	while read -r name index state cap period extra || [[ -n ${name} ]]; do
		[[ -z ${name} ]] && continue
		if [[ -n ${extra} ]] || ! isValidPolicyEntry "${name}" "${index}" "${state}" "${cap}" "${period}" ||
			[[ -n ${POLICY_INDEX[${name}]:-} || -n ${POLICY_RESERVED[${index}]:-} ]]; then
			echo "Invalid traffic policy file: ${PROXY3_POLICY}" >&2
			return 1
		fi
		POLICY_INDEX[${name}]=${index}
		POLICY_STATE[${name}]=${state}
		POLICY_CAP[${name}]=${cap}
		POLICY_PERIOD[${name}]=${period}
		POLICY_RESERVED[${index}]=1
	done <"${PROXY3_POLICY}"
}

# Pick a counter pair that is used neither by the running configuration nor by
# the pending one. An index released in this transaction stays reserved,
# because 3proxy writes its final value on the next reload.
function allocatePolicyIndex() {
	local index=1
	while [[ -n ${POLICY_RESERVED[${index}]:-} ]]; do
		index=$((index + 1))
	done
	POLICY_RESERVED[${index}]=1
	NEW_POLICY_INDEXES+=("${index}")
	ALLOCATED_POLICY_INDEX=${index}
}

# Returns 0 when the in-memory policy changed.
function setPolicyEntry() {
	local name=$1 state=$2 cap=$3 period=$4
	if [[ -n ${POLICY_INDEX[${name}]:-} && ${POLICY_PERIOD[${name}]} == "${period}" ]]; then
		if [[ ${POLICY_STATE[${name}]} == "${state}" && ${POLICY_CAP[${name}]} == "${cap}" ]]; then
			return 1
		fi
		POLICY_STATE[${name}]=${state}
		POLICY_CAP[${name}]=${cap}
		return 0
	fi
	# A new accounting period starts from a fresh, zeroed counter pair.
	allocatePolicyIndex
	POLICY_INDEX[${name}]=${ALLOCATED_POLICY_INDEX}
	POLICY_STATE[${name}]=${state}
	POLICY_CAP[${name}]=${cap}
	POLICY_PERIOD[${name}]=${period}
	return 0
}

function renamePolicyEntry() {
	local old_name=$1 new_name=$2
	[[ ${old_name} == "${new_name}" || -z ${POLICY_INDEX[${old_name}]:-} ]] && return 1
	POLICY_INDEX[${new_name}]=${POLICY_INDEX[${old_name}]}
	POLICY_STATE[${new_name}]=${POLICY_STATE[${old_name}]}
	POLICY_CAP[${new_name}]=${POLICY_CAP[${old_name}]}
	POLICY_PERIOD[${new_name}]=${POLICY_PERIOD[${old_name}]}
	removePolicyEntry "${old_name}"
}

function removePolicyEntry() {
	local name=$1
	[[ -z ${POLICY_INDEX[${name}]:-} ]] && return 1
	unset "POLICY_INDEX[${name}]" "POLICY_STATE[${name}]" "POLICY_CAP[${name}]" "POLICY_PERIOD[${name}]"
	return 0
}

function policyNames() {
	[[ ${#POLICY_INDEX[@]} -eq 0 ]] && return 0
	printf '%s\n' "${!POLICY_INDEX[@]}" | LC_ALL=C sort
}

function writePolicyCandidate() {
	local candidate name
	candidate=$(mktemp "${PROXY3_POLICY}.tmp.XXXXXX") || return 1
	chmod 600 "${candidate}" || {
		rm -f "${candidate}"
		return 1
	}
	for name in $(policyNames); do
		printf '%s %s %s %s %s\n' "${name}" "${POLICY_INDEX[${name}]}" "${POLICY_STATE[${name}]}" \
			"${POLICY_CAP[${name}]}" "${POLICY_PERIOD[${name}]}"
	done >"${candidate}" || {
		rm -f "${candidate}"
		return 1
	}
	printf '%s' "${candidate}"
}

function zeroCounterSlot() {
	local slot=$1 offset size
	[ -f "${PROXY3_COUNTERS}" ] || return 0
	offset=$((COUNTER_HEADER_BYTES + (slot - 1) * COUNTER_RECORD_BYTES))
	size=$(stat -c %s "${PROXY3_COUNTERS}" 2>/dev/null || echo 0)
	[[ ${size} =~ ^[0-9]+$ ]] && [ "${size}" -gt "${offset}" ] || return 0
	dd if=/dev/zero of="${PROXY3_COUNTERS}" bs=1 seek="${offset}" count="${COUNTER_RECORD_BYTES}" \
		conv=notrunc status=none
}

function counterBytes() {
	local slot=$1 offset value
	[ -f "${PROXY3_COUNTERS}" ] || {
		echo 0
		return
	}
	offset=$((COUNTER_HEADER_BYTES + (slot - 1) * COUNTER_RECORD_BYTES))
	value=$(od -A n -t u8 -j "${offset}" -N 8 "${PROXY3_COUNTERS}" 2>/dev/null | tr -d ' \n')
	[[ ${value} =~ ^[0-9]+$ ]] || value=0
	echo "${value}"
}

function reportTraffic() {
	local name index line
	apiResult "traffic_report" "v1"
	if [ -f "${PROXY3_CONFIG}.users" ]; then
		while IFS= read -r line || [[ -n ${line} ]]; do
			if [[ ${line} =~ ^users[[:space:]]+([a-zA-Z0-9_-]+): ]]; then
				apiResult "node_user" "${BASH_REMATCH[1]}"
			fi
		done <"${PROXY3_CONFIG}.users"
	fi
	if [[ -n ${REMOVED_POLICY_NAME} ]]; then
		# Final value of the counter of an account deleted in this run; the
		# reload that applied the deletion wrote it to disk.
		printf 'PM\ttraffic\t%s\tremoved\t%s\t%s\t%s\t%s\n' "${REMOVED_POLICY_NAME}" "${REMOVED_POLICY_CAP}" \
			"${REMOVED_POLICY_PERIOD}" "${REMOVED_POLICY_INDEX}" "$(counterBytes $((REMOVED_POLICY_INDEX * 2)))"
	fi
	for name in $(policyNames); do
		index=${POLICY_INDEX[${name}]}
		printf 'PM\ttraffic\t%s\t%s\t%s\t%s\t%s\t%s\n' "${name}" "${POLICY_STATE[${name}]}" \
			"${POLICY_CAP[${name}]}" "${POLICY_PERIOD[${name}]}" "${index}" "$(counterBytes $((index * 2)))"
	done
}

# Atomically replace the users and/or policy files, then apply the generated
# configuration. Either candidate may be empty. On failure both files are
# restored and the previous configuration is re-applied.
function commitNodeState() {
	local users_candidate=${1:-} policy_candidate=${2:-}
	local users_backup="" policy_backup="" had_users=false had_policy=false index

	if [[ -n ${users_candidate} ]]; then
		users_backup=$(mktemp "${PROXY3_CONFIG}.users.backup.XXXXXX") || return 1
		if [ -f "${PROXY3_CONFIG}.users" ]; then
			cp "${PROXY3_CONFIG}.users" "${users_backup}" || {
				rm -f "${users_backup}"
				return 1
			}
			had_users=true
		fi
	fi
	if [[ -n ${policy_candidate} ]]; then
		policy_backup=$(mktemp "${PROXY3_POLICY}.backup.XXXXXX") || {
			rm -f "${users_backup}"
			return 1
		}
		if [ -f "${PROXY3_POLICY}" ]; then
			cp "${PROXY3_POLICY}" "${policy_backup}" || {
				rm -f "${users_backup}" "${policy_backup}"
				return 1
			}
			had_policy=true
		fi
	fi

	if { [[ -z ${users_candidate} ]] || {
		mv -f "${users_candidate}" "${PROXY3_CONFIG}.users" && chmod 600 "${PROXY3_CONFIG}.users"
	}; } && { [[ -z ${policy_candidate} ]] || {
		mv -f "${policy_candidate}" "${PROXY3_POLICY}" && chmod 600 "${PROXY3_POLICY}"
	}; }; then
		for index in "${NEW_POLICY_INDEXES[@]}"; do
			zeroCounterSlot $((index * 2 - 1))
			zeroCounterSlot $((index * 2))
		done
		if generateConfig && applyProxyConfig; then
			rm -f "${users_backup}" "${policy_backup}"
			return 0
		fi
	fi

	echo "Failed to apply user configuration; restoring the previous version" >&2
	rm -f "${users_candidate}" "${policy_candidate}"
	if [[ -n ${users_candidate} ]]; then
		if [[ ${had_users} == "true" ]]; then
			mv -f "${users_backup}" "${PROXY3_CONFIG}.users"
		else
			rm -f "${PROXY3_CONFIG}.users" "${users_backup}"
		fi
	fi
	if [[ -n ${policy_candidate} ]]; then
		if [[ ${had_policy} == "true" ]]; then
			mv -f "${policy_backup}" "${PROXY3_POLICY}"
		else
			rm -f "${PROXY3_POLICY}" "${policy_backup}"
		fi
	fi
	generateConfig || true
	restartProxyService || true
	return 1
}

function addClientRecord() {
	local name=$1
	local password=$2
	local policy_state=${3:-} policy_cap=${4:-} policy_period=${5:-}
	local users_tmp policy_tmp="" policy_changed=false

	isValidClientName "${name}" || return 2
	isValidClientPassword "${password}" || return 2
	if [[ -n ${policy_state} ]] && ! isValidPolicyEntry "${name}" 1 "${policy_state}" "${policy_cap}" "${policy_period}"; then
		return 2
	fi
	acquireConfigLock || return 1

	if [ -f "${PROXY3_CONFIG}.users" ] && grep -q "^users ${name}:" "${PROXY3_CONFIG}.users"; then
		return 3
	fi
	loadPolicy || return 1

	users_tmp=$(mktemp "${PROXY3_CONFIG}.users.tmp.XXXXXX") || return 1
	if [ -f "${PROXY3_CONFIG}.users" ]; then
		cat "${PROXY3_CONFIG}.users" >"${users_tmp}" || {
			rm -f "${users_tmp}"
			return 1
		}
	fi
	printf 'users %s:CL:%s\n' "${name}" "${password}" >>"${users_tmp}"
	chmod 600 "${users_tmp}"
	# An orphaned policy entry must not carry an old user's state to a new one.
	removePolicyEntry "${name}" && policy_changed=true
	if [[ -n ${policy_state} ]]; then
		setPolicyEntry "${name}" "${policy_state}" "${policy_cap}" "${policy_period}"
		policy_changed=true
	fi
	if [[ ${policy_changed} == "true" ]]; then
		policy_tmp=$(writePolicyCandidate) || {
			rm -f "${users_tmp}"
			return 1
		}
	fi
	commitNodeState "${users_tmp}" "${policy_tmp}"
}

function updateClientRecord() {
	local old_name=$1
	local new_name=$2
	local password=$3
	local policy_state=${4:-} policy_cap=${5:-} policy_period=${6:-}
	local users_tmp line found=false policy_tmp="" policy_changed=false

	isValidClientName "${old_name}" || return 2
	isValidClientName "${new_name}" || return 2
	isValidClientPassword "${password}" || return 2
	if [[ -n ${policy_state} ]] && ! isValidPolicyEntry "${new_name}" 1 "${policy_state}" "${policy_cap}" "${policy_period}"; then
		return 2
	fi
	acquireConfigLock || return 1

	if [ ! -f "${PROXY3_CONFIG}.users" ] || ! grep -q "^users ${old_name}:" "${PROXY3_CONFIG}.users"; then
		return 4
	fi
	if [[ ${old_name} != "${new_name}" ]] && grep -q "^users ${new_name}:" "${PROXY3_CONFIG}.users"; then
		return 3
	fi
	loadPolicy || return 1

	users_tmp=$(mktemp "${PROXY3_CONFIG}.users.tmp.XXXXXX") || return 1
	while IFS= read -r line || [[ -n ${line} ]]; do
		if [[ ${line} == "users ${old_name}:"* ]]; then
			printf 'users %s:CL:%s\n' "${new_name}" "${password}" >>"${users_tmp}"
			found=true
		else
			printf '%s\n' "${line}" >>"${users_tmp}"
		fi
	done <"${PROXY3_CONFIG}.users"
	if [[ ${found} != "true" ]]; then
		rm -f "${users_tmp}"
		return 4
	fi
	chmod 600 "${users_tmp}"
	# A rename keeps the counters and the state of the account.
	if [[ ${old_name} != "${new_name}" ]]; then
		removePolicyEntry "${new_name}" && policy_changed=true
		renamePolicyEntry "${old_name}" "${new_name}" && policy_changed=true
	fi
	if [[ -n ${policy_state} ]] && setPolicyEntry "${new_name}" "${policy_state}" "${policy_cap}" "${policy_period}"; then
		policy_changed=true
	fi
	if [[ ${policy_changed} == "true" ]]; then
		policy_tmp=$(writePolicyCandidate) || {
			rm -f "${users_tmp}"
			return 1
		}
	fi
	commitNodeState "${users_tmp}" "${policy_tmp}"
}

function deleteClientRecord() {
	local name=$1
	local users_tmp line found=false policy_tmp=""

	isValidClientName "${name}" || return 2
	acquireConfigLock || return 1
	if [ ! -f "${PROXY3_CONFIG}.users" ]; then
		return 4
	fi
	loadPolicy || return 1

	users_tmp=$(mktemp "${PROXY3_CONFIG}.users.tmp.XXXXXX") || return 1
	while IFS= read -r line || [[ -n ${line} ]]; do
		if [[ ${line} == "users ${name}:"* ]]; then
			found=true
			continue
		fi
		printf '%s\n' "${line}" >>"${users_tmp}"
	done <"${PROXY3_CONFIG}.users"
	if [[ ${found} != "true" ]]; then
		rm -f "${users_tmp}"
		return 4
	fi
	chmod 600 "${users_tmp}"
	if [[ -n ${POLICY_INDEX[${name}]:-} ]]; then
		REMOVED_POLICY_NAME=${name}
		REMOVED_POLICY_INDEX=${POLICY_INDEX[${name}]}
		REMOVED_POLICY_CAP=${POLICY_CAP[${name}]}
		REMOVED_POLICY_PERIOD=${POLICY_PERIOD[${name}]}
	fi
	if removePolicyEntry "${name}"; then
		policy_tmp=$(writePolicyCandidate) || {
			rm -f "${users_tmp}"
			return 1
		}
	fi
	commitNodeState "${users_tmp}" "${policy_tmp}"
}

# Apply "NAME STATE CAP_MB PERIOD" lines read from stdin. Entries for users that
# do not exist on this node are ignored (the control plane may be ahead of the
# user file) and entries of users that were removed are dropped. Users that are
# not listed keep their current policy.
function applyPolicyRecords() {
	local line name state cap period extra changed=false policy_tmp
	local -A existing=()

	acquireConfigLock || return 1
	loadPolicy || return 1
	if [ -f "${PROXY3_CONFIG}.users" ]; then
		while IFS= read -r line || [[ -n ${line} ]]; do
			if [[ ${line} =~ ^users[[:space:]]+([a-zA-Z0-9_-]+): ]]; then
				existing[${BASH_REMATCH[1]}]=1
			fi
		done <"${PROXY3_CONFIG}.users"
	fi

	while read -r name state cap period extra || [[ -n ${name} ]]; do
		[[ -z ${name} ]] && continue
		if [[ -n ${extra} ]] || ! isValidClientName "${name}" || ! isValidPolicyState "${state}" ||
			! isValidPolicyCap "${cap}" || ! isValidPolicyPeriod "${period}"; then
			echo "Invalid policy record for ${name:-<empty>}" >&2
			return 2
		fi
		[[ -z ${existing[${name}]:-} ]] && continue
		if setPolicyEntry "${name}" "${state}" "${cap}" "${period}"; then
			changed=true
		fi
	done

	for name in $(policyNames); do
		if [[ -z ${existing[${name}]:-} ]]; then
			removePolicyEntry "${name}" && changed=true
		fi
	done

	if [[ ${changed} == "true" ]]; then
		policy_tmp=$(writePolicyCandidate) || return 1
		commitNodeState "" "${policy_tmp}" || return 1
		apiResult "result" "applied"
	else
		apiResult "result" "unchanged"
	fi
	reportTraffic
}

function newClient() {
	echo ""
	echo "Client configuration"
	echo ""
	echo "The client name must consist of alphanumeric character(s). It may also include underscores or dashes."

	until [[ ${CLIENT_NAME} =~ ^[a-zA-Z0-9_-]+$ && ${CLIENT_EXISTS} == '0' ]]; do
		read -rp "Client name: " -e CLIENT_NAME
		if [ -f "${PROXY3_CONFIG}.users" ]; then
			# grep -c prints 0 and exits 1 when there are no matches; do not use "|| echo 0"
			# inside $() — that yields "0\n0" and breaks the duplicate-name check.
			CLIENT_EXISTS=$(grep -c "^users ${CLIENT_NAME}:" "${PROXY3_CONFIG}.users" 2>/dev/null || true)
			CLIENT_EXISTS=${CLIENT_EXISTS:-0}
		else
			CLIENT_EXISTS=0
		fi

		if [[ ${CLIENT_EXISTS} != 0 ]]; then
			echo ""
			echo -e "${ORANGE}A client with the specified name was already created, please choose another name.${NC}"
			echo ""
		fi
	done

	echo ""
	echo "Do you want to set a custom password for this client?"
	echo "   1) Generate random password"
	echo "   2) Set custom password"
	until [[ ${PASS_CHOICE} =~ ^[1-2]$ ]]; do
		read -rp "Password choice [1-2]: " -e -i 1 PASS_CHOICE
	done

	case $PASS_CHOICE in
	1)
		CLIENT_PASSWORD=$(generatePassword)
		;;
	2)
		until isValidClientPassword "${CLIENT_PASSWORD:-}"; do
			read -rp "Custom password (8-128 safe characters): " -e CLIENT_PASSWORD
			if ! isValidClientPassword "${CLIENT_PASSWORD:-}"; then
				echo "Use letters, numbers or one of _@%+=,.!?- and at least 8 characters."
			fi
		done
		;;
	esac

	local add_status=0
	addClientRecord "${CLIENT_NAME}" "${CLIENT_PASSWORD}" || add_status=$?
	if [[ ${add_status} -eq 3 ]]; then
		echo -e "${ORANGE}A client with the specified name was created concurrently. Please retry.${NC}"
		return 1
	elif [[ ${add_status} -ne 0 ]]; then
		echo -e "${RED}Failed to add client ${CLIENT_NAME}.${NC}"
		return 1
	fi

	echo ""
	echo -e "${GREEN}Client ${CLIENT_NAME} added successfully!${NC}"
	echo ""
	echo "=== Proxy Configuration ==="
	echo ""
	echo "URL Format:"
	echo "  HTTP:  http://${CLIENT_NAME}:${CLIENT_PASSWORD}@${SERVER_PUB_IP}:${HTTP_PORT}"
	echo "  HTTPS: https://${CLIENT_NAME}:${CLIENT_PASSWORD}@${SERVER_PUB_IP}:${HTTP_PORT}"
	echo "  SOCKS: socks5://${CLIENT_NAME}:${CLIENT_PASSWORD}@${SERVER_PUB_IP}:${SOCKS_PORT}"
	echo ""
	echo "Separate Configuration:"
	echo ""
	echo "HTTP/HTTPS Proxy:"
	echo "  Protocol: HTTP / HTTPS"
	echo "  Host: ${SERVER_PUB_IP}"
	echo "  Port: ${HTTP_PORT}"
	echo "  Username: ${CLIENT_NAME}"
	echo "  Password: ${CLIENT_PASSWORD}"
	echo ""
	echo "SOCKS Proxy:"
	echo "  Protocol: SOCKS5"
	echo "  Host: ${SERVER_PUB_IP}"
	echo "  Port: ${SOCKS_PORT}"
	echo "  Username: ${CLIENT_NAME}"
	echo "  Password: ${CLIENT_PASSWORD}"
	echo ""
}

function listClients() {
	if [ ! -f "${PROXY3_CONFIG}.users" ]; then
		echo ""
		echo "You have no existing clients!"
		exit 1
	fi

	NUMBER_OF_CLIENTS=$(grep -c "^users " "${PROXY3_CONFIG}.users" 2>/dev/null || true)
	NUMBER_OF_CLIENTS=${NUMBER_OF_CLIENTS:-0}
	if [[ ${NUMBER_OF_CLIENTS} -eq 0 ]]; then
		echo ""
		echo "You have no existing clients!"
		exit 1
	fi

	echo ""
	echo "Existing clients:"
	grep "^users " "${PROXY3_CONFIG}.users" | cut -d' ' -f2 | cut -d':' -f1 | nl -s ') '
}

function removeClient() {
	listClients

	NUMBER_OF_CLIENTS=$(grep -c "^users " "${PROXY3_CONFIG}.users" 2>/dev/null || true)
	NUMBER_OF_CLIENTS=${NUMBER_OF_CLIENTS:-0}
	if [[ ${NUMBER_OF_CLIENTS} == '0' ]]; then
		exit 1
	fi

	echo ""
	echo "Select the existing client you want to remove"
	until [[ ${CLIENT_NUMBER} -ge 1 && ${CLIENT_NUMBER} -le ${NUMBER_OF_CLIENTS} ]]; do
		if [[ ${CLIENT_NUMBER} == '1' ]]; then
			read -rp "Select one client [1]: " CLIENT_NUMBER
		else
			read -rp "Select one client [1-${NUMBER_OF_CLIENTS}]: " CLIENT_NUMBER
		fi
	done

	CLIENT_NAME=$(grep "^users " "${PROXY3_CONFIG}.users" | cut -d' ' -f2 | cut -d':' -f1 | sed -n "${CLIENT_NUMBER}"p)

	if ! deleteClientRecord "${CLIENT_NAME}"; then
		echo -e "${RED}Failed to remove client ${CLIENT_NAME}.${NC}"
		return 1
	fi

	echo ""
	echo -e "${GREEN}Client ${CLIENT_NAME} removed successfully!${NC}"
}

function uninstall3proxy() {
	echo ""
	echo -e "\n${RED}WARNING: This will uninstall 3proxy and remove all the configuration files!${NC}"
	echo -e "${ORANGE}Please backup the ${PROXY3_CONFIG_DIR} directory if you want to keep your configuration files.\n${NC}"
	read -rp "Do you really want to remove 3proxy? [y/n]: " -e REMOVE
	REMOVE=${REMOVE:-n}
	if [[ $REMOVE == 'y' ]]; then
		runSystemctl stop 3proxy
		runSystemctl disable 3proxy

		rm -f "${PROXY3_SERVICE}"
		runSystemctl daemon-reload

		rm -rf "${PROXY3_CONFIG_DIR}"
		rm -f "${PROXY3_BINARY}"

		echo ""
		echo -e "${GREEN}3proxy uninstalled successfully!${NC}"
		exit 0
	else
		echo ""
		echo "Removal aborted!"
	fi
}

function apiResult() {
	printf 'PM\t%s\t%s\n' "$1" "$2"
}

function machineInspect() {
	local installed=false
	local service_status="not_installed"
	local line

	if [ -f "${PROXY3_PARAMS}" ]; then
		loadParams
	fi
	if [ -x "${PROXY3_BINARY}" ] && [ -f "${PROXY3_CONFIG}" ] && [ -f "${PROXY3_SERVICE}" ] && [ -f "${PROXY3_PARAMS}" ]; then
		installed=true
		if runSystemctl is-active --quiet 3proxy; then
			service_status=active
		else
			service_status=$(runSystemctl is-active 3proxy 2>/dev/null || true)
			service_status=${service_status:-inactive}
		fi
	fi

	apiResult "installed" "${installed}"
	apiResult "service" "${service_status}"
	if [[ ${installed} == "true" ]]; then
		apiResult "server_ip" "${SERVER_PUB_IP}"
		apiResult "http_port" "${HTTP_PORT}"
		apiResult "socks_port" "${SOCKS_PORT}"
		apiResult "dns1" "${DNS1}"
		apiResult "dns2" "${DNS2}"
	fi
	if [ -f "${PROXY3_CONFIG}.users" ]; then
		while IFS= read -r line || [[ -n ${line} ]]; do
			if [[ ${line} =~ ^users[[:space:]]+([a-zA-Z0-9_-]+): ]]; then
				apiResult "user" "${BASH_REMATCH[1]}"
			fi
		done <"${PROXY3_CONFIG}.users"
	fi
}

function machineDeploy() {
	if [[ $# -ne 5 ]]; then
		echo "Usage: --api deploy SERVER_IP HTTP_PORT SOCKS_PORT DNS1 DNS2" >&2
		return 2
	fi

	SERVER_PUB_IP=$1
	HTTP_PORT=$2
	SOCKS_PORT=$3
	DNS1=$4
	DNS2=$5
	IPTABLES_AVAILABLE=""
	if ! validateRuntimeParams || [[ ${HTTP_PORT} == "${SOCKS_PORT}" ]]; then
		echo "Invalid deployment parameters" >&2
		return 2
	fi
	acquireConfigLock || return 1

	if [ ! -x "${PROXY3_BINARY}" ]; then
		install3proxyCore || return 1
	else
		checkIptables
		if [ -f "${PROXY3_SERVICE}" ]; then
			if [ -f "${PROXY3_POLICY}" ] && runSystemctl is-active --quiet 3proxy; then
				# Persist traffic counters; 3proxy does not save them on SIGTERM.
				runSystemctl kill --signal=USR1 3proxy >/dev/null 2>&1 && sleep 2
			fi
			runSystemctl stop 3proxy || true
		fi
		mkdir -p "${PROXY3_CONFIG_DIR}"
		generateConfig || return 1
		generateService || return 1
		saveParams || return 1
		runSystemctl daemon-reload || return 1
		runSystemctl enable 3proxy || return 1
		runSystemctl start 3proxy || return 1
	fi

	if ! runSystemctl is-active --quiet 3proxy; then
		echo "3proxy did not become active after deployment" >&2
		return 1
	fi
	apiResult "result" "deployed"
	machineInspect
}

function machineReadPassword() {
	local password
	IFS= read -r password || true
	if ! isValidClientPassword "${password}"; then
		echo "Password must be 8-128 characters using letters, numbers or _@%+=,.!?-" >&2
		return 2
	fi
	printf '%s' "${password}"
}

# Optional second stdin line of user-add/user-update: "STATE CAP_MB PERIOD".
function machineReadPolicyLine() {
	local extra
	POLICY_LINE_STATE=""
	POLICY_LINE_CAP=""
	POLICY_LINE_PERIOD=""
	read -r POLICY_LINE_STATE POLICY_LINE_CAP POLICY_LINE_PERIOD extra || true
	[[ -z ${POLICY_LINE_STATE} ]] && return 0
	if [[ -n ${extra} ]] || ! isValidPolicyState "${POLICY_LINE_STATE}" ||
		! isValidPolicyCap "${POLICY_LINE_CAP}" || ! isValidPolicyPeriod "${POLICY_LINE_PERIOD}"; then
		echo "Invalid traffic policy line" >&2
		return 2
	fi
}

function machineUserAdd() {
	if [[ $# -ne 1 ]] || ! isValidClientName "$1"; then
		echo "Invalid client name" >&2
		return 2
	fi
	[ -f "${PROXY3_PARAMS}" ] || {
		echo "3proxy is not installed" >&2
		return 1
	}
	loadParams
	local password status=0
	password=$(machineReadPassword) || return $?
	machineReadPolicyLine || return $?
	addClientRecord "$1" "${password}" "${POLICY_LINE_STATE}" "${POLICY_LINE_CAP}" "${POLICY_LINE_PERIOD}" || status=$?
	case ${status} in
	0)
		apiResult "result" "created"
		apiResult "user" "$1"
		reportTraffic
		;;
	3)
		echo "Client already exists: $1" >&2
		return 3
		;;
	*)
		echo "Failed to create client: $1" >&2
		return 1
		;;
	esac
}

function machineUserUpdate() {
	if [[ $# -ne 2 ]] || ! isValidClientName "$1" || ! isValidClientName "$2"; then
		echo "Invalid client name" >&2
		return 2
	fi
	[ -f "${PROXY3_PARAMS}" ] || {
		echo "3proxy is not installed" >&2
		return 1
	}
	loadParams
	local password status=0
	password=$(machineReadPassword) || return $?
	machineReadPolicyLine || return $?
	updateClientRecord "$1" "$2" "${password}" "${POLICY_LINE_STATE}" "${POLICY_LINE_CAP}" "${POLICY_LINE_PERIOD}" || status=$?
	case ${status} in
	0)
		apiResult "result" "updated"
		apiResult "user" "$2"
		reportTraffic
		;;
	3)
		echo "Client already exists: $2" >&2
		return 3
		;;
	4)
		echo "Client not found: $1" >&2
		return 4
		;;
	*)
		echo "Failed to update client: $1" >&2
		return 1
		;;
	esac
}

function machineUserDelete() {
	if [[ $# -ne 1 ]] || ! isValidClientName "$1"; then
		echo "Invalid client name" >&2
		return 2
	fi
	[ -f "${PROXY3_PARAMS}" ] || {
		echo "3proxy is not installed" >&2
		return 1
	}
	loadParams
	local status=0
	deleteClientRecord "$1" || status=$?
	case ${status} in
	0)
		apiResult "result" "deleted"
		apiResult "user" "$1"
		reportTraffic
		;;
	4)
		echo "Client not found: $1" >&2
		return 4
		;;
	*)
		echo "Failed to delete client: $1" >&2
		return 1
		;;
	esac
}

function machinePolicyApply() {
	if [[ $# -ne 0 ]]; then
		echo "Usage: --api policy-apply < NAME STATE CAP_MB PERIOD lines" >&2
		return 2
	fi
	[ -f "${PROXY3_PARAMS}" ] || {
		echo "3proxy is not installed" >&2
		return 1
	}
	loadParams
	applyPolicyRecords
}

function machineTraffic() {
	if [[ $# -ne 0 ]]; then
		echo "Usage: --api traffic" >&2
		return 2
	fi
	[ -f "${PROXY3_PARAMS}" ] || {
		echo "3proxy is not installed" >&2
		return 1
	}
	loadParams
	# Read-only: the policy file is replaced atomically and 3proxy writes its
	# counters to disk every minute, so no lock (which a running deployment may
	# hold for minutes) is needed and established sessions are not disturbed.
	loadPolicy || return 1
	reportTraffic
}

function machineService() {
	if [[ $# -ne 1 || ! $1 =~ ^(status|start|stop|restart)$ ]]; then
		echo "Usage: --api service status|start|stop|restart" >&2
		return 2
	fi
	if [ ! -f "${PROXY3_SERVICE}" ]; then
		echo "3proxy is not installed" >&2
		return 1
	fi
	local action=$1
	local service_status
	if [[ ${action} == "stop" || ${action} == "restart" ]] && [ -f "${PROXY3_PARAMS}" ]; then
		loadParams
		# 3proxy does not save its traffic counters on SIGTERM.
		flushTrafficCounters
	fi
	if [[ ${action} != "status" ]]; then
		runSystemctl "${action}" 3proxy || return 1
	fi
	if runSystemctl is-active --quiet 3proxy; then
		service_status=active
	else
		service_status=$(runSystemctl is-active 3proxy 2>/dev/null || true)
		service_status=${service_status:-inactive}
	fi
	if [[ ${action} == "start" || ${action} == "restart" ]] && [[ ${service_status} != "active" ]]; then
		echo "3proxy did not become active" >&2
		return 1
	fi
	apiResult "result" "${action}"
	apiResult "service" "${service_status}"
}

function machineMain() {
	local command=${1:-}
	shift || true
	case "${command}" in
	inspect)
		machineInspect "$@"
		;;
	deploy)
		machineDeploy "$@"
		;;
	user-add)
		machineUserAdd "$@"
		;;
	user-update)
		machineUserUpdate "$@"
		;;
	user-delete)
		machineUserDelete "$@"
		;;
	policy-apply)
		machinePolicyApply "$@"
		;;
	traffic)
		machineTraffic "$@"
		;;
	service)
		machineService "$@"
		;;
	*)
		echo "Unknown API command: ${command:-<empty>}" >&2
		return 2
		;;
	esac
}

function manageMenu() {
	echo "Welcome to 3proxy-install!"
	echo "The git repository is available at: https://github.com/a0s/3proxy-install"
	echo ""
	echo "It looks like 3proxy is already installed."
	echo ""
	echo "What do you want to do?"
	echo "   1) Add a new user"
	echo "   2) Remove existing user"
	echo "   3) Remove 3proxy"
	echo "   4) Exit"
	until [[ ${MENU_OPTION} =~ ^[1-4]$ ]]; do
		read -rp "Select an option [1-4]: " MENU_OPTION
	done
	case "${MENU_OPTION}" in
	1)
		newClient
		;;
	2)
		removeClient
		;;
	3)
		uninstall3proxy
		;;
	4)
		exit 0
		;;
	esac
}

if [[ ${1:-} == "--api" ]]; then
	shift
	initialCheck
	machineMain "$@"
	exit $?
fi

initialCheck

if [[ -e ${PROXY3_PARAMS} ]]; then
	loadParams
	manageMenu
else
	install3proxy
fi
