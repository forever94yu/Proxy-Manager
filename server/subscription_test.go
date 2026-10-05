package main

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestSubscriptionEndpoint(t *testing.T) {
	app := newTestApplication(t, nil)
	loginTestClient(t, app)
	// Nodes are listed by server name, so the IPv6 server comes second.
	first := createTestServer(t, app, "东京 01", "203.0.113.10", "ssh-secret")
	second := createTestServer(t, app, "西雅图 v6", "[2001:db8::1]", "ssh-secret")

	const password = "Sub@Pass+1%2"
	response := requestJSON(t, app.client, http.MethodPost, app.server.URL+"/api/v1/users", map[string]any{
		"username": "sub_user", "passwordMode": "custom", "password": password,
		"serverIds": []string{first.ID, second.ID}, "trafficLimitBytes": 10 << 30,
		"expiresAt": "2030-01-02T03:04:05Z",
	})
	assertStatus(t, response, http.StatusAccepted)
	var created struct {
		Data struct {
			User ProxyUser `json:"user"`
			Job  Job       `json:"job"`
		} `json:"data"`
	}
	decodeBody(t, readBody(t, response), &created)
	_ = waitForJob(t, app, created.Data.Job.ID)
	userID := created.Data.User.ID

	type subscriptionEnvelope struct {
		Data struct {
			Path string `json:"path"`
			URL  string `json:"url"`
		} `json:"data"`
	}
	fetchPath := func(method, suffix string, body any) string {
		t.Helper()
		response := requestJSON(t, app.client, method, app.server.URL+"/api/v1/users/"+userID+suffix, body)
		assertStatus(t, response, http.StatusOK)
		if cacheControl := response.Header.Get("Cache-Control"); cacheControl != "no-store" {
			t.Fatalf("subscription Cache-Control = %q, want no-store", cacheControl)
		}
		var envelope subscriptionEnvelope
		decodeBody(t, readBody(t, response), &envelope)
		if !strings.HasPrefix(envelope.Data.Path, subscriptionPathPrefix) || envelope.Data.URL != "" {
			t.Fatalf("subscription = %+v", envelope.Data)
		}
		return envelope.Data.Path
	}
	path := fetchPath(http.MethodGet, "/subscription", nil)
	if again := fetchPath(http.MethodGet, "/subscription", nil); again != path {
		t.Fatalf("subscription path changed between reads: %q != %q", again, path)
	}

	anonymous := &http.Client{}
	fetch := func(path, userAgent string) *http.Response {
		t.Helper()
		request, err := http.NewRequest(http.MethodGet, app.server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("User-Agent", userAgent)
		response, err := anonymous.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}

	// v2rayNG and unknown clients get the base64 share-link list.
	response = fetch(path, "v2rayNG/1.10.0")
	assertStatus(t, response, http.StatusOK)
	if got := response.Header.Get("Subscription-Userinfo"); got != "upload=0; download=0; total=10737418240; expire=1893553445" {
		t.Fatalf("Subscription-Userinfo = %q", got)
	}
	if got := response.Header.Get("Content-Disposition"); got != `attachment; filename="sub_user.txt"` {
		t.Fatalf("Content-Disposition = %q", got)
	}
	decoded, err := base64.StdEncoding.DecodeString(readBody(t, response))
	if err != nil {
		t.Fatalf("share-link subscription is not base64: %v", err)
	}
	links := strings.Split(string(decoded), "\n")
	userInfo := url.QueryEscape(base64.RawStdEncoding.EncodeToString([]byte("sub_user:" + password)))
	want := []string{
		"socks://" + userInfo + "@203.0.113.10:1080#%E4%B8%9C%E4%BA%AC%2001%20SOCKS5",
		"http://sub_user:Sub%40Pass%2B1%252@203.0.113.10:3128#%E4%B8%9C%E4%BA%AC%2001%20HTTP",
		"socks://" + userInfo + "@[2001:db8::1]:1080#%E8%A5%BF%E9%9B%85%E5%9B%BE%20v6%20SOCKS5",
		"http://sub_user:Sub%40Pass%2B1%252@[2001:db8::1]:3128#%E8%A5%BF%E9%9B%85%E5%9B%BE%20v6%20HTTP",
	}
	if strings.Join(links, "\n") != strings.Join(want, "\n") {
		t.Fatalf("share links =\n%s\nwant\n%s", strings.Join(links, "\n"), strings.Join(want, "\n"))
	}
	for _, link := range links {
		parsed, err := url.Parse(link)
		if err != nil || parsed.Port() == "" {
			t.Fatalf("share link %q does not parse: %v", link, err)
		}
	}

	// Clash-family clients get a complete configuration.
	response = fetch(path, "ClashMetaForAndroid/2.11.0")
	assertStatus(t, response, http.StatusOK)
	if got := response.Header.Get("Content-Disposition"); got != `attachment; filename="sub_user.yaml"` {
		t.Fatalf("Content-Disposition = %q", got)
	}
	wantConfig := `mode: rule
proxies:
  - name: "东京 01 SOCKS5"
    type: socks5
    server: "203.0.113.10"
    port: 1080
    username: "sub_user"
    password: "Sub@Pass+1%2"
  - name: "东京 01 HTTP"
    type: http
    server: "203.0.113.10"
    port: 3128
    username: "sub_user"
    password: "Sub@Pass+1%2"
  - name: "西雅图 v6 SOCKS5"
    type: socks5
    server: "2001:db8::1"
    port: 1080
    username: "sub_user"
    password: "Sub@Pass+1%2"
  - name: "西雅图 v6 HTTP"
    type: http
    server: "2001:db8::1"
    port: 3128
    username: "sub_user"
    password: "Sub@Pass+1%2"
proxy-groups:
  - name: "节点选择"
    type: select
    proxies:
      - "东京 01 SOCKS5"
      - "东京 01 HTTP"
      - "西雅图 v6 SOCKS5"
      - "西雅图 v6 HTTP"
rules:
  - "MATCH,节点选择"
`
	if body := readBody(t, response); body != wantConfig {
		t.Fatalf("Clash subscription =\n%s\nwant\n%s", body, wantConfig)
	}

	// An explicit format overrides the User-Agent.
	response = fetch(path+"?format=base64", "clash-verge/v2.0.0")
	if body := readBody(t, response); strings.Contains(body, "proxies") {
		t.Fatalf("format=base64 returned YAML: %s", body)
	}

	// Malformed and forged tokens are indistinguishable from unknown users.
	token := strings.TrimPrefix(path, subscriptionPathPrefix)
	forged := []byte(token)
	forged[len(forged)-1] ^= 1
	for _, candidate := range []string{"/sub/short", "/sub/" + string(forged), "/sub/" + token + "A"} {
		response = fetch(candidate, "v2rayNG")
		assertStatus(t, response, http.StatusNotFound)
		response.Body.Close()
	}

	// Resetting revokes the old URL.
	newPath := fetchPath(http.MethodPost, "/subscription/reset", map[string]any{})
	if newPath == path {
		t.Fatal("subscription reset returned the same URL")
	}
	response = fetch(path, "v2rayNG")
	assertStatus(t, response, http.StatusNotFound)
	response.Body.Close()
	response = fetch(newPath, "v2rayNG")
	assertStatus(t, response, http.StatusOK)
	response.Body.Close()

	// The admin endpoints require a session.
	response = requestJSON(t, anonymous, http.MethodGet, app.server.URL+"/api/v1/users/"+userID+"/subscription", nil)
	assertStatus(t, response, http.StatusUnauthorized)
	response.Body.Close()

	// Deleting the user revokes the subscription.
	response = requestJSON(t, app.client, http.MethodDelete, app.server.URL+"/api/v1/users/"+userID, nil)
	assertStatus(t, response, http.StatusAccepted)
	var deleted struct {
		Data struct {
			Job Job `json:"job"`
		} `json:"data"`
	}
	decodeBody(t, readBody(t, response), &deleted)
	_ = waitForJob(t, app, deleted.Data.Job.ID)
	response = fetch(newPath, "v2rayNG")
	assertStatus(t, response, http.StatusNotFound)
	response.Body.Close()
}

func TestSubscriptionPublicURL(t *testing.T) {
	app := newTestApplication(t, nil)
	loginTestClient(t, app)
	server := createTestServer(t, app, "public", "public.example.com", "ssh-secret")
	response := requestJSON(t, app.client, http.MethodPost, app.server.URL+"/api/v1/users", map[string]any{
		"username": "public_user", "passwordMode": "generated", "serverIds": []string{server.ID},
	})
	assertStatus(t, response, http.StatusAccepted)
	var created struct {
		Data struct {
			User ProxyUser `json:"user"`
		} `json:"data"`
	}
	decodeBody(t, readBody(t, response), &created)

	app.api.cfg.PublicURL = "https://pm.example.com"
	response = requestJSON(t, app.client, http.MethodGet, app.server.URL+"/api/v1/users/"+created.Data.User.ID+"/subscription", nil)
	assertStatus(t, response, http.StatusOK)
	var envelope struct {
		Data struct {
			Path string `json:"path"`
			URL  string `json:"url"`
		} `json:"data"`
	}
	decodeBody(t, readBody(t, response), &envelope)
	if envelope.Data.URL != "https://pm.example.com"+envelope.Data.Path {
		t.Fatalf("subscription URL = %q, path = %q", envelope.Data.URL, envelope.Data.Path)
	}
}

func TestSubscriptionTokenRoundTrip(t *testing.T) {
	signer := NewSubscriptionSigner([]byte("0123456789abcdef0123456789abcdef"))
	other := NewSubscriptionSigner([]byte("fedcba9876543210fedcba9876543210"))
	id := newID()
	token, err := signer.Token(id, 3)
	if err != nil {
		t.Fatal(err)
	}
	if decoded, ok := signer.UserID(token); !ok || decoded != id {
		t.Fatalf("UserID(%q) = %q, %v; want %q", token, decoded, ok, id)
	}
	if !signer.Valid(token, id, 3) {
		t.Fatal("token is not valid for its own version")
	}
	if signer.Valid(token, id, 4) || other.Valid(token, id, 3) || signer.Valid(token, newID(), 3) {
		t.Fatal("token is valid for another version, key or user")
	}
	if _, err := signer.Token("not-a-uuid", 0); err == nil {
		t.Fatal("Token accepted an invalid user ID")
	}
	if _, ok := signer.UserID(token + "="); ok {
		t.Fatal("UserID accepted a padded token")
	}
}

func TestSubscriptionFormatSelection(t *testing.T) {
	cases := []struct {
		query, userAgent, want string
	}{
		{"", "ClashMetaForAndroid/2.11.0", "clash"},
		{"", "clash-verge/v2.0.0", "clash"},
		{"", "FlClash/v0.8.80 clash-verge Platform/android", "clash"},
		{"", "mihomo/1.19.0", "clash"},
		{"", "Stash/2.4.0 Clash/1.9.0", "clash"},
		{"", "Shadowrocket/2.2.65 CFNetwork/1568 Darwin/24.0.0", "clash"},
		{"", "NekoBox/Android/1.3.9 (Prefer ClashMeta Format)", "clash"},
		{"", "v2rayNG/1.10.0", "base64"},
		{"", "v2rayN/7.0", "base64"},
		{"", "HiddifyNext/2.5.7", "base64"},
		{"", "Mozilla/5.0", "base64"},
		{"", "", "base64"},
		{"format=clash", "v2rayNG/1.10.0", "clash"},
		{"format=base64", "ClashMetaForAndroid/2.11.0", "base64"},
		{"format=unknown", "ClashMetaForAndroid/2.11.0", "clash"},
	}
	for _, tc := range cases {
		query, _ := url.ParseQuery(tc.query)
		if got := subscriptionFormat(query, tc.userAgent); got != tc.want {
			t.Errorf("subscriptionFormat(%q, %q) = %q, want %q", tc.query, tc.userAgent, got, tc.want)
		}
	}
}

func TestEncodeURIComponentMatchesJavaScript(t *testing.T) {
	// Expected values are the output of JavaScript's encodeURIComponent.
	cases := map[string]string{
		"abc-_.!~*'()XYZ09": "abc-_.!~*'()XYZ09",
		"a b+c/d=e":         "a%20b%2Bc%2Fd%3De",
		"@:%?#&,":           "%40%3A%25%3F%23%26%2C",
		"东京":                "%E4%B8%9C%E4%BA%AC",
	}
	for input, want := range cases {
		if got := encodeURIComponent(input); got != want {
			t.Errorf("encodeURIComponent(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestEmptySubscription(t *testing.T) {
	want := `mode: rule
proxies: []
proxy-groups:
  - name: "节点选择"
    type: select
    proxies:
      - DIRECT
rules:
  - "MATCH,节点选择"
`
	if got := string(clashSubscription(nil)); got != want {
		t.Fatalf("empty Clash subscription =\n%s", got)
	}
	if body := shareLinksSubscription(nil); len(body) != 0 {
		t.Fatalf("empty share-link subscription = %q", body)
	}
}

func TestRedactedPathHidesSubscriptionTokens(t *testing.T) {
	if got := redactedPath("/sub/abcdef"); got != "/sub/[redacted]" {
		t.Fatalf("redactedPath = %q", got)
	}
	if got := redactedPath("/api/v1/users"); got != "/api/v1/users" {
		t.Fatalf("redactedPath = %q", got)
	}
}

func TestParsePublicURL(t *testing.T) {
	valid := map[string]string{
		"":                            "",
		"https://pm.example.com":      "https://pm.example.com",
		" https://pm.example.com/ ":   "https://pm.example.com",
		"http://203.0.113.5:8080":     "http://203.0.113.5:8080",
		"https://[2001:db8::1]:8443/": "https://[2001:db8::1]:8443",
	}
	for input, want := range valid {
		got, err := parsePublicURL(input)
		if err != nil || got != want {
			t.Errorf("parsePublicURL(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, input := range []string{"pm.example.com", "ftp://pm.example.com", "https://pm.example.com/console", "https://user@pm.example.com", "https://pm.example.com?x=1", "https://"} {
		if _, err := parsePublicURL(input); err == nil {
			t.Errorf("parsePublicURL(%q) accepted an invalid origin", input)
		}
	}
}
