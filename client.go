package main

import (
	"crypto/md5"
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
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
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

func (dc *DecoClient) Reboot() error {
	_, err := dc.Request("admin/device?form=reboot", map[string]interface{}{
		"operation": "reboot",
	})
	return err
}

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
