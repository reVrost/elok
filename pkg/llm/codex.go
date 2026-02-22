package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/revrost/elok/pkg/config"
)

const (
	codexOAuthClientID          = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexOAuthTokenURL          = "https://auth.openai.com/oauth/token"
	codexDefaultOAuthBaseURL    = "https://chatgpt.com/backend-api"
	codexDefaultOpenAIBaseURL   = "https://api.openai.com/v1"
	codexOAuthResponsesPath     = "/codex/responses"
	codexOpenAIResponsesPath    = "/responses"
	codexChatGPTAuthClaimPath   = "https://api.openai.com/auth"
	codexOpenAIBetaHeader       = "responses=experimental"
	codexOriginatorHeader       = "codex_cli_rs"
	codexDefaultResponseInclude = "reasoning.encrypted_content"
	codexOpenRouterDefaultBase  = "https://openrouter.ai/api/v1"
	codexDefaultModel           = "gpt-5.1-codex"
)

type codexAuthMode string

const (
	codexAuthModeAPIKey  codexAuthMode = "api_key"
	codexAuthModeChatGPT codexAuthMode = "chatgpt_oauth"
)

type CodexClient struct {
	model       string
	apiKey      string
	baseURLHint string
	authPath    string
	http        *http.Client
}

func NewCodex(cfg config.LLMConfig) *CodexClient {
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = codexDefaultModel
	}

	baseURLHint := strings.TrimSpace(cfg.BaseURL)
	normalized := strings.TrimSuffix(strings.ToLower(baseURLHint), "/")
	if normalized == "" || normalized == codexOpenRouterDefaultBase {
		baseURLHint = ""
	}

	authPath := strings.TrimSpace(cfg.CodexAuthPath)
	if authPath == "" {
		authPath = defaultCodexAuthPath()
	}

	return &CodexClient{
		model:       model,
		apiKey:      strings.TrimSpace(cfg.ResolveAPIKey()),
		baseURLHint: strings.TrimSuffix(baseURLHint, "/"),
		authPath:    authPath,
		http: &http.Client{
			Timeout: 90 * time.Second,
		},
	}
}

type codexResponsesRequest struct {
	Model        string              `json:"model"`
	Store        bool                `json:"store"`
	Stream       bool                `json:"stream"`
	Instructions string              `json:"instructions,omitempty"`
	Include      []string            `json:"include,omitempty"`
	Input        []codexMessageInput `json:"input,omitempty"`
}

type codexMessageInput struct {
	Type    string           `json:"type,omitempty"`
	Role    string           `json:"role"`
	Content []codexTextInput `json:"content"`
}

type codexTextInput struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type codexStreamEnvelope struct {
	Type     string                 `json:"type"`
	Delta    string                 `json:"delta,omitempty"`
	Text     string                 `json:"text,omitempty"`
	Response *codexResponseComplete `json:"response,omitempty"`
}

type codexResponseComplete struct {
	Output []codexResponseOutput `json:"output"`
}

type codexResponseOutput struct {
	Type    string               `json:"type,omitempty"`
	Text    string               `json:"text,omitempty"`
	Content []codexResponseBlock `json:"content,omitempty"`
}

type codexResponseBlock struct {
	Type string `json:"type,omitempty"`
	Text string `json:"text,omitempty"`
}

type codexAuthFile struct {
	AuthMode     *string          `json:"auth_mode,omitempty"`
	OpenAIAPIKey *string          `json:"OPENAI_API_KEY,omitempty"`
	Tokens       *codexAuthTokens `json:"tokens,omitempty"`
	LastRefresh  *time.Time       `json:"last_refresh,omitempty"`
}

type codexAuthTokens struct {
	IDToken      string `json:"id_token,omitempty"`
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
	AccountID    string `json:"account_id,omitempty"`
}

type codexAuthState struct {
	Mode         codexAuthMode
	BearerToken  string
	AccountID    string
	RefreshToken string
	IDToken      string
	AuthPath     string
	AuthFile     codexAuthFile
}

type codexRefreshResponse struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

func (c *CodexClient) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	stream, err := c.Stream(ctx, req)
	if err != nil {
		return CompletionResponse{}, err
	}
	text, err := CollectStreamText(ctx, stream)
	if err != nil {
		return CompletionResponse{}, err
	}
	return CompletionResponse{Text: text}, nil
}

func (c *CodexClient) Stream(ctx context.Context, req CompletionRequest) (*Stream, error) {
	if strings.TrimSpace(c.model) == "" {
		return nil, fmt.Errorf("codex model is empty")
	}

	authState, err := c.loadAuthState()
	if err != nil {
		return nil, err
	}

	requestBody := c.buildResponsesRequest(req)
	return c.streamResponses(ctx, requestBody, authState, true)
}

func (c *CodexClient) buildResponsesRequest(req CompletionRequest) codexResponsesRequest {
	input := make([]codexMessageInput, 0, len(req.Messages))
	for _, msg := range req.Messages {
		role := strings.TrimSpace(msg.Role)
		text := strings.TrimSpace(msg.Content)
		if role == "" || text == "" {
			continue
		}
		input = append(input, codexMessageInput{
			Type: "message",
			Role: role,
			Content: []codexTextInput{
				{Type: "input_text", Text: text},
			},
		})
	}

	return codexResponsesRequest{
		Model:        c.model,
		Store:        false,
		Stream:       true,
		Instructions: strings.TrimSpace(req.SystemPrompt),
		Include:      []string{codexDefaultResponseInclude},
		Input:        input,
	}
}

func (c *CodexClient) streamResponses(
	ctx context.Context,
	body codexResponsesRequest,
	authState codexAuthState,
	allowRefresh bool,
) (*Stream, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal codex request: %w", err)
	}

	endpoint := c.resolveResponsesEndpoint(authState.Mode)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create codex request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+authState.BearerToken)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")

	if authState.Mode == codexAuthModeChatGPT {
		httpReq.Header.Set("OpenAI-Beta", codexOpenAIBetaHeader)
		httpReq.Header.Set("originator", codexOriginatorHeader)
		if strings.TrimSpace(authState.AccountID) != "" {
			httpReq.Header.Set("chatgpt-account-id", authState.AccountID)
		}
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("call codex: %w", err)
	}

	if resp.StatusCode == http.StatusUnauthorized &&
		authState.Mode == codexAuthModeChatGPT &&
		allowRefresh &&
		strings.TrimSpace(authState.RefreshToken) != "" {
		_ = resp.Body.Close()
		if refreshErr := c.refreshChatGPTToken(ctx, &authState); refreshErr != nil {
			return nil, fmt.Errorf("codex unauthorized and refresh failed: %w", refreshErr)
		}
		return c.streamResponses(ctx, body, authState, false)
	}

	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		respBody, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, fmt.Errorf("codex status %d and failed reading body: %w", resp.StatusCode, readErr)
		}
		return nil, fmt.Errorf("codex status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	events := make(chan StreamEvent, 256)
	done := make(chan error, 1)

	go func() {
		defer close(events)
		defer close(done)
		defer resp.Body.Close()

		deltaSeen := false
		emit := func(text string) error {
			if text == "" {
				return nil
			}
			deltaSeen = true
			select {
			case <-ctx.Done():
				return ctx.Err()
			case events <- StreamEvent{Type: StreamEventTextDelta, Delta: text}:
				return nil
			}
		}

		err := consumeSSE(ctx, resp.Body, func(event sseEvent) error {
			data := strings.TrimSpace(event.Data)
			if data == "" {
				return nil
			}
			if data == "[DONE]" {
				return io.EOF
			}

			var envelope codexStreamEnvelope
			if err := json.Unmarshal([]byte(data), &envelope); err != nil {
				return nil
			}

			switch envelope.Type {
			case "response.output_text.delta":
				if err := emit(envelope.Delta); err != nil {
					return err
				}
			case "response.output_text.done":
				if !deltaSeen && strings.TrimSpace(envelope.Text) != "" {
					if err := emit(envelope.Text); err != nil {
						return err
					}
				}
			case "response.done", "response.completed":
				if !deltaSeen && envelope.Response != nil {
					if err := emit(extractCodexResponseText(envelope.Response)); err != nil {
						return err
					}
				}
			}
			return nil
		})

		if errors.Is(err, io.EOF) {
			done <- nil
			return
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			done <- fmt.Errorf("codex stream: %w", err)
			return
		}
		done <- nil
	}()

	return &Stream{
		Events: events,
		Done:   done,
	}, nil
}

func (c *CodexClient) resolveResponsesEndpoint(mode codexAuthMode) string {
	baseURL := strings.TrimSuffix(strings.TrimSpace(c.baseURLHint), "/")
	if baseURL == "" {
		switch mode {
		case codexAuthModeAPIKey:
			baseURL = codexDefaultOpenAIBaseURL
		default:
			baseURL = codexDefaultOAuthBaseURL
		}
	}

	switch mode {
	case codexAuthModeAPIKey:
		return appendPath(baseURL, codexOpenAIResponsesPath)
	default:
		return appendPath(baseURL, codexOAuthResponsesPath)
	}
}

func appendPath(baseURL, path string) string {
	trimmed := strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	if trimmed == "" {
		return path
	}
	if strings.HasSuffix(strings.ToLower(trimmed), strings.ToLower(path)) {
		return trimmed
	}
	return trimmed + path
}

func (c *CodexClient) loadAuthState() (codexAuthState, error) {
	authPath := strings.TrimSpace(c.authPath)
	if authPath == "" {
		authPath = defaultCodexAuthPath()
	}

	raw, err := os.ReadFile(authPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if strings.TrimSpace(c.apiKey) != "" {
				return codexAuthState{
					Mode:        codexAuthModeAPIKey,
					BearerToken: strings.TrimSpace(c.apiKey),
				}, nil
			}
			return codexAuthState{}, fmt.Errorf("codex auth not found at %s; run `codex login` or configure llm.api_key_env", authPath)
		}
		return codexAuthState{}, fmt.Errorf("read codex auth file: %w", err)
	}

	var file codexAuthFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return codexAuthState{}, fmt.Errorf("decode codex auth file: %w", err)
	}

	// If auth.json has an API key (token exchange), allow it as fallback.
	if file.OpenAIAPIKey != nil && strings.TrimSpace(*file.OpenAIAPIKey) != "" {
		return codexAuthState{
			Mode:        codexAuthModeAPIKey,
			BearerToken: strings.TrimSpace(*file.OpenAIAPIKey),
		}, nil
	}

	if file.Tokens == nil || strings.TrimSpace(file.Tokens.AccessToken) == "" {
		if strings.TrimSpace(c.apiKey) != "" {
			return codexAuthState{
				Mode:        codexAuthModeAPIKey,
				BearerToken: strings.TrimSpace(c.apiKey),
			}, nil
		}
		return codexAuthState{}, fmt.Errorf("codex auth file missing tokens.access_token: %s", authPath)
	}

	state := codexAuthState{
		Mode:         codexAuthModeChatGPT,
		BearerToken:  strings.TrimSpace(file.Tokens.AccessToken),
		RefreshToken: strings.TrimSpace(file.Tokens.RefreshToken),
		AccountID:    strings.TrimSpace(file.Tokens.AccountID),
		IDToken:      strings.TrimSpace(file.Tokens.IDToken),
		AuthPath:     authPath,
		AuthFile:     file,
	}
	if state.AccountID == "" {
		state.AccountID = extractChatGPTAccountID(state.IDToken)
	}
	if state.AccountID == "" {
		state.AccountID = extractChatGPTAccountID(state.BearerToken)
	}
	if state.AccountID == "" {
		return codexAuthState{}, fmt.Errorf("codex auth token missing chatgpt_account_id; run `codex login` again")
	}

	return state, nil
}

func (c *CodexClient) refreshChatGPTToken(ctx context.Context, state *codexAuthState) error {
	if state == nil || strings.TrimSpace(state.RefreshToken) == "" {
		return fmt.Errorf("refresh token is empty")
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", state.RefreshToken)
	form.Set("client_id", codexOAuthClientID)
	form.Set("scope", "openid profile email")

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, codexOAuthTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("create refresh token request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("refresh codex token: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read refresh token response: %w", err)
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("refresh token status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var parsed codexRefreshResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return fmt.Errorf("decode refresh token response: %w", err)
	}
	if strings.TrimSpace(parsed.AccessToken) == "" {
		return fmt.Errorf("refresh token response missing access_token")
	}

	state.BearerToken = strings.TrimSpace(parsed.AccessToken)
	if strings.TrimSpace(parsed.RefreshToken) != "" {
		state.RefreshToken = strings.TrimSpace(parsed.RefreshToken)
	}
	if strings.TrimSpace(parsed.IDToken) != "" {
		state.IDToken = strings.TrimSpace(parsed.IDToken)
	}
	if state.AccountID == "" {
		state.AccountID = extractChatGPTAccountID(state.IDToken)
	}
	if state.AccountID == "" {
		state.AccountID = extractChatGPTAccountID(state.BearerToken)
	}
	if state.AccountID == "" {
		return fmt.Errorf("refresh succeeded but no chatgpt_account_id found in tokens")
	}

	if err := c.persistAuthState(*state); err != nil {
		return err
	}
	return nil
}

func (c *CodexClient) persistAuthState(state codexAuthState) error {
	if state.Mode != codexAuthModeChatGPT || strings.TrimSpace(state.AuthPath) == "" {
		return nil
	}

	authFile := state.AuthFile
	if authFile.Tokens == nil {
		authFile.Tokens = &codexAuthTokens{}
	}
	authFile.Tokens.AccessToken = state.BearerToken
	authFile.Tokens.RefreshToken = state.RefreshToken
	if strings.TrimSpace(state.IDToken) != "" {
		authFile.Tokens.IDToken = state.IDToken
	}
	if strings.TrimSpace(state.AccountID) != "" {
		authFile.Tokens.AccountID = state.AccountID
	}
	now := time.Now().UTC()
	authFile.LastRefresh = &now

	data, err := json.MarshalIndent(authFile, "", "  ")
	if err != nil {
		return fmt.Errorf("encode codex auth file: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(state.AuthPath), 0o755); err != nil {
		return fmt.Errorf("mkdir codex auth dir: %w", err)
	}
	if err := os.WriteFile(state.AuthPath, data, 0o600); err != nil {
		return fmt.Errorf("write codex auth file: %w", err)
	}
	return nil
}

func extractChatGPTAccountID(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	authClaims, ok := claims[codexChatGPTAuthClaimPath].(map[string]any)
	if !ok {
		return ""
	}
	accountID, _ := authClaims["chatgpt_account_id"].(string)
	return strings.TrimSpace(accountID)
}

func extractCodexResponseText(response *codexResponseComplete) string {
	if response == nil || len(response.Output) == 0 {
		return ""
	}

	parts := make([]string, 0, 4)
	for _, output := range response.Output {
		if strings.TrimSpace(output.Text) != "" {
			parts = append(parts, output.Text)
		}
		for _, block := range output.Content {
			text := strings.TrimSpace(block.Text)
			if text != "" {
				parts = append(parts, text)
			}
		}
	}
	return strings.Join(parts, "")
}

func defaultCodexAuthPath() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ".codex/auth.json"
	}
	return filepath.Join(home, ".codex", "auth.json")
}
