package server

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	wechatcore "github.com/wechatpay-apiv3/wechatpay-go/core"
	"github.com/wechatpay-apiv3/wechatpay-go/core/auth/verifiers"
	"github.com/wechatpay-apiv3/wechatpay-go/core/downloader"
	"github.com/wechatpay-apiv3/wechatpay-go/core/notify"
	"github.com/wechatpay-apiv3/wechatpay-go/core/option"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments"
	"github.com/wechatpay-apiv3/wechatpay-go/services/payments/native"
	"github.com/wechatpay-apiv3/wechatpay-go/utils"

	"github.com/openchat/openchat/server/store/types"
)

type weChatNativePaymentProvider struct {
	appID         string
	mchID         string
	notifyURL     string
	service       native.NativeApiService
	notifyHandler *notify.Handler
}

func NewWeChatNativePaymentProviderFromEnv(ctx context.Context) (CommercialPaymentProvider, []string, error) {
	config := map[string]string{
		"CATS_WECHAT_PAY_APP_ID":               strings.TrimSpace(os.Getenv("CATS_WECHAT_PAY_APP_ID")),
		"CATS_WECHAT_PAY_MCH_ID":               strings.TrimSpace(os.Getenv("CATS_WECHAT_PAY_MCH_ID")),
		"CATS_WECHAT_PAY_MCH_CERT_SERIAL":      strings.TrimSpace(os.Getenv("CATS_WECHAT_PAY_MCH_CERT_SERIAL")),
		"CATS_WECHAT_PAY_MCH_PRIVATE_KEY_FILE": strings.TrimSpace(os.Getenv("CATS_WECHAT_PAY_MCH_PRIVATE_KEY_FILE")),
		"CATS_WECHAT_PAY_API_V3_KEY_FILE":      strings.TrimSpace(os.Getenv("CATS_WECHAT_PAY_API_V3_KEY_FILE")),
		"CATS_WECHAT_PAY_NOTIFY_URL":           strings.TrimSpace(os.Getenv("CATS_WECHAT_PAY_NOTIFY_URL")),
		"CATS_WECHAT_PAY_PUBLIC_KEY_ID":        strings.TrimSpace(os.Getenv("CATS_WECHAT_PAY_PUBLIC_KEY_ID")),
		"CATS_WECHAT_PAY_PUBLIC_KEY_FILE":      strings.TrimSpace(os.Getenv("CATS_WECHAT_PAY_PUBLIC_KEY_FILE")),
	}
	missing := []string{}
	for _, name := range []string{
		"CATS_WECHAT_PAY_APP_ID",
		"CATS_WECHAT_PAY_MCH_ID",
		"CATS_WECHAT_PAY_MCH_CERT_SERIAL",
		"CATS_WECHAT_PAY_MCH_PRIVATE_KEY_FILE",
		"CATS_WECHAT_PAY_API_V3_KEY_FILE",
		"CATS_WECHAT_PAY_NOTIFY_URL",
	} {
		if config[name] == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, missing, nil
	}
	notifyURL, err := url.Parse(config["CATS_WECHAT_PAY_NOTIFY_URL"])
	if err != nil || notifyURL.Scheme != "https" || notifyURL.Host == "" || notifyURL.RawQuery != "" || notifyURL.Fragment != "" {
		return nil, nil, fmt.Errorf("CATS_WECHAT_PAY_NOTIFY_URL must be an HTTPS URL without query or fragment")
	}
	privateKey, err := utils.LoadPrivateKeyWithPath(config["CATS_WECHAT_PAY_MCH_PRIVATE_KEY_FILE"])
	if err != nil {
		return nil, nil, fmt.Errorf("load WeChat Pay merchant private key: %w", err)
	}
	apiV3KeyRaw, err := os.ReadFile(config["CATS_WECHAT_PAY_API_V3_KEY_FILE"])
	if err != nil {
		return nil, nil, fmt.Errorf("load WeChat Pay APIv3 key: %w", err)
	}
	apiV3Key := strings.TrimSpace(string(apiV3KeyRaw))
	if len(apiV3Key) != 32 {
		return nil, nil, fmt.Errorf("WeChat Pay APIv3 key must be exactly 32 bytes")
	}
	publicKeyID := config["CATS_WECHAT_PAY_PUBLIC_KEY_ID"]
	publicKeyFile := config["CATS_WECHAT_PAY_PUBLIC_KEY_FILE"]
	if publicKeyID != "" && publicKeyFile == "" {
		return nil, nil, fmt.Errorf("WeChat Pay public key file is required when a public key ID is configured")
	}
	var client *wechatcore.Client
	var notifyHandler *notify.Handler
	if publicKeyID != "" {
		publicKey, loadErr := utils.LoadPublicKeyWithPath(publicKeyFile)
		if loadErr != nil {
			return nil, nil, fmt.Errorf("load WeChat Pay public key: %w", loadErr)
		}
		client, err = wechatcore.NewClient(ctx, option.WithWechatPayPublicKeyAuthCipher(
			config["CATS_WECHAT_PAY_MCH_ID"],
			config["CATS_WECHAT_PAY_MCH_CERT_SERIAL"],
			privateKey,
			publicKeyID,
			publicKey,
		))
		notifyHandler = notify.NewNotifyHandler(apiV3Key, verifiers.NewSHA256WithRSAPubkeyVerifier(publicKeyID, *publicKey))
	} else {
		client, err = wechatcore.NewClient(ctx, option.WithWechatPayAutoAuthCipher(
			config["CATS_WECHAT_PAY_MCH_ID"],
			config["CATS_WECHAT_PAY_MCH_CERT_SERIAL"],
			privateKey,
			apiV3Key,
		))
		certificateVisitor := downloader.MgrInstance().GetCertificateVisitor(config["CATS_WECHAT_PAY_MCH_ID"])
		notifyHandler = notify.NewNotifyHandler(apiV3Key, verifiers.NewSHA256WithRSAVerifier(certificateVisitor))
	}
	if err != nil {
		return nil, nil, fmt.Errorf("initialize WeChat Pay client: %w", err)
	}
	return &weChatNativePaymentProvider{
		appID:         config["CATS_WECHAT_PAY_APP_ID"],
		mchID:         config["CATS_WECHAT_PAY_MCH_ID"],
		notifyURL:     config["CATS_WECHAT_PAY_NOTIFY_URL"],
		service:       native.NativeApiService{Client: client},
		notifyHandler: notifyHandler,
	}, nil, nil
}

func (p *weChatNativePaymentProvider) Channel() string { return commercialPaymentChannelWeChatNative }
func (p *weChatNativePaymentProvider) Label() string   { return "微信支付" }

func (p *weChatNativePaymentProvider) CreatePayment(ctx context.Context, order *types.CommercialOrder) (*CommercialPaymentIntent, error) {
	if p == nil || order == nil {
		return nil, fmt.Errorf("WeChat Pay provider is unavailable")
	}
	expiresAt := time.Now().UTC().Add(20 * time.Minute)
	if order.ExpiresAt != nil {
		expiresAt = order.ExpiresAt.UTC()
	}
	description := truncateUTF8("CatsCo "+strings.TrimSpace(order.PlanName), 120)
	response, _, err := p.service.Prepay(ctx, native.PrepayRequest{
		Appid:       wechatcore.String(p.appID),
		Mchid:       wechatcore.String(p.mchID),
		Description: wechatcore.String(description),
		OutTradeNo:  wechatcore.String(order.OrderNo),
		TimeExpire:  &expiresAt,
		NotifyUrl:   wechatcore.String(p.notifyURL),
		Amount: &native.Amount{
			Total:    wechatcore.Int64(order.AmountFen),
			Currency: wechatcore.String(order.Currency),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create WeChat Native payment: %w", err)
	}
	if response == nil || response.CodeUrl == nil || strings.TrimSpace(*response.CodeUrl) == "" {
		return nil, fmt.Errorf("WeChat Pay returned an empty code_url")
	}
	return &CommercialPaymentIntent{CodeURL: strings.TrimSpace(*response.CodeUrl), ExpiresAt: expiresAt}, nil
}

func (p *weChatNativePaymentProvider) ParseNotification(ctx context.Context, request *http.Request) (string, *types.CommercialPaymentConfirmation, error) {
	if p == nil || p.notifyHandler == nil || request == nil {
		return "", nil, fmt.Errorf("WeChat Pay notification handler is unavailable")
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, 1<<20))
	if err != nil {
		return "", nil, fmt.Errorf("read WeChat Pay notification: %w", err)
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	transaction := new(payments.Transaction)
	if _, err := p.notifyHandler.ParseNotifyRequest(ctx, request, transaction); err != nil {
		return "", nil, fmt.Errorf("verify WeChat Pay notification: %w", err)
	}
	if transaction.OutTradeNo == nil || strings.TrimSpace(*transaction.OutTradeNo) == "" {
		return "", nil, fmt.Errorf("WeChat Pay notification is missing out_trade_no")
	}
	if transaction.Mchid == nil || strings.TrimSpace(*transaction.Mchid) != p.mchID {
		return "", nil, fmt.Errorf("WeChat Pay merchant mismatch")
	}
	if transaction.Appid == nil || strings.TrimSpace(*transaction.Appid) != p.appID {
		return "", nil, fmt.Errorf("WeChat Pay app mismatch")
	}
	if transaction.TradeState == nil || strings.TrimSpace(*transaction.TradeState) != "SUCCESS" {
		return "", nil, fmt.Errorf("WeChat Pay transaction is not successful")
	}
	if transaction.Amount == nil || transaction.Amount.Total == nil || *transaction.Amount.Total <= 0 {
		return "", nil, fmt.Errorf("WeChat Pay notification is missing amount")
	}
	currency := "CNY"
	if transaction.Amount.Currency != nil && strings.TrimSpace(*transaction.Amount.Currency) != "" {
		currency = strings.ToUpper(strings.TrimSpace(*transaction.Amount.Currency))
	}
	providerTradeNo := ""
	if transaction.TransactionId != nil {
		providerTradeNo = strings.TrimSpace(*transaction.TransactionId)
	}
	paidAt := time.Now().UTC()
	if transaction.SuccessTime != nil {
		if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(*transaction.SuccessTime)); err == nil {
			paidAt = parsed.UTC()
		}
	}
	eventID := providerTradeNo
	if eventID == "" {
		eventID = paymentPayloadHash(body)
	}
	return strings.TrimSpace(*transaction.OutTradeNo), &types.CommercialPaymentConfirmation{
		Channel:         commercialPaymentChannelWeChatNative,
		EventID:         eventID,
		ProviderTradeNo: providerTradeNo,
		AmountFen:       *transaction.Amount.Total,
		Currency:        currency,
		PaidAt:          paidAt,
		PayloadHash:     paymentPayloadHash(body),
	}, nil
}

func truncateUTF8(value string, maxBytes int) string {
	value = strings.TrimSpace(value)
	if len(value) <= maxBytes {
		return value
	}
	for len(value) > maxBytes {
		_, size := utf8.DecodeLastRuneInString(value)
		if size <= 0 {
			break
		}
		value = value[:len(value)-size]
	}
	return strings.TrimSpace(value)
}
