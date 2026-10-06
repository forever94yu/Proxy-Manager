# Proxy Manager

[![Lint](https://github.com/forever94yu/Proxy-Manager/actions/workflows/lint.yml/badge.svg)](https://github.com/forever94yu/Proxy-Manager/actions/workflows/lint.yml)
![Vue 3](https://img.shields.io/badge/Vue-3-42b883)
![Go 1.25+](https://img.shields.io/badge/Go-1.25%2B-00ADD8)
![SQLite](https://img.shields.io/badge/SQLite-embedded-003B57)
![Docker](https://img.shields.io/badge/Docker-ready-2496ED)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

Proxy Manager 是一个 [3proxy](https://github.com/3proxy/3proxy) 多服务器管理控制台。你只需要在一台机器上部署它，就能在网页里通过 SSH 把 3proxy 部署到多台 Linux 服务器，统一管理 HTTP/SOCKS5 代理账号、流量额度和有效期，不用再一台台登录服务器改配置。

![运行概览](docs/images/dashboard.png)

## 目录

- [它能做什么](#它能做什么)
- [界面预览](#界面预览)
- [工作原理](#工作原理)
- [快速体验](#快速体验)
- [生产部署](#生产部署)
- [准备节点服务器](#准备节点服务器)
- [使用指南](#使用指南)
- [流量额度说明](#流量额度说明)
- [配置参考](#配置参考)
- [升级与备份](#升级与备份)
- [常见问题](#常见问题)
- [本地开发](#本地开发)
- [节点脚本](#节点脚本)
- [许可证与致谢](#许可证与致谢)

## 它能做什么

**适用场景**：手上有多台 VPS 或云服务器，想把它们都变成带账号认证的 HTTP/SOCKS5 代理出口，并且希望集中管理账号、分配服务器、限制流量和使用期限。

| 模块 | 能力 |
|---|---|
| 服务器管理 | 在线添加、编辑、删除服务器；测试 SSH 连接；一键把 3proxy 部署到远程服务器；启动、停止、重启 3proxy 服务；勾选多台服务器批量部署或启停 |
| 代理账号 | 创建、修改、删除代理账号；自动生成或自定义密码，支持轮换；同一个账号可以同时分配到多台服务器 |
| 流量与期限 | 按账号设置流量额度（所有服务器合计，上行加下行）、到期时间，以及按日、周、月自动重置流量；支持手动重置、启用和停用；超额或到期后账号会在节点上自动停用并断开连接 |
| 任务记录 | 所有远程操作都会生成后台任务，逐台服务器记录执行结果和错误信息；部分服务器失败时，可以只重试失败的那几台 |
| 安全 | 控制台使用 HttpOnly 会话 Cookie 并限制登录尝试次数；SSH 凭据和代理密码用 AES-256-GCM 加密后才写入数据库；首次连接时记录 SSH 主机指纹，后续连接必须一致 |
| 在线升级 | 在 **系统更新** 页面检查 GitHub 上的新版本、查看更新说明，一键下载并重启到新版本；安装包经过 SHA-256 校验，升级前自动备份数据库，新版本启动失败时自动回滚 |

## 界面预览

> 以下截图使用前端内置的演示数据。

**服务器**：查看每台服务器的连接状态、3proxy 服务状态、系统版本、端口和账号数；行尾图标依次是测试连接、部署或重启、停止、编辑、删除。

![服务器列表](docs/images/servers.png)

**代理用户**：查看每个账号的使用状态、同步状态、本周期流量、到期时间和目标服务器。

![代理用户](docs/images/users.png)

**任务记录**：每个远程操作的进度、成功和失败数量。

![任务记录](docs/images/jobs.png)

**系统更新**：当前版本、GitHub 上的最新版本和更新说明，点击按钮即可在线升级。

![系统更新](docs/images/update.png)

<table>
  <tr>
    <td width="50%"><img src="docs/images/server-form.png" alt="添加服务器"></td>
    <td width="50%"><img src="docs/images/user-form.png" alt="创建代理用户"></td>
  </tr>
  <tr>
    <td align="center">添加服务器：SSH 连接信息与代理端口</td>
    <td align="center">创建代理用户：密码、流量额度、期限、重置周期与目标服务器</td>
  </tr>
  <tr>
    <td width="50%"><img src="docs/images/job-detail.png" alt="任务详情"></td>
    <td width="50%"><img src="docs/images/login.png" alt="登录"></td>
  </tr>
  <tr>
    <td align="center">任务详情：逐台服务器的执行结果，支持重试失败目标</td>
    <td align="center">登录页</td>
  </tr>
</table>

## 工作原理

```mermaid
flowchart LR
    Browser["浏览器<br/>Vue 3 控制台"] -- HTTPS --> RP["反向代理<br/>Caddy / Nginx"]
    RP --> PM["Proxy Manager<br/>Go API + 后台任务"]
    PM --- DB[("SQLite")]
    PM -- "SSH + SCP" --> N1["节点服务器 A<br/>3proxy"]
    PM -- "SSH + SCP" --> N2["节点服务器 B<br/>3proxy"]
    Client["代理客户端"] -- "HTTP / SOCKS5" --> N1
    Client -- "HTTP / SOCKS5" --> N2
```

- **控制面**（本项目）是一个 Go 程序，自带前端页面、SQLite 数据库和后台任务队列，单个容器即可运行。
- 在网页上的每个操作（部署、创建账号、启停服务等）都会写入任务队列。后台任务通过 SSH 连接目标服务器，用 SCP 上传 [`3proxy-install.sh`](3proxy-install.sh) 到 `/tmp` 下的随机文件名，以 `--api` 非交互模式执行，执行完即删除。
- **节点服务器**不需要安装任何 Agent，只需要开放 SSH，并允许登录用户以 root 身份执行命令。首次部署时会从 GitHub 下载固定版本的 3proxy 源码并编译，然后注册为 systemd 服务。
- 代理客户端直接连接节点服务器的 HTTP/SOCKS5 端口，流量不经过控制面。

## 快速体验

不需要任何服务器，5 分钟内就能在本机看到完整界面。默认的 `mock` 模式会完整走一遍 API、数据库和任务队列，但不会真正连接服务器。

**环境要求**：[Node.js](https://nodejs.org/) 22 或更高版本，[Go](https://go.dev/dl/) 1.25 或更高版本，Git。

```bash
git clone https://github.com/forever94yu/Proxy-Manager.git
cd Proxy-Manager
npm run setup   # 安装前端依赖并下载 Go 依赖
npm run dev     # 同时启动 Go API（8080 端口）和 Vite 前端（5173 端口）
```

浏览器打开 <http://127.0.0.1:5173>，用开发环境默认账号登录：

```text
用户名：admin
密码：ProxyManager!2026
```

在 mock 模式下可以随便添加服务器（主机地址填 `10.0.0.1` 这类值，认证方式选“密码”并随便填一个即可）、部署、创建用户。模拟节点每次流量采集会给每个启用的账号增加 32 MiB 用量，方便观察额度流程。主机名以 `fail.` 开头、包含 `.invalid`，或者标签包含 `mock:fail` 的服务器会模拟执行失败，可以用来测试失败重试。

## 生产部署

### 部署前准备

| 项目 | 要求 |
|---|---|
| 控制面主机 | 一台 Linux 服务器（x86_64 或 arm64），建议 1 核 2 GB 内存以上（构建镜像时要编译前端和 Go 程序）；安装 Docker 20.10+ 与 Docker Compose v2 |
| 网络 | 控制面主机能访问所有节点服务器的 SSH 端口 |
| 域名与证书 | 生产模式的会话 Cookie 带 `Secure` 属性，**浏览器必须通过 HTTPS 访问控制台**。需要准备一个解析到控制面主机的域名（如 `pm.example.com`），证书可以用 Caddy 自动申请。没有域名可以用 [SSH 隧道访问](#没有域名时通过-ssh-隧道访问) |
| 节点服务器 | 见 [准备节点服务器](#准备节点服务器) |

### 方式一 Docker Compose（推荐）

**第 1 步：安装 Docker**（已安装可跳过）

```bash
curl -fsSL https://get.docker.com | sudo sh
sudo systemctl enable --now docker
docker compose version   # 确认 Compose v2 可用
```

**第 2 步：获取代码**

```bash
git clone https://github.com/forever94yu/Proxy-Manager.git
cd Proxy-Manager
```

**第 3 步：创建 `.env` 配置文件**

从生产模板 [`.env.production.example`](.env.production.example) 复制一份，并自动填入随机生成的会话密钥和加密主密钥：

```bash
cp .env.production.example .env
sed -i "s|^SESSION_SECRET=.*|SESSION_SECRET=$(openssl rand -hex 32)|; s|^MASTER_KEY=.*|MASTER_KEY=$(openssl rand -base64 32)|" .env
chmod 600 .env
```

然后编辑 `.env`（例如 `nano .env`），填写两项：

- `ADMIN_PASSWORD`：控制台管理员密码，至少 8 位。建议不要包含 `$`、`#`、引号和空格，以免被 Compose 当作变量或注释解析。
- `CORS_ORIGINS`：你实际访问控制台的地址，例如 `https://pm.example.com`，末尾不要带斜杠。如果不设置这一项，通过 HTTPS 反向代理登录时会提示“请求来源不被允许”。

`TRUSTED_PROXY_CIDRS` 可以先留空，第 5 步再设置。模板中的其他项保持默认即可，每一项的含义见文件内注释和 [配置参考](#配置参考)。

> [!IMPORTANT]
> - **不要用 `.env.example` 作为生产配置。** 那是本地开发模板，其中的 `DB_PATH`、`STATIC_DIR`、`INSTALL_SCRIPT_PATH` 是相对路径，会覆盖镜像内置的正确路径，导致页面 404、数据写不进持久化卷。
> - **`MASTER_KEY` 必须妥善备份，并且之后不能修改。** 数据库里的 SSH 凭据和代理密码都用它加密，丢失或更换后这些数据将无法解密。

**第 4 步：构建并启动**

```bash
docker compose up -d --build
```

首次构建需要下载基础镜像并编译，通常要几分钟。完成后检查状态：

```bash
docker compose ps                        # 状态应为 running
docker compose logs -f                   # 查看日志，按 Ctrl+C 退出
curl -fsS http://127.0.0.1:8080/healthz  # 应返回 {"data":{"status":"ok"}}
```

如果容器启动后马上退出，多半是配置有误，`docker compose logs` 会给出原因，例如 `production requires explicit ADMIN_PASSWORD`。

> [!TIP]
> 如果反向代理和容器在同一台机器上，建议把 `compose.yaml` 里的 `"8080:8080"` 改成 `"127.0.0.1:8080:8080"`，只允许本机访问 8080 端口，然后重新执行 `docker compose up -d`。注意 Docker 发布的端口会绕过 ufw 等主机防火墙规则，只靠防火墙挡不住 8080。

**第 5 步：配置 HTTPS 反向代理**

按照下文 [配置 HTTPS 反向代理](#配置-https-反向代理) 完成配置后，打开 `https://pm.example.com`，用 `.env` 里设置的管理员账号登录。

（可选，推荐）设置 `TRUSTED_PROXY_CIDRS`，让登录限流识别真实的客户端 IP。不设置时，所有访问者在控制面看来都来自反向代理的地址，会共用同一个登录失败计数（15 分钟内最多 10 次），别人故意输错密码就可能让你暂时无法登录。反向代理通过宿主机访问 Docker 容器时，容器看到的来源地址是 Docker 网络的网关。**前提是 8080 端口已经按上面的提示只绑定到 `127.0.0.1`**，确保只有本机的反向代理能访问容器，否则被信任的网关地址可能被用来伪造转发头：

```bash
# 网络名一般是 “目录名小写_default”，可以用 docker network ls 查看
docker network inspect proxy-manager_default -f '{{range .IPAM.Config}}{{.Gateway}}{{end}}'
# 例如输出 172.18.0.1，就在 .env 里写 TRUSTED_PROXY_CIDRS=172.18.0.1
docker compose up -d   # 修改 .env 后重新创建容器
```

### 方式二 二进制与 systemd

不想用 Docker 时，可以编译成单个二进制文件运行，它会同时托管前端页面。

**构建**（在任意装有 Node.js 22+ 和 Go 1.25+ 的机器上都可以，包括 Windows 和 macOS）：

```bash
git clone https://github.com/forever94yu/Proxy-Manager.git
cd Proxy-Manager
npm ci --prefix web
npm run build:web
# 交叉编译 Linux 版本；arm64 服务器请把 GOARCH 改为 arm64
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go -C server build -trimpath -ldflags="-s -w" -o ../dist/proxy-manager .
```

以上是 Linux、macOS 或 Git Bash 的写法。Windows PowerShell 下先执行 `$env:CGO_ENABLED="0"; $env:GOOS="linux"; $env:GOARCH="amd64"`，再运行 `go -C server build ...` 这一行。

需要上传到服务器的有这几样：`dist/proxy-manager`、`web/dist/` 目录、`3proxy-install.sh`，以及配置模板 `.env.production.example`。

**安装**（在控制面服务器上，以下路径可以自行调整）：

```bash
sudo useradd --system --home-dir /opt/proxy-manager --shell /usr/sbin/nologin proxy-manager
sudo mkdir -p /opt/proxy-manager/web
sudo cp dist/proxy-manager 3proxy-install.sh /opt/proxy-manager/
sudo cp -r web/dist/. /opt/proxy-manager/web/
sudo chmod +x /opt/proxy-manager/proxy-manager
```

从生产模板创建配置文件 `/etc/proxy-manager.env`。下面的命令会填入随机密钥，取消模板末尾路径配置的注释，并信任本机的反向代理：

```bash
sudo cp .env.production.example /etc/proxy-manager.env
sudo chmod 600 /etc/proxy-manager.env
sudo sed -i \
  -e "s|^SESSION_SECRET=.*|SESSION_SECRET=$(openssl rand -hex 32)|" \
  -e "s|^MASTER_KEY=.*|MASTER_KEY=$(openssl rand -base64 32)|" \
  -e "s|^TRUSTED_PROXY_CIDRS=.*|TRUSTED_PROXY_CIDRS=127.0.0.1/32,::1/128|" \
  -e 's/^# \(HTTP_ADDR\|DB_PATH\|STATIC_DIR\|INSTALL_SCRIPT_PATH\)=/\1=/' \
  /etc/proxy-manager.env
```

然后执行 `sudo nano /etc/proxy-manager.env`，填写 `ADMIN_PASSWORD` 和 `CORS_ORIGINS`（例如 `https://pm.example.com`）。如果改了上面的安装路径，还要同步修改文件末尾的 `STATIC_DIR` 和 `INSTALL_SCRIPT_PATH`。

创建 systemd 服务 `/etc/systemd/system/proxy-manager.service`：

```ini
[Unit]
Description=Proxy Manager
After=network-online.target
Wants=network-online.target

[Service]
User=proxy-manager
WorkingDirectory=/opt/proxy-manager
EnvironmentFile=/etc/proxy-manager.env
StateDirectory=proxy-manager
ExecStart=/opt/proxy-manager/proxy-manager
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

启动服务：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now proxy-manager
sudo systemctl status proxy-manager
journalctl -u proxy-manager -f   # 查看日志
```

然后同样需要 [配置 HTTPS 反向代理](#配置-https-反向代理)。

### 配置 HTTPS 反向代理

以下两种任选其一。先把域名解析到控制面主机，并在防火墙和安全组放行 80、443 端口。

**Caddy（推荐，自动申请和续期证书）**

安装方法见 [Caddy 官方文档](https://caddyserver.com/docs/install)，然后编辑 `/etc/caddy/Caddyfile`：

```caddyfile
pm.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

```bash
sudo systemctl reload caddy
```

Caddy 默认会转发原始 `Host`，并正确设置 `X-Forwarded-For` 和 `X-Forwarded-Proto`，无需额外配置。

**Nginx**

证书可以用 [Certbot](https://certbot.eff.org/) 申请。站点配置示例：

```nginx
server {
    listen 80;
    server_name pm.example.com;
    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl;
    server_name pm.example.com;

    ssl_certificate     /etc/letsencrypt/live/pm.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/pm.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host              $host;
        # 覆盖而不是追加，防止客户端伪造来源 IP
        proxy_set_header X-Forwarded-For   $remote_addr;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

```bash
sudo nginx -t && sudo systemctl reload nginx
```

### 没有域名时通过 SSH 隧道访问

如果只是自己使用、暂时没有域名，可以不配置反向代理，通过 SSH 隧道访问。Chrome、Edge 和 Firefox 会把 `http://localhost` 当作安全来源，所以带 `Secure` 属性的 Cookie 也能正常保存。

1. 按照上文的提示，让 8080 端口只监听 `127.0.0.1`。这种方式下浏览器地址与服务端同源，`CORS_ORIGINS` 可以留空。
2. 在你自己的电脑上执行：

   ```bash
   ssh -N -L 8080:127.0.0.1:8080 user@控制面主机IP
   ```

3. 浏览器打开 <http://localhost:8080> 并登录。

> 不要直接用 `http://服务器IP:8080` 登录生产环境的控制台。浏览器会丢弃这种明文连接下的 Secure Cookie，现象是登录成功后立刻被跳回登录页。

## 准备节点服务器

节点服务器是实际运行 3proxy、给代理客户端提供服务的机器。每台节点需要满足：

- **操作系统**：使用 systemd 的 Linux 发行版。项目测试矩阵覆盖 Ubuntu 18.04 至 26.04、Debian 11 至 13、Fedora 42 和 43、CentOS Stream 9 和 10。
- **SSH 权限**：SSH 用户是 `root`，或者可以通过 `sudo -n` 免密执行命令。
- **基础工具**：安装了 `bash` 和 `scp`。最小化安装的 CentOS、Fedora 可能没有 `scp`，需要先执行 `dnf install -y openssh-clients`。
- **外网访问**：首次部署时需要访问系统软件源安装编译依赖，并从 GitHub 下载 3proxy 源码。如果服务器访问 GitHub 很慢，首次部署可能超时（默认 15 分钟，见 `COMMAND_TIMEOUT`）。
- **端口放行**：在云服务商的安全组或上游防火墙中放行 SSH 端口（只需对控制面主机开放），以及代理端口（默认 HTTP `3128`、SOCKS5 `1080`，需要对代理客户端开放）。

### 推荐：使用专用账号和 SSH 密钥

**1. 在你自己的电脑上生成一对专用密钥**（不要设置密码短语，控制面不支持带密码的私钥）：

```bash
ssh-keygen -t ed25519 -N "" -C proxy-manager -f ./proxy-manager-key
# 生成两个文件：proxy-manager-key（私钥，稍后粘贴到控制台）和 proxy-manager-key.pub（公钥）
```

Windows PowerShell 下去掉 `-N ""`，在提示输入密码短语（passphrase）时直接按两次回车即可。

**2. 在每台节点上以 root 身份创建专用账号**，把 `proxy-manager-key.pub` 的内容填到下面的 `ssh-ed25519 AAAA...` 位置：

```bash
useradd -m -s /bin/bash proxymgr
install -d -m 700 -o proxymgr -g proxymgr /home/proxymgr/.ssh
echo 'ssh-ed25519 AAAA...替换为你的公钥... proxy-manager' > /home/proxymgr/.ssh/authorized_keys
chown proxymgr:proxymgr /home/proxymgr/.ssh/authorized_keys
chmod 600 /home/proxymgr/.ssh/authorized_keys

echo 'proxymgr ALL=(ALL) NOPASSWD: ALL' > /etc/sudoers.d/proxymgr
chmod 440 /etc/sudoers.d/proxymgr
visudo -cf /etc/sudoers.d/proxymgr   # 检查语法，应输出 parsed OK
```

**3. 在控制面主机上验证**（可选）：

```bash
ssh -i proxy-manager-key proxymgr@节点IP 'sudo -n true && echo OK'
```

> [!NOTE]
> 控制面需要以 root 权限执行安装脚本，所以免密 sudo 实际上等同于 root 权限。请把这个私钥当作 root 凭据保管。
>
> 如果使用 firewalld，需要手动放行代理端口：`firewall-cmd --permanent --add-port=3128/tcp --add-port=1080/tcp && firewall-cmd --reload`。安装脚本会在服务启动时通过 iptables 插入放行规则，但在 firewalld（nftables 后端）环境下这些规则不一定生效。

## 使用指南

下面以“把两台服务器变成代理出口，并给一个客户开通账号”为例，介绍完整流程。

### 1. 添加服务器

进入 **服务器** 页面，点击右上角 **添加服务器**，填写表单：

| 字段 | 说明 |
|---|---|
| 服务器名称 | 便于识别的名称，例如“东京 01”，不能重名 |
| 标签 | 可选，用逗号分隔，例如 `生产, 日本`，可以在列表中按标签搜索 |
| 主机地址 | 节点的 IP 地址或域名，控制面通过它连接 SSH，通常也就是代理客户端要连接的地址 |
| 端口 / SSH 用户 | SSH 端口（默认 22）和登录用户（例如上文创建的 `proxymgr`，或 `root`） |
| 认证方式 | **密钥**：粘贴完整的私钥内容，包括 `-----BEGIN OPENSSH PRIVATE KEY-----` 开头和结尾两行；**密码**：填写 SSH 登录密码 |
| HTTP / HTTPS 端口 | 3proxy HTTP 代理端口，默认 `3128`，同时支持 HTTPS 的 CONNECT 请求 |
| SOCKS5 端口 | 默认 `1080`，不能和 HTTP 端口相同 |
| DNS 服务器 | 3proxy 解析域名时使用的 1 到 2 个 IPv4 地址，例如 `1.1.1.1, 8.8.8.8` |

保存后，凭据会加密存储，之后不会在页面上再次显示。编辑服务器时，凭据留空表示保持不变。

### 2. 测试连接

在服务器所在行点击 **测试连接**（插头图标）。任务会在后台执行，可以在 **任务记录** 页面查看结果。连接成功后，服务器会显示为“在线”，并识别出操作系统版本。

首次连接成功时会记录节点的 SSH 主机指纹（TOFU，首次使用即信任），之后每次连接都会校验指纹是否一致。如果对安全要求较高，接入前请通过可信渠道核对节点身份。

### 3. 部署 3proxy

点击服务器行的 **部署 3proxy**（火箭图标），确认后开始部署。部署过程包括安装编译依赖、下载并编译 3proxy、生成配置、注册并启动 systemd 服务，首次部署通常需要几分钟。完成后，服务器状态变为“已部署”和“运行中”。

**批量操作**：勾选多台服务器后，列表上方会出现 **批量部署 / 启动 / 停止 / 重启** 按钮，每台服务器的执行结果会分别记录。

**修改端口或 DNS**：编辑服务器保存后，新配置不会自动下发。需要勾选这台服务器，点击 **批量部署** 重新部署，新端口和 DNS 才会生效。

### 4. 创建代理用户

进入 **代理用户** 页面，点击 **创建用户**：

1. **代理用户名**：1 到 64 位字母、数字、下划线或短横线。
2. **密码处理**：
   - **自动生成**（推荐）：由服务器生成 32 位随机密码。
   - **自定义**：8 到 128 位，只能使用字母、数字和 `_@%+=,.!?-`。
3. **使用限制**：
   - **流量额度**：不限，或者设置一个上限（所有目标服务器合计，上行加下行）。
   - **使用期限**：永久，或者指定到期时间，可以用 +7 天、+30 天、+90 天、+1 年快速设置。
   - **流量重置**：不重置，或者每天、每周、每月从指定时间点开始自动清零。
   - **账号已启用**开关：关闭后账号保留，但无法使用代理。
4. **目标服务器**：勾选要开通这个账号的服务器，只有已部署的服务器可以选择。

点击 **创建并同步** 后，账号会通过后台任务下发到每台目标服务器。同步状态会依次显示为“等待同步”、“已同步”，部分服务器失败时显示“部分同步”或“失败”。

保存后会弹出 **连接信息** 窗口，列出用户名、密码，以及每台目标服务器的地址、HTTP/SOCKS5 端口和代理链接。每一项右侧都有复制按钮，**复制全部** 会把所有信息以文本形式复制到剪贴板。关闭窗口后，可以随时点击用户行的 **连接信息**（链接图标）再次查看和复制。

窗口顶部的 **扫码导入** 可以生成二维码，供手机代理客户端扫码导入，有两种方式：

| 方式 | 二维码内容 | 适合 | 注意 |
|---|---|---|---|
| **单个节点** | 一台服务器的 SOCKS5 连接信息 | 只绑定了一台服务器的账号（默认） | 导入后不需要访问控制台；修改密码或服务器后需要重新扫码 |
| **订阅** | 订阅地址，包含全部目标服务器的 SOCKS5 和 HTTP 节点 | 绑定了多台服务器的账号（默认） | 客户端更新订阅即可同步密码和服务器变更；手机必须能访问控制台 |

账号只绑定一台服务器时默认显示节点二维码，绑定多台时默认显示订阅二维码，两种方式都可以手动切换。二维码按客户端生成：

- **v2rayNG / NekoBox**：节点为 v2rayN 格式的 `socks://` 链接；订阅为 `https://控制台地址/sub/...`。
- **Shadowrocket**：节点为 Shadowrocket 格式的 `socks://` 链接；订阅包装为 `shadowrocket://add/sub/...`，用 Shadowrocket 首页左上角扫码，或用 iPhone 相机扫码后在 Shadowrocket 中打开。
- **Clash**（Clash Meta for Android、FlClash 等）：只能通过订阅导入，客户端会得到包含全部节点、一个“节点选择”策略组和全局规则的完整配置。

订阅地址不需要登录控制台即可访问，相当于账号密码，请不要公开分享。泄露后点击 **重置订阅地址**，旧地址会立即失效，已导入的客户端需要重新扫码。删除代理用户后，订阅地址也随之失效。

订阅地址基于控制台的访问地址生成。如果你通过 SSH 隧道或内网地址访问控制台，请设置 [`PUBLIC_URL`](#配置参考) 为手机能访问的地址，例如 `https://pm.example.com`。v2rayNG 默认只接受 HTTPS 订阅地址（局域网 IP 除外）。

### 5. 客户端连接

把以下信息交给使用者即可（可直接从 **连接信息** 窗口复制），其中 `203.0.113.10` 替换为节点的主机地址：

```text
HTTP/HTTPS 代理：203.0.113.10:3128
SOCKS5 代理：   203.0.113.10:1080
用户名 / 密码： 在控制台创建的代理账号
```

用 curl 验证，返回的应该是节点服务器的公网 IP：

```bash
# HTTP 代理
curl -x http://alice:PASSWORD@203.0.113.10:3128 https://api.ipify.org
# SOCKS5 代理（socks5h 表示由代理端解析域名）
curl -x socks5h://alice:PASSWORD@203.0.113.10:1080 https://api.ipify.org
```

> 自动生成的密码只包含字母、数字、`-` 和 `_`，可以直接放在 URL 中。自定义密码如果包含 `@`、`%`、`+`、`=`、`,`、`!`、`?` 等字符，写进 URL 时需要先做 URL 编码，例如 `@` 要写成 `%40`。控制台 **连接信息** 窗口中复制出的代理链接已经完成编码。

浏览器可以使用 SwitchyOmega 等代理插件，系统层面可以使用 Clash、Proxifier 等支持 HTTP/SOCKS5 认证的客户端。

### 6. 日常管理

| 操作 | 位置 |
|---|---|
| 启动、停止、重启 3proxy | 服务器行尾图标，或勾选多台后批量操作 |
| 查看或复制账号密码、连接地址和代理链接，生成扫码导入二维码 | 代理用户行的 **连接信息**（链接图标） |
| 订阅地址泄露后作废旧地址 | **连接信息** → **扫码导入** → **订阅** → **重置订阅地址** |
| 修改账号、轮换密码、调整目标服务器 | 代理用户行的 **编辑** 按钮；密码可以选择保持不变、重新生成或自定义 |
| 手动清零本周期流量 | 代理用户行的 **重置流量** 按钮 |
| 临时停用或恢复账号 | 代理用户行的 **停用 / 启用** 按钮 |
| 删除账号 | 代理用户行的 **删除** 按钮，会同步从所有目标服务器上删除 |
| 查看执行详情 | **任务记录** 页面点击 **查看详情**（眼睛图标），可以看到每台服务器的状态、尝试次数和错误信息 |
| 重试失败的操作 | 任务详情中的 **重试失败目标**，只会重新执行失败的那几台服务器 |
| 移除服务器 | 服务器行的 **删除** 按钮。**只删除控制台里的记录，不会卸载节点上的 3proxy**，账号与这台服务器的绑定关系也会一并删除。如需卸载，请登录节点运行安装脚本并选择卸载 |

服务器有排队中或执行中的任务时，不能编辑或删除，需要等任务结束后再操作。

## 流量额度说明

- **统计方式**：由节点上的 3proxy 计数器统计，包括上行和下行流量。控制面每 5 分钟（`TRAFFIC_SYNC_INTERVAL`）采集一次，把所有服务器的用量加起来与额度比较。
- **超额处理**：单台服务器上，账号用量达到上限后，3proxy 会直接拒绝新连接。分配到多台服务器的账号，控制面会根据其他服务器的已用流量动态计算每台服务器的本地上限，因此可能少量超用，超用量大约等于一个采集周期内其他服务器产生的流量。
- **自动停用**：控制面每 30 秒（`TRAFFIC_RECONCILE_INTERVAL`）检查一次是否到期、是否用尽额度、是否到了重置时间，必要时向节点下发策略。账号被停用后，已经建立的连接也会被断开。
- **重置规则**：修改额度、期限或重置周期不会清空已用流量，只有手动重置或到达自动重置时间才会清零。月度重置如果设在 29 至 31 日，在没有这一天的月份会落在月末。
- **防止绕过**：把账号从服务器上解绑后，本周期在这台服务器上用掉的流量仍然计入额度，下个周期才会清除。

更完整的设计说明见 [架构文档](docs/ARCHITECTURE.md#9-流量额度使用期限与流量重置)。

## 配置参考

所有配置都通过环境变量设置：Docker 部署写在 `.env`，systemd 部署写在 `EnvironmentFile`，本地开发时 `npm run dev` 会自动读取项目根目录的 `.env`。

| 变量 | 默认值 | 说明 |
|---|---|---|
| `APP_ENV` | `development` | `development`、`test` 或 `production`。生产环境必须显式设置下面三个密钥，否则拒绝启动 |
| `ADMIN_USERNAME` | `admin` | 控制台管理员用户名 |
| `ADMIN_PASSWORD` | 开发环境默认 `ProxyManager!2026` | 控制台管理员密码，至少 8 位，生产环境必填 |
| `SESSION_SECRET` | 开发环境内置值 | 会话签名密钥，至少 32 个字符，生产环境必填 |
| `MASTER_KEY` | 开发环境内置值 | 数据加密主密钥，推荐使用 `openssl rand -base64 32` 生成，生产环境必填，**设置后不能修改** |
| `EXECUTOR_MODE` | `mock`（Docker 镜像中为 `ssh`） | `mock` 只模拟执行，`ssh` 会真实连接服务器 |
| `HTTP_ADDR` | `:8080` | 监听地址 |
| `DB_PATH` | `data/proxy-manager.db`（Docker 镜像中为 `/data/proxy-manager.db`） | SQLite 数据库路径 |
| `STATIC_DIR` | `../web/dist`（Docker 镜像中为 `/app/web`） | 前端静态文件目录 |
| `INSTALL_SCRIPT_PATH` | `../3proxy-install.sh`（Docker 镜像中为 `/app/3proxy-install.sh`） | 上传到节点执行的安装脚本 |
| `COOKIE_SECURE` | 生产环境强制为 `true` | 会话 Cookie 是否只通过 HTTPS 发送 |
| `CORS_ORIGINS` | 生产环境为空 | 允许访问 API 的来源，多个用逗号分隔，必须与浏览器地址栏的协议、域名、端口完全一致，末尾不带斜杠。放在 HTTPS 反向代理后面时必须设置 |
| `TRUSTED_PROXY_CIDRS` | 空 | 可信反向代理的 IP 或 CIDR，只有来自这些地址的 `X-Forwarded-For` 和 `X-Forwarded-Proto` 请求头才会被采信 |
| `PUBLIC_URL` | 空 | 代理客户端访问控制台的地址，例如 `https://pm.example.com`，用于生成订阅地址。留空时使用浏览器当前访问控制台的地址 |
| `SESSION_TTL` | `12h` | 登录会话有效期 |
| `WORKER_CONCURRENCY` | `4` | 同时执行的任务数，范围 1 到 64 |
| `WORKER_POLL_INTERVAL` | `300ms` | 任务队列轮询间隔 |
| `SSH_TIMEOUT` | `10s` | SSH 连接超时 |
| `COMMAND_TIMEOUT` | `15m` | 单个远程操作的超时时间；如果节点下载源码或编译很慢，可以适当调大 |
| `TRAFFIC_SYNC_INTERVAL` | `5m` | 流量采集间隔，最小 `30s` |
| `TRAFFIC_RECONCILE_INTERVAL` | `30s` | 检查到期、额度用尽和周期重置的间隔，最小 `5s` |
| `UPDATE_ENABLED` | 生产环境为 `true`，其他为 `false` | 是否允许在控制台在线升级 |
| `UPDATE_REPO` | `forever94yu/Proxy-Manager` | 检查新版本的 GitHub 仓库，格式为 `owner/name` |
| `UPDATE_DIR` | 数据库所在目录下的 `releases/`（Docker 中为 `/data/releases`） | 在线升级安装的新版本和升级前的数据库备份，服务用户必须可写 |

时间类配置使用 Go duration 格式，例如 `30s`、`5m`、`1h30m`。

## 升级与备份

**在线升级（推荐）**

进入控制台的 **系统更新** 页面，可以看到当前版本和 GitHub 上的最新版本。有新版本时，侧边栏会显示红点，点击 **升级到 vX.Y.Z** 并确认即可。控制台会依次：

1. 下载适用于当前平台的安装包，用发布页的 `SHA256SUMS.txt` 校验；
2. 检查新程序能在这台机器上运行，并备份数据库到 `releases/backups/`（保留最近 3 份）；
3. 等待正在执行的任务结束，然后重启到新版本，页面自动刷新。

重启期间控制台会中断几秒到几十秒，代理节点和代理连接不受影响。如果新版本没能启动，会自动回滚到原来的版本，并在 **系统更新** 页面提示。

- 控制面主机需要能访问 `api.github.com` 和 `github.com`。如果需要通过代理访问，可以在 `.env` 中设置 `HTTPS_PROXY`。
- 新版本保存在数据目录的 `releases/` 下（Docker 为数据卷中的 `/data/releases`），重启或重建容器后依然生效。之后如果用 `docker compose up -d --build` 升级到更新的镜像，镜像里的程序会自动接管。
- systemd 部署时，`releases/` 位于 `/var/lib/proxy-manager/`（由 `StateDirectory` 创建，服务用户可写），不需要修改 `/opt/proxy-manager` 的权限。
- 新版本启动成功后，数据库已经按新版本迁移。如需退回旧版本，请停止服务后用 `releases/backups/` 中的备份恢复数据库，再删除 `releases/state.json`。
- 不想在控制台升级时，设置 `UPDATE_ENABLED=false`。

**手动升级（Docker）**

```bash
cd Proxy-Manager
git pull
docker compose up -d --build
```

数据库结构变更会在程序启动时自动迁移。升级前建议先备份。如果你改过 `compose.yaml`（例如把端口绑定到 `127.0.0.1`），`git pull` 前先执行 `git stash`，拉取后再执行 `git stash pop`。

**手动升级（二进制与 systemd）**

从 [Releases](https://github.com/forever94yu/Proxy-Manager/releases) 下载对应平台的压缩包，停止服务后替换 `/opt/proxy-manager/` 下的程序、`web/` 目录和 `3proxy-install.sh`，再启动服务。

**备份**

需要备份的有两样：数据卷中的 SQLite 数据库，以及 `.env`（其中的 `MASTER_KEY` 用于解密数据库中的凭据）。**只有数据库没有 `MASTER_KEY`，SSH 凭据和代理密码就无法恢复。**

数据保存在 Docker 命名卷中，卷名一般是 `proxy-manager_proxy-manager-data`，可以用 `docker volume ls` 确认。在项目目录下执行：

```bash
docker compose stop
docker run --rm -v proxy-manager_proxy-manager-data:/data -v "$PWD":/backup alpine \
  tar czf /backup/pm-data-$(date +%F).tar.gz -C /data .
cp .env pm-env-$(date +%F).bak
docker compose start
```

数据库启用了 WAL 模式，所以要先停止服务再打包，以保证备份完整一致。systemd 部署时，停止服务后备份 `/var/lib/proxy-manager/` 目录和 `/etc/proxy-manager.env` 即可。

**恢复**

```bash
docker compose stop
docker run --rm -v proxy-manager_proxy-manager-data:/data -v "$PWD":/backup alpine \
  sh -c 'rm -rf /data/* && tar xzf /backup/pm-data-2026-01-01.tar.gz -C /data'
docker compose start
```

用 tar 打包和解包会保留文件属主，容器内的非 root 用户（uid 10001）才能继续写入数据库。恢复时请使用与备份时相同的 `MASTER_KEY`。

## 常见问题

<details>
<summary><b>容器启动后马上退出</b></summary>

执行 `docker compose logs` 查看原因，常见的有：

- `production requires explicit ...`：`.env` 缺少后面列出的配置项，例如 `ADMIN_PASSWORD`、`SESSION_SECRET`、`MASTER_KEY`。
- `SESSION_SECRET must contain at least 32 characters`：会话密钥太短，可以用 `openssl rand -hex 32` 重新生成。
- `ADMIN_PASSWORD must contain at least 8 characters`：管理员密码太短。

</details>

<details>
<summary><b>登录时提示“请求来源不被允许”</b></summary>

`CORS_ORIGINS` 没有设置，或者与浏览器地址栏不一致。它的值必须是完整来源，例如 `https://pm.example.com`，协议、域名、端口都要一致，末尾不能带 `/`。修改后执行 `docker compose up -d`。

</details>

<details>
<summary><b>登录成功后又被跳回登录页</b></summary>

你正在通过明文 HTTP 访问生产环境（例如 `http://服务器IP:8080`），浏览器丢弃了带 `Secure` 属性的会话 Cookie。请通过 HTTPS 域名访问，或者使用 [SSH 隧道](#没有域名时通过-ssh-隧道访问)。

</details>

<details>
<summary><b>忘记管理员密码</b></summary>

管理员账号只从环境变量读取，不保存在数据库中。修改 `.env` 里的 `ADMIN_PASSWORD` 后执行 `docker compose up -d` 即可生效，不影响已有数据。

</details>

<details>
<summary><b>测试连接失败</b></summary>

在任务详情中查看具体错误：

- `SSH connection failed ... i/o timeout`：控制面主机无法访问节点的 SSH 端口。请检查安全组、防火墙和 SSH 端口是否填写正确。
- `unable to authenticate`：用户名、密码或私钥错误，或者节点没有配置对应的公钥。
- `SSH private key cannot be parsed or requires an unsupported passphrase`：私钥设置了密码短语，或者粘贴时内容不完整。可以用 `ssh-keygen -p -f 私钥文件 -N ""` 去掉密码短语，或者按上文重新生成一对专用密钥。
- `sudo: a password is required`：SSH 用户不是 root，且没有配置免密 sudo，参考 [准备节点服务器](#准备节点服务器)。
- `SCP upload failed` 且提示 `scp: command not found`：节点缺少 `scp`，在 CentOS 或 Fedora 上执行 `dnf install -y openssh-clients`。

</details>

<details>
<summary><b>提示 SSH host key mismatch</b></summary>

节点的 SSH 主机密钥与首次连接时记录的不一致。常见原因是服务器重装过系统，也可能遭遇了中间人攻击。确认是重装导致的之后，可以在控制台删除这台服务器再重新添加（重新添加后需要重新给代理用户分配这台服务器）。修改服务器的主机地址或 SSH 端口也会清除已记录的指纹。

</details>

<details>
<summary><b>部署失败或超时</b></summary>

- 查看任务详情中的错误信息，确认节点是否能访问系统软件源和 GitHub（`curl -I https://github.com`）。
- 如果节点网络较慢，可以调大 `COMMAND_TIMEOUT`（例如 `30m`）后重新部署。
- 部署失败后可以在任务详情中直接点击 **重试失败目标**。

</details>

<details>
<summary><b>部署成功，但客户端连不上代理</b></summary>

- 检查云服务商安全组是否放行了 HTTP 和 SOCKS5 端口。
- 使用 firewalld 的系统需要手动放行端口，见 [准备节点服务器](#准备节点服务器)。
- 在控制台确认账号状态为“正常”、同步状态为“已同步”，并且这台服务器在账号的目标服务器列表中。
- 在节点上执行 `systemctl status 3proxy` 和 `ss -lntp | grep 3proxy`，确认服务正在监听。

</details>

<details>
<summary><b>扫码导入订阅后客户端更新失败</b></summary>

- 手机必须能访问订阅地址。用手机浏览器打开 **连接信息** 中的订阅地址，应该能下载到一段文本。
- 订阅地址是 `localhost`、`127.0.0.1` 或内网地址时，设置 `PUBLIC_URL` 为手机能访问的控制台地址后重新打开连接信息。
- v2rayNG 默认拒绝 HTTP 订阅地址（局域网 IP 除外），请通过 HTTPS 访问控制台，或在 v2rayNG 订阅设置中允许不安全的地址。
- 订阅地址被重置过，或者代理用户已被删除时，旧地址会返回 404，需要重新扫码。

</details>

<details>
<summary><b>系统更新页面提示检查更新失败或下载失败</b></summary>

- `无法连接 GitHub`：控制面主机访问不了 `api.github.com` 或 `github.com`。可以在容器或服务的环境变量中设置 `HTTPS_PROXY=http://代理地址:端口` 后重启。
- `GitHub API 请求次数已达上限`：GitHub 对未登录的 API 请求限制为每个 IP 每小时 60 次，稍后再试即可。控制台每小时最多自动检查一次。
- `升级目录不可写`：服务用户对 `UPDATE_DIR`（默认在数据库所在目录下）没有写权限。
- `该版本没有提供适用于当前平台的安装包`：发布页缺少当前平台的压缩包，请按上文手动升级。

升级失败不会影响正在运行的版本，处理后可以重试。

</details>

<details>
<summary><b>流量数据没有更新，或服务器显示“流量采集失败”</b></summary>

流量每 5 分钟采集一次，新账号需要等一个采集周期才会显示数据。只有已部署、并且已经成功连接过（记录了主机指纹）的服务器才会被采集。如果显示“流量采集失败”，请先对这台服务器执行一次测试连接，排查 SSH 连接问题。

</details>

## 本地开发

```bash
npm run setup   # 安装依赖
npm run dev     # 启动 API（8080）和前端（5173），前端会把 /api 请求代理到 8080
npm run build   # 构建前端到 web/dist，构建后端到 dist/
npm test        # 前端类型检查与构建，以及 Go 单元测试
npm run release # 构建前端，并把 5 个平台的发布压缩包和 SHA256SUMS.txt 输出到 dist/release/
```

- **连接真实服务器调试**：复制 `.env.example` 为 `.env`，把 `EXECUTOR_MODE` 改为 `ssh`。
- **纯前端演示模式**：新建 `web/.env.local`，写入 `VITE_DEMO_MODE=true`，然后执行 `npm run dev:web`。前端会使用内置的演示数据，不需要启动后端，README 中的截图就是在这个模式下截取的。

**项目结构**

```text
web/                 Vue 3 + TypeScript + Vite 控制台
server/              Go API、SQLite 存储、任务队列、SSH/SCP 执行器
  migrations/        数据库迁移脚本（编译进二进制，启动时自动执行）
3proxy-install.sh    3proxy 安装脚本：交互菜单与 --api 非交互接口
tests/               安装脚本的实机测试和多发行版 Docker 测试
scripts/             本地开发、构建与发布打包脚本
docs/                架构文档与截图
Dockerfile           多阶段构建的生产镜像
compose.yaml         单实例部署编排
```

**Shell 脚本检查与测试**

```bash
shellcheck 3proxy-install.sh tests/**/*.sh
shfmt -d 3proxy-install.sh tests/docker
./tests/docker/run.sh --dist ubuntu-24.04   # 在 Docker 中测试单个发行版的安装流程
```

Docker 测试覆盖安装、交互增删用户、非交互 API、流量策略下发、服务启停和卸载。实机测试 `tests/3proxy-install-test.sh` 会真实修改本机的 `/etc/3proxy`，只能在专用测试机上运行。

## 节点脚本

`3proxy-install.sh` 也可以脱离控制台，单独在一台服务器上使用：

```bash
curl -O https://raw.githubusercontent.com/forever94yu/Proxy-Manager/main/3proxy-install.sh
chmod +x 3proxy-install.sh
sudo ./3proxy-install.sh   # 交互式安装；已安装时进入管理菜单（添加/删除用户、卸载等）
```

控制台调用的是它的非交互接口，也可以手动调用来排查问题：

```bash
sudo ./3proxy-install.sh --api inspect
sudo ./3proxy-install.sh --api deploy 203.0.113.10 3128 1080 1.1.1.1 1.0.0.1
printf '%s\n' 'SafePass_123!' | sudo ./3proxy-install.sh --api user-add alice
printf '%s\n' 'NewPass_456!' | sudo ./3proxy-install.sh --api user-update alice alice_new
sudo ./3proxy-install.sh --api user-delete alice_new
sudo ./3proxy-install.sh --api service restart
sudo ./3proxy-install.sh --api traffic
# 流量策略：user-add 的 stdin 第二行可选，格式为 "STATE CAP_MB PERIOD"
printf '%s\n%s\n' 'SafePass_123!' 'enabled 10240 0' | sudo ./3proxy-install.sh --api user-add bob
printf '%s\n' 'bob disabled 10240 0' | sudo ./3proxy-install.sh --api policy-apply
```

账号变更使用文件锁和原子写入，服务启动失败时会自动回滚。安装脚本固定使用 3proxy 上游 commit `7c1bc48c853f99f2574deb61fb9347a5d3056ad0`。接口细节见 [server/README.md](server/README.md)，数据模型、安全边界与后续规划见 [架构文档](docs/ARCHITECTURE.md)。

## 许可证与致谢

本项目使用 [MIT License](LICENSE)。安装脚本基于 [a0s/3proxy-install](https://github.com/a0s/3proxy-install)，后者受 [angristan/openvpn-install](https://github.com/angristan/openvpn-install) 与 [angristan/wireguard-install](https://github.com/angristan/wireguard-install) 启发。代理服务由 [3proxy](https://github.com/3proxy/3proxy) 提供。
