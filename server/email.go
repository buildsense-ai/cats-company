package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"os"
	"sync"
	"time"
)

type verificationCode struct {
	Code    string
	Expires int64
}

var (
	verificationCodes = make(map[string]*verificationCode)
	codesMutex        sync.RWMutex
)

func generateCode() string {
	return fmt.Sprintf("%06d", rand.Intn(900000)+100000)
}

func sendEmailViaResend(email, code, apiKey string) {
	emailFrom := os.Getenv("EMAIL_FROM")
	if emailFrom == "" {
		emailFrom = "onboarding@resend.dev"
	}

	payload := map[string]interface{}{
		"from":    emailFrom,
		"to":      []string{email},
		"subject": "验证码",
		"html":    fmt.Sprintf("<p>您的验证码是：<strong>%s</strong></p><p>验证码5分钟内有效。</p>", code),
	}

	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", "https://api.resend.com/emails", bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("[EMAIL_ERROR] Failed to send: %v\n", err)
		return
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)

	if resp.StatusCode != 200 {
		fmt.Printf("[EMAIL_ERROR] Status: %d, Response: %v\n", resp.StatusCode, result)
	} else {
		fmt.Printf("[EMAIL_SUCCESS] Sent to %s, Response: %v\n", email, result)
	}
}

func verifyCode(email, code string) bool {
	codesMutex.RLock()
	stored, exists := verificationCodes[email]
	codesMutex.RUnlock()

	if !exists {
		return false
	}

	if time.Now().Unix() > stored.Expires {
		codesMutex.Lock()
		delete(verificationCodes, email)
		codesMutex.Unlock()
		return false
	}

	if stored.Code != code {
		return false
	}

	codesMutex.Lock()
	delete(verificationCodes, email)
	codesMutex.Unlock()
	return true
}

func sendVerificationCode(email string) (string, error) {
	code := generateCode()
	expires := time.Now().Add(5 * time.Minute).Unix()

	codesMutex.Lock()
	verificationCodes[email] = &verificationCode{
		Code:    code,
		Expires: expires,
	}
	codesMutex.Unlock()

	resendAPIKey := os.Getenv("RESEND_API_KEY")
	if resendAPIKey != "" {
		go sendEmailViaResend(email, code, resendAPIKey)
	}
	fmt.Printf("[EMAIL_DEV] Verification code for %s: %s\n", email, code)

	return code, nil
}
