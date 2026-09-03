package codexruntime

import (
	"context"
	"strings"
)

// AccountStatus contains only display-safe account metadata. Codex owns all
// access and refresh credentials inside the actor-specific CODEX_HOME.
type AccountStatus struct {
	Connected          bool   `json:"connected"`
	AccountType        string `json:"account_type,omitempty"`
	Email              string `json:"email,omitempty"`
	PlanType           string `json:"plan_type,omitempty"`
	RequiresOpenAIAuth bool   `json:"requires_openai_auth"`
}

type DeviceLogin struct {
	LoginID         string `json:"login_id"`
	VerificationURL string `json:"verification_url"`
	UserCode        string `json:"user_code"`
}

type CancelLoginResult struct {
	Status string `json:"status"`
}

func (m *Manager) Account(ctx context.Context, actorID string, refresh bool) (AccountStatus, error) {
	session, err := m.Session(ctx, actorID)
	if err != nil {
		return AccountStatus{}, err
	}
	var response struct {
		Account *struct {
			Type     string  `json:"type"`
			Email    *string `json:"email"`
			PlanType string  `json:"planType"`
		} `json:"account"`
		RequiresOpenAIAuth bool `json:"requiresOpenaiAuth"`
	}
	if err := session.Call(ctx, "account/read", map[string]bool{"refreshToken": refresh}, &response); err != nil {
		return AccountStatus{}, err
	}
	status := AccountStatus{RequiresOpenAIAuth: response.RequiresOpenAIAuth}
	if response.Account != nil {
		status.AccountType = response.Account.Type
		status.Connected = response.Account.Type == "chatgpt"
		status.PlanType = response.Account.PlanType
		if response.Account.Email != nil {
			status.Email = *response.Account.Email
		}
	}
	return status, nil
}

func (m *Manager) StartDeviceLogin(ctx context.Context, actorID string) (DeviceLogin, error) {
	session, err := m.Session(ctx, actorID)
	if err != nil {
		return DeviceLogin{}, err
	}
	var response struct {
		Type            string `json:"type"`
		LoginID         string `json:"loginId"`
		VerificationURL string `json:"verificationUrl"`
		UserCode        string `json:"userCode"`
	}
	if err := session.Call(ctx, "account/login/start", map[string]string{"type": "chatgptDeviceCode"}, &response); err != nil {
		return DeviceLogin{}, err
	}
	if response.Type != "chatgptDeviceCode" || response.LoginID == "" || len(response.LoginID) > 200 ||
		!strings.HasPrefix(response.VerificationURL, "https://") || len(response.VerificationURL) > 2_000 ||
		response.UserCode == "" || len(response.UserCode) > 100 {
		return DeviceLogin{}, ErrInvalidAccountResponse
	}
	return DeviceLogin{LoginID: response.LoginID, VerificationURL: response.VerificationURL, UserCode: response.UserCode}, nil
}

func (m *Manager) CancelDeviceLogin(ctx context.Context, actorID, loginID string) (CancelLoginResult, error) {
	session, err := m.Session(ctx, actorID)
	if err != nil {
		return CancelLoginResult{}, err
	}
	var response CancelLoginResult
	if err := session.Call(ctx, "account/login/cancel", map[string]string{"loginId": loginID}, &response); err != nil {
		return CancelLoginResult{}, err
	}
	return response, nil
}

func (m *Manager) LogoutAccount(ctx context.Context, actorID string) error {
	session, err := m.Session(ctx, actorID)
	if err != nil {
		return err
	}
	if err := session.Call(ctx, "account/logout", nil, nil); err != nil {
		return err
	}
	return m.CloseActor(ctx, actorID)
}

// Draft runs one schema-constrained, read-only turn in the requesting actor's
// runtime. It is intentionally separate from account management so HTTP tests
// can verify that actor routing is never inferred from project data.
func (m *Manager) Draft(ctx context.Context, actorID string, request RunRequest) (RunResult, error) {
	session, err := m.Session(ctx, actorID)
	if err != nil {
		return RunResult{}, err
	}
	return session.Run(ctx, actorID, request)
}
