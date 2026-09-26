# Proxy Manager

Proxy Manager 是一个面向 3proxy 的多服务器控制面。项目保留原有单机 Bash 安装器，同时新增 Vue 3 在线控制台、Go API、SQLite 持久化任务和 SSH 执行器。

主要能力：

- 在线添加、编辑、移除服务器资产。
- 测试 SSH 连接并将 3proxy 部署到远程 Linux 服务器。
- 在线启动、停止和重启每台服务器上的 3proxy 服务。
- 多选服务器执行批量部署、启动、停止和重启。
- 创建、修改、轮换密码和删除代理账号。
- 将同一个代理账号分配到一台或多台服务器。
- 为代理账号设置流量额度（多台服务器合计，上行 + 下行）、使用期限和按日/周/月的自动流量重置；支持手动重置流量、启用和停用。超额或到期后账号在节点上自动停用并断开连接。
- 逐服务器记录部署和同步结果，在任务详情中展示每个目标的状态与错误，支持部分失败和只重试失败目标。
- 使用 HttpOnly 会话保护控制台，使用 AES-256-GCM 加密落盘凭据。

完整的原项目分析、数据模型、安全边界和演进建议见 [架构文档](docs/ARCHITECTURE.md)。

## 项目结构

```text
web/                  Vue 3 + TypeScript + Vite 控制台
server/               Go API、SQLite、worker、SSH/SCP 执行器
3proxy-install.sh      交互安装器与非交互节点接口
tests/                 原有实机测试和多发行版 Docker 测试
scripts/               本地开发与构建入口
Dockerfile             前后端生产镜像
compose.yaml           单实例部署编排
```

## 本地启动

要求：

- Node.js 22 或更高版本。
- Go 1.25 或更高版本。

安装依赖并启动：

```bash
npm run setup
npm run dev
```

打开 `http://127.0.0.1:5173`。开发环境默认账号：

```text
用户名：admin
密码：ProxyManager!2026
```

默认 `EXECUTOR_MODE=mock`。此模式完整执行 API、数据库、任务队列和页面流程，但不会连接真实服务器，适合本地验收界面和业务操作；模拟节点每次流量采集会为每个启用账号增加 32 MiB 用量，便于验证额度流程。主机名以 `fail.` 开头、包含 `.invalid`，或标签包含 `mock:fail` 时，可模拟目标服务器失败。

Vite 运行在 `5173`，Go API 运行在 `8080`。生产构建：

```bash
npm run build
npm test
```

## 连接真实服务器

在根目录创建 `.env`，可从 [.env.example](.env.example) 开始，然后至少设置：

```dotenv
APP_ENV=development
EXECUTOR_MODE=ssh
ADMIN_PASSWORD=replace-this-password
SESSION_SECRET=replace-with-at-least-32-random-characters
MASTER_KEY=replace-with-a-base64-encoded-32-byte-key
```

可使用 OpenSSL 生成控制面主密钥：

```bash
openssl rand -base64 32
```

目标服务器需要满足：

- 使用受支持的 Linux 发行版和 systemd。
- SSH 用户是 root，或者具备无需交互输入的 `sudo -n` 权限。
- 安装 Bash、SCP 和基础系统工具。
- 能访问软件包源和 GitHub，以便首次部署下载并编译固定版本的 3proxy。
- 云安全组或上游防火墙允许所配置的 HTTP/SOCKS 端口。

服务器表单支持 SSH 密码或未加密的 OpenSSH/PEM 私钥。浏览器提交后不会再次获得 SSH 凭据。首次连接采用 TOFU 记录主机密钥指纹，后续连接必须匹配；生产接入前应通过可信渠道核对目标服务器身份。

## 生产容器

生产环境必须设置随机强凭据，否则 API 会拒绝启动：

```dotenv
APP_ENV=production
ADMIN_USERNAME=admin
ADMIN_PASSWORD=replace-with-a-strong-password
SESSION_SECRET=replace-with-at-least-32-random-characters
MASTER_KEY=replace-with-a-base64-encoded-32-byte-key
EXECUTOR_MODE=ssh
COOKIE_SECURE=true
```

启动：

```bash
docker compose up -d --build
```

容器在 `http://SERVER:8080` 提供反向代理上游。生产模式强制使用 Secure 会话 Cookie，因此浏览器必须通过前置 TLS 反向代理的 `https://YOUR_DOMAIN` 访问，不能把明文 `http://SERVER:8080` 作为控制台登录入口。反向代理应覆盖而不是追加 `X-Forwarded-For` 与 `X-Forwarded-Proto`；将代理地址加入 `TRUSTED_PROXY_CIDRS`，分离部署时再把公开 HTTPS 来源加入 `CORS_ORIGINS`。

SQLite 数据保存在命名卷 `proxy-manager-data` 中，备份时必须同时保护数据库和 `MASTER_KEY`；丢失主密钥后无法恢复已加密的 SSH 凭据和代理密码。仅在受信任的本地开发环境需要直接 HTTP 登录时，使用 `APP_ENV=development` 和 `COOKIE_SECURE=false`，不要在生产环境关闭 HTTPS。

## 节点机器接口

原有交互方式仍然可用：

```bash
curl -O https://raw.githubusercontent.com/a0s/3proxy-install/master/3proxy-install.sh
chmod +x 3proxy-install.sh
sudo ./3proxy-install.sh
```

控制面使用新增的固定命令接口：

```bash
sudo ./3proxy-install.sh --api inspect
sudo ./3proxy-install.sh --api deploy 203.0.113.10 3128 1080 1.1.1.1 1.0.0.1
printf '%s\n' 'SafePass_123!' | sudo ./3proxy-install.sh --api user-add alice
printf '%s\n' 'NewPass_456!' | sudo ./3proxy-install.sh --api user-update alice alice_new
sudo ./3proxy-install.sh --api user-delete alice_new
sudo ./3proxy-install.sh --api service restart
# 流量策略：可选的第二行 stdin 为 "STATE CAP_MB PERIOD"
printf '%s
%s
' 'SafePass_123!' 'enabled 10240 0' | sudo ./3proxy-install.sh --api user-add bob
printf '%s
' 'bob disabled 10240 0' | sudo ./3proxy-install.sh --api policy-apply
sudo ./3proxy-install.sh --api traffic
```

用户名规则为 `[A-Za-z0-9_-]{1,64}`。密码长度为 8 到 128，只允许字母、数字和 `_@%+=,.!?-`。账号变更使用文件锁、原子写入和服务失败回滚。

## 配置项

| 变量 | 默认值 | 说明 |
|---|---:|---|
| `APP_ENV` | `development` | `development`、`test` 或 `production` |
| `HTTP_ADDR` | `:8080` | API 与生产静态页面监听地址 |
| `DB_PATH` | `server/data/proxy-manager.db` | SQLite 数据库路径 |
| `ADMIN_USERNAME` | `admin` | 控制台管理员账号 |
| `ADMIN_PASSWORD` | 仅开发默认值 | 生产环境必填 |
| `SESSION_SECRET` | 仅开发默认值 | 会话签名密钥，生产环境必填 |
| `MASTER_KEY` | 仅开发默认值 | AES-GCM 主密钥，生产环境必填 |
| `SESSION_TTL` | `12h` | 管理员登录会话有效期 |
| `EXECUTOR_MODE` | `mock` | `mock` 或 `ssh` |
| `WORKER_CONCURRENCY` | `4` | 并行任务 worker 数 |
| `WORKER_POLL_INTERVAL` | `300ms` | 持久化任务轮询间隔 |
| `SSH_TIMEOUT` | `10s` | SSH 连接超时 |
| `COMMAND_TIMEOUT` | `15m` | 单个远程操作超时 |
| `TRAFFIC_SYNC_INTERVAL` | `5m` | 从节点采集流量计数的间隔（最小 30s） |
| `TRAFFIC_RECONCILE_INTERVAL` | `30s` | 检查周期重置、到期和额度用尽并下发策略的间隔（最小 5s） |
| `COOKIE_SECURE` | 生产为 `true` | 只通过 HTTPS 发送会话 Cookie |
| `CORS_ORIGINS` | 空 | 分离部署时允许的来源列表 |
| `TRUSTED_PROXY_CIDRS` | 空 | 可提供转发 IP/协议头的可信反向代理地址或 CIDR |

## 测试

Web 与 API：

```bash
npm test
```

Shell 静态检查：

```bash
shellcheck 3proxy-install.sh tests/**/*.sh
shfmt -d 3proxy-install.sh tests/docker
```

单个发行版的安装器场景：

```bash
./tests/docker/run.sh --dist ubuntu-24.04
```

Docker 场景覆盖安装、交互增删、重复用户名、非交互 API 的查询/新增/修改/删除、流量策略下发与流量报告、服务启停和卸载。测试会创建临时容器；实机测试 `tests/3proxy-install-test.sh` 会真实修改本机 `/etc/3proxy`，只能在专用测试机上运行。

## 3proxy 安装器支持范围

已在项目测试矩阵中列出的发行版：

- Ubuntu 18.04、20.04、22.04、24.04、26.04。
- Debian 11、12、13。
- Fedora 42、43。
- CentOS Stream 9、10。

安装器固定使用 3proxy 上游 commit `7c1bc48c853f99f2574deb61fb9347a5d3056ad0`。

## License 与来源

项目沿用 MIT License。原始 3proxy 安装器来自 [a0s/3proxy-install](https://github.com/a0s/3proxy-install)，并受 [angristan/openvpn-install](https://github.com/angristan/openvpn-install) 与 [angristan/wireguard-install](https://github.com/angristan/wireguard-install) 启发。
