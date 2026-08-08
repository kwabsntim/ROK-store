package payment

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

const paystackBaseURL = "https://api.paystack.co"

// Client wraps the Paystack API.
type Client struct {
	secretKey  string
	httpClient *http.Client
}

// NewClient creates a Paystack Client using PAYSTACK_SECRET_KEY from the environment.
func NewClient() *Client {
	return &Client{
		secretKey:  os.Getenv("PAYSTACK_SECRET_KEY"),
		httpClient: &http.Client{},
	}
}

// InitializeTransactionRequest is the payload sent to Paystack's initialize endpoint.
type InitializeTransactionRequest struct {
	Email       string            `json:"email"`
	AmountKobo  int64             `json:"amount"` // amount in smallest currency unit (kobo for NGN/GHS pesewas)
	Reference   string            `json:"reference"`
	CallbackURL string            `json:"callback_url,omitempty"` // where Paystack redirects the user after payment
	Metadata    map[string]string `json:"metadata,omitempty"`
}

// InitializeTransactionResponse holds the relevant fields from Paystack's response.
type InitializeTransactionResponse struct {
	AuthorizationURL string `json:"authorization_url"`
	AccessCode       string `json:"access_code"`
	Reference        string `json:"reference"`
}

// InitializeTransaction calls Paystack's /transaction/initialize endpoint.
func (c *Client) InitializeTransaction(req InitializeTransactionRequest) (*InitializeTransactionResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal paystack request: %w", err)
	}

	httpReq, err := http.NewRequest(http.MethodPost, paystackBaseURL+"/transaction/initialize", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create paystack request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.secretKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("paystack request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("paystack returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Status bool   `json:"status"`
		Message string `json:"message"`
		Data   InitializeTransactionResponse `json:"data"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode paystack response: %w", err)
	}
	if !result.Status {
		return nil, fmt.Errorf("paystack error: %s", result.Message)
	}
	return &result.Data, nil
}

// VerifyTransaction calls Paystack's /transaction/verify/:reference endpoint.
// Used as a fallback when the webhook hasn't arrived yet.
func (c *Client) VerifyTransaction(reference string) (string, error) {
	httpReq, err := http.NewRequest(http.MethodGet,
		paystackBaseURL+"/transaction/verify/"+reference, nil)
	if err != nil {
		return "", fmt.Errorf("create verify request: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.secretKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("verify request failed: %w", err)
	}
	defer resp.Body.Close()

	var result struct {
		Status bool `json:"status"`
		Data   struct {
			Status string `json:"status"` // "success", "failed", "pending"
		} `json:"data"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode verify response: %w", err)
	}
	return result.Data.Status, nil
}

// VerifyWebhookSignature validates a Paystack webhook payload against the
// X-Paystack-Signature header using HMAC-SHA512.
func VerifyWebhookSignature(payload []byte, headerSignature string) bool {
	secret := []byte(os.Getenv("PAYSTACK_WEBHOOK_SECRET"))
	mac := hmac.New(sha512.New, secret)
	mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(headerSignature))
}
