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
	local config_tmp
	config_tmp=$(mktemp "${PROXY3_CONFIG}.tmp.XXXXXX") || return 1
	chmod 600 "${config_tmp}" || {
		rm -f "${config_tmp}"
		return 1
	}

	if ! cat >"${config_tmp}" <<EOF; then
nserver ${DNS1}
nserver ${DNS2}

log
logformat "L%t%. L%t.%. %N.%p %E %U %C:%c %R:%r %O %I %h %T"

EOF
		rm -f "${config_tmp}"
		return 1
	fi

	if [ -f "${PROXY3_CONFIG}.users" ]; then
		cat "${PROXY3_CONFIG}.users" >>"${config_tmp}" || {
			rm -f "${config_tmp}"
			return 1
		}
	fi

	if ! cat >>"${config_tmp}" <<EOF; then

auth strong
allow *
proxy -p${HTTP_PORT}
socks -p${SOCKS_PORT}
flush
EOF
		rm -f "${config_tmp}"
		return 1
	fi

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
	if ! cat >"${params_tmp}" <<EOF; then
SERVER_PUB_IP=${SERVER_PUB_IP}
HTTP_PORT=${HTTP_PORT}
SOCKS_PORT=${SOCKS_PORT}
DNS1=${DNS1}
DNS2=${DNS2}
IPTABLES_AVAILABLE=${IPTABLES_AVAILABLE}
EOF
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

function commitUsersFile() {
	local candidate=$1
	local backup
	local had_users=false
	backup=$(mktemp "${PROXY3_CONFIG}.users.backup.XXXXXX") || return 1

	if [ -f "${PROXY3_CONFIG}.users" ]; then
		cp "${PROXY3_CONFIG}.users" "${backup}" || {
			rm -f "${backup}"
			return 1
		}
		had_users=true
	fi

	mv -f "${candidate}" "${PROXY3_CONFIG}.users" || {
		rm -f "${backup}"
		return 1
	}
	chmod 600 "${PROXY3_CONFIG}.users" || return 1
	if generateConfig && restartProxyService; then
		rm -f "${backup}"
		return 0
	fi

	echo "Failed to apply user configuration; restoring the previous version" >&2
	if [[ ${had_users} == "true" ]]; then
		mv -f "${backup}" "${PROXY3_CONFIG}.users"
	else
		rm -f "${PROXY3_CONFIG}.users" "${backup}"
	fi
	generateConfig || true
	restartProxyService || true
	return 1
}

function addClientRecord() {
	local name=$1
	local password=$2
	local users_tmp

	isValidClientName "${name}" || return 2
	isValidClientPassword "${password}" || return 2
	acquireConfigLock || return 1

	if [ -f "${PROXY3_CONFIG}.users" ] && grep -q "^users ${name}:" "${PROXY3_CONFIG}.users"; then
		return 3
	fi

	users_tmp=$(mktemp "${PROXY3_CONFIG}.users.tmp.XXXXXX") || return 1
	if [ -f "${PROXY3_CONFIG}.users" ]; then
		cat "${PROXY3_CONFIG}.users" >"${users_tmp}" || {
			rm -f "${users_tmp}"
			return 1
		}
	fi
	printf 'users %s:CL:%s\n' "${name}" "${password}" >>"${users_tmp}"
	chmod 600 "${users_tmp}"
	commitUsersFile "${users_tmp}"
}

function updateClientRecord() {
	local old_name=$1
	local new_name=$2
	local password=$3
	local users_tmp line found=false

	isValidClientName "${old_name}" || return 2
	isValidClientName "${new_name}" || return 2
	isValidClientPassword "${password}" || return 2
	acquireConfigLock || return 1

	if [ ! -f "${PROXY3_CONFIG}.users" ] || ! grep -q "^users ${old_name}:" "${PROXY3_CONFIG}.users"; then
		return 4
	fi
	if [[ ${old_name} != "${new_name}" ]] && grep -q "^users ${new_name}:" "${PROXY3_CONFIG}.users"; then
		return 3
	fi

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
	commitUsersFile "${users_tmp}"
}

function deleteClientRecord() {
	local name=$1
	local users_tmp line found=false

	isValidClientName "${name}" || return 2
	acquireConfigLock || return 1
	if [ ! -f "${PROXY3_CONFIG}.users" ]; then
		return 4
	fi

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
	commitUsersFile "${users_tmp}"
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
	addClientRecord "$1" "${password}" || status=$?
	case ${status} in
	0)
		apiResult "result" "created"
		apiResult "user" "$1"
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
	updateClientRecord "$1" "$2" "${password}" || status=$?
	case ${status} in
	0)
		apiResult "result" "updated"
		apiResult "user" "$2"
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
