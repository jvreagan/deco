package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ==================== OLLAMA CONNECTIVITY TESTS ====================

func TestCheckOllamaNotRunning(t *testing.T) {
	err := checkOllama("http://127.0.0.1:1") // port 1 should never be listening
	if err == nil {
		t.Fatal("expected error for unreachable Ollama")
	}
	if !strings.Contains(err.Error(), "cannot reach Ollama") {
		t.Errorf("expected helpful error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "ollama serve") {
		t.Errorf("expected hint about 'ollama serve', got: %v", err)
	}
}

func TestCheckOllamaRunning(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("expected /api/tags, got %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"models":[]}`)
	}))
	defer ts.Close()

	err := checkOllama(ts.URL)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
}

// ==================== STREAMING TESTS ====================

func TestStreamOllamaChat(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("expected /api/chat, got %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

		// Verify request body
		var req ollamaChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("failed to decode request: %v", err)
		}
		if req.Model != "testmodel" {
			t.Errorf("expected model 'testmodel', got %q", req.Model)
		}
		if !req.Stream {
			t.Error("expected stream: true")
		}

		// Return NDJSON chunks
		w.Header().Set("Content-Type", "application/x-ndjson")
		chunks := []ollamaChatChunk{
			{Done: false},
			{Done: false},
			{Done: true},
		}
		chunks[0].Message.Content = "Hello "
		chunks[1].Message.Content = "world!"

		for _, chunk := range chunks {
			data, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "%s\n", data)
		}
	}))
	defer ts.Close()

	req := ollamaChatRequest{
		Model: "testmodel",
		Messages: []ollamaMessage{
			{Role: "user", Content: "test"},
		},
		Stream: true,
	}

	var buf bytes.Buffer
	response, err := streamOllamaChat(ts.URL, req, &buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if response != "Hello world!" {
		t.Errorf("expected 'Hello world!', got %q", response)
	}
	if buf.String() != "Hello world!" {
		t.Errorf("expected streamed output 'Hello world!', got %q", buf.String())
	}
}

func TestStreamOllamaChatError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, "model not found")
	}))
	defer ts.Close()

	req := ollamaChatRequest{
		Model:    "nonexistent",
		Messages: []ollamaMessage{{Role: "user", Content: "test"}},
		Stream:   true,
	}

	var buf bytes.Buffer
	_, err := streamOllamaChat(ts.URL, req, &buf)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("expected status 500 in error, got: %v", err)
	}
}

func TestStreamOllamaChatConnectionRefused(t *testing.T) {
	req := ollamaChatRequest{
		Model:    "llama3.2",
		Messages: []ollamaMessage{{Role: "user", Content: "test"}},
		Stream:   true,
	}

	var buf bytes.Buffer
	_, err := streamOllamaChat("http://127.0.0.1:1", req, &buf)
	if err == nil {
		t.Fatal("expected error for connection refused")
	}
	if !strings.Contains(err.Error(), "ollama request failed") {
		t.Errorf("expected 'ollama request failed' in error, got: %v", err)
	}
}

func TestStreamOllamaChatEmptyChunks(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		// Chunks with empty content interspersed
		chunks := []ollamaChatChunk{
			{Done: false}, // empty content
			{Done: false},
			{Done: false}, // empty content
			{Done: true},
		}
		chunks[1].Message.Content = "only this"
		for _, chunk := range chunks {
			data, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "%s\n", data)
		}
	}))
	defer ts.Close()

	req := ollamaChatRequest{
		Model:    "test",
		Messages: []ollamaMessage{{Role: "user", Content: "test"}},
		Stream:   true,
	}

	var buf bytes.Buffer
	response, err := streamOllamaChat(ts.URL, req, &buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if response != "only this" {
		t.Errorf("expected 'only this', got %q", response)
	}
}

// ==================== CONTEXT GATHERING TESTS ====================

func TestGatherNetworkContextDBOnly(t *testing.T) {
	tmpDir := testEnv(t) // isolate config to temp dir
	_ = tmpDir

	// Init DB and insert some bandwidth data
	db, err := initDB()
	if err != nil {
		t.Fatalf("initDB failed: %v", err)
	}
	defer db.Close()

	// Insert a sample for "today"
	ts := time.Now().UTC().Format(time.RFC3339)
	_, err = db.Exec(`INSERT INTO bandwidth_samples
		(timestamp, mac, name, ip, connection, device_type, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		ts, "AA-BB-CC-DD-EE-FF", "TestDevice", "192.168.68.100", "WiFi 5GHz", "phone", 5000, 1000)
	if err != nil {
		t.Fatalf("insert failed: %v", err)
	}

	ctx := gatherNetworkContext(false)

	// Should contain the system prompt header
	if !strings.Contains(ctx, "network assistant") {
		t.Error("expected 'network assistant' in context")
	}
	// Should note live data is unavailable (no valid router config in temp dir)
	if !strings.Contains(ctx, "Live router data unavailable") {
		t.Error("expected 'Live router data unavailable' note")
	}
	// Should contain bandwidth data from DB
	if !strings.Contains(ctx, "TOP BANDWIDTH TODAY") {
		t.Error("expected 'TOP BANDWIDTH TODAY' section from DB data")
	}
	if !strings.Contains(ctx, "TestDevice") {
		t.Error("expected 'TestDevice' in bandwidth context")
	}
}

func TestGatherNetworkContextEmpty(t *testing.T) {
	testEnv(t) // isolate config — no DB data, no router

	ctx := gatherNetworkContext(false)

	// Should still return a valid prompt
	if !strings.Contains(ctx, "network assistant") {
		t.Error("expected 'network assistant' in context")
	}
	if !strings.Contains(ctx, "Live router data unavailable") {
		t.Error("expected 'Live router data unavailable' note")
	}
	// Should NOT contain bandwidth section (no data)
	if strings.Contains(ctx, "TOP BANDWIDTH TODAY") {
		t.Error("did not expect 'TOP BANDWIDTH TODAY' with empty DB")
	}
}

func TestGatherNetworkContextAliasSubstitution(t *testing.T) {
	testEnv(t)

	// Set up an alias
	aliases := map[string]string{
		"AA-BB-CC-DD-EE-FF": "Living Room TV",
	}
	saveAliases(aliases)

	// Insert DB data with that MAC
	db, err := initDB()
	if err != nil {
		t.Fatalf("initDB failed: %v", err)
	}
	defer db.Close()

	ts := time.Now().UTC().Format(time.RFC3339)
	_, err = db.Exec(`INSERT INTO bandwidth_samples
		(timestamp, mac, name, ip, connection, device_type, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		ts, "AA-BB-CC-DD-EE-FF", "OriginalName", "192.168.68.100", "WiFi 5GHz", "tv", 3000, 500)
	if err != nil {
		t.Fatalf("insert failed: %v", err)
	}

	ctx := gatherNetworkContext(false)

	// Alias should appear instead of original name
	if !strings.Contains(ctx, "Living Room TV") {
		t.Error("expected alias 'Living Room TV' in context")
	}
}

func TestGatherNetworkContextMultipleDevicesOrdered(t *testing.T) {
	testEnv(t)

	db, err := initDB()
	if err != nil {
		t.Fatalf("initDB failed: %v", err)
	}
	defer db.Close()

	ts := time.Now().UTC().Format(time.RFC3339)
	// Insert a low-bandwidth device
	_, _ = db.Exec(`INSERT INTO bandwidth_samples
		(timestamp, mac, name, ip, connection, device_type, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		ts, "11-22-33-44-55-66", "SmallDevice", "192.168.68.101", "Wired", "pc", 100, 50)
	// Insert a high-bandwidth device
	_, _ = db.Exec(`INSERT INTO bandwidth_samples
		(timestamp, mac, name, ip, connection, device_type, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		ts, "AA-BB-CC-DD-EE-FF", "BigDevice", "192.168.68.102", "WiFi 5GHz", "pc", 50000, 10000)

	ctx := gatherNetworkContext(false)

	if !strings.Contains(ctx, "TOP BANDWIDTH TODAY") {
		t.Fatal("expected bandwidth section")
	}
	// BigDevice should appear before SmallDevice (ordered by total bandwidth DESC)
	bigIdx := strings.Index(ctx, "BigDevice")
	smallIdx := strings.Index(ctx, "SmallDevice")
	if bigIdx < 0 || smallIdx < 0 {
		t.Fatalf("expected both devices in context, big=%d small=%d", bigIdx, smallIdx)
	}
	if bigIdx > smallIdx {
		t.Error("expected BigDevice listed before SmallDevice (higher bandwidth)")
	}
}

func TestGatherNetworkContextNoNameFallsBackToMAC(t *testing.T) {
	testEnv(t)

	db, err := initDB()
	if err != nil {
		t.Fatalf("initDB failed: %v", err)
	}
	defer db.Close()

	ts := time.Now().UTC().Format(time.RFC3339)
	// Insert device with empty name
	_, _ = db.Exec(`INSERT INTO bandwidth_samples
		(timestamp, mac, name, ip, connection, device_type, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		ts, "AA-BB-CC-DD-EE-FF", "", "192.168.68.100", "Wired", "unknown", 1000, 200)

	ctx := gatherNetworkContext(false)

	// Should fall back to MAC address as display name
	if !strings.Contains(ctx, "AA-BB-CC-DD-EE-FF") {
		t.Error("expected MAC address as fallback name for unnamed device")
	}
}

func TestGatherNetworkContextAliasesListed(t *testing.T) {
	testEnv(t)

	aliases := map[string]string{
		"AA-BB-CC-DD-EE-FF": "Living Room TV",
		"11-22-33-44-55-66": "Kitchen Speaker",
	}
	saveAliases(aliases)

	ctx := gatherNetworkContext(false)

	if !strings.Contains(ctx, "KNOWN DEVICES (aliases)") {
		t.Error("expected 'KNOWN DEVICES (aliases)' section")
	}
	if !strings.Contains(ctx, "Living Room TV") {
		t.Error("expected 'Living Room TV' alias listed")
	}
	if !strings.Contains(ctx, "Kitchen Speaker") {
		t.Error("expected 'Kitchen Speaker' alias listed")
	}
}

func TestGatherNetworkContextNetworkSnapshots(t *testing.T) {
	testEnv(t)

	db, err := initDB()
	if err != nil {
		t.Fatalf("initDB failed: %v", err)
	}
	defer db.Close()

	ts := time.Now().UTC().Format(time.RFC3339)
	_, _ = db.Exec(`INSERT INTO network_snapshots
		(timestamp, wan_ip, wan_gateway, wan_dns1, wan_dns2, cpu_percent, mem_percent)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ts, "1.2.3.4", "1.2.3.1", "8.8.8.8", "8.8.4.4", 15.0, 42.0)

	ctx := gatherNetworkContext(false)

	if !strings.Contains(ctx, "WAN IP HISTORY") {
		t.Error("expected 'WAN IP HISTORY' section")
	}
	if !strings.Contains(ctx, "1.2.3.4") {
		t.Error("expected WAN IP '1.2.3.4' in context")
	}
	if !strings.Contains(ctx, "Performance") {
		t.Error("expected Performance summary in context")
	}
}

func TestGatherNetworkContextMeshSnapshots(t *testing.T) {
	testEnv(t)

	db, err := initDB()
	if err != nil {
		t.Fatalf("initDB failed: %v", err)
	}
	defer db.Close()

	ts := time.Now().UTC().Format(time.RFC3339)
	_, _ = db.Exec(`INSERT INTO mesh_snapshots
		(timestamp, name, role, ip, mac, model, firmware, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		ts, "Main", "master", "192.168.68.1", "8C-90-2D-B5-5F-86", "BE63", "1.2.10", "online")
	_, _ = db.Exec(`INSERT INTO mesh_snapshots
		(timestamp, name, role, ip, mac, model, firmware, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		ts, "Deco_5F8C", "slave", "192.168.71.250", "8C-90-2D-B5-5F-8C", "BE63", "1.2.10", "online")

	ctx := gatherNetworkContext(false)

	if !strings.Contains(ctx, "MESH NODE UPTIME") {
		t.Error("expected 'MESH NODE UPTIME' section")
	}
	if !strings.Contains(ctx, "Main") {
		t.Error("expected mesh node 'Main' in context")
	}
	if !strings.Contains(ctx, "100.0%") {
		t.Error("expected 100% uptime for all-online nodes")
	}
}

func TestGatherNetworkContextAllKnownDevices(t *testing.T) {
	testEnv(t)

	db, err := initDB()
	if err != nil {
		t.Fatalf("initDB failed: %v", err)
	}
	defer db.Close()

	ts := time.Now().UTC().Format(time.RFC3339)
	// Insert two devices
	_, _ = db.Exec(`INSERT INTO bandwidth_samples
		(timestamp, mac, name, ip, connection, device_type, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		ts, "AA-BB-CC-DD-EE-FF", "DeviceA", "192.168.68.100", "Wired", "pc", 1000, 100)
	_, _ = db.Exec(`INSERT INTO bandwidth_samples
		(timestamp, mac, name, ip, connection, device_type, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		ts, "11-22-33-44-55-66", "DeviceB", "192.168.68.101", "WiFi 5GHz", "phone", 500, 50)

	ctx := gatherNetworkContext(false)

	if !strings.Contains(ctx, "ALL KNOWN DEVICES") {
		t.Error("expected 'ALL KNOWN DEVICES' section")
	}
	if !strings.Contains(ctx, "DeviceA") {
		t.Error("expected 'DeviceA' in known devices")
	}
	if !strings.Contains(ctx, "DeviceB") {
		t.Error("expected 'DeviceB' in known devices")
	}
}

func TestGatherNetworkContextCompactMode(t *testing.T) {
	testEnv(t)

	db, err := initDB()
	if err != nil {
		t.Fatalf("initDB failed: %v", err)
	}
	defer db.Close()

	ts := time.Now().UTC().Format(time.RFC3339)
	// Insert 30 devices — compact should show fewer
	for i := 0; i < 30; i++ {
		mac := fmt.Sprintf("AA-BB-CC-DD-%02X-%02X", i/256, i%256)
		name := fmt.Sprintf("Device%d", i)
		_, _ = db.Exec(`INSERT INTO bandwidth_samples
			(timestamp, mac, name, ip, connection, device_type, download_kbps, upload_kbps)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			ts, mac, name, "192.168.68.100", "Wired", "pc", (30-i)*100, (30-i)*10)
	}

	full := gatherNetworkContext(false)
	compact := gatherNetworkContext(true)

	// Compact context should be shorter than full
	if len(compact) >= len(full) {
		t.Errorf("expected compact (%d bytes) < full (%d bytes)", len(compact), len(full))
	}

	// Both should have the essential sections
	for _, ctx := range []string{full, compact} {
		if !strings.Contains(ctx, "TOP BANDWIDTH TODAY") {
			t.Error("expected bandwidth section")
		}
		if !strings.Contains(ctx, "ALL KNOWN DEVICES") {
			t.Error("expected known devices section")
		}
	}
}

// ==================== RUNCHAT TESTS ====================

func TestRunChatOllamaDown(t *testing.T) {
	err := runChat("llama3.2", "http://127.0.0.1:1", "test question", false)
	if err == nil {
		t.Fatal("expected error when Ollama is down")
	}
	if !strings.Contains(err.Error(), "cannot reach Ollama") {
		t.Errorf("expected helpful error message, got: %v", err)
	}
}

func TestRunChatSingleShot(t *testing.T) {
	testEnv(t) // isolate config

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"models":[]}`)
			return
		}
		if r.URL.Path == "/api/chat" {
			// Verify it's a POST with proper body
			var req ollamaChatRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("failed to decode request: %v", err)
			}
			// Should have system + user messages
			if len(req.Messages) < 2 {
				t.Errorf("expected at least 2 messages, got %d", len(req.Messages))
			}
			if req.Messages[0].Role != "system" {
				t.Errorf("expected system message first, got %q", req.Messages[0].Role)
			}
			if req.Messages[len(req.Messages)-1].Content != "how many devices?" {
				t.Errorf("expected user query in last message, got %q", req.Messages[len(req.Messages)-1].Content)
			}

			// Stream back a response
			w.Header().Set("Content-Type", "application/x-ndjson")
			chunks := []ollamaChatChunk{
				{Done: false},
				{Done: true},
			}
			chunks[0].Message.Content = "There are 5 devices connected."
			for _, chunk := range chunks {
				data, _ := json.Marshal(chunk)
				fmt.Fprintf(w, "%s\n", data)
			}
			return
		}
		t.Errorf("unexpected path: %s", r.URL.Path)
	}))
	defer ts.Close()

	// Capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runChat("llama3.2", ts.URL, "how many devices?", false)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, "There are 5 devices connected.") {
		t.Errorf("expected streamed response in output, got: %q", output)
	}
}

func TestRunChatOllamaHostEnvVar(t *testing.T) {
	testEnv(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"models":[]}`)
			return
		}
		if r.URL.Path == "/api/chat" {
			w.Header().Set("Content-Type", "application/x-ndjson")
			chunk := ollamaChatChunk{Done: true}
			chunk.Message.Content = "env var works"
			data, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "%s\n", data)
			return
		}
	}))
	defer ts.Close()

	// Set OLLAMA_HOST env var — should be used when ollamaURL is the default
	t.Setenv("OLLAMA_HOST", ts.URL)

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runChat("llama3.2", "http://localhost:11434", "test", false)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	if !strings.Contains(buf.String(), "env var works") {
		t.Errorf("expected OLLAMA_HOST to be used, got: %q", buf.String())
	}
}

func TestRunChatSystemPromptContainsContext(t *testing.T) {
	testEnv(t)

	// Insert some DB data so context is non-trivial
	db, err := initDB()
	if err != nil {
		t.Fatalf("initDB failed: %v", err)
	}
	ts := time.Now().UTC().Format(time.RFC3339)
	_, _ = db.Exec(`INSERT INTO bandwidth_samples
		(timestamp, mac, name, ip, connection, device_type, download_kbps, upload_kbps)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		ts, "AA-BB-CC-DD-EE-FF", "TestPC", "192.168.68.50", "Wired", "pc", 2000, 500)
	db.Close()

	var capturedSystem string
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"models":[]}`)
			return
		}
		if r.URL.Path == "/api/chat" {
			var req ollamaChatRequest
			json.NewDecoder(r.Body).Decode(&req)
			for _, m := range req.Messages {
				if m.Role == "system" {
					capturedSystem = m.Content
				}
			}
			w.Header().Set("Content-Type", "application/x-ndjson")
			chunk := ollamaChatChunk{Done: true}
			chunk.Message.Content = "ok"
			data, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "%s\n", data)
			return
		}
	}))
	defer mockServer.Close()

	old := os.Stdout
	_, w, _ := os.Pipe()
	os.Stdout = w

	runChat("llama3.2", mockServer.URL, "test", false)

	w.Close()
	os.Stdout = old

	if !strings.Contains(capturedSystem, "network assistant") {
		t.Error("system prompt missing 'network assistant' header")
	}
	if !strings.Contains(capturedSystem, "TestPC") {
		t.Error("system prompt should include DB bandwidth data")
	}
}

// ==================== COBRA WIRING TESTS ====================

func TestChatCmdDefaults(t *testing.T) {
	cmd := chatCmd()

	if cmd.Use != "chat [question]" {
		t.Errorf("unexpected Use: %q", cmd.Use)
	}

	model, err := cmd.Flags().GetString("model")
	if err != nil {
		t.Fatalf("model flag error: %v", err)
	}
	if model != "llama3.2" {
		t.Errorf("expected default model 'llama3.2', got %q", model)
	}

	url, err := cmd.Flags().GetString("ollama-url")
	if err != nil {
		t.Fatalf("ollama-url flag error: %v", err)
	}
	if url != "http://localhost:11434" {
		t.Errorf("expected default URL 'http://localhost:11434', got %q", url)
	}

	compact, err := cmd.Flags().GetBool("compact")
	if err != nil {
		t.Fatalf("compact flag error: %v", err)
	}
	if compact != false {
		t.Errorf("expected default compact=false, got %v", compact)
	}
}

func TestChatCmdAcceptsOptionalArg(t *testing.T) {
	cmd := chatCmd()

	// No args should be valid
	if err := cmd.Args(cmd, []string{}); err != nil {
		t.Errorf("expected no args to be valid: %v", err)
	}

	// One arg should be valid
	if err := cmd.Args(cmd, []string{"how many devices?"}); err != nil {
		t.Errorf("expected one arg to be valid: %v", err)
	}

	// Two args should fail
	if err := cmd.Args(cmd, []string{"a", "b"}); err == nil {
		t.Error("expected two args to be rejected")
	}
}

// ==================== SAVE CONVERSATION TESTS ====================

func TestSaveConversation(t *testing.T) {
	tmpDir := t.TempDir()
	path := fmt.Sprintf("%s/test-chat.md", tmpDir)

	messages := []ollamaMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "how many devices?"},
		{Role: "assistant", Content: "There are 5 devices."},
		{Role: "user", Content: "which one uses the most?"},
		{Role: "assistant", Content: "Jamess-MBP uses the most bandwidth."},
	}

	err := saveConversation(messages, path)
	if err != nil {
		t.Fatalf("saveConversation failed: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read saved file: %v", err)
	}
	content := string(data)

	// Should have header
	if !strings.Contains(content, "# Deco Chat") {
		t.Error("expected '# Deco Chat' header")
	}
	// Should skip system prompt
	if strings.Contains(content, "system prompt") {
		t.Error("system prompt should not appear in export")
	}
	// Should have user and assistant messages
	if !strings.Contains(content, "**You:** how many devices?") {
		t.Error("expected user message in export")
	}
	if !strings.Contains(content, "**Assistant:** There are 5 devices.") {
		t.Error("expected assistant message in export")
	}
	// Should have both turns
	if !strings.Contains(content, "Jamess-MBP") {
		t.Error("expected second turn in export")
	}
}

func TestSaveConversationDefaultFilename(t *testing.T) {
	// Save with empty path should generate a timestamped filename in home dir
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home dir")
	}

	messages := []ollamaMessage{
		{Role: "user", Content: "test"},
	}

	err = saveConversation(messages, "")
	if err != nil {
		t.Fatalf("saveConversation with default path failed: %v", err)
	}

	// Find and clean up the generated file
	pattern := fmt.Sprintf("deco-chat-%s-*.md", time.Now().Format("2006-01-02"))
	matches, _ := filepath.Glob(filepath.Join(home, pattern))
	for _, m := range matches {
		os.Remove(m)
	}
}

func TestSaveConversationEmptyMessages(t *testing.T) {
	tmpDir := t.TempDir()
	path := fmt.Sprintf("%s/empty-chat.md", tmpDir)

	// Only system message — export should be just the header
	messages := []ollamaMessage{
		{Role: "system", Content: "prompt"},
	}

	err := saveConversation(messages, path)
	if err != nil {
		t.Fatalf("saveConversation failed: %v", err)
	}

	data, _ := os.ReadFile(path)
	content := string(data)
	if !strings.Contains(content, "# Deco Chat") {
		t.Error("expected header even with no conversation")
	}
	if strings.Contains(content, "**You:**") {
		t.Error("expected no user messages")
	}
}

// ==================== ANTI-HALLUCINATION PROMPT TEST ====================

func TestGatherNetworkContextAntiHallucination(t *testing.T) {
	testEnv(t)
	ctx := gatherNetworkContext(false)

	if !strings.Contains(ctx, "Only state facts") {
		t.Error("expected anti-hallucination instruction in context")
	}
	if !strings.Contains(ctx, "Do not guess") {
		t.Error("expected 'Do not guess' instruction in context")
	}
}

// ==================== REPL MODE TESTS ====================

func TestRunChatREPLExit(t *testing.T) {
	testEnv(t)

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"models":[]}`)
			return
		}
	}))
	defer mockServer.Close()

	// Pipe "exit" to stdin
	oldStdin := os.Stdin
	r, w, _ := os.Pipe()
	w.WriteString("exit\n")
	w.Close()
	os.Stdin = r

	oldStdout := os.Stdout
	_, wOut, _ := os.Pipe()
	os.Stdout = wOut

	err := runChat("llama3.2", mockServer.URL, "", false)

	wOut.Close()
	os.Stdin = oldStdin
	os.Stdout = oldStdout

	if err != nil {
		t.Fatalf("REPL exit should not error: %v", err)
	}
}

func TestRunChatREPLSave(t *testing.T) {
	testEnv(t)
	tmpDir := t.TempDir()
	savePath := filepath.Join(tmpDir, "test-save.md")

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{"models":[]}`)
			return
		}
		if r.URL.Path == "/api/chat" {
			w.Header().Set("Content-Type", "application/x-ndjson")
			chunk := ollamaChatChunk{Done: true}
			chunk.Message.Content = "test response"
			data, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "%s\n", data)
			return
		}
	}))
	defer mockServer.Close()

	// Pipe commands: ask a question, save, exit
	oldStdin := os.Stdin
	r, w, _ := os.Pipe()
	w.WriteString("hello\nsave " + savePath + "\nexit\n")
	w.Close()
	os.Stdin = r

	oldStdout := os.Stdout
	_, wOut, _ := os.Pipe()
	os.Stdout = wOut

	err := runChat("llama3.2", mockServer.URL, "", false)

	wOut.Close()
	os.Stdin = oldStdin
	os.Stdout = oldStdout

	if err != nil {
		t.Fatalf("REPL with save should not error: %v", err)
	}

	// Verify the file was saved
	data, err := os.ReadFile(savePath)
	if err != nil {
		t.Fatalf("saved file should exist: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "**You:** hello") {
		t.Error("saved file should contain user message")
	}
	if !strings.Contains(content, "**Assistant:** test response") {
		t.Error("saved file should contain assistant response")
	}
}

