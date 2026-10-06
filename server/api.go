package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"
)

type API struct {
	cfg           Config
	store         *Store
	box           *SecretBox
	sessions      *SessionManager
	worker        *Worker
	logger        *slog.Logger
	logins        *loginLimiter
	subscriptions *SubscriptionSigner
	updater       *Updater
}

type apiErrorEnvelope struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code    string      `json:"code"`
	Message string      `json:"message"`
	Fields  FieldErrors `json:"fields,omitempty"`
}

func NewAPI(cfg Config, store *Store, box *SecretBox, sessions *SessionManager, worker *Worker, updater *Updater, logger *slog.Logger) *API {
	return &API{
		cfg: cfg, store: store, box: box, sessions: sessions, worker: worker, updater: updater, logger: logger,
		logins:        newLoginLimiter(10, 15*time.Minute),
		subscriptions: NewSubscriptionSigner(cfg.MasterKey),
	}
}

func (a *API) Handler() http.Handler {
	root := http.NewServeMux()
	protected := http.NewServeMux()

	root.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeData(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	root.HandleFunc("GET /readyz", a.ready)
	root.HandleFunc("POST /api/v1/auth/login", a.login)
	// Proxy clients fetch subscriptions without a console session; the URL
	// token is the credential.
	root.HandleFunc("GET "+subscriptionPathPrefix+"{token}", a.serveSubscription)
	root.Handle("/api/v1/", a.sessions.Middleware(protected))

	protected.HandleFunc("POST /api/v1/auth/logout", a.logout)
	protected.HandleFunc("GET /api/v1/auth/me", a.me)
	protected.HandleFunc("GET /api/v1/dashboard", a.dashboard)

	protected.HandleFunc("GET /api/v1/servers", a.listServers)
	protected.HandleFunc("POST /api/v1/servers", a.createServer)
	protected.HandleFunc("POST /api/v1/servers/actions/deploy", a.deployServers)
	protected.HandleFunc("POST /api/v1/servers/actions/service", a.serviceServers)
	protected.HandleFunc("GET /api/v1/servers/{id}", a.getServer)
	protected.HandleFunc("PUT /api/v1/servers/{id}", a.updateServer)
	protected.HandleFunc("DELETE /api/v1/servers/{id}", a.deleteServer)
	protected.HandleFunc("POST /api/v1/servers/{id}/test", a.testServer)
	protected.HandleFunc("POST /api/v1/servers/{id}/deploy", a.deployServer)
	protected.HandleFunc("GET /api/v1/servers/{id}/service", a.getService)
	protected.HandleFunc("POST /api/v1/servers/{id}/service", a.serviceAction)

	protected.HandleFunc("GET /api/v1/users", a.listUsers)
	protected.HandleFunc("POST /api/v1/users", a.createUser)
	protected.HandleFunc("GET /api/v1/users/{id}", a.getUser)
	protected.HandleFunc("GET /api/v1/users/{id}/credentials", a.getUserCredentials)
	protected.HandleFunc("GET /api/v1/users/{id}/subscription", a.getUserSubscription)
	protected.HandleFunc("POST /api/v1/users/{id}/subscription/reset", a.resetUserSubscription)
	protected.HandleFunc("PUT /api/v1/users/{id}", a.updateUser)
	protected.HandleFunc("DELETE /api/v1/users/{id}", a.deleteUser)
	protected.HandleFunc("POST /api/v1/users/{id}/traffic/reset", a.resetUserTraffic)
	protected.HandleFunc("POST /api/v1/users/{id}/state", a.setUserState)

	protected.HandleFunc("GET /api/v1/jobs", a.listJobs)
	protected.HandleFunc("GET /api/v1/jobs/{id}", a.getJob)
	protected.HandleFunc("POST /api/v1/jobs/{id}/retry", a.retryJob)

	protected.HandleFunc("GET /api/v1/system/update", a.getUpdate)
	protected.HandleFunc("POST /api/v1/system/update", a.startUpdate)

	if handler := staticHandler(a.cfg.StaticDir); handler != nil {
		root.Handle("/", handler)
	} else {
		root.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusNotFound, "not_found", "Resource not found", nil)
		})
	}

	return a.recoverMiddleware(a.securityHeaders(a.corsMiddleware(a.accessLog(root))))
}

func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "Database is unavailable", nil)
		return
	}
	writeData(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	clientIP := loginClientIP(r, a.cfg.TrustedProxyCIDRs)
	if allowed, retryAfter := a.logins.allow(clientIP, now); !allowed {
		seconds := int(retryAfter.Round(time.Second).Seconds())
		if seconds < 1 {
			seconds = 1
		}
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
		writeError(w, http.StatusTooManyRequests, "login_rate_limited", "Too many failed login attempts; try again later", nil)
		return
	}
	var request struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	if len(request.Username) > 128 || len(request.Password) > 1024 || !a.sessions.Authenticate(request.Username, request.Password) {
		a.logins.failure(clientIP, now)
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "Username or password is incorrect", nil)
		return
	}
	a.logins.success(clientIP)
	if err := a.sessions.SetCookie(w, now); err != nil {
		a.internalError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"user": a.sessions.Operator()})
}

func (a *API) logout(w http.ResponseWriter, _ *http.Request) {
	a.sessions.ClearCookie(w)
	writeData(w, http.StatusOK, map[string]any{})
}

func (a *API) me(w http.ResponseWriter, _ *http.Request) {
	writeData(w, http.StatusOK, map[string]any{"user": a.sessions.Operator()})
}

func (a *API) dashboard(w http.ResponseWriter, r *http.Request) {
	dashboard, err := a.store.Dashboard(r.Context())
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, dashboard)
}

func (a *API) listServers(w http.ResponseWriter, r *http.Request) {
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if err := validateSearch(search); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", err.Error(), nil)
		return
	}
	if status != "" && !slices.Contains([]string{
		"online", "offline", "unknown", "running", "stopped",
		"installed", "deploying", "not_installed", "failed",
	}, status) {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "Server status filter is invalid", FieldErrors{"status": "Invalid status"})
		return
	}
	servers, err := a.store.ListServers(r.Context(), search, status)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, servers)
}

func (a *API) createServer(w http.ResponseWriter, r *http.Request) {
	var input ServerInput
	if !decodeJSON(w, r, &input) {
		return
	}
	if fields := validateServerInput(&input, true); len(fields) > 0 {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "Server details are invalid", fields)
		return
	}
	id := newID()
	ciphertext, err := a.box.Encrypt([]byte(input.Credential), "server:"+id+":credential")
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	now := time.Now().UTC()
	server := Server{
		ID: id, Name: input.Name, Host: input.Host, SSHPort: input.SSHPort,
		SSHUser: input.SSHUser, AuthMethod: input.AuthMethod, CredentialCipher: ciphertext,
		Status: "unknown", ServiceStatus: "unknown", InstallStatus: "not_installed",
		PublicIP: strings.Trim(input.Host, "[]"), HTTPPort: input.HTTPPort, SocksPort: input.SocksPort,
		DNS: append([]string(nil), input.DNS...), Tags: append([]string(nil), input.Tags...),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := a.store.CreateServer(r.Context(), server); err != nil {
		a.storeError(w, r, err, "A server with this name already exists")
		return
	}
	created, err := a.store.GetServer(r.Context(), id)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeData(w, http.StatusCreated, created)
}

func (a *API) getServer(w http.ResponseWriter, r *http.Request) {
	server, err := a.serverFromPath(r)
	if err != nil {
		a.storeError(w, r, err, "Server not found")
		return
	}
	writeData(w, http.StatusOK, server)
}

func (a *API) updateServer(w http.ResponseWriter, r *http.Request) {
	existing, err := a.serverFromPath(r)
	if err != nil {
		a.storeError(w, r, err, "Server not found")
		return
	}
	var input ServerInput
	if !decodeJSON(w, r, &input) {
		return
	}
	fields := validateServerInput(&input, false)
	if input.Credential == "" && input.AuthMethod != existing.AuthMethod {
		fields["credential"] = "SSH credential is required when changing authentication method"
	}
	if len(fields) > 0 {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "Server details are invalid", fields)
		return
	}
	existing.Name = input.Name
	existing.Host = input.Host
	existing.SSHPort = input.SSHPort
	existing.SSHUser = input.SSHUser
	existing.AuthMethod = input.AuthMethod
	existing.PublicIP = strings.Trim(input.Host, "[]")
	existing.HTTPPort = input.HTTPPort
	existing.SocksPort = input.SocksPort
	existing.DNS = append([]string(nil), input.DNS...)
	existing.Tags = append([]string(nil), input.Tags...)
	existing.UpdatedAt = time.Now().UTC()
	replaceCredential := input.Credential != ""
	if replaceCredential {
		existing.CredentialCipher, err = a.box.Encrypt([]byte(input.Credential), "server:"+existing.ID+":credential")
		if err != nil {
			a.internalError(w, r, err)
			return
		}
	}
	if err := a.store.UpdateServer(r.Context(), existing, replaceCredential); err != nil {
		message := "A server with this name already exists"
		if errors.Is(err, ErrServerBusy) {
			message = "Server settings cannot be changed while it has queued or running jobs"
		}
		a.storeError(w, r, err, message)
		return
	}
	updated, err := a.store.GetServer(r.Context(), existing.ID)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, updated)
}

func (a *API) deleteServer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validID(id) {
		writeError(w, http.StatusNotFound, "not_found", "Server not found", nil)
		return
	}
	if err := a.store.DeleteServer(r.Context(), id); err != nil {
		message := "Server not found"
		if errors.Is(err, ErrConflict) {
			message = "Server has queued or running jobs"
		}
		a.storeError(w, r, err, message)
		return
	}
	writeData(w, http.StatusOK, map[string]any{})
}

func (a *API) testServer(w http.ResponseWriter, r *http.Request) {
	if !decodeEmptyObject(w, r) {
		return
	}
	server, err := a.serverFromPath(r)
	if err != nil {
		a.storeError(w, r, err, "Server not found")
		return
	}
	job, err := a.enqueueSingleServerJob(r.Context(), server, "connection_test", TargetTask{Action: "inspect"})
	if err != nil {
		a.enqueueError(w, r, err, "Server not found")
		return
	}
	writeData(w, http.StatusAccepted, map[string]any{"job": job, "message": "Connection test queued"})
}

func (a *API) deployServer(w http.ResponseWriter, r *http.Request) {
	if !decodeEmptyObject(w, r) {
		return
	}
	server, err := a.serverFromPath(r)
	if err != nil {
		a.storeError(w, r, err, "Server not found")
		return
	}
	job, err := a.enqueueDeploymentJob(r.Context(), []Server{server})
	if err != nil {
		a.enqueueError(w, r, err, "Server not found")
		return
	}
	writeData(w, http.StatusAccepted, map[string]any{"job": job})
}

func (a *API) deployServers(w http.ResponseWriter, r *http.Request) {
	serverIDs, ok := decodeServerIDs(w, r)
	if !ok {
		return
	}
	servers, err := a.serversByID(r.Context(), serverIDs)
	if err != nil {
		a.storeError(w, r, err, "One or more selected servers do not exist")
		return
	}
	job, err := a.enqueueDeploymentJob(r.Context(), servers)
	if err != nil {
		a.enqueueError(w, r, err, "One or more selected servers do not exist")
		return
	}
	writeData(w, http.StatusAccepted, map[string]any{"job": job})
}

func (a *API) getService(w http.ResponseWriter, r *http.Request) {
	server, err := a.serverFromPath(r)
	if err != nil {
		a.storeError(w, r, err, "Server not found")
		return
	}
	writeData(w, http.StatusOK, map[string]any{"status": server.ServiceStatus})
}

func (a *API) serviceAction(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Action string `json:"action"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if !slices.Contains([]string{"start", "stop", "restart"}, input.Action) {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "Service action is invalid", FieldErrors{"action": "Use start, stop, or restart"})
		return
	}
	server, err := a.serverFromPath(r)
	if err != nil {
		a.storeError(w, r, err, "Server not found")
		return
	}
	job, err := a.enqueueSingleServerJob(r.Context(), server, "service_"+input.Action,
		TargetTask{Action: "service", ServiceAction: input.Action})
	if err != nil {
		a.enqueueError(w, r, err, "Server not found")
		return
	}
	writeData(w, http.StatusAccepted, map[string]any{"job": job})
}

func (a *API) serviceServers(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ServerIDs []string `json:"serverIds"`
		Action    string   `json:"action"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	input.ServerIDs = sortedUnique(input.ServerIDs)
	fields := validateServerIDs(input.ServerIDs)
	if !slices.Contains([]string{"start", "stop", "restart"}, input.Action) {
		fields["action"] = "Use start, stop, or restart"
	}
	if len(fields) > 0 {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "Service action is invalid", fields)
		return
	}
	servers, err := a.serversByID(r.Context(), input.ServerIDs)
	if err != nil {
		a.storeError(w, r, err, "One or more selected servers do not exist")
		return
	}
	jobID := newID()
	targets := make([]NewJobTarget, 0, len(servers))
	for _, server := range servers {
		payload, err := json.Marshal(TargetTask{Action: "service", ServiceAction: input.Action})
		if err != nil {
			a.internalError(w, r, err)
			return
		}
		targets = append(targets, NewJobTarget{
			ID: newID(), ServerID: server.ID, ServerConfigRevision: server.ConfigRevision, Payload: string(payload),
		})
	}
	spec := NewJob{ID: jobID, Type: "service_" + input.Action, Actor: actorFromContext(r.Context()), EntityType: "fleet", Targets: targets}
	if err := a.store.CreateJob(r.Context(), spec); err != nil {
		a.enqueueError(w, r, err, "One or more selected servers do not exist")
		return
	}
	a.worker.Notify()
	job, err := a.store.GetJob(r.Context(), jobID)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeData(w, http.StatusAccepted, map[string]any{"job": job})
}

func (a *API) listUsers(w http.ResponseWriter, r *http.Request) {
	search := strings.TrimSpace(r.URL.Query().Get("search"))
	syncStatus := strings.TrimSpace(r.URL.Query().Get("syncStatus"))
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if err := validateSearch(search); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", err.Error(), nil)
		return
	}
	if syncStatus != "" && !slices.Contains([]string{"synced", "pending", "partial", "failed"}, syncStatus) {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "Sync status filter is invalid", FieldErrors{"syncStatus": "Invalid sync status"})
		return
	}
	if status != "" && !validUserStatus(status) {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "User status filter is invalid", FieldErrors{"status": "Invalid status"})
		return
	}
	users, err := a.store.ListProxyUsers(r.Context(), search, syncStatus, status)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, users)
}

func (a *API) getUser(w http.ResponseWriter, r *http.Request) {
	user, err := a.userFromPath(r)
	if err != nil {
		a.storeError(w, r, err, "Proxy user not found")
		return
	}
	writeData(w, http.StatusOK, user)
}

// getUserCredentials reveals the current proxy password of one user so the
// operator can view and copy the connection details again after creation.
func (a *API) getUserCredentials(w http.ResponseWriter, r *http.Request) {
	user, err := a.userFromPath(r)
	if err != nil {
		a.storeError(w, r, err, "Proxy user not found")
		return
	}
	plaintext, err := a.box.Decrypt(user.PasswordCipher, "user:"+user.ID+":password")
	if err != nil {
		a.internalError(w, r, errors.New("stored proxy password cannot be decrypted"))
		return
	}
	password := string(plaintext)
	for index := range plaintext {
		plaintext[index] = 0
	}
	a.logger.Info("proxy credentials revealed", "user", user.ID, "actor", actorFromContext(r.Context()))
	w.Header().Set("Cache-Control", "no-store")
	writeData(w, http.StatusOK, map[string]string{
		"id":       user.ID,
		"username": user.Username,
		"password": password,
	})
}

// getUserSubscription returns the subscription URL of a user: url is absolute
// when PUBLIC_URL is configured; otherwise the console resolves path against
// the origin it talks to.
func (a *API) getUserSubscription(w http.ResponseWriter, r *http.Request) {
	user, err := a.userFromPath(r)
	if err != nil {
		a.storeError(w, r, err, "Proxy user not found")
		return
	}
	// The URL grants access to the password, so reveals are audited like
	// credentials.
	a.logger.Info("proxy subscription revealed", "user", user.ID, "actor", actorFromContext(r.Context()))
	a.writeSubscription(w, r, user)
}

// resetUserSubscription revokes the current subscription URL and returns the
// new one.
func (a *API) resetUserSubscription(w http.ResponseWriter, r *http.Request) {
	if !decodeEmptyObject(w, r) {
		return
	}
	user, err := a.userFromPath(r)
	if err != nil {
		a.storeError(w, r, err, "Proxy user not found")
		return
	}
	if err := a.store.RotateProxyUserSubscription(r.Context(), user.ID); err != nil {
		a.storeError(w, r, err, "Proxy user not found")
		return
	}
	user.SubscriptionVersion++
	a.logger.Info("proxy subscription reset", "user", user.ID, "actor", actorFromContext(r.Context()))
	a.writeSubscription(w, r, user)
}

func (a *API) writeSubscription(w http.ResponseWriter, r *http.Request, user ProxyUser) {
	token, err := a.subscriptions.Token(user.ID, user.SubscriptionVersion)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	data := map[string]string{"path": subscriptionPathPrefix + token}
	if a.cfg.PublicURL != "" {
		data["url"] = a.cfg.PublicURL + data["path"]
	}
	w.Header().Set("Cache-Control", "no-store")
	writeData(w, http.StatusOK, data)
}

// serveSubscription answers proxy clients. Every failure is a plain 404 so the
// endpoint does not reveal whether a user exists.
func (a *API) serveSubscription(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	userID, ok := a.subscriptions.UserID(token)
	if !ok {
		http.NotFound(w, r)
		return
	}
	user, err := a.store.GetProxyUser(r.Context(), userID)
	if errors.Is(err, ErrNotFound) || (err == nil && !a.subscriptions.Valid(token, user.ID, user.SubscriptionVersion)) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	servers, err := a.serversByID(r.Context(), user.ServerIDs)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	// Same order as the console's server list.
	slices.SortFunc(servers, func(left, right Server) int {
		return strings.Compare(strings.ToLower(left.Name), strings.ToLower(right.Name))
	})
	plaintext, err := a.box.Decrypt(user.PasswordCipher, "user:"+user.ID+":password")
	if err != nil {
		a.internalError(w, r, errors.New("stored proxy password cannot be decrypted"))
		return
	}
	nodes := subscriptionNodes(servers, user.Username, string(plaintext))
	for index := range plaintext {
		plaintext[index] = 0
	}
	format := subscriptionFormat(r.URL.Query(), r.UserAgent())
	body, fileName, contentType := shareLinksSubscription(nodes), user.Username+".txt", "text/plain; charset=utf-8"
	if format == "clash" {
		body, fileName, contentType = clashSubscription(nodes), user.Username+".yaml", "text/yaml; charset=utf-8"
	}
	a.logger.Info("proxy subscription fetched", "user", user.ID, "format", format, "nodes", len(nodes))
	header := w.Header()
	header.Set("Content-Type", contentType)
	header.Set("Cache-Control", "no-store")
	// Clash clients name the profile after the file name.
	header.Set("Content-Disposition", `attachment; filename="`+fileName+`"`)
	header.Set("Profile-Update-Interval", strconv.Itoa(subscriptionUpdateHours))
	header.Set("Subscription-Userinfo", subscriptionUserInfo(user))
	w.WriteHeader(http.StatusOK)
	w.Write(body)
}

func (a *API) createUser(w http.ResponseWriter, r *http.Request) {
	var input UserInput
	if !decodeJSON(w, r, &input) {
		return
	}
	if fields := validateUserInput(&input, true); len(fields) > 0 {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "Proxy user details are invalid", fields)
		return
	}
	now := time.Now().UTC()
	user := ProxyUser{ServerIDs: input.ServerIDs, ServerCount: len(input.ServerIDs), SyncStatus: "pending", CreatedAt: now, UpdatedAt: now}
	if fields := applyUsageInput(&user, input, true, now); len(fields) > 0 {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "Proxy user details are invalid", fields)
		return
	}
	servers, err := a.serversByID(r.Context(), input.ServerIDs)
	if err != nil {
		a.storeError(w, r, err, "One or more selected servers do not exist")
		return
	}
	password, generated, err := passwordForInput(input.PasswordMode, input.Password, "")
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	userID := newID()
	jobID := newID()
	userCipher, err := a.box.Encrypt([]byte(password), "user:"+userID+":password")
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	jobCipher, err := a.box.Encrypt([]byte(password), "job:"+jobID+":password")
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	user.ID = userID
	user.Username = input.Username
	user.PasswordCipher = userCipher
	targets, err := makeTargets(servers, func(string) TargetTask {
		return TargetTask{Action: "user-add", Username: input.Username, PasswordCiphertext: jobCipher}
	})
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	jobSpec := NewJob{ID: jobID, Type: "user_create", Actor: actorFromContext(r.Context()), EntityType: "proxy_user", EntityID: userID, Targets: targets}
	if err := a.store.CreateProxyUser(r.Context(), user, jobSpec); err != nil {
		message := "A proxy user with this username already exists"
		if errors.Is(err, ErrStale) {
			message = "Server settings changed while the operation was being queued; reload and try again"
		}
		a.storeError(w, r, err, message)
		return
	}
	job, err := a.store.GetJob(r.Context(), jobID)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	a.worker.Notify()
	created, err := a.store.GetProxyUser(r.Context(), userID)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	data := map[string]any{"user": created, "job": job}
	if generated {
		data["generatedPassword"] = password
	}
	writeData(w, http.StatusAccepted, data)
}

func (a *API) updateUser(w http.ResponseWriter, r *http.Request) {
	existing, err := a.userFromPath(r)
	if err != nil {
		a.storeError(w, r, err, "Proxy user not found")
		return
	}
	var input UserInput
	if !decodeJSON(w, r, &input) {
		return
	}
	if fields := validateUserInput(&input, false); len(fields) > 0 {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "Proxy user details are invalid", fields)
		return
	}
	expectedUpdatedAt := existing.UpdatedAt
	previousUsername := existing.Username
	previousServerIDs := existing.ServerIDs
	now := time.Now().UTC()
	if fields := applyUsageInput(&existing, input, false, now); len(fields) > 0 {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "Proxy user details are invalid", fields)
		return
	}
	if _, err := a.store.ServerNames(r.Context(), input.ServerIDs); err != nil {
		a.storeError(w, r, err, "One or more selected servers do not exist")
		return
	}
	oldPassword, err := a.box.Decrypt(existing.PasswordCipher, "user:"+existing.ID+":password")
	if err != nil {
		a.internalError(w, r, errors.New("stored proxy password cannot be decrypted"))
		return
	}
	password, generated, err := passwordForInput(input.PasswordMode, input.Password, string(oldPassword))
	for index := range oldPassword {
		oldPassword[index] = 0
	}
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	userCipher := existing.PasswordCipher
	if input.PasswordMode != "unchanged" {
		userCipher, err = a.box.Encrypt([]byte(password), "user:"+existing.ID+":password")
		if err != nil {
			a.internalError(w, r, err)
			return
		}
	}
	jobID := newID()
	jobCipher, err := a.box.Encrypt([]byte(password), "job:"+jobID+":password")
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	oldSet := stringSet(previousServerIDs)
	newSet := stringSet(input.ServerIDs)
	allServerIDs := sortedUnique(append(append([]string(nil), previousServerIDs...), input.ServerIDs...))
	servers, err := a.serversByID(r.Context(), allServerIDs)
	if err != nil {
		a.storeError(w, r, err, "One or more selected servers do not exist")
		return
	}
	targets, err := makeTargets(servers, func(serverID string) TargetTask {
		_, wasBound := oldSet[serverID]
		_, isBound := newSet[serverID]
		switch {
		case wasBound && isBound:
			return TargetTask{Action: "user-update", OldUsername: previousUsername, Username: input.Username, PasswordCiphertext: jobCipher}
		case wasBound:
			return TargetTask{Action: "user-delete", Username: previousUsername}
		default:
			return TargetTask{Action: "user-add", Username: input.Username, PasswordCiphertext: jobCipher}
		}
	})
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	existing.Username = input.Username
	existing.PasswordCipher = userCipher
	existing.ServerIDs = input.ServerIDs
	existing.ServerCount = len(input.ServerIDs)
	existing.SyncStatus = "pending"
	existing.UpdatedAt = now
	jobSpec := NewJob{ID: jobID, Type: "user_update", Actor: actorFromContext(r.Context()), EntityType: "proxy_user", EntityID: existing.ID, Targets: targets}
	if err := a.store.UpdateProxyUser(r.Context(), existing, expectedUpdatedAt, jobSpec); err != nil {
		message := "A proxy user with this username already exists"
		if errors.Is(err, ErrStale) {
			message = "Proxy user changed while this edit was being prepared; reload and try again"
		}
		a.storeError(w, r, err, message)
		return
	}
	job, err := a.store.GetJob(r.Context(), jobID)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	a.worker.Notify()
	updated, err := a.store.GetProxyUser(r.Context(), existing.ID)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	data := map[string]any{"user": updated, "job": job}
	if generated {
		data["generatedPassword"] = password
	}
	writeData(w, http.StatusAccepted, data)
}

func (a *API) deleteUser(w http.ResponseWriter, r *http.Request) {
	user, err := a.userFromPath(r)
	if err != nil {
		a.storeError(w, r, err, "Proxy user not found")
		return
	}
	if len(user.ServerIDs) == 0 {
		if err := a.store.DeleteProxyUserWithoutJob(r.Context(), user.ID, user.UpdatedAt); err != nil {
			message := "Proxy user not found"
			if errors.Is(err, ErrStale) {
				message = "Proxy user changed before it could be deleted; reload and try again"
			}
			a.storeError(w, r, err, message)
			return
		}
		writeData(w, http.StatusOK, map[string]any{})
		return
	}
	jobID := newID()
	servers, err := a.serversByID(r.Context(), user.ServerIDs)
	if err != nil {
		a.storeError(w, r, err, "One or more selected servers do not exist")
		return
	}
	targets, err := makeTargets(servers, func(string) TargetTask {
		return TargetTask{Action: "user-delete", Username: user.Username}
	})
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	jobSpec := NewJob{ID: jobID, Type: "user_delete", Actor: actorFromContext(r.Context()), EntityType: "proxy_user", EntityID: user.ID, Targets: targets}
	if err := a.store.DeleteProxyUser(r.Context(), user.ID, user.UpdatedAt, jobSpec); err != nil {
		message := "Proxy user not found"
		if errors.Is(err, ErrStale) {
			message = "Proxy user changed before it could be deleted; reload and try again"
		}
		a.storeError(w, r, err, message)
		return
	}
	job, err := a.store.GetJob(r.Context(), jobID)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	a.worker.Notify()
	writeData(w, http.StatusAccepted, map[string]any{"job": job})
}

func (a *API) resetUserTraffic(w http.ResponseWriter, r *http.Request) {
	if !decodeEmptyObject(w, r) {
		return
	}
	a.mutateUserPolicy(w, r, "user_traffic_reset", func(ctx context.Context, user ProxyUser, now time.Time, job *NewJob) error {
		return a.store.ResetProxyUserTraffic(ctx, user.ID, user.UpdatedAt, now, job)
	})
}

func (a *API) setUserState(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.Enabled == nil {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "Proxy user state is invalid", FieldErrors{"enabled": "Enabled must be true or false"})
		return
	}
	jobType := "user_disable"
	if *input.Enabled {
		jobType = "user_enable"
	}
	a.mutateUserPolicy(w, r, jobType, func(ctx context.Context, user ProxyUser, now time.Time, job *NewJob) error {
		return a.store.SetProxyUserEnabled(ctx, user.ID, *input.Enabled, user.UpdatedAt, now, job)
	})
}

// mutateUserPolicy applies a change that only affects the traffic policy of a
// user and queues a policy-apply on every bound server. Users without servers
// are updated without a job.
func (a *API) mutateUserPolicy(w http.ResponseWriter, r *http.Request, jobType string, mutate func(context.Context, ProxyUser, time.Time, *NewJob) error) {
	user, err := a.userFromPath(r)
	if err != nil {
		a.storeError(w, r, err, "Proxy user not found")
		return
	}
	var jobSpec *NewJob
	if len(user.ServerIDs) > 0 {
		servers, err := a.serversByID(r.Context(), user.ServerIDs)
		if err != nil {
			a.storeError(w, r, err, "One or more selected servers do not exist")
			return
		}
		targets, err := makeTargets(servers, func(string) TargetTask { return TargetTask{Action: "policy-apply"} })
		if err != nil {
			a.internalError(w, r, err)
			return
		}
		jobSpec = &NewJob{ID: newID(), Type: jobType, Actor: actorFromContext(r.Context()), EntityType: "proxy_user", EntityID: user.ID, Targets: targets}
	}
	if err := mutate(r.Context(), user, time.Now().UTC(), jobSpec); err != nil {
		message := "Proxy user not found"
		if errors.Is(err, ErrStale) {
			message = "Proxy user changed while this edit was being prepared; reload and try again"
		}
		a.storeError(w, r, err, message)
		return
	}
	updated, err := a.store.GetProxyUser(r.Context(), user.ID)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	data := map[string]any{"user": updated}
	if jobSpec != nil {
		a.worker.Notify()
		job, err := a.store.GetJob(r.Context(), jobSpec.ID)
		if err != nil {
			a.internalError(w, r, err)
			return
		}
		data["job"] = job
	}
	status := http.StatusOK
	if jobSpec != nil {
		status = http.StatusAccepted
	}
	writeData(w, status, data)
}

func (a *API) listJobs(w http.ResponseWriter, r *http.Request) {
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	jobType := strings.TrimSpace(r.URL.Query().Get("type"))
	if status != "" && !slices.Contains([]string{"queued", "running", "succeeded", "partially_failed", "failed", "cancelled"}, status) {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "Job status filter is invalid", FieldErrors{"status": "Invalid status"})
		return
	}
	if len(jobType) > 64 {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "Job type filter is invalid", FieldErrors{"type": "Type is too long"})
		return
	}
	jobs, err := a.store.ListJobs(r.Context(), status, jobType, 250)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, jobs)
}

func (a *API) getJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validID(id) {
		writeError(w, http.StatusNotFound, "not_found", "Job not found", nil)
		return
	}
	job, err := a.store.GetJob(r.Context(), id)
	if err != nil {
		a.storeError(w, r, err, "Job not found")
		return
	}
	targets, err := a.store.ListJobTargets(r.Context(), id)
	if err != nil {
		a.internalError(w, r, err)
		return
	}
	job.Targets = targets
	writeData(w, http.StatusOK, job)
}

func (a *API) retryJob(w http.ResponseWriter, r *http.Request) {
	if !decodeEmptyObject(w, r) {
		return
	}
	id := r.PathValue("id")
	if !validID(id) {
		writeError(w, http.StatusNotFound, "not_found", "Job not found", nil)
		return
	}
	job, err := a.store.RetryJob(r.Context(), id)
	if err != nil {
		message := "Job not found"
		if errors.Is(err, ErrConflict) {
			message = "Job cannot be retried: it is not failed, or newer changes to the same servers or users supersede it"
		}
		a.storeError(w, r, err, message)
		return
	}
	a.worker.Notify()
	writeData(w, http.StatusAccepted, map[string]any{"job": job})
}

// getUpdate reports the running version and the latest GitHub release;
// ?refresh=1 checks GitHub again instead of using the hourly cached result.
func (a *API) getUpdate(w http.ResponseWriter, r *http.Request) {
	refresh := r.URL.Query().Get("refresh") == "1"
	w.Header().Set("Cache-Control", "no-store")
	writeData(w, http.StatusOK, a.updater.Status(r.Context(), refresh))
}

// startUpdate downloads and installs the latest release in the background and
// restarts into it. The body names the version the operator confirmed.
func (a *API) startUpdate(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Version string `json:"version"`
	}
	if !decodeJSON(w, r, &input) {
		return
	}
	if normalizeVersion(input.Version) == "" {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "Update version is invalid", FieldErrors{"version": "Version must look like 1.4.0"})
		return
	}
	status, err := a.updater.Start(r.Context(), input.Version)
	switch {
	case err == nil:
		a.logger.Info("update requested", "from", Version, "to", status.TargetVersion, "actor", actorFromContext(r.Context()))
		writeData(w, http.StatusAccepted, status)
	case errors.Is(err, ErrUpdateDisabled), errors.Is(err, ErrUpdateNoPackage):
		writeError(w, http.StatusUnprocessableEntity, "update_unavailable", err.Error(), nil)
	case errors.Is(err, ErrUpdateInProgress), errors.Is(err, ErrUpdateNotLatest),
		errors.Is(err, ErrUpdateCurrent), errors.Is(err, ErrUpdateJobsActive):
		writeError(w, http.StatusConflict, "conflict", err.Error(), nil)
	default:
		a.internalError(w, r, err)
	}
}

func (a *API) enqueueSingleServerJob(ctx context.Context, server Server, jobType string, task TargetTask) (Job, error) {
	payload, err := json.Marshal(task)
	if err != nil {
		return Job{}, err
	}
	jobID := newID()
	spec := NewJob{
		ID: jobID, Type: jobType, Actor: actorFromContext(ctx), EntityType: "server", EntityID: server.ID,
		Targets: []NewJobTarget{{ID: newID(), ServerID: server.ID, ServerConfigRevision: server.ConfigRevision, Payload: string(payload)}},
	}
	if err := a.store.CreateJob(ctx, spec); err != nil {
		return Job{}, err
	}
	a.worker.Notify()
	return a.store.GetJob(ctx, jobID)
}

func (a *API) enqueueDeploymentJob(ctx context.Context, servers []Server) (Job, error) {
	jobID := newID()
	targets := make([]NewJobTarget, 0, len(servers))
	for _, server := range servers {
		task := TargetTask{
			Action: "deploy", PublicIP: server.PublicIP, HTTPPort: server.HTTPPort,
			SocksPort: server.SocksPort, DNS: append([]string(nil), server.DNS...),
		}
		payload, err := json.Marshal(task)
		if err != nil {
			return Job{}, err
		}
		targets = append(targets, NewJobTarget{
			ID: newID(), ServerID: server.ID, ServerConfigRevision: server.ConfigRevision, Payload: string(payload),
		})
	}
	spec := NewJob{ID: jobID, Type: "deploy", Actor: actorFromContext(ctx), EntityType: "fleet", Targets: targets}
	if err := a.store.CreateDeploymentJob(ctx, spec); err != nil {
		return Job{}, err
	}
	a.worker.Notify()
	return a.store.GetJob(ctx, jobID)
}

func (a *API) serversByID(ctx context.Context, ids []string) ([]Server, error) {
	servers := make([]Server, 0, len(ids))
	for _, id := range ids {
		server, err := a.store.GetServer(ctx, id)
		if err != nil {
			return nil, err
		}
		servers = append(servers, server)
	}
	return servers, nil
}

func decodeServerIDs(w http.ResponseWriter, r *http.Request) ([]string, bool) {
	var input struct {
		ServerIDs []string `json:"serverIds"`
	}
	if !decodeJSON(w, r, &input) {
		return nil, false
	}
	input.ServerIDs = sortedUnique(input.ServerIDs)
	if fields := validateServerIDs(input.ServerIDs); len(fields) > 0 {
		writeError(w, http.StatusUnprocessableEntity, "validation_error", "Server selection is invalid", fields)
		return nil, false
	}
	return input.ServerIDs, true
}

func validateServerIDs(serverIDs []string) FieldErrors {
	fields := make(FieldErrors)
	if len(serverIDs) == 0 {
		fields["serverIds"] = "Select at least one server"
		return fields
	}
	if len(serverIDs) > 500 {
		fields["serverIds"] = "No more than 500 servers may be selected"
		return fields
	}
	for _, serverID := range serverIDs {
		if !validID(serverID) {
			fields["serverIds"] = "A server identifier is invalid"
			break
		}
	}
	return fields
}

func makeTargets(servers []Server, task func(string) TargetTask) ([]NewJobTarget, error) {
	targets := make([]NewJobTarget, 0, len(servers))
	for _, server := range servers {
		payload, err := json.Marshal(task(server.ID))
		if err != nil {
			return nil, err
		}
		targets = append(targets, NewJobTarget{
			ID: newID(), ServerID: server.ID, ServerConfigRevision: server.ConfigRevision, Payload: string(payload),
		})
	}
	return targets, nil
}

func passwordForInput(mode, custom, unchanged string) (string, bool, error) {
	switch mode {
	case "generated":
		password, err := generateProxyPassword()
		return password, true, err
	case "custom":
		return custom, false, nil
	case "unchanged":
		if unchanged == "" {
			return "", false, errors.New("existing password is unavailable")
		}
		return unchanged, false, nil
	default:
		return "", false, errors.New("invalid password mode")
	}
}

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func (a *API) serverFromPath(r *http.Request) (Server, error) {
	id := r.PathValue("id")
	if !validID(id) {
		return Server{}, ErrNotFound
	}
	return a.store.GetServer(r.Context(), id)
}

func (a *API) userFromPath(r *http.Request) (ProxyUser, error) {
	id := r.PathValue("id")
	if !validID(id) {
		return ProxyUser{}, ErrNotFound
	}
	return a.store.GetProxyUser(r.Context(), id)
}

func decodeEmptyObject(w http.ResponseWriter, r *http.Request) bool {
	var body map[string]json.RawMessage
	if !decodeJSON(w, r, &body) {
		return false
	}
	if len(body) != 0 {
		writeError(w, http.StatusBadRequest, "invalid_json", "This operation accepts only an empty JSON object", nil)
		return false
	}
	return true
}

func (a *API) storeError(w http.ResponseWriter, r *http.Request, err error, message string) {
	switch {
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", message, nil)
	case errors.Is(err, ErrConflict), errors.Is(err, ErrServerBusy), errors.Is(err, ErrStale):
		writeError(w, http.StatusConflict, "conflict", message, nil)
	default:
		a.internalError(w, r, err)
	}
}

func (a *API) enqueueError(w http.ResponseWriter, r *http.Request, err error, notFoundMessage string) {
	message := notFoundMessage
	if errors.Is(err, ErrStale) {
		message = "Server settings changed while the operation was being queued; reload and try again"
	}
	a.storeError(w, r, err, message)
}

func (a *API) internalError(w http.ResponseWriter, r *http.Request, err error) {
	a.logger.Error("request failed", "method", r.Method, "path", redactedPath(r.URL.Path), "error", sanitizeMessage(err.Error()))
	writeError(w, http.StatusInternalServerError, "internal_error", "The operation could not be completed", nil)
}

func writeData(w http.ResponseWriter, status int, data any) {
	writeJSON(w, status, map[string]any{"data": data})
}

func writeError(w http.ResponseWriter, status int, code, message string, fields FieldErrors) {
	writeJSON(w, status, apiErrorEnvelope{Error: apiError{Code: code, Message: message, Fields: fields}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		return
	}
}

func newID() string {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		panic("cryptographic random source unavailable: " + err.Error())
	}
	random[6] = (random[6] & 0x0f) | 0x40
	random[8] = (random[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(random)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}

func (a *API) recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				a.logger.Error("HTTP handler panic", "method", r.Method, "path", redactedPath(r.URL.Path),
					"panic", fmt.Sprint(recovered), "stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, "internal_error", "The operation could not be completed", nil)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (a *API) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'")
		next.ServeHTTP(w, r)
	})
}

func (a *API) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			if !a.originAllowed(r, origin) {
				writeError(w, http.StatusForbidden, "origin_forbidden", "Origin is not allowed", nil)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Add("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *API) originAllowed(r *http.Request, origin string) bool {
	if _, allowed := a.cfg.AllowedOrigins[origin]; allowed {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	scheme := a.requestScheme(r)
	return strings.EqualFold(parsed.Scheme, scheme) && strings.EqualFold(parsed.Host, r.Host)
}

func (a *API) requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	if remoteIP := remoteAddressIP(r.RemoteAddr); ipInNetworks(remoteIP, a.cfg.TrustedProxyCIDRs) {
		forwardedProto := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0])
		if strings.EqualFold(forwardedProto, "https") {
			return "https"
		}
	}
	return "http"
}

func (a *API) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		a.logger.Info("HTTP request", "method", r.Method, "path", redactedPath(r.URL.Path), "duration", time.Since(started))
	})
}

func staticHandler(directory string) http.Handler {
	indexPath := filepath.Join(directory, "index.html")
	if info, err := os.Stat(indexPath); err != nil || info.IsDir() {
		return nil
	}
	fileServer := http.FileServer(http.Dir(directory))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed", nil)
			return
		}
		cleanPath := filepath.Clean(strings.TrimPrefix(r.URL.Path, "/"))
		if cleanPath == "." {
			cleanPath = "index.html"
		}
		candidate := filepath.Join(directory, cleanPath)
		if relative, err := filepath.Rel(directory, candidate); err != nil || strings.HasPrefix(relative, "..") {
			writeError(w, http.StatusNotFound, "not_found", "Resource not found", nil)
			return
		}
		if info, err := os.Stat(candidate); err != nil || info.IsDir() {
			// Only extensionless client routes fall back to the SPA shell. A
			// missing asset (stale hashed chunk, favicon, robots.txt, …) must be
			// a real 404; answering it with index.html makes the browser try to
			// execute HTML as JavaScript and hides deployment mistakes.
			if strings.HasPrefix(filepath.ToSlash(cleanPath), "assets/") || filepath.Ext(cleanPath) != "" {
				writeError(w, http.StatusNotFound, "not_found", "Resource not found", nil)
				return
			}
			http.ServeFile(w, r, indexPath)
			return
		}
		if contentType := mime.TypeByExtension(filepath.Ext(candidate)); contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		fileServer.ServeHTTP(w, r)
	})
}

func parseLimit(raw string, fallback, maximum int) int {
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return fallback
	}
	if value > maximum {
		return maximum
	}
	return value
}
