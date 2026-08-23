# Proxy Manager server

The server is a self-contained Go control plane for the Vue application. It uses
SQLite for inventory and durable jobs. `EXECUTOR_MODE=mock` is the safe default,
so the entire UI can be demonstrated without SSH targets.

## Run locally

From the repository root:

```powershell
go -C server run .
```

The API listens on `http://localhost:8080`. Development credentials are:

```text
username: admin
password: ProxyManager!2026
```

The default database is `server/data/proxy-manager.db` when the command above is
used. Build the Vue application into `web/dist` to let the same process serve it.

## Production configuration

Production refuses to start unless all three secrets are explicitly configured:

```powershell
$env:APP_ENV = "production"
$env:ADMIN_PASSWORD = "a-long-unique-administrator-password"
$env:SESSION_SECRET = "at-least-32-random-characters"
$env:MASTER_KEY = "a-base64-encoded-32-byte-key-or-32-random-characters"
$env:EXECUTOR_MODE = "ssh"
go -C server run .
```

Other useful settings are `HTTP_ADDR`, `DB_PATH`, `ADMIN_USERNAME`,
`SESSION_TTL`, `COOKIE_SECURE`, `WORKER_CONCURRENCY`, `WORKER_POLL_INTERVAL`,
`SSH_TIMEOUT`, `COMMAND_TIMEOUT`, `INSTALL_SCRIPT_PATH`, `STATIC_DIR`, and
`CORS_ORIGINS` (comma-separated exact origins). `TRUSTED_PROXY_CIDRS` accepts a
comma-separated set of reverse-proxy addresses or CIDRs; only those peers may
supply `X-Forwarded-For` for login rate limiting or `X-Forwarded-Proto` for
same-origin checks.

Development permits `http://localhost:5173` and `http://127.0.0.1:5173` by
default for the Vite proxy. Production accepts direct same-origin requests and
origins listed explicitly in `CORS_ORIGINS`. When TLS terminates at a reverse
proxy, either list the public HTTPS origin explicitly or configure the proxy's
address in `TRUSTED_PROXY_CIDRS` and have it overwrite `X-Forwarded-Proto`.

SSH uses strict SHA256 host fingerprints. The first successful connection records
the observed fingerprint; every later connection must match it. A non-root SSH
account must be allowed to run the uploaded script through `sudo -n`. A root SSH
account executes it directly. The remote host must provide `scp` for temporary
script upload.

The repository installer must expose the machine interface used by the worker:

```text
--api inspect
--api deploy IP HTTP_PORT SOCKS_PORT DNS1 DNS2
--api user-add NAME                 # password on stdin
--api user-update OLD_NAME NEW_NAME # password on stdin
--api user-delete NAME
--api service status|start|stop|restart
```

When the script does not expose that contract, SSH jobs fail explicitly after
upload instead of trying to drive its interactive menu.
