package decoclient

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// ==================== MOCK DECO SERVER ====================

type mockDecoServer struct {
	rsaKey   *rsa.PrivateKey
	password string
	stok     string
	seq      int
	aesKey   []byte
	aesIV    []byte
	mux      *http.ServeMux

	// Mock data controls
	clients  []map[string]interface{}
	meshDevs []map[string]interface{}
}

func newMockDecoServer(t *testing.T, password string) *mockDecoServer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}

	s := &mockDecoServer{
		rsaKey:   key,
		password: password,
		stok:     "test-stok-12345",
		seq:      100,
	}
	s.setupDefaultData()
	s.mux = http.NewServeMux()
	s.mux.HandleFunc("/", s.handleRequest)
	return s
}

func (s *mockDecoServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *mockDecoServer) setupDefaultData() {
	s.clients = []map[string]interface{}{
		{
			"name":            base64.StdEncoding.EncodeToString([]byte("TestPhone")),
			"ip":              "192.168.68.100",
			"mac":             "AA-BB-CC-DD-EE-FF",
			"online":          true,
			"connection_type": "band5",
			"client_type":     "phone",
			"down_speed":      float64(1500),
			"up_speed":        float64(300),
		},
		{
			"name":            base64.StdEncoding.EncodeToString([]byte("TestPC")),
			"ip":              "192.168.68.101",
			"mac":             "11-22-33-44-55-66",
			"online":          true,
			"connection_type": "wired",
			"client_type":     "computer",
			"down_speed":      float64(0),
			"up_speed":        float64(0),
		},
		{
			"name":            base64.StdEncoding.EncodeToString([]byte("OfflineDevice")),
			"ip":              "192.168.68.102",
			"mac":             "77-88-99-AA-BB-CC",
			"online":          false,
			"connection_type": "band2_4",
			"client_type":     "phone",
			"down_speed":      float64(0),
			"up_speed":        float64(0),
		},
	}

	s.meshDevs = []map[string]interface{}{
		{
			"custom_nickname": "",
			"nickname":        "Main",
			"device_model":    "Deco BE63",
			"role":            "master",
			"device_ip":       "192.168.68.1",
			"mac":             "8C-90-2D-B5-5F-86",
			"software_ver":    "1.2.10",
			"inet_status":     "online",
		},
	}
}

func (s *mockDecoServer) handleRequest(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	query := r.URL.RawQuery

	if strings.Contains(path, "/login") || strings.Contains(query, "form=keys") || strings.Contains(query, "form=auth") || strings.Contains(query, "form=login") {
		form := r.URL.Query().Get("form")
		if form == "" {
			if strings.Contains(query, "form=keys") {
				form = "keys"
			} else if strings.Contains(query, "form=auth") {
				form = "auth"
			} else if strings.Contains(query, "form=login") {
				form = "login"
			}
		}
		switch form {
		case "keys":
			s.handleKeys(w)
		case "auth":
			s.handleAuth(w)
		case "login":
			s.handleLogin(w, r)
		default:
			http.Error(w, "unknown login form", 400)
		}
		return
	}

	s.handleAPI(w, r, path, query)
}

func (s *mockDecoServer) handleKeys(w http.ResponseWriter) {
	nHex := hex.EncodeToString(s.rsaKey.PublicKey.N.Bytes())
	eHex := fmt.Sprintf("%x", s.rsaKey.PublicKey.E)

	resp := map[string]interface{}{
		"result": map[string]interface{}{
			"password": []string{nHex, eHex},
		},
		"error_code": 0,
	}
	json.NewEncoder(w).Encode(resp)
}

func (s *mockDecoServer) handleAuth(w http.ResponseWriter) {
	nHex := hex.EncodeToString(s.rsaKey.PublicKey.N.Bytes())
	eHex := fmt.Sprintf("%x", s.rsaKey.PublicKey.E)

	resp := map[string]interface{}{
		"result": map[string]interface{}{
			"key": []string{nHex, eHex},
			"seq": s.seq,
		},
		"error_code": 0,
	}
	json.NewEncoder(w).Encode(resp)
}

func (s *mockDecoServer) handleLogin(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	bodyStr := string(body)

	parts := strings.SplitN(bodyStr, "&data=", 2)
	if len(parts) < 2 {
		http.Error(w, "bad login body", 400)
		return
	}

	signHex := strings.TrimPrefix(parts[0], "sign=")

	signPlain, err := rsaDecryptChunked(signHex, s.rsaKey)
	if err != nil {
		http.Error(w, fmt.Sprintf("signature decrypt failed: %v", err), 500)
		return
	}

	params := parseSignatureParams(signPlain)
	s.aesKey = []byte(params["k"])
	s.aesIV = []byte(params["i"])

	respData := map[string]interface{}{
		"result":     map[string]interface{}{"stok": s.stok},
		"error_code": 0,
	}
	respJSON, _ := json.Marshal(respData)
	encResp, _ := aesEncrypt(respJSON, s.aesKey, s.aesIV)

	http.SetCookie(w, &http.Cookie{Name: "sysauth", Value: "test-sysauth"})

	resp := map[string]interface{}{
		"data":       encResp,
		"error_code": 0,
	}
	json.NewEncoder(w).Encode(resp)
}

func (s *mockDecoServer) handleAPI(w http.ResponseWriter, r *http.Request, path, query string) {
	if s.aesKey == nil {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}

	body, _ := io.ReadAll(r.Body)
	bodyStr := string(body)

	parts := strings.SplitN(bodyStr, "&data=", 2)
	if len(parts) < 2 {
		http.Error(w, "bad API body", 400)
		return
	}

	encData, _ := url.QueryUnescape(parts[1])
	_, err := aesDecrypt(encData, s.aesKey, s.aesIV)
	if err != nil {
		http.Error(w, fmt.Sprintf("decrypt failed: %v", err), 500)
		return
	}

	var respResult interface{}

	switch {
	case strings.Contains(query, "form=client_list") || strings.Contains(path, "client") && strings.Contains(query, "form=client_list"):
		respResult = map[string]interface{}{
			"client_list": s.clients,
		}
	case strings.Contains(query, "form=wan_ipv4") || strings.Contains(path, "network") && strings.Contains(query, "form=wan_ipv4"):
		respResult = s.mockNetwork()
	case strings.Contains(query, "form=performance"):
		respResult = map[string]interface{}{
			"cpu_usage": float64(15),
			"mem_usage": float64(42),
		}
	case strings.Contains(query, "form=wlan"):
		respResult = s.mockWireless()
	case strings.Contains(query, "form=device_list"):
		respResult = map[string]interface{}{
			"device_list": s.meshDevs,
		}
	case strings.Contains(query, "form=reboot"):
		respResult = map[string]interface{}{}
	case strings.Contains(query, "form=block"):
		respResult = map[string]interface{}{}
	case strings.Contains(query, "form=logout"):
		respResult = map[string]interface{}{}
	default:
		respResult = map[string]interface{}{}
	}

	respPayload := map[string]interface{}{
		"result":     respResult,
		"error_code": 0,
	}
	respJSON, _ := json.Marshal(respPayload)
	encResp, _ := aesEncrypt(respJSON, s.aesKey, s.aesIV)

	resp := map[string]interface{}{
		"data":       encResp,
		"error_code": 0,
	}
	json.NewEncoder(w).Encode(resp)
}

func (s *mockDecoServer) mockNetwork() map[string]interface{} {
	return map[string]interface{}{
		"wan": map[string]interface{}{
			"ip_info": map[string]interface{}{
				"ip":      "1.2.3.4",
				"gateway": "1.2.3.1",
				"mask":    "255.255.255.0",
				"mac":     "AA-BB-CC-DD-EE-00",
				"dns1":    "8.8.8.8",
				"dns2":    "8.8.4.4",
			},
		},
		"lan": map[string]interface{}{
			"ip_info": map[string]interface{}{
				"ip":   "192.168.68.1",
				"mask": "255.255.255.0",
				"mac":  "AA-BB-CC-DD-EE-01",
			},
		},
	}
}

func (s *mockDecoServer) mockWireless() map[string]interface{} {
	return map[string]interface{}{
		"band5_1": map[string]interface{}{
			"host": map[string]interface{}{
				"enable":        true,
				"ssid":          base64.StdEncoding.EncodeToString([]byte("TestNetwork")),
				"channel":       "149",
				"channel_width": "80MHz",
			},
			"guest": map[string]interface{}{
				"enable": false,
				"ssid":   base64.StdEncoding.EncodeToString([]byte("TestGuest")),
			},
		},
	}
}

// ==================== RSA HELPERS FOR MOCK ====================

func rsaDecryptChunked(hexData string, key *rsa.PrivateKey) (string, error) {
	data, err := hex.DecodeString(hexData)
	if err != nil {
		return "", fmt.Errorf("hex decode failed: %v", err)
	}

	modSize := key.Size()
	var result []byte

	for i := 0; i < len(data); i += modSize {
		end := i + modSize
		if end > len(data) {
			end = len(data)
		}
		block := data[i:end]
		decrypted, err := rsa.DecryptPKCS1v15(rand.Reader, key, block)
		if err != nil {
			return "", fmt.Errorf("RSA decrypt block at offset %d failed: %v", i, err)
		}
		result = append(result, decrypted...)
	}

	return string(result), nil
}

func parseSignatureParams(sig string) map[string]string {
	params := map[string]string{}
	for _, part := range strings.Split(sig, "&") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) == 2 {
			params[kv[0]] = kv[1]
		}
	}
	return params
}

// ==================== INTEGRATION TESTS ====================

func setupMockClient(t *testing.T, password string) (*DecoClient, *mockDecoServer, *httptest.Server) {
	t.Helper()
	mock := newMockDecoServer(t, password)
	server := httptest.NewServer(mock)
	t.Cleanup(server.Close)

	host := strings.TrimPrefix(server.URL, "http://")
	client := NewDecoClient(host, password)

	return client, mock, server
}

func TestAuthorize(t *testing.T) {
	client, _, _ := setupMockClient(t, "testpass")

	if err := client.Authorize(); err != nil {
		t.Fatalf("Authorize failed: %v", err)
	}

	if !client.logged {
		t.Error("client should be logged in")
	}
	if client.stok == "" {
		t.Error("stok should be set")
	}
	if client.stok != "test-stok-12345" {
		t.Errorf("stok = %q, want %q", client.stok, "test-stok-12345")
	}
}

func TestGetClients(t *testing.T) {
	client, _, _ := setupMockClient(t, "testpass")

	if err := client.Authorize(); err != nil {
		t.Fatalf("Authorize failed: %v", err)
	}

	data, err := client.GetClients(context.Background())
	if err != nil {
		t.Fatalf("GetClients failed: %v", err)
	}

	if data.Count != 2 {
		t.Errorf("Count = %d, want 2", data.Count)
	}

	if len(data.Clients) != 2 {
		t.Fatalf("len(Clients) = %d, want 2", len(data.Clients))
	}

	c := data.Clients[0]
	if c.Name != "TestPhone" {
		t.Errorf("Name = %q, want %q", c.Name, "TestPhone")
	}
	if c.IP != "192.168.68.100" {
		t.Errorf("IP = %q, want %q", c.IP, "192.168.68.100")
	}
	if c.MAC != "AA-BB-CC-DD-EE-FF" {
		t.Errorf("MAC = %q, want %q", c.MAC, "AA-BB-CC-DD-EE-FF")
	}
	if c.Connection != "WiFi 5GHz" {
		t.Errorf("Connection = %q, want %q", c.Connection, "WiFi 5GHz")
	}
	if c.DownloadKbps != 1500 {
		t.Errorf("DownloadKbps = %d, want 1500", c.DownloadKbps)
	}
	if c.UploadKbps != 300 {
		t.Errorf("UploadKbps = %d, want 300", c.UploadKbps)
	}

	c2 := data.Clients[1]
	if c2.Name != "TestPC" {
		t.Errorf("Name = %q, want %q", c2.Name, "TestPC")
	}
	if c2.Connection != "Wired" {
		t.Errorf("Connection = %q, want %q", c2.Connection, "Wired")
	}
}

func TestGetNetwork(t *testing.T) {
	client, _, _ := setupMockClient(t, "testpass")
	if err := client.Authorize(); err != nil {
		t.Fatalf("Authorize failed: %v", err)
	}

	data, err := client.GetNetwork(context.Background())
	if err != nil {
		t.Fatalf("GetNetwork failed: %v", err)
	}

	if data.WAN.IP != "1.2.3.4" {
		t.Errorf("WAN.IP = %q, want %q", data.WAN.IP, "1.2.3.4")
	}
	if data.WAN.Gateway != "1.2.3.1" {
		t.Errorf("WAN.Gateway = %q, want %q", data.WAN.Gateway, "1.2.3.1")
	}
	if data.LAN.IP != "192.168.68.1" {
		t.Errorf("LAN.IP = %q, want %q", data.LAN.IP, "192.168.68.1")
	}
	if data.Performance.CPUPercent == nil {
		t.Error("CPUPercent should not be nil")
	} else if *data.Performance.CPUPercent != 15 {
		t.Errorf("CPUPercent = %f, want 15", *data.Performance.CPUPercent)
	}
	if data.Performance.MemPercent == nil {
		t.Error("MemPercent should not be nil")
	} else if *data.Performance.MemPercent != 42 {
		t.Errorf("MemPercent = %f, want 42", *data.Performance.MemPercent)
	}
	if len(data.WAN.DNS) != 2 || data.WAN.DNS[0] != "8.8.8.8" || data.WAN.DNS[1] != "8.8.4.4" {
		t.Errorf("DNS = %v, want [8.8.8.8, 8.8.4.4]", data.WAN.DNS)
	}
}

func TestGetWireless(t *testing.T) {
	client, _, _ := setupMockClient(t, "testpass")
	if err := client.Authorize(); err != nil {
		t.Fatalf("Authorize failed: %v", err)
	}

	data, err := client.GetWireless(context.Background())
	if err != nil {
		t.Fatalf("GetWireless failed: %v", err)
	}

	band, ok := data.Bands["5GHz"]
	if !ok {
		t.Fatal("5GHz band not found")
	}
	if band.Host.SSID != "TestNetwork" {
		t.Errorf("SSID = %q, want %q", band.Host.SSID, "TestNetwork")
	}
	if !band.Host.Enabled {
		t.Error("Host should be enabled")
	}
	if band.Host.Channel != "149" {
		t.Errorf("Channel = %q, want %q", band.Host.Channel, "149")
	}
	if band.Guest.Enabled {
		t.Error("Guest should not be enabled")
	}
}

func TestGetMesh(t *testing.T) {
	client, _, _ := setupMockClient(t, "testpass")
	if err := client.Authorize(); err != nil {
		t.Fatalf("Authorize failed: %v", err)
	}

	data, err := client.GetMesh(context.Background())
	if err != nil {
		t.Fatalf("GetMesh failed: %v", err)
	}

	if data.Count != 1 {
		t.Errorf("Count = %d, want 1", data.Count)
	}
	if len(data.Devices) != 1 {
		t.Fatalf("len(Devices) = %d, want 1", len(data.Devices))
	}

	d := data.Devices[0]
	if d.Name != "Main" {
		t.Errorf("Name = %q, want %q", d.Name, "Main")
	}
	if d.Role != "master" {
		t.Errorf("Role = %q, want %q", d.Role, "master")
	}
	if d.Model != "Deco BE63" {
		t.Errorf("Model = %q, want %q", d.Model, "Deco BE63")
	}
}

func TestReboot(t *testing.T) {
	client, _, _ := setupMockClient(t, "testpass")
	if err := client.Authorize(); err != nil {
		t.Fatalf("Authorize failed: %v", err)
	}

	if err := client.Reboot(context.Background()); err != nil {
		t.Errorf("Reboot failed: %v", err)
	}
}

func TestBlockUnblock(t *testing.T) {
	client, _, _ := setupMockClient(t, "testpass")
	if err := client.Authorize(); err != nil {
		t.Fatalf("Authorize failed: %v", err)
	}

	t.Run("block", func(t *testing.T) {
		if err := client.BlockClient(context.Background(), "AA-BB-CC-DD-EE-FF"); err != nil {
			t.Errorf("BlockClient failed: %v", err)
		}
	})

	t.Run("unblock", func(t *testing.T) {
		if err := client.UnblockClient(context.Background(), "AA-BB-CC-DD-EE-FF"); err != nil {
			t.Errorf("UnblockClient failed: %v", err)
		}
	})
}

func TestLogout(t *testing.T) {
	client, _, _ := setupMockClient(t, "testpass")
	if err := client.Authorize(); err != nil {
		t.Fatalf("Authorize failed: %v", err)
	}

	client.Logout()

	if client.logged {
		t.Error("client should not be logged in after Logout")
	}
}

func TestRequestNotLoggedIn(t *testing.T) {
	client, _, _ := setupMockClient(t, "testpass")

	_, err := client.Request(context.Background(), "admin/client?form=client_list", map[string]interface{}{
		"operation": "read",
	})
	if err == nil {
		t.Error("Request should fail when not logged in")
	}
	if !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("error = %q, want 'not logged in'", err.Error())
	}
}

func TestEnsureAuthorized(t *testing.T) {
	client, _, _ := setupMockClient(t, "testpass")

	if err := client.EnsureAuthorized(context.Background()); err != nil {
		t.Fatalf("first EnsureAuthorized failed: %v", err)
	}
	if !client.logged {
		t.Error("should be logged in")
	}

	stok := client.stok
	if err := client.EnsureAuthorized(context.Background()); err != nil {
		t.Fatalf("second EnsureAuthorized failed: %v", err)
	}
	if client.stok != stok {
		t.Error("stok should not change on re-call when already logged in")
	}
}

func TestInvalidateAndReauth(t *testing.T) {
	client, _, _ := setupMockClient(t, "testpass")

	if err := client.Authorize(); err != nil {
		t.Fatalf("Authorize failed: %v", err)
	}

	client.Invalidate()
	if client.logged {
		t.Error("should not be logged in after Invalidate")
	}

	if err := client.EnsureAuthorized(context.Background()); err != nil {
		t.Fatalf("EnsureAuthorized after Invalidate failed: %v", err)
	}
	if !client.logged {
		t.Error("should be logged in after EnsureAuthorized")
	}
}

func TestValidateConfig(t *testing.T) {
	// Valid Deco-like server returns JSON.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"result":{},"error_code":0}`))
	}))
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	config := &Config{Host: host, Password: "test"}

	if err := ValidateConfig(config); err != nil {
		t.Errorf("ValidateConfig should succeed for Deco-like server: %v", err)
	}

	// Non-Deco server returns HTML.
	htmlServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><body>Not a Deco</body></html>"))
	}))
	defer htmlServer.Close()

	htmlHost := strings.TrimPrefix(htmlServer.URL, "http://")
	config3 := &Config{Host: htmlHost, Password: "test"}
	if err := ValidateConfig(config3); err == nil {
		t.Error("ValidateConfig should fail for non-Deco server")
	}

	// Unreachable host.
	config2 := &Config{Host: "192.0.2.1:1", Password: "test"}
	if err := ValidateConfig(config2); err == nil {
		t.Error("ValidateConfig should fail for unreachable host")
	}
}

func TestProactiveSessionRefresh(t *testing.T) {
	client, _, _ := setupMockClient(t, "testpass")

	if err := client.Authorize(); err != nil {
		t.Fatalf("Authorize failed: %v", err)
	}

	if !client.logged {
		t.Fatal("should be logged in")
	}

	client.lastAuthTime = time.Now().Add(-6 * time.Minute)

	if err := client.EnsureAuthorized(context.Background()); err != nil {
		t.Fatalf("EnsureAuthorized failed: %v", err)
	}

	if !client.logged {
		t.Error("should be logged in after proactive refresh")
	}

	if time.Since(client.lastAuthTime) > 5*time.Second {
		t.Error("lastAuthTime should have been refreshed")
	}
}

func TestSessionExpiryRetry(t *testing.T) {
	mock := newMockDecoServer(t, "testpass")
	callCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		query := r.URL.RawQuery

		if strings.Contains(path, "/login") || strings.Contains(query, "form=keys") ||
			strings.Contains(query, "form=auth") || strings.Contains(query, "form=login") {
			mock.ServeHTTP(w, r)
			return
		}

		callCount++
		if callCount == 1 {
			if mock.aesKey == nil {
				http.Error(w, "not authenticated", http.StatusUnauthorized)
				return
			}

			io.ReadAll(r.Body)

			respPayload := map[string]interface{}{
				"error_code": -40401,
			}
			respJSON, _ := json.Marshal(respPayload)
			encResp, _ := aesEncrypt(respJSON, mock.aesKey, mock.aesIV)

			resp := map[string]interface{}{
				"data":       encResp,
				"error_code": 0,
			}
			json.NewEncoder(w).Encode(resp)
			return
		}

		mock.ServeHTTP(w, r)
	}))
	defer server.Close()

	host := strings.TrimPrefix(server.URL, "http://")
	client := NewDecoClient(host, "testpass")

	if err := client.Authorize(); err != nil {
		t.Fatalf("Authorize failed: %v", err)
	}

	data, err := client.GetClients(context.Background())
	if err != nil {
		t.Fatalf("GetClients should succeed after retry: %v", err)
	}

	if data.Count != 2 {
		t.Errorf("Count = %d, want 2", data.Count)
	}

	if callCount < 2 {
		t.Errorf("expected at least 2 API calls (1 failed + 1 retry), got %d", callCount)
	}
}

// ==================== HELPER TESTS ====================

func TestToString(t *testing.T) {
	tests := []struct {
		name string
		in   interface{}
		want string
	}{
		{"string", "hello", "hello"},
		{"nil", nil, ""},
		{"int", 42, "42"},
		{"float", 3.14, "3.14"},
		{"bool", true, "true"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToString(tt.in)
			if got != tt.want {
				t.Errorf("ToString(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestToBool(t *testing.T) {
	tests := []struct {
		name string
		in   interface{}
		want bool
	}{
		{"true", true, true},
		{"false", false, false},
		{"nil", nil, false},
		{"string", "true", false},
		{"int", 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToBool(tt.in)
			if got != tt.want {
				t.Errorf("ToBool(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestClientListJSON(t *testing.T) {
	data := &ClientList{
		Clients: []ClientInfo{
			{Name: "Test", IP: "192.168.1.1", MAC: "AA-BB-CC-DD-EE-FF", Connection: "Wired", Type: "phone", DownloadKbps: 100, UploadKbps: 50},
		},
		Count: 1,
	}

	out, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("JSON marshal failed: %v", err)
	}

	var result map[string]interface{}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("JSON unmarshal failed: %v", err)
	}

	if result["count"].(float64) != 1 {
		t.Error("JSON should contain count=1")
	}

	clients := result["clients"].([]interface{})
	if len(clients) != 1 {
		t.Fatal("JSON should contain 1 client")
	}

	client := clients[0].(map[string]interface{})
	if client["name"] != "Test" {
		t.Errorf("JSON name = %v, want Test", client["name"])
	}
	if client["download_kbps"].(float64) != 100 {
		t.Errorf("JSON download_kbps = %v, want 100", client["download_kbps"])
	}
}
