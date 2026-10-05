package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// Subscriptions let proxy clients (Clash, Shadowrocket, v2rayNG, …) import and
// refresh every server bound to one proxy user from a single URL.
//
// The URL is /sub/<token>, where the token is base64url(user ID || MAC) and the
// MAC covers the user ID and the user's subscription version. Nothing is
// stored: incrementing the version revokes the URL, and the key is derived
// from MASTER_KEY so URLs survive restarts and session secret rotation.

const (
	subscriptionPathPrefix = "/sub/"
	subscriptionMACSize    = 16
	// subscriptionUpdateHours is the refresh interval advertised to clients
	// that honour the Profile-Update-Interval header.
	subscriptionUpdateHours = 12
)

type SubscriptionSigner struct {
	key []byte
}

func NewSubscriptionSigner(masterKey []byte) *SubscriptionSigner {
	mac := hmac.New(sha256.New, masterKey)
	mac.Write([]byte("proxy-manager/subscription/v1"))
	return &SubscriptionSigner{key: mac.Sum(nil)}
}

func (s *SubscriptionSigner) Token(userID string, version int64) (string, error) {
	if !validID(userID) {
		return "", fmt.Errorf("invalid user ID %q", userID)
	}
	id, err := hex.DecodeString(strings.ReplaceAll(userID, "-", ""))
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(append(id, s.mac(id, version)...)), nil
}

// UserID extracts the user ID from a well-formed token. The token is not
// authenticated until Valid is called with the user's current version.
func (s *SubscriptionSigner) UserID(token string) (string, bool) {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(raw) != 16+subscriptionMACSize {
		return "", false
	}
	encoded := hex.EncodeToString(raw[:16])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], true
}

func (s *SubscriptionSigner) Valid(token, userID string, version int64) bool {
	expected, err := s.Token(userID, version)
	return err == nil && hmac.Equal([]byte(token), []byte(expected))
}

func (s *SubscriptionSigner) mac(id []byte, version int64) []byte {
	mac := hmac.New(sha256.New, s.key)
	mac.Write(id)
	var encodedVersion [8]byte
	binary.BigEndian.PutUint64(encodedVersion[:], uint64(version))
	mac.Write(encodedVersion[:])
	return mac.Sum(nil)[:subscriptionMACSize]
}

// subscriptionNode is one proxy endpoint of a subscription.
type subscriptionNode struct {
	Name     string
	Type     string // socks5 or http
	Host     string // IPv6 literals without brackets
	Port     int
	Username string
	Password string
}

// subscriptionNodes lists a SOCKS5 and an HTTP node per server. Server names
// are unique and the suffixes differ, so node names are unique too.
func subscriptionNodes(servers []Server, username, password string) []subscriptionNode {
	nodes := make([]subscriptionNode, 0, 2*len(servers))
	for _, server := range servers {
		host := strings.Trim(server.Host, "[]")
		if server.SocksPort > 0 {
			nodes = append(nodes, subscriptionNode{
				Name: server.Name + " SOCKS5", Type: "socks5", Host: host, Port: server.SocksPort,
				Username: username, Password: password,
			})
		}
		if server.HTTPPort > 0 {
			nodes = append(nodes, subscriptionNode{
				Name: server.Name + " HTTP", Type: "http", Host: host, Port: server.HTTPPort,
				Username: username, Password: password,
			})
		}
	}
	return nodes
}

// clashSubscription renders a complete Clash / mihomo configuration: Clash
// clients use a subscription as their whole profile, so it needs a selector
// group and a catch-all rule besides the proxies. Strings are written as JSON
// strings, which are valid YAML double-quoted scalars.
func clashSubscription(nodes []subscriptionNode) []byte {
	const group = "节点选择"
	var builder strings.Builder
	builder.WriteString("mode: rule\n")
	if len(nodes) == 0 {
		builder.WriteString("proxies: []\n")
	} else {
		builder.WriteString("proxies:\n")
	}
	for _, node := range nodes {
		fmt.Fprintf(&builder, "  - name: %s\n    type: %s\n    server: %s\n    port: %d\n    username: %s\n    password: %s\n",
			yamlString(node.Name), node.Type, yamlString(node.Host), node.Port, yamlString(node.Username), yamlString(node.Password))
	}
	fmt.Fprintf(&builder, "proxy-groups:\n  - name: %s\n    type: select\n    proxies:\n", yamlString(group))
	for _, node := range nodes {
		fmt.Fprintf(&builder, "      - %s\n", yamlString(node.Name))
	}
	if len(nodes) == 0 {
		builder.WriteString("      - DIRECT\n")
	}
	fmt.Fprintf(&builder, "rules:\n  - %s\n", yamlString("MATCH,"+group))
	return []byte(builder.String())
}

func yamlString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// shareLinksSubscription renders the base64 list of share links read by
// v2rayN, v2rayNG, Hiddify and similar clients.
func shareLinksSubscription(nodes []subscriptionNode) []byte {
	links := make([]string, 0, len(nodes))
	for _, node := range nodes {
		links = append(links, shareLink(node))
	}
	return []byte(base64.StdEncoding.EncodeToString([]byte(strings.Join(links, "\n"))))
}

// shareLink writes the links v2rayN and v2rayNG export themselves. SOCKS
// carries base64(user:pass) as user info: standard alphabet, no padding,
// percent-encoded. HTTP has no such convention and uses plain percent-encoded
// credentials. buildNodeShareLink in web/src/utils.ts builds the same SOCKS
// links for node QR codes.
func shareLink(node subscriptionNode) string {
	address := hostPort(node.Host, node.Port)
	remark := "#" + encodeURIComponent(node.Name)
	if node.Type == "socks5" {
		userInfo := base64.RawStdEncoding.EncodeToString([]byte(node.Username + ":" + node.Password))
		return "socks://" + encodeURIComponent(userInfo) + "@" + address + remark
	}
	return "http://" + encodeURIComponent(node.Username) + ":" + encodeURIComponent(node.Password) + "@" + address + remark
}

// encodeURIComponent matches the JavaScript function, so links built here and
// in the console are identical. Spaces become %20: v2rayNG decodes + as a
// literal plus.
func encodeURIComponent(value string) string {
	const hexDigits = "0123456789ABCDEF"
	var builder strings.Builder
	for _, b := range []byte(value) {
		if 'A' <= b && b <= 'Z' || 'a' <= b && b <= 'z' || '0' <= b && b <= '9' || strings.IndexByte("-_.!~*'()", b) >= 0 {
			builder.WriteByte(b)
			continue
		}
		builder.WriteByte('%')
		builder.WriteByte(hexDigits[b>>4])
		builder.WriteByte(hexDigits[b&0x0f])
	}
	return builder.String()
}

func hostPort(host string, port int) string {
	if strings.Contains(host, ":") {
		return fmt.Sprintf("[%s]:%d", host, port)
	}
	return fmt.Sprintf("%s:%d", host, port)
}

// subscriptionFormat chooses the response format. An explicit ?format= wins;
// otherwise clients that read Clash configurations (Shadowrocket and NekoBox
// included) get YAML and everything else the base64 share-link list.
func subscriptionFormat(query url.Values, userAgent string) string {
	switch strings.ToLower(query.Get("format")) {
	case "clash", "mihomo", "yaml":
		return "clash"
	case "base64", "v2ray":
		return "base64"
	}
	agent := strings.ToLower(userAgent)
	for _, marker := range []string{"clash", "mihomo", "stash", "shadowrocket", "nekobox"} {
		if strings.Contains(agent, marker) {
			return "clash"
		}
	}
	return "base64"
}

// subscriptionUserInfo renders the Subscription-Userinfo header that clients
// show as used traffic, quota and expiry; 0 means unlimited or never.
func subscriptionUserInfo(user ProxyUser) string {
	var expire int64
	if user.ExpiresAt != nil {
		expire = user.ExpiresAt.Unix()
	}
	return fmt.Sprintf("upload=0; download=%d; total=%d; expire=%d", user.TrafficUsedBytes, user.TrafficLimitBytes, expire)
}

// redactedPath keeps subscription tokens out of logs.
func redactedPath(path string) string {
	if strings.HasPrefix(path, subscriptionPathPrefix) {
		return subscriptionPathPrefix + "[redacted]"
	}
	return path
}
