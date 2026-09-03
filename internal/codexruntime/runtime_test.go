package codexruntime

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_CODEX_HELPER") != "1" {
		return
	}
	mode := os.Getenv("CODEX_HELPER_MODE")
	if mode == "malformed" {
		fmt.Println("{not-json")
		os.Exit(0)
	}
	if mode == "oversized" {
		fmt.Println(strings.Repeat("x", 2048))
		os.Exit(0)
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil || request.ID == nil {
			continue
		}
		switch request.Method {
		case "initialize":
			_ = encoder.Encode(map[string]any{"id": *request.ID, "result": map[string]any{"home": os.Getenv("CODEX_HOME")}})
		case "test/home":
			_ = encoder.Encode(map[string]any{"id": *request.ID, "result": map[string]any{"home": os.Getenv("CODEX_HOME"), "openaiKey": os.Getenv("OPENAI_API_KEY"), "accessToken": os.Getenv("CODEX_ACCESS_TOKEN")}})
		case "account/read":
			_ = encoder.Encode(map[string]any{"id": *request.ID, "result": map[string]any{"account": map[string]any{"type": "chatgpt", "email": "person@example.com", "planType": "plus"}, "requiresOpenaiAuth": true}})
		case "account/login/start":
			_ = encoder.Encode(map[string]any{"id": *request.ID, "result": map[string]any{"type": "chatgptDeviceCode", "loginId": "login-1", "verificationUrl": "https://auth.openai.test/device", "userCode": "TEST-CODE"}})
		case "account/login/cancel":
			_ = encoder.Encode(map[string]any{"id": *request.ID, "result": map[string]any{"status": "canceled"}})
		case "account/logout":
			_ = encoder.Encode(map[string]any{"id": *request.ID, "result": map[string]any{}})
		case "thread/start":
			_ = encoder.Encode(map[string]any{"id": *request.ID, "result": map[string]any{"thread": map[string]any{"id": "thread-1"}}})
		case "turn/start":
			_ = encoder.Encode(map[string]any{"id": *request.ID, "result": map[string]any{"turn": map[string]any{"id": "turn-1"}}})
			if mode != "hang" {
				_ = encoder.Encode(map[string]any{"method": "item/agentMessage/delta", "params": map[string]any{"delta": "draft"}})
				_ = encoder.Encode(map[string]any{"method": "item/completed", "params": map[string]any{"item": map[string]any{"type": "agentMessage", "text": "final draft"}}})
				_ = encoder.Encode(map[string]any{"method": "turn/completed", "params": map[string]any{"turn": map[string]any{"id": "turn-1", "status": "completed", "error": nil}}})
			}
		case "turn/interrupt":
			_ = encoder.Encode(map[string]any{"id": *request.ID, "result": map[string]any{}})
		default:
			_ = encoder.Encode(map[string]any{"id": *request.ID, "error": map[string]any{"code": -32601, "message": "unknown"}})
		}
	}
	os.Exit(0)
}

func helperOptions(t *testing.T, mode string) Options {
	t.Helper()
	t.Setenv("GO_WANT_CODEX_HELPER", "1")
	t.Setenv("CODEX_HELPER_MODE", mode)
	return Options{
		Binary:          os.Args[0],
		HomeRoot:        t.TempDir(),
		MaxMessageBytes: 1024,
		ShutdownGrace:   100 * time.Millisecond,
	}
}

func TestSessionRunAndActorIsolation(t *testing.T) {
	manager := NewManager(helperOptions(t, "normal"))
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	session, err := manager.Session(context.Background(), "actor-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := session.RequireActor("actor-b"); err != ErrActorMismatch {
		t.Fatalf("actor mismatch = %v", err)
	}
	result, err := session.Run(context.Background(), "actor-a", RunRequest{Prompt: "draft a task", Model: "gpt-5.6-luna", Effort: "medium", OutputSchema: json.RawMessage(`{"type":"object"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if result.ThreadID != "thread-1" || result.TurnID != "turn-1" || result.Status != "completed" || result.Output != "final draft" {
		t.Fatalf("unexpected run result: %+v", result)
	}
}

func TestManagerUsesDistinctProtectedActorHomes(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "must-not-leak")
	t.Setenv("CODEX_ACCESS_TOKEN", "must-not-leak")
	opts := helperOptions(t, "normal")
	manager := NewManager(opts)
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	first, err := manager.Session(context.Background(), "actor-a")
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Session(context.Background(), "actor-b")
	if err != nil {
		t.Fatal(err)
	}
	readHome := func(session *Session) string {
		var value struct {
			Home        string `json:"home"`
			OpenAIKey   string `json:"openaiKey"`
			AccessToken string `json:"accessToken"`
		}
		if err := session.Call(context.Background(), "test/home", map[string]any{}, &value); err != nil {
			t.Fatal(err)
		}
		if value.OpenAIKey != "" {
			t.Fatal("host OPENAI_API_KEY leaked into an actor runtime")
		}
		if value.AccessToken != "" {
			t.Fatal("host CODEX_ACCESS_TOKEN leaked into an actor runtime")
		}
		return value.Home
	}
	firstHome, secondHome := readHome(first), readHome(second)
	if firstHome == "" || secondHome == "" || firstHome == secondHome {
		t.Fatalf("actor homes are not isolated: %q %q", firstHome, secondHome)
	}
	for _, home := range []string{firstHome, secondHome} {
		if filepath.Dir(home) != opts.HomeRoot {
			t.Fatalf("home escaped root: %q", home)
		}
		info, err := os.Stat(home)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("home mode = %o", info.Mode().Perm())
		}
	}
}

func TestAccountLifecycleUsesManagedChatGPTDeviceCode(t *testing.T) {
	manager := NewManager(helperOptions(t, "normal"))
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	status, err := manager.Account(context.Background(), "actor-a", false)
	if err != nil || !status.Connected || status.AccountType != "chatgpt" || status.PlanType != "plus" {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	login, err := manager.StartDeviceLogin(context.Background(), "actor-a")
	if err != nil || login.LoginID != "login-1" || login.UserCode != "TEST-CODE" {
		t.Fatalf("login=%+v err=%v", login, err)
	}
	canceled, err := manager.CancelDeviceLogin(context.Background(), "actor-a", login.LoginID)
	if err != nil || canceled.Status != "canceled" {
		t.Fatalf("cancel=%+v err=%v", canceled, err)
	}
	if err := manager.LogoutAccount(context.Background(), "actor-a"); err != nil {
		t.Fatal(err)
	}
}

func TestRunCancellationInterruptsTurn(t *testing.T) {
	manager := NewManager(helperOptions(t, "hang"))
	t.Cleanup(func() { _ = manager.Close(context.Background()) })
	session, err := manager.Session(context.Background(), "actor-a")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, err = session.Run(ctx, "actor-a", RunRequest{Prompt: "wait"})
	if !errorsIs(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestStartFailuresAreBounded(t *testing.T) {
	t.Run("missing executable", func(t *testing.T) {
		manager := NewManager(Options{Binary: filepath.Join(t.TempDir(), "missing"), HomeRoot: t.TempDir()})
		if _, err := manager.Session(context.Background(), "actor"); err == nil || !strings.Contains(err.Error(), "start Codex") {
			t.Fatalf("missing executable error = %v", err)
		}
	})
	for _, mode := range []string{"malformed", "oversized"} {
		t.Run(mode, func(t *testing.T) {
			manager := NewManager(helperOptions(t, mode))
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err := manager.Session(ctx, "actor"); err == nil {
				t.Fatal("invalid protocol unexpectedly initialized")
			}
		})
	}
}

func TestManagerRestartsExitedSession(t *testing.T) {
	manager := NewManager(helperOptions(t, "normal"))
	first, err := manager.Session(context.Background(), "actor")
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, err := manager.Session(context.Background(), "actor")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close(context.Background())
	if first == second {
		t.Fatal("closed session was reused")
	}
}

// Keep the test compatible with Go's wrapped context errors without importing
// a second name that can be confused with the runtime package errors above.
func errorsIs(err, target error) bool {
	for err != nil {
		if err == target {
			return true
		}
		value, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = value.Unwrap()
	}
	return false
}
