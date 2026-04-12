package decoclient

import (
	"bytes"
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

// MinRequestInterval is the minimum time between consecutive API requests
// to avoid hammering the router. See issue #39.
const MinRequestInterval = 100 * time.Millisecond

// DecoClient is safe for concurrent use; a mutex serializes access to session state.
type DecoClient struct {
	host         string
	password     string
	mu           sync.Mutex
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

// ValidateConfig checks that the router is reachable with a quick TCP dial.
func ValidateConfig(config *Config) error {
	host := config.Host
	if !strings.Contains(host, ":") {
		host = host + ":80"
	}
	conn, err := net.DialTimeout("tcp", host, 3*time.Second)
	if err != nil {
		return fmt.Errorf("cannot reach router at %s: %v", config.Host, err)
	}
	conn.Close()
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

func (dc *DecoClient) baseURL() string {
	return fmt.Sprintf("http://%s/cgi-bin/luci/;stok=%s", dc.host, dc.stok)
}

// EnsureAuthorized checks if the session is active and re-authorizes if needed.
func (dc *DecoClient) EnsureAuthorized() error {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	if dc.logged && time.Since(dc.lastAuthTime) < SessionTimeout {
		return nil
	}
	if dc.logged {
		decolog.Debug("session older than %s, re-authenticating", SessionTimeout)
		dc.invalidate()
	}
	// Reset HTTP client to clear stale cookies
	jar, _ := cookiejar.New(nil) // cookiejar.New never returns an error with nil options
	dc.client.Jar = jar
	var err error
	dc.aesKey, dc.aesIV, err = generateAESKeyIV()
	if err != nil {
		return fmt.Errorf("failed to generate encryption keys: %v", err)
	}
	return dc.Authorize()
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

func (dc *DecoClient) getPasswordKeys() error {
	apiURL := fmt.Sprintf("http://%s/cgi-bin/luci/;stok=/login?form=keys&operation=read", dc.host)

	resp, err := dc.client.Post(apiURL, "application/json", nil)
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
	dc.pwdN.SetString(result.Result.Password[0], 16)

	return nil
}

func (dc *DecoClient) getAuthKeys() error {
	apiURL := fmt.Sprintf("http://%s/cgi-bin/luci/;stok=/login?form=auth&operation=read", dc.host)

	resp, err := dc.client.Post(apiURL, "application/json", nil)
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
	dc.signN.SetString(result.Result.Key[0], 16)
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
func (dc *DecoClient) Authorize() error {
	// Get RSA keys
	if err := dc.getPasswordKeys(); err != nil {
		return fmt.Errorf("failed to get password keys: %v", err)
	}

	if err := dc.getAuthKeys(); err != nil {
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

	req, err := http.NewRequest("POST", loginURL, strings.NewReader(bodyStr))
	if err != nil {
		return fmt.Errorf("failed to create login request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
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

	dc.stok = decryptedResp.Result.Stok

	// Extract sysauth cookie
	cookieHeader := resp.Header.Get("Set-Cookie")
	matches := sysauthRegexp.FindStringSubmatch(cookieHeader)
	if len(matches) > 1 {
		dc.sysauth = matches[1]
	}

	dc.logged = true
	dc.lastAuthTime = time.Now()
	return nil
}

func (dc *DecoClient) requestOnce(path string, reqData map[string]interface{}) (map[string]interface{}, error) {
	// Rate limit outside the mutex to avoid blocking other callers during sleep.
	dc.mu.Lock()
	var sleepDur time.Duration
	if !dc.lastRequest.IsZero() {
		if elapsed := time.Since(dc.lastRequest); elapsed < MinRequestInterval {
			sleepDur = MinRequestInterval - elapsed
		}
	}
	dc.mu.Unlock()
	if sleepDur > 0 {
		time.Sleep(sleepDur)
	}
	dc.mu.Lock()
	defer dc.mu.Unlock()
	return dc.requestOnceUnlocked(path, reqData)
}

// requestOnceUnlocked is the lock-free implementation of requestOnce; callers must hold dc.mu.
func (dc *DecoClient) requestOnceUnlocked(path string, reqData map[string]interface{}) (map[string]interface{}, error) {
	dc.lastRequest = time.Now()

	start := time.Now()
	decolog.Debug("POST %s", path)

	jsonData, err := json.Marshal(reqData)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request data: %v", err)
	}

	// Encrypt request
	encryptedData, err := aesEncrypt(jsonData, dc.aesKey, dc.aesIV)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt request: %v", err)
	}

	// Get signature
	signature, err := dc.getSignature(len(encryptedData), false)
	if err != nil {
		return nil, fmt.Errorf("failed to get request signature: %v", err)
	}

	// Build request
	reqURL := fmt.Sprintf("%s/%s", dc.baseURL(), path)

	// Build body (sign before data)
	bodyStr := fmt.Sprintf("sign=%s&data=%s", signature, url.QueryEscape(encryptedData))

	req, err := http.NewRequest("POST", reqURL, strings.NewReader(bodyStr))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Referer", fmt.Sprintf("http://%s/webpages/index.html", dc.host))

	if dc.sysauth != "" {
		req.AddCookie(&http.Cookie{Name: "sysauth", Value: dc.sysauth})
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
	decrypted, err := aesDecrypt(responseData, dc.aesKey, dc.aesIV)
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

// Request sends an encrypted API request to the router.
func (dc *DecoClient) Request(path string, reqData map[string]interface{}) (map[string]interface{}, error) {
	dc.mu.Lock()
	logged := dc.logged
	dc.mu.Unlock()
	if !logged {
		return nil, fmt.Errorf("not logged in")
	}

	result, err := dc.requestOnce(path, reqData)
	if err != nil {
		// Check for session expired/forbidden errors — auto-retry once.
		// Use the mutex to ensure only one goroutine re-authenticates.
		errMsg := err.Error()
		if strings.Contains(errMsg, "API error code: -40401") || strings.Contains(errMsg, "API error code: -40403") {
			dc.mu.Lock()
			needsReauth := dc.logged // still logged means no one else invalidated yet
			if needsReauth {
				dc.invalidate()
			}
			dc.mu.Unlock()
			if needsReauth {
				if authErr := dc.Authorize(); authErr != nil {
					return nil, fmt.Errorf("re-auth failed: %v (original: %v)", authErr, err)
				}
			} else {
				// Another goroutine already invalidated; wait briefly then let EnsureAuthorized handle it
				if authErr := dc.EnsureAuthorized(); authErr != nil {
					return nil, fmt.Errorf("re-auth failed: %v (original: %v)", authErr, err)
				}
			}
			return dc.requestOnce(path, reqData)
		}
		return nil, err
	}
	return result, nil
}

// Logout sends a logout request to the router.
func (dc *DecoClient) Logout() {
	dc.mu.Lock()
	defer dc.mu.Unlock()
	if dc.logged {
		if _, err := dc.requestOnceUnlocked("admin/system?form=logout", map[string]interface{}{"operation": "write"}); err != nil {
			decolog.Debug("logout request failed: %v", err)
		}
		dc.logged = false
	}
}

// ==================== API METHODS ====================

// GetClients returns the list of connected devices.
func (dc *DecoClient) GetClients() (*ClientList, error) {
	data, err := dc.Request("admin/client?form=client_list", map[string]interface{}{
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
func (dc *DecoClient) GetNetwork() (*NetworkInfo, error) {
	data, err := dc.Request("admin/network?form=wan_ipv4", map[string]interface{}{"operation": "read"})
	if err != nil {
		return nil, err
	}

	perf, perfErr := dc.Request("admin/network?form=performance", map[string]interface{}{"operation": "read"})
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
func (dc *DecoClient) GetWireless() (*WirelessInfo, error) {
	data, err := dc.Request("admin/wireless?form=wlan", map[string]interface{}{"operation": "read"})
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
func (dc *DecoClient) GetMesh() (*MeshInfo, error) {
	data, err := dc.Request("admin/device?form=device_list", map[string]interface{}{"operation": "read"})
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
func (dc *DecoClient) Reboot() error {
	_, err := dc.Request("admin/device?form=reboot", map[string]interface{}{
		"operation": "reboot",
	})
	return err
}

// BlockClient blocks a device by MAC address.
func (dc *DecoClient) BlockClient(mac string) error {
	_, err := dc.Request("admin/client?form=block", map[string]interface{}{
		"operation": "write",
		"params": map[string]interface{}{
			"mac":    mac,
			"enable": true,
		},
	})
	return err
}

// UnblockClient unblocks a device by MAC address.
func (dc *DecoClient) UnblockClient(mac string) error {
	_, err := dc.Request("admin/client?form=block", map[string]interface{}{
		"operation": "write",
		"params": map[string]interface{}{
			"mac":    mac,
			"enable": false,
		},
	})
	return err
}
