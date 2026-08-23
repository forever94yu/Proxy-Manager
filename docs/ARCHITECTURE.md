# Proxy Manager 架构与代码分析

## 1. 原项目结构

原项目是一个面向单台 Linux 主机的 3proxy 交互式安装器，并不是 Web 应用。

```text
Proxy-Manager/
|-- 3proxy-install.sh          # 安装、配置、用户增删、systemd 管理
|-- tests/
|   |-- 3proxy-install-test.sh # Ubuntu 实机测试
|   `-- docker/                # 多发行版 Docker 场景测试
|-- .github/workflows/lint.yml # ShellCheck 与 shfmt
`-- README.md
```

`3proxy-install.sh` 的原始职责集中在一个文件中：

1. 检查 root、Linux 发行版和虚拟化环境。
2. 收集公网地址、HTTP/SOCKS 端口和 DNS。
3. 下载并编译固定 commit 的 3proxy。
4. 写入 `/etc/3proxy/3proxy.cfg`、`/etc/3proxy/params` 和 systemd unit。
5. 将代理账号保存为 `/etc/3proxy/3proxy.cfg.users` 中的 `users NAME:CL:PASSWORD`。
6. 通过交互菜单增加或删除账号、卸载服务。

原实现不具备 HTTP API、控制台认证、数据库、服务器资产、多机任务、账号修改或稳定的机器输出。Docker 测试也明确采用输入文件模拟交互，这种方式不适合作为远程控制协议。

## 2. 本次新增结构

```text
Proxy-Manager/
|-- web/                       # Vue 3 + TypeScript + Vite 控制台
|-- server/                    # Go 控制面、SQLite、任务 worker、SSH 执行器
|-- scripts/dev.mjs            # 同时启动 API 与 Vite
|-- docs/ARCHITECTURE.md       # 本文档
|-- Dockerfile
|-- compose.yaml
|-- .env.example
|-- package.json
|-- 3proxy-install.sh          # 保留交互模式，并新增受控的 --api 模式
`-- tests/
```

运行时调用关系：

```text
Vue 3 browser console
        |
        | HTTPS / JSON / HttpOnly session cookie
        v
Go control plane
  |-- SQLite: servers, proxy users, bindings, jobs, job targets
  |-- AES-GCM: SSH credentials and proxy passwords at rest
  `-- persistent async workers
              |
              | SSH with host-key pinning + SCP
              v
      temporary 3proxy-install.sh
              |
              | sudo -n (or direct execution for root)
              v
      3proxy --api command
        |-- inspect / deploy
        |-- user-add / user-update / user-delete
        `-- service status / start / stop / restart
```

## 3. 领域模型

### Server

服务器记录保存 SSH 连接信息、3proxy 端口/DNS 期望配置、安装状态、服务状态和最后检查时间。SSH 密码或私钥只以 AES-GCM 密文保存，普通查询接口永不返回凭据。

### ProxyUser

代理账号是控制面的全局对象，通过多对多绑定分配到一台或多台服务器。密码同样加密保存，便于后续扩容或失败重试，但列表接口不会返回密码。随机密码只在创建响应中展示一次。

### Job 与 JobTarget

部署、连接测试、服务控制和账号变更不会在 HTTP 请求中长时间等待，而是创建持久化任务。每台目标服务器都有独立 target 状态，因此批量操作可以准确表达：

- `queued`
- `running`
- `succeeded`
- `failed`
- 汇总后的 `partially_failed`

多服务器不存在真正的分布式事务。系统保留成功节点结果，并允许只重试失败节点，不会把部分成功错误地显示成整体成功。

## 4. 节点端机器接口

原交互入口保持兼容；Web 控制面只调用以下固定命令，不接受任意 Shell 命令：

```bash
sudo bash 3proxy-install.sh --api inspect
sudo bash 3proxy-install.sh --api deploy SERVER_IP HTTP_PORT SOCKS_PORT DNS1 DNS2
printf '%s\n' "$PASSWORD" | sudo bash 3proxy-install.sh --api user-add USERNAME
printf '%s\n' "$PASSWORD" | sudo bash 3proxy-install.sh --api user-update OLD_NAME NEW_NAME
sudo bash 3proxy-install.sh --api user-delete USERNAME
sudo bash 3proxy-install.sh --api service status
sudo bash 3proxy-install.sh --api service start
sudo bash 3proxy-install.sh --api service stop
sudo bash 3proxy-install.sh --api service restart
```

机器输出使用只包含受校验值的制表符协议：

```text
PM  installed  true
PM  service    active
PM  http_port  3128
PM  user       example_user
```

账号密码只从 stdin 读取，不出现在远程命令参数中。

## 5. 节点安全改造

本次同时修复了原脚本中会被 Web 放大的风险：

1. `/etc/3proxy/params` 不再通过 root `source` 执行，只读取固定键并验证类型。
2. 公网地址、端口、DNS、用户名和密码均使用白名单校验。
3. 默认 `umask 077`；参数、用户和主配置文件权限设为 `0600`。
4. 账号修改使用 `/run/lock/3proxy-manager.lock` 排他锁。
5. 用户文件和主配置先写同目录临时文件，再使用原子 `mv` 替换。
6. 配置应用后检查服务；失败时恢复原用户文件并重新生成配置。
7. 新增按用户名修改账号和轮换密码，不再依赖易漂移的菜单序号。

3proxy 的 `CL` 认证格式仍要求节点保存明文密码，这是上游格式约束。节点文件已限制为 root 可读；控制面日志会脱敏，浏览器列表也不会获得密码。

## 6. 控制面安全边界

- 生产环境必须显式设置 `ADMIN_PASSWORD`、`SESSION_SECRET` 和 `MASTER_KEY`，否则拒绝启动。
- 登录会话使用签名、限时的 HttpOnly Cookie；生产环境启用 Secure 属性。
- SSH 首次连接采用 TOFU 记录主机指纹，后续连接必须匹配，防止静默接受主机替换。
- SSH 凭据和代理密码使用不同关联数据的 AES-256-GCM 密文保存。
- 远程脚本使用随机临时文件名，执行完成后清理。
- 远程参数来自严格枚举或校验后的字段，Shell 参数逐个引用。
- 任务输出限制大小、过滤控制字符，并对密码、私钥和 3proxy 凭据行脱敏。
- 登录按可信客户端地址限流；只有 `TRUSTED_PROXY_CIDRS` 中的反向代理才能提供转发地址和协议头。
- 服务器存在排队或执行中的目标任务时，连接与部署配置不可修改或删除，避免旧任务在新主机上执行。
- 任务重试会验证目标期望状态；已被后续任务取代的失败操作不能再次排队。

生产服务器建议创建专用 SSH 用户，并仅授予执行受控脚本所需的免密 sudo 权限。当前 MVP 仍上传并以 root 权限运行完整安装脚本；长期版本应部署 root 所有、普通用户不可写的独立 `3proxyctl`，并在 sudoers 中只授权该二进制。

## 7. API 边界

所有业务接口位于 `/api/v1`，统一返回 `{ "data": ... }`：

```text
POST   /auth/login
POST   /auth/logout
GET    /auth/me
GET    /dashboard

GET    /servers
POST   /servers
PUT    /servers/{id}
DELETE /servers/{id}
POST   /servers/{id}/test
POST   /servers/{id}/deploy
POST   /servers/{id}/service
POST   /servers/actions/deploy
POST   /servers/actions/service

GET    /users
POST   /users
PUT    /users/{id}
DELETE /users/{id}

GET    /jobs
GET    /jobs/{id}
POST   /jobs/{id}/retry
```

两个 `servers/actions/*` 接口接收服务器 ID 列表并创建真正的批量任务；`GET /jobs/{id}` 返回任务及其逐服务器 target，包含状态、尝试次数、时间与脱敏后的错误。失败重试只重新排队仍然有效的失败 target。

服务器删除默认只移除控制面记录，不会远程卸载 3proxy。远程卸载属于独立高风险能力，没有混入普通删除接口。

## 8. 已知边界与后续演进

当前版本是可运行的单实例控制面，适合小到中等规模服务器池。以下能力适合作为下一阶段：

- 独立 worker 和 PostgreSQL 队列，以支持控制面水平扩展。
- SSE/WebSocket 任务推送；当前页面采用短周期刷新。
- OIDC/MFA、多操作者 RBAC 和不可变审计事件。
- 服务器分组、配置模板、灰度部署和失败阈值。
- 节点配置哈希与漂移检测。
- 预构建、签名并校验的 3proxy 软件包，替代每台机器 root 在线编译。
- 专用低权限 3proxy 系统用户，以及 nftables/firewalld/云安全组策略。
- 使用真实 HTTP/SOCKS 认证请求进行端到端健康检查。

这些边界不会影响当前的核心流程：在线纳管服务器、部署 3proxy、启停/重启服务，以及在多台服务器上创建、修改和删除代理账号。
