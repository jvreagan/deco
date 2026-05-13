package decoclient

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jvreagan/deco/internal/decolog"
	"github.com/jvreagan/deco/internal/paths"
)

const maxResponseSize = 10 * 1024 * 1024 // 10 MB

var sysauthRegexp = regexp.MustCompile(`sysauth=([^;]+)`)

// Config holds router credentials
type Config struct {
	Host     string `json:"host"`
	Password string `json:"password"`
}

// SessionTimeout is the max age of an auth session before re-authentication.
// The Deco router firmware typically expires sessions after 5 minutes.
const SessionTimeout = 5 * time.Minute

// sessionRefreshThreshold is the proactive re-auth threshold. Refreshing
// slightly before the actual timeout avoids wasting a round-trip on an
// expired session that would trigger a retry anyway.
const sessionRefreshThreshold = 4 * time.Minute

// MinRequestInterval is the minimum time between consecutive API requests
// to avoid hammering the router. See issue #39.
const MinRequestInterval = 100 * time.Millisecond

// DecoClient is safe for concurrent use; a mutex serializes access to session state.
type DecoClient struct {
	host         string
	password     string
	mu           sync.Mutex
	authCond     *sync.Cond // signaled when auth completes; lazily initialized
	authorizing  bool       // true while an auth handshake is in progress
	client       *http.Client
	stok         string
	sysauth      string
	logged       bool
	lastAuthTime time.Time
	lastRequest  time.Time // tracks last API call for rate limiting

	// Encryption
	aesKey []byte
	aesIV  []byte

	// RSA keys for password encryption
	pwdN *big.Int
	pwdE int

	// RSA keys for signature
	signN *big.Int
	signE int
	seq   int
}

// LoadConfig reads the router config from the config directory.
func LoadConfig() (*Config, error) {
	paths.MigrateIfNeeded()

	configFile := paths.CfgPath("deco_config.json")

	data, err := os.ReadFile(configFile)
	if err != nil {
		return nil, fmt.Errorf("cannot find config file at %s\nRun 'deco setup' to create one", configFile)
	}

	// Warn if config file is world-readable
	if info, statErr := os.Stat(configFile); statErr == nil {
		if info.Mode().Perm()&0077 != 0 {
			decolog.Warn("config %s is readable by other users (mode %o). Consider: chmod 600 %s",
				configFile, info.Mode().Perm(), configFile)
		}
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

// ValidateConfig checks that the router is reachable and responds like a
// Deco router by hitting the keys endpoint. Falls back to a TCP dial if
// the HTTP request fails for non-connection reasons.
func ValidateConfig(config *Config) error {
	base := "http://" + config.Host
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", base+"/cgi-bin/luci/;stok=/login?form=keys", nil)
	if err != nil {
		return fmt.Errorf("cannot reach router at %s: %v", config.Host, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach router at %s: %v\nCheck that the host is correct, or run 'deco setup'", config.Host, err)
	}
	defer resp.Body.Close()
	// A Deco router returns JSON with a "result" key. If we get HTML or a
	// 404, it's probably not a Deco.
	var body map[string]interface{}
	if decErr := json.NewDecoder(resp.Body).Decode(&body); decErr != nil {
		return fmt.Errorf("host %s responded but does not look like a Deco router\nRun 'deco setup' to reconfigure", config.Host)
	}
	return nil
}

// NewDecoClient creates a new DecoClient for the given host and password.
func NewDecoClient(host, password string) *DecoClient {
	jar, _ := cookiejar.New(nil) // cookiejar.New never returns an error with nil options
	client := &http.Client{
		Jar:     jar,
		Timeout: 30 * time.Second,
	}

	dc := &DecoClient{
		host:     host,
		password: password,
		client:   client,
		pwdE:     0x10001,
		signE:    0x10001,
	}

	var err error
	dc.aesKey, dc.aesIV, err = generateAESKeyIV()
	if err != nil {
		// Crypto randomness failure is unrecoverable
		panic(err)
	}
	return dc
}

// EnsureAuthorized checks if the session is active and re-authorizes if needed.
// The mutex is released during the HTTP auth handshake so that concurrent callers
// waiting for auth are not blocked for the full round-trip duration.
func (dc *DecoClient) EnsureAuthorized(ctx context.Context) error {
	dc.mu.Lock()
	// Lazy-init the condition variable
	if dc.authCond == nil {
		dc.authCond = sync.NewCond(&dc.mu)
	}

	// If already logged in and session is fresh, return immediately.
	if dc.logged && time.Since(dc.lastAuthTime) < sessionRefreshThreshold {
		dc.mu.Unlock()
		return nil
	}

	// If another goroutine is already authorizing, wait for it to finish.
	// Spawn a watcher that broadcasts on context cancellation so we don't
	// block indefinitely if the caller's context expires.
	if dc.authorizing {
		done := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				dc.authCond.Broadcast()
			case <-done:
			}
		}()
		for dc.authorizing {
			dc.authCond.Wait()
			if ctx.Err() != nil {
				close(done)
				dc.mu.Unlock()
				return ctx.Err()
			}
		}
		close(done)
	}
	// Re-check after waking up — the auth may have succeeded.
	if dc.logged && time.Since(dc.lastAuthTime) < sessionRefreshThreshold {
		dc.mu.Unlock()
		return nil
	}

	// We are the goroutine that will perform auth.
	if dc.logged {
		decolog.Debug("session older than %s, re-authenticating", SessionTimeout)
		dc.invalidate()
	}
	// Reset HTTP client to clear stale cookies
	jar, _ := cookiejar.New(nil)
	dc.client.Jar = jar
	var err error
	dc.aesKey, dc.aesIV, err = generateAESKeyIV()
	if err != nil {
		dc.mu.Unlock()
		return fmt.Errorf("failed to generate encryption keys: %v", err)
	}
	dc.authorizing = true
	dc.mu.Unlock()

	// Perform auth without holding the mutex — HTTP round-trips happen here.
	authErr := dc.authorize(ctx)

	dc.mu.Lock()
	dc.authorizing = false
	dc.authCond.Broadcast()
	dc.mu.Unlock()

	return authErr
}

// Invalidate marks the session as expired so EnsureAuthorized will re-auth.
func (dc *DecoClient) Invalidate() {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	dc.invalidate()
}

// invalidate is the lock-free implementation of Invalidate; callers must hold dc.mu.
func (dc *DecoClient) invalidate() {
	dc.logged = false
	dc.stok = ""
	dc.sysauth = ""
}

func (dc *DecoClient) getPasswordKeys(ctx context.Context) error {
	apiURL := fmt.Sprintf("http://%s/cgi-bin/luci/;stok=/login?form=keys&operation=read", dc.host)

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := dc.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return fmt.Errorf("failed to read password keys response: %v", err)
	}

	var result struct {
		Result struct {
			Password []string `json:"password"`
		} `json:"result"`
		ErrorCode int `json:"error_code"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return err
	}

	if len(result.Result.Password) < 2 {
		return fmt.Errorf("invalid password keys response")
	}

	dc.pwdN = new(big.Int)
	if _, ok := dc.pwdN.SetString(result.Result.Password[0], 16); !ok {
		return fmt.Errorf("invalid RSA modulus in password keys response")
	}

	return nil
}

func (dc *DecoClient) getAuthKeys(ctx context.Context) error {
	apiURL := fmt.Sprintf("http://%s/cgi-bin/luci/;stok=/login?form=auth&operation=read", dc.host)

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := dc.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return fmt.Errorf("failed to read auth keys response: %v", err)
	}

	var result struct {
		Result struct {
			Key []string `json:"key"`
			Seq int      `json:"seq"`
		} `json:"result"`
		ErrorCode int `json:"error_code"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		return err
	}

	if len(result.Result.Key) < 2 {
		return fmt.Errorf("invalid auth keys response")
	}

	dc.signN = new(big.Int)
	if _, ok := dc.signN.SetString(result.Result.Key[0], 16); !ok {
		return fmt.Errorf("invalid RSA modulus in auth keys response")
	}
	dc.seq = result.Result.Seq

	return nil
}

func (dc *DecoClient) getSignature(dataLen int, isLogin bool) (string, error) {
	hash := md5.Sum([]byte("admin" + dc.password))
	hashStr := hex.EncodeToString(hash[:])

	var signData string
	if isLogin {
		signData = fmt.Sprintf("k=%s&i=%s&h=%s&s=%d",
			string(dc.aesKey), string(dc.aesIV), hashStr, dc.seq+dataLen)
	} else {
		signData = fmt.Sprintf("h=%s&s=%d", hashStr, dc.seq+dataLen)
	}

	return rsaEncrypt(signData, dc.signN, dc.signE)
}

// Authorize performs the login handshake with the router.
// It routes through EnsureAuthorized to use the authorizing guard,
// preventing concurrent auth races on shared RSA state.
func (dc *DecoClient) Authorize() error {
	return dc.EnsureAuthorized(context.Background())
}

// authorize performs the login handshake with the router.
func (dc *DecoClient) authorize(ctx context.Context) error {
	// Get RSA keys
	if err := dc.getPasswordKeys(ctx); err != nil {
		return fmt.Errorf("failed to get password keys: %v", err)
	}

	if err := dc.getAuthKeys(ctx); err != nil {
		return fmt.Errorf("failed to get auth keys: %v", err)
	}

	// Encrypt password
	encPassword, err := rsaEncrypt(dc.password, dc.pwdN, dc.pwdE)
	if err != nil {
		return fmt.Errorf("failed to encrypt password: %v", err)
	}

	// Build login data - Deco uses JSON format
	loginDataMap := map[string]interface{}{
		"params":    map[string]string{"password": encPassword},
		"operation": "login",
	}
	loginDataBytes, err := json.Marshal(loginDataMap)
	if err != nil {
		return fmt.Errorf("failed to marshal login data: %v", err)
	}
	loginData := string(loginDataBytes)

	// Encrypt with AES
	encryptedData, err := aesEncrypt([]byte(loginData), dc.aesKey, dc.aesIV)
	if err != nil {
		return fmt.Errorf("failed to encrypt login data: %v", err)
	}

	// Get signature
	signature, err := dc.getSignature(len(encryptedData), true)
	if err != nil {
		return fmt.Errorf("failed to get login signature: %v", err)
	}

	// POST login
	loginURL := fmt.Sprintf("http://%s/cgi-bin/luci/;stok=/login?form=login", dc.host)

	// Build body manually to match Python's order (sign before data)
	bodyStr := fmt.Sprintf("sign=%s&data=%s", signature, url.QueryEscape(encryptedData))

	req, err := http.NewRequestWithContext(ctx, "POST", loginURL, strings.NewReader(bodyStr))
	if err != nil {
		return fmt.Errorf("failed to create login request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", fmt.Sprintf("http://%s/webpages/index.html", dc.host))

	resp, err := dc.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return fmt.Errorf("failed to read login response: %v", err)
	}

	if len(body) == 0 {
		return fmt.Errorf("empty response from login endpoint, status: %d, url: %s", resp.StatusCode, loginURL)
	}

	var responseData string

	// Try parsing as JSON first
	var loginResp struct {
		Data      string `json:"data"`
		ErrorCode int    `json:"error_code"`
	}

	if err := json.Unmarshal(body, &loginResp); err != nil {
		// Not JSON - body might be encrypted data directly
		responseData = string(body)
	} else {
		responseData = loginResp.Data
	}

	// Decrypt response
	decrypted, err := aesDecrypt(responseData, dc.aesKey, dc.aesIV)
	if err != nil {
		return fmt.Errorf("failed to decrypt login response: %v", err)
	}

	var decryptedResp struct {
		Result struct {
			Stok string `json:"stok"`
		} `json:"result"`
		ErrorCode int `json:"error_code"`
	}

	if err := json.Unmarshal(decrypted, &decryptedResp); err != nil {
		return fmt.Errorf("failed to parse decrypted response: %v, raw: %s", err, string(decrypted))
	}

	if decryptedResp.ErrorCode != 0 {
		return fmt.Errorf("login failed with error code: %d", decryptedResp.ErrorCode)
	}

	// Return session values — the caller (EnsureAuthorized) writes them
	// under the mutex to avoid races with concurrent requestOnce calls.
	stok := decryptedResp.Result.Stok

	// Extract sysauth cookie
	var sysauth string
	cookieHeader := resp.Header.Get("Set-Cookie")
	matches := sysauthRegexp.FindStringSubmatch(cookieHeader)
	if len(matches) > 1 {
		sysauth = matches[1]
	}

	dc.mu.Lock()
	dc.stok = stok
	dc.sysauth = sysauth
	dc.logged = true
	dc.lastAuthTime = time.Now()
	dc.mu.Unlock()

	return nil
}

// requestSnapshot holds a copy of shared state needed to build a request.
// Snapshot under the lock, then release before doing crypto and HTTP I/O.
type requestSnapshot struct {
	aesKey  []byte
	aesIV   []byte
	stok    string
	sysauth string
	host    string
	// Signature fields
	password string
	signN    *big.Int
	signE    int
	seq      int
}

func (dc *DecoClient) requestOnce(ctx context.Context, path string, reqData map[string]interface{}) (map[string]interface{}, error) {
	// Rate limit and snapshot in a single critical section.
	// Pre-reserve the time slot so concurrent goroutines see the updated
	// lastRequest and space their own requests correctly (no TOCTOU gap).
	dc.mu.Lock()
	var sleepDur time.Duration
	if !dc.lastRequest.IsZero() {
		if elapsed := time.Since(dc.lastRequest); elapsed < MinRequestInterval {
			sleepDur = MinRequestInterval - elapsed
		}
	}
	dc.lastRequest = time.Now().Add(sleepDur) // pre-reserve the slot
	// Deep-copy mutable reference types so the snapshot is truly independent
	// of dc's state once the mutex is released.
	var clonedSignN *big.Int
	if dc.signN != nil {
		clonedSignN = new(big.Int).Set(dc.signN)
	}
	snap := requestSnapshot{
		aesKey:   slices.Clone(dc.aesKey),
		aesIV:    slices.Clone(dc.aesIV),
		stok:     dc.stok,
		sysauth:  dc.sysauth,
		host:     dc.host,
		password: dc.password,
		signN:    clonedSignN,
		signE:    dc.signE,
		seq:      dc.seq,
	}
	dc.mu.Unlock()

	if sleepDur > 0 {
		time.Sleep(sleepDur)
	}

	// All crypto, HTTP I/O, and response parsing happen without holding the mutex.
	return dc.doRequest(ctx, path, reqData, snap)
}

// doRequest performs the actual encrypted API request using a snapshot of shared state.
// No mutex is held during this method — crypto, HTTP, and parsing run concurrently.
func (dc *DecoClient) doRequest(ctx context.Context, path string, reqData map[string]interface{}, snap requestSnapshot) (map[string]interface{}, error) {
	start := time.Now()
	decolog.Debug("POST %s", path)

	jsonData, err := json.Marshal(reqData)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request data: %v", err)
	}

	// Encrypt request
	encryptedData, err := aesEncrypt(jsonData, snap.aesKey, snap.aesIV)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt request: %v", err)
	}

	// Get signature (RSA computation — can be expensive)
	signature, err := getSignatureFromSnapshot(snap, len(encryptedData), false)
	if err != nil {
		return nil, fmt.Errorf("failed to get request signature: %v", err)
	}

	// Build request
	reqURL := fmt.Sprintf("http://%s/cgi-bin/luci/;stok=%s/%s", snap.host, snap.stok, path)
	bodyStr := fmt.Sprintf("sign=%s&data=%s", signature, url.QueryEscape(encryptedData))

	req, err := http.NewRequestWithContext(ctx, "POST", reqURL, strings.NewReader(bodyStr))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", fmt.Sprintf("http://%s/webpages/index.html", snap.host))

	if snap.sysauth != "" {
		req.AddCookie(&http.Cookie{Name: "sysauth", Value: snap.sysauth})
	}

	resp, err := dc.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %v", err)
	}

	decolog.Debug("%s -> %d (%d bytes, %s)", path, resp.StatusCode, len(body), time.Since(start))

	if len(body) == 0 {
		return nil, fmt.Errorf("empty response from router")
	}

	var responseData string

	// Try parsing as JSON first
	var result struct {
		Data      string `json:"data"`
		ErrorCode int    `json:"error_code"`
	}

	if err := json.Unmarshal(body, &result); err != nil {
		// Not JSON - body might be encrypted data directly
		responseData = string(body)
	} else {
		responseData = result.Data
	}

	// Decrypt response
	decrypted, err := aesDecrypt(responseData, snap.aesKey, snap.aesIV)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt response: %v", err)
	}

	if len(decrypted) == 0 {
		return nil, fmt.Errorf("empty decrypted response from router")
	}

	var decryptedResp map[string]interface{}
	if err := json.Unmarshal(decrypted, &decryptedResp); err != nil {
		return nil, fmt.Errorf("failed to parse response (%d bytes): %v", len(decrypted), err)
	}

	// Check for error_code in the decrypted response
	if errCode, ok := decryptedResp["error_code"]; ok {
		if code := ToInt(errCode); code != 0 {
			return nil, fmt.Errorf("API error code: %d", code)
		}
	}

	// Return the result block (Deco uses 'result' not 'data')
	if data, ok := decryptedResp["result"].(map[string]interface{}); ok {
		return data, nil
	}

	return decryptedResp, nil
}

// getSignatureFromSnapshot computes the RSA signature using snapshot state.
func getSignatureFromSnapshot(snap requestSnapshot, dataLen int, isLogin bool) (string, error) {
	hash := md5.Sum([]byte("admin" + snap.password))
	hashStr := hex.EncodeToString(hash[:])

	var signData string
	if isLogin {
		signData = fmt.Sprintf("k=%s&i=%s&h=%s&s=%d",
			string(snap.aesKey), string(snap.aesIV), hashStr, snap.seq+dataLen)
	} else {
		signData = fmt.Sprintf("h=%s&s=%d", hashStr, snap.seq+dataLen)
	}

	return rsaEncrypt(signData, snap.signN, snap.signE)
}

// Request sends an encrypted API request to the router.
func (dc *DecoClient) Request(ctx context.Context, path string, reqData map[string]interface{}) (map[string]interface{}, error) {
	dc.mu.Lock()
	logged := dc.logged
	dc.mu.Unlock()
	if !logged {
		return nil, fmt.Errorf("not logged in")
	}

	result, err := dc.requestOnce(ctx, path, reqData)
	if err != nil {
		// Check for session expired/forbidden errors — auto-retry once.
		// Use the mutex to ensure only one goroutine re-authenticates.
		errMsg := err.Error()
		if strings.Contains(errMsg, "API error code: -40401") || strings.Contains(errMsg, "API error code: -40403") {
			dc.mu.Lock()
			if dc.logged {
				dc.invalidate()
			}
			dc.mu.Unlock()
			// Always use EnsureAuthorized so the authorizing guard serializes
			// concurrent re-auth attempts (avoids racing on AES keys/stok).
			if authErr := dc.EnsureAuthorized(ctx); authErr != nil {
				return nil, fmt.Errorf("re-auth failed: %v (original: %v)", authErr, err)
			}
			return dc.requestOnce(ctx, path, reqData)
		}
		return nil, err
	}
	return result, nil
}

// Logout sends a logout request to the router.
// The session is marked as logged-out immediately under the lock,
// then the HTTP call is made without holding the mutex.
func (dc *DecoClient) Logout() {
	dc.mu.Lock()
	if !dc.logged {
		dc.mu.Unlock()
		return
	}
	dc.mu.Unlock()

	// Best-effort logout; don't block callers on unresponsive router.
	// Send the logout request before clearing the logged flag so concurrent
	// goroutines don't start re-authenticating while the logout is in flight.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dc.requestOnce(ctx, "admin/system?form=logout", map[string]interface{}{"operation": "write"})

	dc.mu.Lock()
	dc.logged = false
	dc.mu.Unlock()
}

// ==================== API METHODS ====================

// GetClients returns the list of connected devices.
func (dc *DecoClient) GetClients(ctx context.Context) (*ClientList, error) {
	data, err := dc.Request(ctx, "admin/client?form=client_list", map[string]interface{}{
		"operation": "read",
		// "default" is the Deco API convention meaning "all devices" (not a specific MAC).
		"params": map[string]string{"device_mac": "default"},
	})
	if err != nil {
		return nil, err
	}

	var clients []ClientInfo
	if clientList, ok := data["client_list"].([]interface{}); ok {
		for _, c := range clientList {
			client, ok := c.(map[string]interface{})
			if !ok {
				continue
			}
			if online, ok := client["online"].(bool); !ok || !online {
				continue
			}

			name := ""
			if n, ok := client["name"].(string); ok {
				if decoded, err := base64.StdEncoding.DecodeString(n); err == nil {
					name = string(decoded)
				} else {
					name = n
				}
			}

			connType := ""
			if ct, ok := client["connection_type"].(string); ok {
				switch ct {
				case "wired":
					connType = "Wired"
				case "band2_4":
					connType = "WiFi 2.4GHz"
				case "band5":
					connType = "WiFi 5GHz"
				case "band6":
					connType = "WiFi 6GHz"
				default:
					connType = ct
				}
			}

			clients = append(clients, ClientInfo{
				Name:         name,
				IP:           ToString(client["ip"]),
				MAC:          ToString(client["mac"]),
				Connection:   connType,
				Type:         ToString(client["client_type"]),
				DownloadKbps: ToInt(client["down_speed"]),
				UploadKbps:   ToInt(client["up_speed"]),
			})
		}
	}

	// Sort by IP (numerically)
	sort.Slice(clients, func(i, j int) bool {
		ipA := net.ParseIP(clients[i].IP)
		ipB := net.ParseIP(clients[j].IP)
		if ipA == nil || ipB == nil {
			return clients[i].IP < clients[j].IP
		}
		return bytes.Compare(ipA, ipB) < 0
	})

	return &ClientList{
		Clients: clients,
		Count:   len(clients),
	}, nil
}

// GetNetwork returns the network configuration (WAN, LAN, performance).
// Both API calls are made in parallel for lower latency.
func (dc *DecoClient) GetNetwork(ctx context.Context) (*NetworkInfo, error) {
	var (
		data, perf          map[string]interface{}
		wanErr, perfErr     error
		wg                  sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		data, wanErr = dc.Request(ctx, "admin/network?form=wan_ipv4", map[string]interface{}{"operation": "read"})
	}()
	go func() {
		defer wg.Done()
		perf, perfErr = dc.Request(ctx, "admin/network?form=performance", map[string]interface{}{"operation": "read"})
	}()
	wg.Wait()

	if wanErr != nil {
		return nil, wanErr
	}
	if perfErr != nil {
		decolog.Debug("performance query failed: %v", perfErr)
	}

	wan := GetMap(data, "wan")
	lan := GetMap(data, "lan")
	wanIP := GetMap(wan, "ip_info")
	lanIP := GetMap(lan, "ip_info")

	result := &NetworkInfo{
		WAN: WANInfo{
			IP:      ToString(wanIP["ip"]),
			Gateway: ToString(wanIP["gateway"]),
			Netmask: ToString(wanIP["mask"]),
			MAC:     ToString(wanIP["mac"]),
			DNS:     []string{ToString(wanIP["dns1"]), ToString(wanIP["dns2"])},
		},
		LAN: LANInfo{
			IP:      ToString(lanIP["ip"]),
			Netmask: ToString(lanIP["mask"]),
			MAC:     ToString(lanIP["mac"]),
		},
	}

	if perf != nil {
		if cpu, ok := perf["cpu_usage"]; ok && cpu != nil {
			v := ToFloat(cpu)
			result.Performance.CPUPercent = &v
		}
		if mem, ok := perf["mem_usage"]; ok && mem != nil {
			v := ToFloat(mem)
			result.Performance.MemPercent = &v
		}
	}

	return result, nil
}

// GetWireless returns the wireless configuration.
func (dc *DecoClient) GetWireless(ctx context.Context) (*WirelessInfo, error) {
	data, err := dc.Request(ctx, "admin/wireless?form=wlan", map[string]interface{}{"operation": "read"})
	if err != nil {
		return nil, err
	}

	bands := map[string]BandInfo{}

	parseBand := func(bandData map[string]interface{}, bandName string) BandInfo {
		host := GetMap(bandData, "host")
		guest := GetMap(bandData, "guest")

		ssid := ""
		if s, ok := host["ssid"].(string); ok {
			if decoded, err := base64.StdEncoding.DecodeString(s); err == nil {
				ssid = string(decoded)
			}
		}

		guestSSID := ""
		if s, ok := guest["ssid"].(string); ok {
			if decoded, err := base64.StdEncoding.DecodeString(s); err == nil {
				guestSSID = string(decoded)
			}
		}

		return BandInfo{
			Band: bandName,
			Host: HostInfo{
				Enabled:      ToBool(host["enable"]),
				SSID:         ssid,
				Channel:      ToString(host["channel"]),
				ChannelWidth: ToString(host["channel_width"]),
			},
			Guest: GuestInfo{
				Enabled: ToBool(guest["enable"]),
				SSID:    guestSSID,
			},
		}
	}

	if b, ok := data["band2_4"].(map[string]interface{}); ok {
		bands["2.4GHz"] = parseBand(b, "2.4GHz")
	}
	if b, ok := data["band5_1"].(map[string]interface{}); ok {
		bands["5GHz"] = parseBand(b, "5GHz")
	}
	if b, ok := data["band6"].(map[string]interface{}); ok {
		bands["6GHz"] = parseBand(b, "6GHz")
	}

	return &WirelessInfo{Bands: bands}, nil
}

// GetMesh returns the mesh topology.
func (dc *DecoClient) GetMesh(ctx context.Context) (*MeshInfo, error) {
	data, err := dc.Request(ctx, "admin/device?form=device_list", map[string]interface{}{"operation": "read"})
	if err != nil {
		return nil, err
	}

	var devices []MeshDevice
	if deviceList, ok := data["device_list"].([]interface{}); ok {
		for _, d := range deviceList {
			dev, ok := d.(map[string]interface{})
			if !ok {
				continue
			}

			name := ""
			if n, ok := dev["custom_nickname"].(string); ok && n != "" {
				if decoded, err := base64.StdEncoding.DecodeString(n); err == nil {
					name = string(decoded)
				}
			}
			if name == "" {
				if n, ok := dev["nickname"].(string); ok {
					name = n
				}
			}

			devices = append(devices, MeshDevice{
				Name:     name,
				Model:    ToString(dev["device_model"]),
				Role:     ToString(dev["role"]),
				IP:       ToString(dev["device_ip"]),
				MAC:      ToString(dev["mac"]),
				Firmware: ToString(dev["software_ver"]),
				Status:   ToString(dev["inet_status"]),
			})
		}
	}

	return &MeshInfo{
		Devices: devices,
		Count:   len(devices),
	}, nil
}

// Reboot sends a reboot command to the router.
func (dc *DecoClient) Reboot(ctx context.Context) error {
	_, err := dc.Request(ctx, "admin/device?form=reboot", map[string]interface{}{
		"operation": "reboot",
	})
	return err
}

// BlockClient blocks a device by MAC address.
func (dc *DecoClient) BlockClient(ctx context.Context, mac string) error {
	_, err := dc.Request(ctx, "admin/client?form=block", map[string]interface{}{
		"operation": "write",
		"params": map[string]interface{}{
			"mac":    mac,
			"enable": true,
		},
	})
	return err
}

// UnblockClient unblocks a device by MAC address.
func (dc *DecoClient) UnblockClient(ctx context.Context, mac string) error {
	_, err := dc.Request(ctx, "admin/client?form=block", map[string]interface{}{
		"operation": "write",
		"params": map[string]interface{}{
			"mac":    mac,
			"enable": false,
		},
	})
	return err
}
