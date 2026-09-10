package recruitingexecutor

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

const browserBrokerProtocolVersion = "recruiting.browser-broker.v1"

type browserProfileTask struct {
	Version             string         `json:"version"`
	RequestID           string         `json:"request_id"`
	Kind                string         `json:"kind"`
	SessionID           string         `json:"session_id"`
	ProfileID           string         `json:"profile_id"`
	SecurityDomain      string         `json:"security_domain"`
	ExpiresAt           string         `json:"expires_at"`
	CanaryURL           string         `json:"canary_url"`
	CanaryMinimum       int            `json:"canary_minimum_records"`
	CanaryRecipe        recipeabi.Spec `json:"canary_recipe"`
	CanaryRecipeID      string         `json:"canary_recipe_id"`
	CanaryRecipeVersion uint64         `json:"canary_recipe_version"`
	CanaryContentHash   string         `json:"canary_content_hash"`
	CanaryContractHash  string         `json:"canary_contract_hash"`
}

type browserProfileEvidence struct {
	Version             string `json:"version"`
	TaskKind            string `json:"task_kind"`
	SecurityDomain      string `json:"security_domain"`
	ObservedAt          string `json:"observed_at"`
	Probe               string `json:"probe"`
	CanaryRecipeID      string `json:"canary_recipe_id"`
	CanaryRecipeVersion uint64 `json:"canary_recipe_version"`
	CanaryContentHash   string `json:"canary_content_hash"`
	CanaryContractHash  string `json:"canary_contract_hash"`
	RecordCount         int    `json:"record_count"`
}

type browserProfileResult struct {
	Version       string                 `json:"version"`
	RequestID     string                 `json:"request_id"`
	Status        string                 `json:"status"`
	NextSecretRef string                 `json:"next_secret_ref,omitempty"`
	Authenticated bool                   `json:"authenticated,omitempty"`
	Evidence      browserProfileEvidence `json:"evidence"`
	FailureCode   string                 `json:"failure_code,omitempty"`
}

type browserBroker interface {
	Execute(context.Context, browserProfileTask) (browserProfileResult, error)
}

type httpBrowserBroker struct {
	endpoint string
	token    string
	client   *http.Client
}

func newHTTPBrowserBroker(endpoint, tokenFile string, timeout time.Duration) (*httpBrowserBroker, error) {
	if err := validateBrowserBrokerURL(endpoint); err != nil {
		return nil, err
	}
	token, err := readBrokerTokenFile(tokenFile)
	if err != nil {
		return nil, err
	}
	if timeout < time.Second || timeout > time.Hour {
		return nil, errors.New("browser broker timeout must be in [1s,1h]")
	}
	return &httpBrowserBroker{endpoint: strings.TrimRight(endpoint, "/") + "/profile-task", token: token,
		client: &http.Client{Timeout: timeout}}, nil
}

func validateBrowserBrokerURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return errors.New("browser broker URL must be an HTTP loopback origin without credentials, path, query, or fragment")
	}
	host := net.ParseIP(parsed.Hostname())
	if host == nil || !host.IsLoopback() {
		return errors.New("browser broker URL must use an explicit loopback IP")
	}
	return nil
}

func readBrokerTokenFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat browser broker token file: %w", err)
	}
	if !info.Mode().IsRegular() || (info.Mode().Perm() != 0o400 && info.Mode().Perm() != 0o600) || info.Size() < 32 || info.Size() > 4096 {
		return "", errors.New("browser broker token file must be a 0400 or 0600 regular file containing 32-4096 bytes")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read browser broker token file: %w", err)
	}
	token := strings.TrimSpace(string(raw))
	if len(token) < 32 || len(token) > 4096 || strings.ContainsAny(token, "\r\n\x00") {
		return "", errors.New("browser broker token is invalid")
	}
	return token, nil
}

func (b *httpBrowserBroker) Execute(ctx context.Context, task browserProfileTask) (browserProfileResult, error) {
	if b == nil || b.client == nil || ctx == nil {
		return browserProfileResult{}, errors.New("browser broker client is not configured")
	}
	body, err := json.Marshal(task)
	if err != nil {
		return browserProfileResult{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint, bytes.NewReader(body))
	if err != nil {
		return browserProfileResult{}, err
	}
	request.Header.Set("Authorization", "Bearer "+b.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := b.client.Do(request)
	if err != nil {
		return browserProfileResult{}, fmt.Errorf("call browser broker: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.CopyN(io.Discard, response.Body, 4096)
		return browserProfileResult{}, fmt.Errorf("browser broker returned HTTP %d", response.StatusCode)
	}
	limited := io.LimitReader(response.Body, 64<<10)
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	var result browserProfileResult
	if err := decoder.Decode(&result); err != nil {
		return browserProfileResult{}, fmt.Errorf("decode browser broker response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return browserProfileResult{}, errors.New("browser broker returned trailing JSON")
	}
	if subtle.ConstantTimeCompare([]byte(result.RequestID), []byte(task.RequestID)) != 1 || result.Version != browserBrokerProtocolVersion {
		return browserProfileResult{}, errors.New("browser broker returned a mismatched response")
	}
	if result.Status == "failed" && result.FailureCode != "" {
		return browserProfileResult{}, fmt.Errorf("browser broker task failed: %s", result.FailureCode)
	}
	return result, nil
}
