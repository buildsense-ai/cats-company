package server

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

type verificationCode struct {
	Code    string
	Expires int64
}

const (
	verificationPurposeRegister      = "register"
	verificationPurposePasswordReset = "password_reset"
)

var (
	verificationCodes = make(map[string]*verificationCode)
	codesMutex        sync.RWMutex
)

func generateCode() string {
	return fmt.Sprintf("%06d", rand.Intn(900000)+100000)
}

func sendEmailViaResend(email, code, apiKey, subject string) error {
	emailFrom := os.Getenv("EMAIL_FROM")
	if emailFrom == "" {
		emailFrom = "onboarding@resend.dev"
	}

	payload := map[string]interface{}{
		"from":    emailFrom,
		"to":      []string{email},
		"subject": subject,
		"html":    fmt.Sprintf("<p>您的 Cats Company 验证码是：<strong>%s</strong></p><p>验证码 5 分钟内有效，请勿泄露给他人。</p>", code),
	}

	body, _ := json.Marshal(payload)
	req, _ := http.NewRequest("POST", "https://api.resend.com/emails", bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("[EMAIL_ERROR] Failed to send via Resend: %v\n", err)
		return err
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := fmt.Errorf("resend status %d: %v", resp.StatusCode, result)
		fmt.Printf("[EMAIL_ERROR] %v\n", err)
		return err
	}

	fmt.Printf("[EMAIL_SUCCESS] Sent via Resend to %s, Response: %v\n", email, result)
	return nil
}

type tencentSESTemplate struct {
	TemplateID   uint64 `json:"TemplateID"`
	TemplateData string `json:"TemplateData"`
}

type tencentSESSendEmailRequest struct {
	FromEmailAddress string             `json:"FromEmailAddress"`
	ReplyToAddresses string             `json:"ReplyToAddresses,omitempty"`
	Destination      []string           `json:"Destination"`
	Template         tencentSESTemplate `json:"Template"`
	Subject          string             `json:"Subject"`
	TriggerType      int                `json:"TriggerType,omitempty"`
	HeaderFrom       string             `json:"HeaderFrom,omitempty"`
}

type tencentCloudError struct {
	Code    string `json:"Code"`
	Message string `json:"Message"`
}

type tencentCloudSendEmailResponse struct {
	Response struct {
		Error     *tencentCloudError `json:"Error,omitempty"`
		MessageID string             `json:"MessageId,omitempty"`
		RequestID string             `json:"RequestId,omitempty"`
	} `json:"Response"`
}

func envTrim(name string) string {
	return strings.TrimSpace(os.Getenv(name))
}

func hasTencentSESConfig() bool {
	for _, name := range []string{
		"TENCENTCLOUD_SECRET_ID",
		"TENCENTCLOUD_SECRET_KEY",
		"TENCENT_SES_FROM_EMAIL",
		"TENCENT_SES_TEMPLATE_ID",
	} {
		if envTrim(name) != "" {
			return true
		}
	}
	return false
}

func tencentSESRequiredConfig() (secretID, secretKey, region, fromEmail, templateID string, err error) {
	secretID = envTrim("TENCENTCLOUD_SECRET_ID")
	secretKey = envTrim("TENCENTCLOUD_SECRET_KEY")
	region = envTrim("TENCENTCLOUD_REGION")
	if region == "" {
		region = "ap-guangzhou"
	}
	fromEmail = envTrim("TENCENT_SES_FROM_EMAIL")
	templateID = envTrim("TENCENT_SES_TEMPLATE_ID")

	var missing []string
	for name, value := range map[string]string{
		"TENCENTCLOUD_SECRET_ID":  secretID,
		"TENCENTCLOUD_SECRET_KEY": secretKey,
		"TENCENT_SES_FROM_EMAIL":  fromEmail,
		"TENCENT_SES_TEMPLATE_ID": templateID,
	} {
		if value == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		err = fmt.Errorf("missing Tencent SES config: %s", strings.Join(missing, ", "))
	}
	return
}

func tencentSESFromAddress(fromEmail string) string {
	fromName := envTrim("TENCENT_SES_FROM_NAME")
	if fromName == "" {
		return fromEmail
	}
	return fmt.Sprintf("%s <%s>", fromName, fromEmail)
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func hmacSHA256(key []byte, msg string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(msg))
	return mac.Sum(nil)
}

func buildTencentCloudAuthorization(secretID, secretKey, service, host, action, payload string, timestamp int64) string {
	const algorithm = "TC3-HMAC-SHA256"
	const contentType = "application/json; charset=utf-8"

	date := time.Unix(timestamp, 0).UTC().Format("2006-01-02")
	canonicalHeaders := fmt.Sprintf("content-type:%s\nhost:%s\nx-tc-action:%s\n", contentType, host, strings.ToLower(action))
	signedHeaders := "content-type;host;x-tc-action"
	canonicalRequest := strings.Join([]string{
		"POST",
		"/",
		"",
		canonicalHeaders,
		signedHeaders,
		sha256Hex(payload),
	}, "\n")

	credentialScope := fmt.Sprintf("%s/%s/tc3_request", date, service)
	stringToSign := strings.Join([]string{
		algorithm,
		strconv.FormatInt(timestamp, 10),
		credentialScope,
		sha256Hex(canonicalRequest),
	}, "\n")

	secretDate := hmacSHA256([]byte("TC3"+secretKey), date)
	secretService := hmacSHA256(secretDate, service)
	secretSigning := hmacSHA256(secretService, "tc3_request")
	signature := hex.EncodeToString(hmacSHA256(secretSigning, stringToSign))

	return fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s", algorithm, secretID, credentialScope, signedHeaders, signature)
}

func sendEmailViaTencentSES(email, code, subject string) error {
	secretID, secretKey, region, fromEmail, templateIDRaw, err := tencentSESRequiredConfig()
	if err != nil {
		return err
	}

	templateID, err := strconv.ParseUint(templateIDRaw, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid TENCENT_SES_TEMPLATE_ID: %w", err)
	}

	templateData, _ := json.Marshal(map[string]string{"code": code})
	if envSubject := envTrim("TENCENT_SES_SUBJECT"); envSubject != "" {
		subject = envSubject
	}
	replyTo := envTrim("TENCENT_SES_REPLY_TO")
	if replyTo == "" {
		replyTo = fromEmail
	}

	payload := tencentSESSendEmailRequest{
		FromEmailAddress: tencentSESFromAddress(fromEmail),
		ReplyToAddresses: replyTo,
		Destination:      []string{email},
		Template: tencentSESTemplate{
			TemplateID:   templateID,
			TemplateData: string(templateData),
		},
		Subject:     subject,
		TriggerType: 1,
		HeaderFrom:  fromEmail,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	const host = "ses.tencentcloudapi.com"
	const service = "ses"
	const action = "SendEmail"
	const version = "2020-10-02"
	const contentType = "application/json; charset=utf-8"
	bodyString := string(body)
	timestamp := time.Now().Unix()
	req, err := http.NewRequest("POST", "https://"+host, strings.NewReader(bodyString))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", buildTencentCloudAuthorization(secretID, secretKey, service, host, action, bodyString, timestamp))
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Host", host)
	req.Header.Set("X-TC-Action", action)
	req.Header.Set("X-TC-Timestamp", strconv.FormatInt(timestamp, 10))
	req.Header.Set("X-TC-Version", version)
	req.Header.Set("X-TC-Region", region)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("[EMAIL_ERROR] Tencent SES request failed: %v\n", err)
		return err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var parsed tencentCloudSendEmailResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return fmt.Errorf("tencent ses invalid response status=%d body=%s", resp.StatusCode, string(respBody))
	}
	if parsed.Response.Error != nil {
		err := fmt.Errorf("tencent ses error %s: %s", parsed.Response.Error.Code, parsed.Response.Error.Message)
		fmt.Printf("[EMAIL_ERROR] %v request_id=%s\n", err, parsed.Response.RequestID)
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := fmt.Errorf("tencent ses http status %d request_id=%s", resp.StatusCode, parsed.Response.RequestID)
		fmt.Printf("[EMAIL_ERROR] %v\n", err)
		return err
	}

	fmt.Printf("[EMAIL_SUCCESS] Sent via Tencent SES to %s, message_id=%s request_id=%s\n", email, parsed.Response.MessageID, parsed.Response.RequestID)
	return nil
}

func isProductionLikeEnv() bool {
	for _, name := range []string{"APP_ENV", "OC_ENV", "GO_ENV", "ENV"} {
		switch strings.ToLower(envTrim(name)) {
		case "prod", "production":
			return true
		}
	}
	return false
}

func exposeVerificationCodeInResponse() bool {
	switch strings.ToLower(envTrim("EMAIL_EXPOSE_DEV_CODE")) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return !isProductionLikeEnv()
}

func verificationCodeKey(email, purpose string) string {
	return purpose + ":" + strings.ToLower(strings.TrimSpace(email))
}

func deleteVerificationCode(email, purpose string) {
	codesMutex.Lock()
	delete(verificationCodes, verificationCodeKey(email, purpose))
	codesMutex.Unlock()
}

func storeVerificationCode(email, code string, expires int64, purpose string) {
	codesMutex.Lock()
	verificationCodes[verificationCodeKey(email, purpose)] = &verificationCode{
		Code:    code,
		Expires: expires,
	}
	codesMutex.Unlock()
}

func verificationEmailSubject(purpose string) string {
	if purpose == verificationPurposePasswordReset {
		return "Cats Company 重置密码验证码"
	}
	return "Cats Company 注册验证码"
}

func sendCodeEmail(email, code, purpose string) error {
	subject := verificationEmailSubject(purpose)
	if hasTencentSESConfig() {
		return sendEmailViaTencentSES(email, code, subject)
	}

	if resendAPIKey := envTrim("RESEND_API_KEY"); resendAPIKey != "" {
		return sendEmailViaResend(email, code, resendAPIKey, subject)
	}

	if isProductionLikeEnv() {
		return errors.New("email provider is not configured")
	}

	fmt.Printf("[EMAIL_DEV] Verification code for %s: %s\n", email, code)
	return nil
}

func verifyCode(email, code string) bool {
	return verifyCodeForPurpose(email, code, verificationPurposeRegister)
}

func verifyCodeForPurpose(email, code, purpose string) bool {
	codesMutex.RLock()
	stored, exists := verificationCodes[verificationCodeKey(email, purpose)]
	codesMutex.RUnlock()

	if !exists {
		return false
	}

	if time.Now().Unix() > stored.Expires {
		deleteVerificationCode(email, purpose)
		return false
	}

	if stored.Code != code {
		return false
	}

	deleteVerificationCode(email, purpose)
	return true
}

func sendVerificationCode(email string) (string, error) {
	return sendVerificationCodeForPurpose(email, verificationPurposeRegister)
}

func sendVerificationCodeForPurpose(email, purpose string) (string, error) {
	code := generateCode()
	expires := time.Now().Add(5 * time.Minute).Unix()

	storeVerificationCode(email, code, expires, purpose)

	if err := sendCodeEmail(email, code, purpose); err != nil {
		deleteVerificationCode(email, purpose)
		return "", err
	}

	return code, nil
}
