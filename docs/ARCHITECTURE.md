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
|-- .env.example               # 本地开发配置模板
|-- .env.production.example    # 生产部署配置模板
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
        |-- policy-apply / traffic
        `-- service status / start / stop / restart
```

## 3. 领域模型

### Server

服务器记录保存 SSH 连接信息、3proxy 端口/DNS 期望配置、安装状态、服务状态和最后检查时间。SSH 密码或私钥只以 AES-GCM 密文保存，普通查询接口永不返回凭据。

### ProxyUser

代理账号是控制面的全局对象，通过多对多绑定分配到一台或多台服务器。密码同样加密保存，便于后续扩容或失败重试，但列表接口不会返回密码。随机密码只在创建响应中展示一次。

每个账号还有使用限制：启用开关、流量额度（`0` 为不限）、到期时间和周期重置规则（不重置/每天/每周/每月 + 周期起点）。账号状态不单独存储，由控制面按 `已停用 > 已到期 > 流量用尽 > 正常` 的优先级实时推导。详见第 9 节。

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
printf '%s\n%s\n' "$PASSWORD" "STATE CAP_MB PERIOD" | sudo bash 3proxy-install.sh --api user-add USERNAME
printf '%s\n%s\n' "$PASSWORD" "STATE CAP_MB PERIOD" | sudo bash 3proxy-install.sh --api user-update OLD_NAME NEW_NAME
sudo bash 3proxy-install.sh --api user-delete USERNAME
printf '%s\n' "NAME STATE CAP_MB PERIOD" | sudo bash 3proxy-install.sh --api policy-apply
sudo bash 3proxy-install.sh --api traffic
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

账号密码只从 stdin 读取，不出现在远程命令参数中。`user-add`/`user-update` 可在 stdin 第二行附带该账号的流量策略（可选）。账号变更、`policy-apply` 和 `traffic` 都会输出流量报告：

```text
PM  traffic_report  v1
PM  node_user       alice
PM  traffic         alice  enabled  10240  3  1  52428800
PM  traffic         bob    removed  512    0  2  1048576
```

`traffic` 行的字段依次为：用户名、状态（enabled/disabled/removed）、节点上限（MiB）、周期令牌、计数器序号、计数器已用字节。`removed` 行是本次删除的账号计数器的最终值。

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

GET    /users?search=&syncStatus=&status=
POST   /users
PUT    /users/{id}
DELETE /users/{id}
POST   /users/{id}/traffic/reset
POST   /users/{id}/state            {"enabled": true|false}

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

## 9. 流量额度、使用期限与流量重置

### 模型

- `proxy_users` 保存启用开关、`traffic_limit_bytes`、`expires_at`、`reset_period`、`reset_anchor`（带 UTC 偏移的 RFC 3339）和当前核算周期令牌 `period_token`。
- `proxy_user_traffic` 按（用户，服务器）保存节点最近一次报告的策略状态和计数：计数器序号、计数器字节数，以及本周期内已结束计数器的累计字节数（`retained_bytes`）。
  - 这些行不随绑定删除：解绑或删除服务器后，本周期已用流量仍计入额度，下个周期再清理。这样无法通过“解绑再绑定”绕过额度。
- 本周期已用流量等于该用户所有 `period_token` 与当前令牌相同的行之和。
  - 修改额度、期限或重置规则都不会清零已用流量。
  - 只有手动重置或周期到点才会让令牌加一。

### 节点执行

- 每个账号在 `/etc/3proxy/3proxy.cfg.policy` 中有一行 `NAME INDEX STATE CAP_MB PERIOD`。
- 生成配置时写入 `counter /etc/3proxy/3proxy.counters`，并为每个账号写两条规则：
  - `countout`：让上行流量也被计入；
  - `countall INDEX/NAME N CAP_MB NAME`：统计总流量，达到上限后 3proxy 拒绝该账号认证。
- `disabled` 账号不写入生效的 `users` 行，reload 后它已建立的会话也会断开。账号本身仍保留在 `3proxy.cfg.users` 中。
- `PERIOD` 变化时分配一对新的、已清零的计数器，这就是节点侧的“重置”。
- 配置变更通过 `SIGUSR1` 原地 reload：未受影响账号的连接不中断，3proxy 也会在 reload 时把计数写回磁盘。

### 额度分配

额度是全部绑定服务器用量的合计。控制面为每台节点计算本地上限：

```text
CAP_MB = ceil((额度 - 其他服务器上本周期已用) / 1 MiB)，至少为 1
```

- 不限额账号的上限为 1 PiB。
- 只绑定一台服务器的账号，其上限由节点直接执行。
- 多节点账号可能少量超用，上限约为一个采集周期内其他节点产生的流量。
- 3proxy 在每个连接认证时按“上限 - 已用”计算该连接的剩余配额。因此：
  - 并发或紧接着发起的多个连接可能各自拿到同一份剩余配额，导致少量超用；
  - 计数达到上限后，新连接一律被拒绝，控制面也会在下一次采集后把账号判定为“流量用尽”并停用。
- 已建立的连接在配置 reload 后会在下一次收发数据时重新认证，停用或到期的账号因此会被断开。
- reload 期间 3proxy 会先建新监听、再关旧监听，被旧监听接受的连接会被重置。安装器会等到旧监听消失后才返回，而且只在配置确实变化时才 reload。

### 后台循环

- **采集**（`TRAFFIC_SYNC_INTERVAL`，默认 5 分钟）：
  - 对每台已部署且已登记主机指纹的服务器，直接通过执行器运行 `--api traffic`，不进入任务列表。
  - 失败只记录在 `servers.traffic_error`，控制台服务器列表会显示“流量采集失败”。
- **对账**（`TRAFFIC_RECONCILE_INTERVAL`，默认 30 秒；每次采集结束后也会立即执行）：
  1. 处理到期的周期重置。错过多个周期时只重置一次。
  2. 清理不再计入额度的流量行。
  3. 比较每个绑定的期望策略与节点最近一次报告。出现以下任一情况时，为该服务器排队一个 `user_policy` 任务（操作者为 `system`）：
     - 状态或周期令牌不同；
     - 上限偏差超过 `max(64 MiB, 额度的 5%)`；
     - 节点本地上限即将挡住仍有剩余额度的用户。
- **防止任务堆积**：以下情况不会排队新的策略任务：
  - 服务器已有排队或执行中的目标；
  - 上一个账号类任务的效果尚未被新的流量报告确认；
  - 15 分钟内有失败的 `user_policy` 任务（退避）。

### 任务与重试

- 创建或编辑账号时，`user-add`/`user-update` 会附带策略行。
- 手动重置（`user_traffic_reset`）和启用/停用（`user_enable`/`user_disable`）会在每台绑定服务器上执行 `policy-apply`。
- 策略不写入任务载荷，而是由 worker 在执行前按数据库最新状态计算，因此排队或重试的任务不会下发过期的额度或状态。
- 重试规则：
  - 系统 `user_policy` 任务不会让更早失败的账号任务变得不可重试；
  - 策略类任务本身重试时，不受“存在更新任务”的限制。

### 时间

- 周期按设置时浏览器所在的固定 UTC 偏移计算，不跟随夏令时变化。
- 月重置从起点逐月推算：起点为 29 至 31 日时，小月落在月末，不会累积漂移。
- 到期由对账循环检测，最迟约 `TRAFFIC_RECONCILE_INTERVAL` 后生效。
- 流量用尽在每次采集后检测。
