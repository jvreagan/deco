package main

import (
	"bytes"
	"context"
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
		fmt.Fprint(w, `{"models":[{"name":"llama3.2:latest","size":2000000000}]}`)
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
	response, err := streamOllamaChat(context.Background(), ts.URL, req, &buf)
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
	_, err := streamOllamaChat(context.Background(), ts.URL, req, &buf)
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
	_, err := streamOllamaChat(context.Background(), "http://127.0.0.1:1", req, &buf)
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
	response, err := streamOllamaChat(context.Background(), ts.URL, req, &buf)
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

	ctx, _ := gatherNetworkContext(false, nil)

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

	ctx, _ := gatherNetworkContext(false, nil)

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

	ctx, _ := gatherNetworkContext(false, nil)

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

	ctx, _ := gatherNetworkContext(false, nil)

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

	ctx, _ := gatherNetworkContext(false, nil)

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

	ctx, _ := gatherNetworkContext(false, nil)

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

	ctx, _ := gatherNetworkContext(false, nil)

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

	ctx, _ := gatherNetworkContext(false, nil)

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

	ctx, _ := gatherNetworkContext(false, nil)

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

	full, _ := gatherNetworkContext(false, nil)
	compact, _ := gatherNetworkContext(true, nil)

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
	err := runChat("llama3.2", "http://127.0.0.1:1", "test question", false, false)
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
			fmt.Fprint(w, `{"models":[{"name":"llama3.2:latest","size":2000000000}]}`)
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

	err := runChat("llama3.2", ts.URL, "how many devices?", false, false)

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
			fmt.Fprint(w, `{"models":[{"name":"llama3.2:latest","size":2000000000}]}`)
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

	err := runChat("llama3.2", "http://localhost:11434", "test", false, false)

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
			fmt.Fprint(w, `{"models":[{"name":"llama3.2:latest","size":2000000000}]}`)
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

	runChat("llama3.2", mockServer.URL, "test", false, false)

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
	// Redirect HOME to a temp dir so the test doesn't write to the real home
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	messages := []ollamaMessage{
		{Role: "user", Content: "test"},
	}

	err := saveConversation(messages, "")
	if err != nil {
		t.Fatalf("saveConversation with default path failed: %v", err)
	}

	// Verify the file was created in the temp dir
	pattern := fmt.Sprintf("deco-chat-%s-*.md", time.Now().Format("2006-01-02"))
	matches, _ := filepath.Glob(filepath.Join(tmpDir, pattern))
	if len(matches) == 0 {
		t.Error("expected generated file in temp home dir")
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
	ctx, _ := gatherNetworkContext(false, nil)

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
			fmt.Fprint(w, `{"models":[{"name":"llama3.2:latest","size":2000000000}]}`)
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

	err := runChat("llama3.2", mockServer.URL, "", false, false)

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
			fmt.Fprint(w, `{"models":[{"name":"llama3.2:latest","size":2000000000}]}`)
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

	err := runChat("llama3.2", mockServer.URL, "", false, false)

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

// ==================== LIST MODELS TESTS ====================

func TestListOllamaModels(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("expected /api/tags, got %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"models":[
			{"name":"llama3.2:latest","size":2019393189,"modified_at":"2025-12-01T10:30:00Z"},
			{"name":"mistral:latest","size":4109394944,"modified_at":"2025-11-15T08:00:00Z"}
		]}`)
	}))
	defer ts.Close()

	// Capture stdout
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := listOllamaModels(ts.URL)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	output := buf.String()

	if !strings.Contains(output, "llama3.2:latest") {
		t.Error("expected 'llama3.2:latest' in output")
	}
	if !strings.Contains(output, "mistral:latest") {
		t.Error("expected 'mistral:latest' in output")
	}
	if !strings.Contains(output, "NAME") {
		t.Error("expected header row")
	}
}

func TestListOllamaModelsEmpty(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"models":[]}`)
	}))
	defer ts.Close()

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := listOllamaModels(ts.URL)

	w.Close()
	os.Stdout = old

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(r)
	if !strings.Contains(buf.String(), "No models installed") {
		t.Error("expected 'No models installed' message")
	}
}

func TestListOllamaModelsUnreachable(t *testing.T) {
	err := listOllamaModels("http://127.0.0.1:1")
	if err == nil {
		t.Fatal("expected error for unreachable Ollama")
	}
	if !strings.Contains(err.Error(), "cannot reach Ollama") {
		t.Errorf("expected helpful error, got: %v", err)
	}
}

func TestChatCmdListModelsFlag(t *testing.T) {
	cmd := chatCmd()
	lm, err := cmd.Flags().GetBool("list-models")
	if err != nil {
		t.Fatalf("list-models flag error: %v", err)
	}
	if lm != false {
		t.Error("expected default list-models=false")
	}
}

// ==================== REFRESH DIFF TESTS ====================

func TestParseNetworkSnapshot(t *testing.T) {
	prompt := `You are a network assistant.

=== CONNECTED DEVICES (3) ===
NAME                      IP               MAC                CONNECTION     DOWN       UP
Jamess-MBP                192.168.68.50    8C-85-90-89-F5-DA  WiFi 5GHz      120KB/s    5KB/s
XBOX                      192.168.68.71    68-6C-E6-F2-75-5B  WiFi 5GHz      -          -
Printer                   192.168.68.74    90-CD-B6-54-18-89  Wired          -          -

=== NETWORK ===
WAN IP: 1.2.3.4 | Gateway: 1.2.3.1 | CPU: 15% | Memory: 42%
`

	snap := parseNetworkSnapshot(prompt)

	if snap.DeviceCount != 3 {
		t.Errorf("expected 3 devices, got %d", snap.DeviceCount)
	}
	if snap.WANIP != "1.2.3.4" {
		t.Errorf("expected WAN IP '1.2.3.4', got %q", snap.WANIP)
	}
	if len(snap.DeviceMACs) != 3 {
		t.Errorf("expected 3 MAC entries, got %d", len(snap.DeviceMACs))
	}
	if name, ok := snap.DeviceMACs["8C-85-90-89-F5-DA"]; !ok || name != "Jamess-MBP" {
		t.Errorf("expected Jamess-MBP for 8C-85-90-89-F5-DA, got %q (ok=%v)", name, ok)
	}
}

func TestDiffNetworkSnapshotsNewDevice(t *testing.T) {
	old := networkSnapshot{
		DeviceCount: 2,
		DeviceMACs: map[string]string{
			"AA-BB-CC-DD-EE-FF": "DeviceA",
			"11-22-33-44-55-66": "DeviceB",
		},
		WANIP: "1.2.3.4",
	}
	new := networkSnapshot{
		DeviceCount: 3,
		DeviceMACs: map[string]string{
			"AA-BB-CC-DD-EE-FF": "DeviceA",
			"11-22-33-44-55-66": "DeviceB",
			"77-88-99-AA-BB-CC": "NewDevice",
		},
		WANIP: "1.2.3.4",
	}

	diff := diffNetworkSnapshots(old, new)

	if !strings.Contains(diff, "2 → 3") {
		t.Errorf("expected device count change, got: %s", diff)
	}
	if !strings.Contains(diff, "+ NewDevice") {
		t.Errorf("expected new device indicator, got: %s", diff)
	}
}

func TestDiffNetworkSnapshotsDepartedDevice(t *testing.T) {
	old := networkSnapshot{
		DeviceCount: 2,
		DeviceMACs: map[string]string{
			"AA-BB-CC-DD-EE-FF": "DeviceA",
			"11-22-33-44-55-66": "DeviceB",
		},
	}
	new := networkSnapshot{
		DeviceCount: 1,
		DeviceMACs: map[string]string{
			"AA-BB-CC-DD-EE-FF": "DeviceA",
		},
	}

	diff := diffNetworkSnapshots(old, new)

	if !strings.Contains(diff, "2 → 1") {
		t.Errorf("expected device count change, got: %s", diff)
	}
	if !strings.Contains(diff, "- DeviceB") {
		t.Errorf("expected departed device indicator, got: %s", diff)
	}
}

func TestDiffNetworkSnapshotsWANChange(t *testing.T) {
	old := networkSnapshot{
		DeviceCount: 1,
		DeviceMACs:  map[string]string{"AA-BB-CC-DD-EE-FF": "A"},
		WANIP:      "1.2.3.4",
	}
	new := networkSnapshot{
		DeviceCount: 1,
		DeviceMACs:  map[string]string{"AA-BB-CC-DD-EE-FF": "A"},
		WANIP:      "5.6.7.8",
	}

	diff := diffNetworkSnapshots(old, new)

	if !strings.Contains(diff, "1.2.3.4 → 5.6.7.8") {
		t.Errorf("expected WAN IP change, got: %s", diff)
	}
}

func TestDiffNetworkSnapshotsNoChange(t *testing.T) {
	snap := networkSnapshot{
		DeviceCount: 2,
		DeviceMACs: map[string]string{
			"AA-BB-CC-DD-EE-FF": "A",
			"11-22-33-44-55-66": "B",
		},
		WANIP: "1.2.3.4",
	}

	diff := diffNetworkSnapshots(snap, snap)

	if !strings.Contains(diff, "no change") {
		t.Errorf("expected 'no change', got: %s", diff)
	}
}

// ==================== RESOLVE MODEL TESTS ====================

func TestResolveModelFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"models": []map[string]interface{}{
				{"name": "llama3.2:latest", "size": 2000000000},
				{"name": "mistral:latest", "size": 4000000000},
			},
		})
	}))
	defer ts.Close()

	model, err := resolveModel(ts.URL, "llama3.2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model != "llama3.2" {
		t.Errorf("expected llama3.2, got %s", model)
	}
}

func TestResolveModelNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"models": []map[string]interface{}{
				{"name": "mistral:latest", "size": 4000000000},
			},
		})
	}))
	defer ts.Close()

	model, err := resolveModel(ts.URL, "llama3.2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model != "mistral:latest" {
		t.Errorf("expected mistral:latest (auto-selected), got %s", model)
	}
}

func TestResolveModelNoModels(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"models": []map[string]interface{}{},
		})
	}))
	defer ts.Close()

	_, err := resolveModel(ts.URL, "llama3.2")
	if err == nil {
		t.Fatal("expected error for no models")
	}
	if !strings.Contains(err.Error(), "no models installed") {
		t.Errorf("expected 'no models installed', got: %v", err)
	}
}

func TestResolveModelUnreachable(t *testing.T) {
	// When Ollama is unreachable, should return the model unchanged (optimistic)
	model, err := resolveModel("http://127.0.0.1:1", "llama3.2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model != "llama3.2" {
		t.Errorf("expected llama3.2 (unchanged), got %s", model)
	}
}

// ==================== SHOW CONTEXT TESTS ====================

func TestRunChatShowContext(t *testing.T) {
	testEnv(t)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			fmt.Fprint(w, `{"models":[{"name":"llama3.2:latest","size":2000000000}]}`)
			return
		}
		t.Errorf("should not call /api/chat when show-context is true")
	}))
	defer ts.Close()

	// Capture stderr (show-context prints there)
	oldStderr := os.Stderr
	rErr, wErr, _ := os.Pipe()
	os.Stderr = wErr

	err := runChat("llama3.2", ts.URL, "test", false, true)

	wErr.Close()
	os.Stderr = oldStderr

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var buf bytes.Buffer
	buf.ReadFrom(rErr)
	output := buf.String()

	if !strings.Contains(output, "network assistant") {
		t.Error("expected system prompt in stderr output")
	}
}

