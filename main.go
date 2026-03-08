package main

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// Config holds router credentials
type Config struct {
	Host     string `json:"host"`
	Password string `json:"password"`
}

// DecoClient handles API communication
type DecoClient struct {
	host     string
	password string
	client   *http.Client
	stok     string
	sysauth  string
	logged   bool

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

// Database constants
const (
	DBSizeLimitBytes = 250 * 1024 * 1024 * 1024 // 250 GB
)

var dbPath string

func init() {
	exe, _ := os.Executable()
	dir := filepath.Dir(exe)
	if _, err := os.Stat(dir); err == nil {
		dbPath = filepath.Join(dir, "network_usage.db")
	} else {
		dbPath = "network_usage.db"
	}
}

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]

	switch cmd {
	case "clients":
		jsonOut := hasFlag("--json", "-j")
		runClients(jsonOut)
	case "network":
		jsonOut := hasFlag("--json", "-j")
		runNetwork(jsonOut)
	case "wireless":
		jsonOut := hasFlag("--json", "-j")
		runWireless(jsonOut)
	case "mesh":
		jsonOut := hasFlag("--json", "-j")
		runMesh(jsonOut)
	case "all":
		runAll()
	case "poll":
		interval := getFlagInt("--interval", "-i", 5)
		runPoll(interval)
	case "monitor":
		interval := getFlagInt("--interval", "-i", 60)
		runMonitor(interval)
	case "report":
		period := "today"
		if len(os.Args) > 2 && !strings.HasPrefix(os.Args[2], "-") {
			period = os.Args[2]
		}
		jsonOut := hasFlag("--json", "-j")
		runReport(period, jsonOut)
	case "status":
		runStatus()
	case "purge":
		force := hasFlag("--force", "-f")
		runPurge(force)
	case "setup":
		runSetup()
	case "api":
		if len(os.Args) < 3 {
			fmt.Println("Usage: deco api <endpoint> [json_body]")
			fmt.Println("Example: deco api 'admin/client?form=client_list' '{\"operation\":\"read\"}'")
			os.Exit(1)
		}
		endpoint := os.Args[2]
		body := "{}"
		if len(os.Args) > 3 {
			body = os.Args[3]
		}
		runAPI(endpoint, body)
	default:
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`Deco Network Tool

Commands:
  setup                Interactive configuration wizard
  clients              List connected devices
  network              Show WAN/LAN configuration
  wireless             Show WiFi configuration
  mesh                 Show mesh topology
  all                  Complete network snapshot (JSON)
  poll                 Start bandwidth monitoring
  monitor              Full network monitoring to SQLite
  report [period]      Show usage report (today/hour/all)
  status               Show database statistics
  purge                Delete all records
  api <endpoint>       Call a raw API endpoint

Options:
  --json, -j           Output as JSON
  --interval, -i N     Polling interval in seconds (default: 5 for poll, 60 for monitor)
  --force, -f          Skip confirmation for purge

Examples:
  deco setup
  deco clients
  deco clients --json
  deco poll --interval 10
  deco monitor
  deco monitor --interval 30
  deco report today
  deco report hour --json
  deco purge --force`)
}

func runSetup() {
	scanner := bufio.NewScanner(os.Stdin)

	fmt.Print("Router IP [192.168.68.1]: ")
	scanner.Scan()
	host := strings.TrimSpace(scanner.Text())
	if host == "" {
		host = "192.168.68.1"
	}

	fmt.Print("Admin password: ")
	scanner.Scan()
	password := strings.TrimSpace(scanner.Text())
	if password == "" {
		fmt.Println("Error: password is required")
		os.Exit(1)
	}

	config := Config{Host: host, Password: password}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}

	exe, _ := os.Executable()
	configPath := filepath.Join(filepath.Dir(exe), "deco_config.json")

	if err := os.WriteFile(configPath, data, 0600); err != nil {
		// Fall back to current directory
		configPath = "deco_config.json"
		if err := os.WriteFile(configPath, data, 0600); err != nil {
			fmt.Printf("Error writing config: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Printf("\nConfig saved to %s\n", configPath)
	fmt.Println("Try it out: deco clients")
}

func hasFlag(flags ...string) bool {
	for _, arg := range os.Args {
		for _, flag := range flags {
			if arg == flag {
				return true
			}
		}
	}
	return false
}

func getFlagInt(long, short string, defaultVal int) int {
	for i, arg := range os.Args {
		if (arg == long || arg == short) && i+1 < len(os.Args) {
			var val int
			fmt.Sscanf(os.Args[i+1], "%d", &val)
			if val > 0 {
				return val
			}
		}
	}
	return defaultVal
}

// ==================== CONFIG ====================

func loadConfig() (*Config, error) {
	exe, _ := os.Executable()
	configPath := filepath.Join(filepath.Dir(exe), "deco_config.json")

	data, err := os.ReadFile(configPath)
	if err != nil {
		data, err = os.ReadFile("deco_config.json")
		if err != nil {
			return nil, fmt.Errorf("cannot find deco_config.json")
		}
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

// ==================== ENCRYPTION ====================

func generateAESKeyIV() ([]byte, []byte) {
	// Generate random 16-byte hex strings (like Python library)
	keyBytes := make([]byte, 8)
	ivBytes := make([]byte, 8)
	rand.Read(keyBytes)
	rand.Read(ivBytes)

	key := []byte(hex.EncodeToString(keyBytes))
	iv := []byte(hex.EncodeToString(ivBytes))

	return key, iv
}

func pkcs7Pad(data []byte, blockSize int) []byte {
	padding := blockSize - len(data)%blockSize
	padtext := bytes.Repeat([]byte{byte(padding)}, padding)
	return append(data, padtext...)
}

func pkcs7Unpad(data []byte) []byte {
	length := len(data)
	if length == 0 {
		return data
	}
	padding := int(data[length-1])
	if padding > length || padding > aes.BlockSize {
		return data
	}
	return data[:length-padding]
}

func aesEncrypt(data, key, iv []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}

	padded := pkcs7Pad(data, aes.BlockSize)
	encrypted := make([]byte, len(padded))

	mode := cipher.NewCBCEncrypter(block, iv)
	mode.CryptBlocks(encrypted, padded)

	return base64.StdEncoding.EncodeToString(encrypted), nil
}

func aesDecrypt(data string, key, iv []byte) ([]byte, error) {
	encrypted, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	if len(encrypted)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("ciphertext is not a multiple of block size")
	}

	decrypted := make([]byte, len(encrypted))
	mode := cipher.NewCBCDecrypter(block, iv)
	mode.CryptBlocks(decrypted, encrypted)

	return pkcs7Unpad(decrypted), nil
}

func rsaEncryptPKCS1(data []byte, n *big.Int, e int) ([]byte, error) {
	// Go's crypto/rsa rejects keys < 1024 bits, so we implement PKCS1v1.5 manually
	modSize := (n.BitLen() + 7) / 8

	// PKCS#1 v1.5 padding: 0x00 0x02 [random non-zero bytes] 0x00 [data]
	if len(data) > modSize-11 {
		return nil, fmt.Errorf("message too long for RSA key size")
	}

	padded := make([]byte, modSize)
	padded[0] = 0x00
	padded[1] = 0x02

	// Fill with random non-zero bytes
	psLen := modSize - len(data) - 3
	for i := 0; i < psLen; {
		b := make([]byte, 1)
		rand.Read(b)
		if b[0] != 0 {
			padded[2+i] = b[0]
			i++
		}
	}
	padded[2+psLen] = 0x00
	copy(padded[3+psLen:], data)

	// RSA: c = m^e mod n
	m := new(big.Int).SetBytes(padded)
	c := new(big.Int).Exp(m, big.NewInt(int64(e)), n)

	// Ensure output is modSize bytes
	result := c.Bytes()
	if len(result) < modSize {
		padResult := make([]byte, modSize)
		copy(padResult[modSize-len(result):], result)
		result = padResult
	}

	return result, nil
}

func rsaEncrypt(data string, n *big.Int, e int) string {
	modSize := (n.BitLen() + 7) / 8
	step := 53 // Python uses 53 byte chunks

	var result strings.Builder
	for i := 0; i < len(data); i += step {
		end := i + step
		if end > len(data) {
			end = len(data)
		}
		chunk := []byte(data[i:end])

		// Use PKCS1v1.5 encryption
		encrypted, err := rsaEncryptPKCS1(chunk, n, e)
		if err != nil {
			// Fallback shouldn't happen but just in case
			continue
		}

		// Pad result to modulus size
		encHex := hex.EncodeToString(encrypted)
		for len(encHex) < modSize*2 {
			encHex = "0" + encHex
		}
		result.WriteString(encHex)
	}
	return result.String()
}

// ==================== DECO CLIENT ====================

func NewDecoClient(host, password string) *DecoClient {
	jar, _ := cookiejar.New(nil)
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

	dc.aesKey, dc.aesIV = generateAESKeyIV()
	return dc
}

func (dc *DecoClient) baseURL() string {
	return fmt.Sprintf("http://%s/cgi-bin/luci/;stok=%s", dc.host, dc.stok)
}

func (dc *DecoClient) getPasswordKeys() error {
	apiURL := fmt.Sprintf("http://%s/cgi-bin/luci/;stok=/login?form=keys&operation=read", dc.host)

	resp, err := dc.client.Post(apiURL, "application/json", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

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

	body, _ := io.ReadAll(resp.Body)

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

func (dc *DecoClient) getSignature(dataLen int, isLogin bool) string {
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

func (dc *DecoClient) Authorize() error {
	// Get RSA keys
	if err := dc.getPasswordKeys(); err != nil {
		return fmt.Errorf("failed to get password keys: %v", err)
	}

	if err := dc.getAuthKeys(); err != nil {
		return fmt.Errorf("failed to get auth keys: %v", err)
	}

	// Encrypt password
	encPassword := rsaEncrypt(dc.password, dc.pwdN, dc.pwdE)

	// Build login data - Deco uses JSON format
	loginDataMap := map[string]interface{}{
		"params":    map[string]string{"password": encPassword},
		"operation": "login",
	}
	loginDataBytes, _ := json.Marshal(loginDataMap)
	loginData := string(loginDataBytes)

	// Encrypt with AES
	encryptedData, err := aesEncrypt([]byte(loginData), dc.aesKey, dc.aesIV)
	if err != nil {
		return fmt.Errorf("failed to encrypt login data: %v", err)
	}

	// Get signature
	signature := dc.getSignature(len(encryptedData), true)

	// POST login
	loginURL := fmt.Sprintf("http://%s/cgi-bin/luci/;stok=/login?form=login", dc.host)

	// Build body manually to match Python's order (sign before data)
	bodyStr := fmt.Sprintf("sign=%s&data=%s", signature, url.QueryEscape(encryptedData))

	req, _ := http.NewRequest("POST", loginURL, strings.NewReader(bodyStr))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Referer", fmt.Sprintf("http://%s/webpages/index.html", dc.host))

	resp, err := dc.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	// Debug: show raw response
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
	re := regexp.MustCompile(`sysauth=([^;]+)`)
	matches := re.FindStringSubmatch(cookieHeader)
	if len(matches) > 1 {
		dc.sysauth = matches[1]
	}

	dc.logged = true
	return nil
}

func (dc *DecoClient) Request(path string, reqData map[string]interface{}) (map[string]interface{}, error) {
	if !dc.logged {
		return nil, fmt.Errorf("not logged in")
	}

	jsonData, _ := json.Marshal(reqData)

	// Encrypt request
	encryptedData, _ := aesEncrypt(jsonData, dc.aesKey, dc.aesIV)

	// Get signature
	signature := dc.getSignature(len(encryptedData), false)

	// Build request
	reqURL := fmt.Sprintf("%s/%s", dc.baseURL(), path)

	// Build body (sign before data)
	bodyStr := fmt.Sprintf("sign=%s&data=%s", signature, url.QueryEscape(encryptedData))

	req, _ := http.NewRequest("POST", reqURL, strings.NewReader(bodyStr))
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

	body, _ := io.ReadAll(resp.Body)

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
		return nil, err
	}

	var decryptedResp map[string]interface{}
	if err := json.Unmarshal(decrypted, &decryptedResp); err != nil {
		return nil, err
	}

	// Return the result block (Deco uses 'result' not 'data')
	if data, ok := decryptedResp["result"].(map[string]interface{}); ok {
		return data, nil
	}

	return decryptedResp, nil
}

func (dc *DecoClient) Logout() {
	if dc.logged {
		dc.Request("admin/system?form=logout", map[string]interface{}{"operation": "write"})
		dc.logged = false
	}
}

// ==================== API METHODS ====================

func (dc *DecoClient) GetClients() (map[string]interface{}, error) {
	data, err := dc.Request("admin/client?form=client_list", map[string]interface{}{
		"operation": "read",
		"params":    map[string]string{"device_mac": "default"},
	})
	if err != nil {
		return nil, err
	}

	clients := []map[string]interface{}{}
	if clientList, ok := data["client_list"].([]interface{}); ok {
		for _, c := range clientList {
			client := c.(map[string]interface{})
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

			clients = append(clients, map[string]interface{}{
				"name":          name,
				"ip":            client["ip"],
				"mac":           client["mac"],
				"connection":    connType,
				"type":          client["client_type"],
				"download_kbps": toInt(client["down_speed"]),
				"upload_kbps":   toInt(client["up_speed"]),
			})
		}
	}

	// Sort by IP
	sort.Slice(clients, func(i, j int) bool {
		return fmt.Sprintf("%v", clients[i]["ip"]) < fmt.Sprintf("%v", clients[j]["ip"])
	})

	return map[string]interface{}{
		"clients": clients,
		"count":   len(clients),
	}, nil
}

func (dc *DecoClient) GetNetwork() (map[string]interface{}, error) {
	data, err := dc.Request("admin/network?form=wan_ipv4", map[string]interface{}{"operation": "read"})
	if err != nil {
		return nil, err
	}

	perf, _ := dc.Request("admin/network?form=performance", map[string]interface{}{"operation": "read"})

	wan := getMap(data, "wan")
	lan := getMap(data, "lan")
	wanIP := getMap(wan, "ip_info")
	lanIP := getMap(lan, "ip_info")

	result := map[string]interface{}{
		"wan": map[string]interface{}{
			"ip":      wanIP["ip"],
			"gateway": wanIP["gateway"],
			"netmask": wanIP["mask"],
			"mac":     wanIP["mac"],
			"dns":     []interface{}{wanIP["dns1"], wanIP["dns2"]},
		},
		"lan": map[string]interface{}{
			"ip":      lanIP["ip"],
			"netmask": lanIP["mask"],
			"mac":     lanIP["mac"],
		},
		"performance": map[string]interface{}{
			"cpu_percent": nil,
			"mem_percent": nil,
		},
	}

	if perf != nil {
		result["performance"] = map[string]interface{}{
			"cpu_percent": perf["cpu_usage"],
			"mem_percent": perf["mem_usage"],
		}
	}

	return result, nil
}

func (dc *DecoClient) GetWireless() (map[string]interface{}, error) {
	data, err := dc.Request("admin/wireless?form=wlan", map[string]interface{}{"operation": "read"})
	if err != nil {
		return nil, err
	}

	bands := map[string]interface{}{}

	parseBand := func(bandData map[string]interface{}, bandName string) map[string]interface{} {
		if bandData == nil {
			return nil
		}
		host := getMap(bandData, "host")
		guest := getMap(bandData, "guest")

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

		return map[string]interface{}{
			"band": bandName,
			"host": map[string]interface{}{
				"enabled":       host["enable"],
				"ssid":          ssid,
				"channel":       host["channel"],
				"channel_width": host["channel_width"],
			},
			"guest": map[string]interface{}{
				"enabled": guest["enable"],
				"ssid":    guestSSID,
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

	return map[string]interface{}{"bands": bands}, nil
}

func (dc *DecoClient) GetMesh() (map[string]interface{}, error) {
	data, err := dc.Request("admin/device?form=device_list", map[string]interface{}{"operation": "read"})
	if err != nil {
		return nil, err
	}

	devices := []map[string]interface{}{}
	if deviceList, ok := data["device_list"].([]interface{}); ok {
		for _, d := range deviceList {
			dev := d.(map[string]interface{})

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

			devices = append(devices, map[string]interface{}{
				"name":     name,
				"model":    dev["device_model"],
				"role":     dev["role"],
				"ip":       dev["device_ip"],
				"mac":      dev["mac"],
				"firmware": dev["software_ver"],
				"status":   dev["inet_status"],
			})
		}
	}

	return map[string]interface{}{
		"devices": devices,
		"count":   len(devices),
	}, nil
}

// ==================== CLI COMMANDS ====================

func runClients(jsonOut bool) {
	config, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	client := NewDecoClient(config.Host, config.Password)
	if err := client.Authorize(); err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting: %v\n", err)
		os.Exit(1)
	}
	defer client.Logout()

	data, err := client.GetClients()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if jsonOut {
		printJSON(data)
	} else {
		printClientsTable(data)
	}
}

func runNetwork(jsonOut bool) {
	config, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	client := NewDecoClient(config.Host, config.Password)
	if err := client.Authorize(); err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting: %v\n", err)
		os.Exit(1)
	}
	defer client.Logout()

	data, err := client.GetNetwork()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if jsonOut {
		printJSON(data)
	} else {
		printNetworkTable(data)
	}
}

func runWireless(jsonOut bool) {
	config, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	client := NewDecoClient(config.Host, config.Password)
	if err := client.Authorize(); err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting: %v\n", err)
		os.Exit(1)
	}
	defer client.Logout()

	data, err := client.GetWireless()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if jsonOut {
		printJSON(data)
	} else {
		printWirelessTable(data)
	}
}

func runMesh(jsonOut bool) {
	config, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	client := NewDecoClient(config.Host, config.Password)
	if err := client.Authorize(); err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting: %v\n", err)
		os.Exit(1)
	}
	defer client.Logout()

	data, err := client.GetMesh()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	if jsonOut {
		printJSON(data)
	} else {
		printMeshTable(data)
	}
}

func runAPI(endpoint, body string) {
	config, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	client := NewDecoClient(config.Host, config.Password)
	if err := client.Authorize(); err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting: %v\n", err)
		os.Exit(1)
	}
	defer client.Logout()

	var reqData map[string]interface{}
	if err := json.Unmarshal([]byte(body), &reqData); err != nil {
		fmt.Fprintf(os.Stderr, "Invalid JSON body: %v\n", err)
		os.Exit(1)
	}

	resp, err := client.Request(endpoint, reqData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "API error: %v\n", err)
		os.Exit(1)
	}

	printJSON(resp)
}

func runAll() {
	config, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	client := NewDecoClient(config.Host, config.Password)
	if err := client.Authorize(); err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting: %v\n", err)
		os.Exit(1)
	}
	defer client.Logout()

	network, _ := client.GetNetwork()
	wireless, _ := client.GetWireless()
	mesh, _ := client.GetMesh()
	clients, _ := client.GetClients()

	data := map[string]interface{}{
		"timestamp": time.Now().Format(time.RFC3339),
		"router":    config.Host,
		"network":   network,
		"wireless":  wireless,
		"mesh":      mesh,
		"clients":   clients,
	}

	printJSON(data)
}

// ==================== MONITORING ====================

func initDB() (*sql.DB, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, err
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS bandwidth_samples (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp DATETIME DEFAULT CURRENT_TIMESTAMP,
			mac TEXT NOT NULL,
			name TEXT,
			ip TEXT,
			connection TEXT,
			device_type TEXT,
			download_kbps INTEGER DEFAULT 0,
			upload_kbps INTEGER DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS idx_timestamp ON bandwidth_samples(timestamp);
		CREATE INDEX IF NOT EXISTS idx_mac ON bandwidth_samples(mac);

		CREATE TABLE IF NOT EXISTS network_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp DATETIME NOT NULL,
			wan_ip TEXT,
			wan_gateway TEXT,
			wan_dns1 TEXT,
			wan_dns2 TEXT,
			lan_ip TEXT,
			lan_netmask TEXT,
			cpu_percent REAL,
			mem_percent REAL
		);
		CREATE INDEX IF NOT EXISTS idx_net_timestamp ON network_snapshots(timestamp);

		CREATE TABLE IF NOT EXISTS mesh_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp DATETIME NOT NULL,
			name TEXT,
			role TEXT,
			ip TEXT,
			mac TEXT,
			model TEXT,
			firmware TEXT,
			status TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_mesh_timestamp ON mesh_snapshots(timestamp);

		CREATE TABLE IF NOT EXISTS wireless_snapshots (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp DATETIME NOT NULL,
			band TEXT,
			ssid TEXT,
			channel TEXT,
			channel_width TEXT,
			host_enabled INTEGER,
			guest_enabled INTEGER,
			guest_ssid TEXT
		);
		CREATE INDEX IF NOT EXISTS idx_wireless_timestamp ON wireless_snapshots(timestamp);
	`)
	if err != nil {
		return nil, err
	}

	return db, nil
}

func getDBSize() int64 {
	info, err := os.Stat(dbPath)
	if err != nil {
		return 0
	}
	return info.Size()
}

func checkDBSizeLimit() (bool, int64) {
	size := getDBSize()
	return size < DBSizeLimitBytes, size
}

func formatSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	} else if bytes < 1024*1024 {
		return fmt.Sprintf("%.2f KB", float64(bytes)/1024)
	} else if bytes < 1024*1024*1024 {
		return fmt.Sprintf("%.2f MB", float64(bytes)/(1024*1024))
	}
	return fmt.Sprintf("%.2f GB", float64(bytes)/(1024*1024*1024))
}

func formatBytes(kb float64) string {
	if kb < 1024 {
		return fmt.Sprintf("%.1f KB", kb)
	} else if kb < 1024*1024 {
		return fmt.Sprintf("%.2f MB", kb/1024)
	}
	return fmt.Sprintf("%.2f GB", kb/(1024*1024))
}

func runPoll(interval int) {
	db, err := initDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	ok, size := checkDBSizeLimit()
	if !ok {
		fmt.Println(strings.Repeat("=", 70))
		fmt.Println("ERROR: DATABASE SIZE LIMIT EXCEEDED")
		fmt.Println(strings.Repeat("=", 70))
		fmt.Printf("Current size: %s\n", formatSize(size))
		fmt.Printf("Limit:        %s\n", formatSize(DBSizeLimitBytes))
		fmt.Println("\nPolling cannot continue. Please run 'purge' to clear the database:")
		fmt.Println("  deco purge")
		fmt.Println(strings.Repeat("=", 70))
		os.Exit(1)
	}

	config, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Starting bandwidth monitor (polling every %ds)\n", interval)
	fmt.Printf("Database: %s\n", dbPath)
	fmt.Printf("DB Size:  %s / %s limit\n", formatSize(size), formatSize(DBSizeLimitBytes))
	fmt.Println("Press Ctrl+C to stop")

	// Handle signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	running := true
	sampleCount := 0

	go func() {
		<-sigChan
		fmt.Println("\nStopping monitor...")
		running = false
	}()

	for running {
		// Check size limit
		ok, size := checkDBSizeLimit()
		if !ok {
			fmt.Println(strings.Repeat("=", 70))
			fmt.Println("ERROR: DATABASE SIZE LIMIT EXCEEDED")
			fmt.Println(strings.Repeat("=", 70))
			fmt.Printf("Current size: %s\n", formatSize(size))
			fmt.Printf("Limit:        %s\n", formatSize(DBSizeLimitBytes))
			fmt.Println("\nPolling stopped. Please run 'purge' to clear the database:")
			fmt.Println("  deco purge")
			fmt.Println(strings.Repeat("=", 70))
			return
		}

		start := time.Now()

		client := NewDecoClient(config.Host, config.Password)
		if err := client.Authorize(); err != nil {
			fmt.Printf("Error connecting: %v, retrying...\n", err)
			time.Sleep(time.Duration(interval) * time.Second)
			continue
		}

		data, err := client.GetClients()
		client.Logout()

		if err != nil {
			fmt.Printf("Error getting clients: %v, retrying...\n", err)
			time.Sleep(time.Duration(interval) * time.Second)
			continue
		}

		clients := data["clients"].([]map[string]interface{})
		timestamp := time.Now().Format(time.RFC3339)

		// Record samples
		for _, c := range clients {
			_, err := db.Exec(`
				INSERT INTO bandwidth_samples (timestamp, mac, name, ip, connection, device_type, download_kbps, upload_kbps)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			`, timestamp, c["mac"], c["name"], c["ip"], c["connection"], c["type"], c["download_kbps"], c["upload_kbps"])
			if err != nil {
				fmt.Printf("DB error: %v\n", err)
			}
		}

		sampleCount++

		// Show activity
		activeCount := 0
		totalDown := 0
		totalUp := 0
		for _, c := range clients {
			down := toInt(c["download_kbps"])
			up := toInt(c["upload_kbps"])
			if down > 0 || up > 0 {
				activeCount++
			}
			totalDown += down
			totalUp += up
		}

		ts := time.Now().Format("15:04:05")
		fmt.Printf("[%s] Sample #%d: %d clients, %d active, %d KB/s down %d KB/s up\n",
			ts, sampleCount, len(clients), activeCount, totalDown, totalUp)

		// Wait for next interval
		elapsed := time.Since(start)
		sleepTime := time.Duration(interval)*time.Second - elapsed
		if sleepTime > 0 && running {
			time.Sleep(sleepTime)
		}
	}

	fmt.Printf("Monitor stopped. Recorded %d samples.\n", sampleCount)
}

func runMonitor(interval int) {
	db, err := initDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error initializing database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	ok, size := checkDBSizeLimit()
	if !ok {
		fmt.Println(strings.Repeat("=", 70))
		fmt.Println("ERROR: DATABASE SIZE LIMIT EXCEEDED")
		fmt.Println(strings.Repeat("=", 70))
		fmt.Printf("Current size: %s\n", formatSize(size))
		fmt.Printf("Limit:        %s\n", formatSize(DBSizeLimitBytes))
		fmt.Println("\nMonitoring cannot continue. Please run 'purge' to clear the database:")
		fmt.Println("  deco purge")
		fmt.Println(strings.Repeat("=", 70))
		os.Exit(1)
	}

	config, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Starting full network monitor (polling every %ds)\n", interval)
	fmt.Printf("Database: %s\n", dbPath)
	fmt.Printf("DB Size:  %s / %s limit\n", formatSize(size), formatSize(DBSizeLimitBytes))
	fmt.Println("Press Ctrl+C to stop")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	running := true
	cycleCount := 0

	go func() {
		<-sigChan
		fmt.Println("\nStopping monitor...")
		running = false
	}()

	for running {
		ok, size := checkDBSizeLimit()
		if !ok {
			fmt.Println(strings.Repeat("=", 70))
			fmt.Println("ERROR: DATABASE SIZE LIMIT EXCEEDED")
			fmt.Println(strings.Repeat("=", 70))
			fmt.Printf("Current size: %s\n", formatSize(size))
			fmt.Printf("Limit:        %s\n", formatSize(DBSizeLimitBytes))
			fmt.Println("\nMonitoring stopped. Please run 'purge' to clear the database:")
			fmt.Println("  deco purge")
			fmt.Println(strings.Repeat("=", 70))
			return
		}

		start := time.Now()

		client := NewDecoClient(config.Host, config.Password)
		if err := client.Authorize(); err != nil {
			fmt.Printf("[%s] Error connecting: %v, retrying next cycle...\n", time.Now().Format("15:04:05"), err)
			time.Sleep(time.Duration(interval) * time.Second)
			continue
		}

		clientData, clientErr := client.GetClients()
		networkData, networkErr := client.GetNetwork()
		wirelessData, wirelessErr := client.GetWireless()
		meshData, meshErr := client.GetMesh()
		client.Logout()

		timestamp := time.Now().Format(time.RFC3339)
		cycleCount++

		// Insert bandwidth_samples (same as poll)
		clientCount := 0
		if clientErr == nil {
			if clients, ok := clientData["clients"].([]map[string]interface{}); ok {
				clientCount = len(clients)
				for _, c := range clients {
					db.Exec(`INSERT INTO bandwidth_samples (timestamp, mac, name, ip, connection, device_type, download_kbps, upload_kbps)
						VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
						timestamp, c["mac"], c["name"], c["ip"], c["connection"], c["type"], c["download_kbps"], c["upload_kbps"])
				}
			}
		} else {
			fmt.Printf("[%s] Error getting clients: %v\n", time.Now().Format("15:04:05"), clientErr)
		}

		// Insert network_snapshot
		var cpuPct, memPct interface{}
		if networkErr == nil {
			wan := getMap(networkData, "wan")
			lan := getMap(networkData, "lan")
			perf := getMap(networkData, "performance")

			cpuPct = perf["cpu_percent"]
			memPct = perf["mem_percent"]

			var dns1, dns2 interface{}
			if dnsSlice, ok := wan["dns"].([]interface{}); ok && len(dnsSlice) >= 2 {
				dns1 = dnsSlice[0]
				dns2 = dnsSlice[1]
			}

			db.Exec(`INSERT INTO network_snapshots (timestamp, wan_ip, wan_gateway, wan_dns1, wan_dns2, lan_ip, lan_netmask, cpu_percent, mem_percent)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				timestamp, wan["ip"], wan["gateway"], dns1, dns2, lan["ip"], lan["netmask"], cpuPct, memPct)
		} else {
			fmt.Printf("[%s] Error getting network: %v\n", time.Now().Format("15:04:05"), networkErr)
		}

		// Insert mesh_snapshots
		meshCount := 0
		if meshErr == nil {
			if devices, ok := meshData["devices"].([]map[string]interface{}); ok {
				meshCount = len(devices)
				for _, d := range devices {
					db.Exec(`INSERT INTO mesh_snapshots (timestamp, name, role, ip, mac, model, firmware, status)
						VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
						timestamp, d["name"], d["role"], d["ip"], d["mac"], d["model"], d["firmware"], d["status"])
				}
			}
		} else {
			fmt.Printf("[%s] Error getting mesh: %v\n", time.Now().Format("15:04:05"), meshErr)
		}

		// Insert wireless_snapshots
		if wirelessErr == nil {
			if bands, ok := wirelessData["bands"].(map[string]interface{}); ok {
				for bandName, b := range bands {
					if b == nil {
						continue
					}
					band := b.(map[string]interface{})
					host := getMap(band, "host")
					guest := getMap(band, "guest")

					hostEnabled := 0
					if e, ok := host["enabled"].(bool); ok && e {
						hostEnabled = 1
					}
					guestEnabled := 0
					if e, ok := guest["enabled"].(bool); ok && e {
						guestEnabled = 1
					}

					db.Exec(`INSERT INTO wireless_snapshots (timestamp, band, ssid, channel, channel_width, host_enabled, guest_enabled, guest_ssid)
						VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
						timestamp, bandName, host["ssid"], host["channel"], host["channel_width"], hostEnabled, guestEnabled, guest["ssid"])
				}
			}
		} else {
			fmt.Printf("[%s] Error getting wireless: %v\n", time.Now().Format("15:04:05"), wirelessErr)
		}

		// Print summary line
		cpuStr := "?"
		memStr := "?"
		if cpuPct != nil {
			cpuStr = fmt.Sprintf("%.0f%%", toFloat(cpuPct))
		}
		if memPct != nil {
			memStr = fmt.Sprintf("%.0f%%", toFloat(memPct))
		}

		ts := time.Now().Format("15:04:05")
		fmt.Printf("[%s] Cycle #%d: %d clients, CPU %s, Mem %s, %d mesh nodes\n",
			ts, cycleCount, clientCount, cpuStr, memStr, meshCount)

		elapsed := time.Since(start)
		sleepTime := time.Duration(interval)*time.Second - elapsed
		if sleepTime > 0 && running {
			time.Sleep(sleepTime)
		}
	}

	fmt.Printf("Monitor stopped. Completed %d cycles.\n", cycleCount)
}

func runReport(period string, jsonOut bool) {
	db, err := initDB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	var startTime time.Time
	var periodName string

	switch period {
	case "today":
		now := time.Now()
		startTime = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		periodName = "Today"
	case "hour":
		startTime = time.Now().Add(-1 * time.Hour)
		periodName = "Last hour"
	default:
		startTime = time.Time{}
		periodName = "All time"
	}

	query := `
		SELECT
			mac, name, ip, connection, device_type,
			COUNT(*) as sample_count,
			SUM(download_kbps) as total_download,
			SUM(upload_kbps) as total_upload,
			MAX(download_kbps) as max_download,
			MAX(upload_kbps) as max_upload
		FROM bandwidth_samples
		WHERE timestamp >= ?
		GROUP BY mac
		ORDER BY (total_download + total_upload) DESC
	`

	rows, err := db.Query(query, startTime.Format(time.RFC3339))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Query error: %v\n", err)
		os.Exit(1)
	}
	defer rows.Close()

	devices := []map[string]interface{}{}
	for rows.Next() {
		var mac, name, ip, connection, deviceType sql.NullString
		var sampleCount, totalDown, totalUp, maxDown, maxUp int64

		err := rows.Scan(&mac, &name, &ip, &connection, &deviceType,
			&sampleCount, &totalDown, &totalUp, &maxDown, &maxUp)
		if err != nil {
			continue
		}

		devices = append(devices, map[string]interface{}{
			"mac":            mac.String,
			"name":           name.String,
			"ip":             ip.String,
			"connection":     connection.String,
			"device_type":    deviceType.String,
			"sample_count":   sampleCount,
			"total_download": totalDown,
			"total_upload":   totalUp,
			"max_download":   maxDown,
			"max_upload":     maxUp,
		})
	}

	// Get total samples
	var totalSamples int64
	db.QueryRow("SELECT COUNT(DISTINCT timestamp) FROM bandwidth_samples WHERE timestamp >= ?",
		startTime.Format(time.RFC3339)).Scan(&totalSamples)

	report := map[string]interface{}{
		"period":           periodName,
		"start_time":       startTime.Format(time.RFC3339),
		"query_time":       time.Now().Format(time.RFC3339),
		"interval_seconds": 5,
		"total_samples":    totalSamples,
		"devices":          devices,
	}

	if jsonOut {
		// Add calculated totals for JSON
		for _, d := range devices {
			totalDown := d["total_download"].(int64)
			totalUp := d["total_upload"].(int64)
			d["download_kb"] = totalDown * 5
			d["upload_kb"] = totalUp * 5
			d["total_kb"] = (totalDown + totalUp) * 5
		}
		printJSON(report)
	} else {
		printReport(report)
	}
}

func runStatus() {
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		fmt.Println("No database found. Run 'poll' first to collect data.")
		return
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return
	}
	defer db.Close()

	var totalSamples, uniqueDevices int64
	var firstSample, lastSample sql.NullString

	db.QueryRow("SELECT COUNT(*) FROM bandwidth_samples").Scan(&totalSamples)
	db.QueryRow("SELECT COUNT(DISTINCT mac) FROM bandwidth_samples").Scan(&uniqueDevices)
	db.QueryRow("SELECT MIN(timestamp), MAX(timestamp) FROM bandwidth_samples").Scan(&firstSample, &lastSample)

	size := getDBSize()

	fmt.Printf("\nDatabase: %s\n", dbPath)
	fmt.Printf("Size: %.2f MB / %.0f GB limit\n", float64(size)/(1024*1024), float64(DBSizeLimitBytes)/(1024*1024*1024))
	fmt.Printf("Total samples: %d\n", totalSamples)
	fmt.Printf("Unique devices: %d\n", uniqueDevices)
	fmt.Printf("First sample: %s\n", firstSample.String)
	fmt.Printf("Last sample: %s\n", lastSample.String)
}

func runPurge(force bool) {
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		fmt.Println("No database found. Nothing to purge.")
		return
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return
	}

	var totalSamples int64
	db.QueryRow("SELECT COUNT(*) FROM bandwidth_samples").Scan(&totalSamples)

	if !force {
		fmt.Printf("\nWARNING: This will delete ALL %d records!\n", totalSamples)
		fmt.Printf("Database: %s\n", dbPath)
		fmt.Print("Type 'yes' to confirm: ")

		var confirm string
		fmt.Scanln(&confirm)
		if strings.ToLower(confirm) != "yes" {
			fmt.Println("Purge cancelled.")
			db.Close()
			return
		}
	}

	_, err = db.Exec("DELETE FROM bandwidth_samples")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error deleting records: %v\n", err)
		db.Close()
		return
	}

	db.Exec("VACUUM")
	db.Close()

	fmt.Printf("Purged %d records from database.\n", totalSamples)
	fmt.Printf("Database size after purge: %s\n", formatSize(getDBSize()))
}

// ==================== OUTPUT FORMATTERS ====================

func printJSON(data interface{}) {
	out, _ := json.MarshalIndent(data, "", "  ")
	fmt.Println(string(out))
}

func printClientsTable(data map[string]interface{}) {
	clients := data["clients"].([]map[string]interface{})

	fmt.Printf("\n%-25s %-16s %-18s %-14s %-12s %-8s %-8s\n",
		"NAME", "IP", "MAC", "CONNECTION", "TYPE", "DOWN", "UP")
	fmt.Println(strings.Repeat("-", 110))

	for _, c := range clients {
		name := fmt.Sprintf("%v", c["name"])
		if len(name) > 24 {
			name = name[:24]
		}

		down := "-"
		up := "-"
		if d := toInt(c["download_kbps"]); d > 0 {
			down = fmt.Sprintf("%dKB/s", d)
		}
		if u := toInt(c["upload_kbps"]); u > 0 {
			up = fmt.Sprintf("%dKB/s", u)
		}

		fmt.Printf("%-25s %-16v %-18v %-14v %-12v %-8s %-8s\n",
			name, c["ip"], c["mac"], c["connection"], c["type"], down, up)
	}

	fmt.Printf("\nTotal: %v clients\n", data["count"])
}

func printNetworkTable(data map[string]interface{}) {
	wan := data["wan"].(map[string]interface{})
	lan := data["lan"].(map[string]interface{})
	perf := data["performance"].(map[string]interface{})

	fmt.Println("\n=== WAN ===")
	fmt.Printf("  IP:      %v\n", wan["ip"])
	fmt.Printf("  Gateway: %v\n", wan["gateway"])
	fmt.Printf("  Netmask: %v\n", wan["netmask"])
	fmt.Printf("  MAC:     %v\n", wan["mac"])

	fmt.Println("\n=== LAN ===")
	fmt.Printf("  IP:      %v\n", lan["ip"])
	fmt.Printf("  Netmask: %v\n", lan["netmask"])
	fmt.Printf("  MAC:     %v\n", lan["mac"])

	fmt.Println("\n=== Performance ===")
	fmt.Printf("  CPU:    %v%%\n", perf["cpu_percent"])
	fmt.Printf("  Memory: %v%%\n", perf["mem_percent"])
}

func printWirelessTable(data map[string]interface{}) {
	bands := data["bands"].(map[string]interface{})

	fmt.Print("\n=== Wireless Networks ===\n\n")
	for bandName, b := range bands {
		if b == nil {
			continue
		}
		band := b.(map[string]interface{})
		host := band["host"].(map[string]interface{})
		guest := band["guest"].(map[string]interface{})

		status := "x"
		if enabled, ok := host["enabled"].(bool); ok && enabled {
			status = "o"
		}
		fmt.Printf("[%s] %s: %v\n", status, bandName, host["ssid"])
		fmt.Printf("    Channel: %v | Width: %v\n", host["channel"], host["channel_width"])

		if enabled, ok := guest["enabled"].(bool); ok && enabled {
			fmt.Printf("    Guest: %v\n", guest["ssid"])
		}
		fmt.Println()
	}
}

func printMeshTable(data map[string]interface{}) {
	devices := data["devices"].([]map[string]interface{})

	fmt.Printf("\n=== Mesh Devices (%v) ===\n\n", data["count"])
	for _, d := range devices {
		role := "  "
		if d["role"] == "master" {
			role = "* "
		}
		fmt.Printf("%s%v (%v)\n", role, d["name"], d["model"])
		fmt.Printf("    Role: %v | IP: %v | MAC: %v\n", d["role"], d["ip"], d["mac"])
		fmt.Printf("    Firmware: %v | Status: %v\n", d["firmware"], d["status"])
		fmt.Println()
	}
}

func printReport(report map[string]interface{}) {
	fmt.Println(strings.Repeat("=", 70))
	fmt.Printf("BANDWIDTH USAGE REPORT - %v\n", report["period"])
	fmt.Println(strings.Repeat("=", 70))
	fmt.Printf("From: %v\n", report["start_time"])
	fmt.Printf("To:   %v\n", report["query_time"])
	fmt.Printf("Samples: %v (every %vs)\n", report["total_samples"], report["interval_seconds"])

	devices := report["devices"].([]map[string]interface{})
	if len(devices) == 0 {
		fmt.Println("\nNo data recorded for this period.")
		return
	}

	interval := 5

	fmt.Printf("\n%-24s %-16s %-12s %-12s %-12s %-12s\n",
		"NAME", "IP", "CONNECTION", "DOWNLOAD", "UPLOAD", "TOTAL")
	fmt.Println(strings.Repeat("-", 90))

	var grandDown, grandUp int64

	for _, d := range devices {
		totalDown := d["total_download"].(int64) * int64(interval)
		totalUp := d["total_upload"].(int64) * int64(interval)
		total := totalDown + totalUp

		if total == 0 {
			continue
		}

		grandDown += totalDown
		grandUp += totalUp

		name := fmt.Sprintf("%v", d["name"])
		if len(name) > 23 {
			name = name[:23]
		}
		if name == "" {
			name = "Unknown"
		}

		fmt.Printf("%-24s %-16v %-12v %-12s %-12s %-12s\n",
			name, d["ip"], d["connection"],
			formatBytes(float64(totalDown)), formatBytes(float64(totalUp)), formatBytes(float64(total)))
	}

	fmt.Println(strings.Repeat("-", 90))
	fmt.Printf("%-24s %-16s %-12s %-12s %-12s %-12s\n",
		"TOTAL", "", "",
		formatBytes(float64(grandDown)), formatBytes(float64(grandUp)), formatBytes(float64(grandDown+grandUp)))
}

// ==================== HELPERS ====================

func getMap(data map[string]interface{}, key string) map[string]interface{} {
	if v, ok := data[key].(map[string]interface{}); ok {
		return v
	}
	return map[string]interface{}{}
}

func toInt(v interface{}) int {
	switch val := v.(type) {
	case int:
		return val
	case int64:
		return int(val)
	case float64:
		return int(val)
	default:
		return 0
	}
}

func toFloat(v interface{}) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case int:
		return float64(val)
	case int64:
		return float64(val)
	default:
		return 0
	}
}
