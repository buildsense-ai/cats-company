package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/smartwalle/alipay/v3"

	"github.com/openchat/openchat/server/store/types"
)

const (
	alipayProductCodePagePay   = "FAST_INSTANT_TRADE_PAY"
	alipayIntegrationTypePCWeb = "PCWEB"
	alipayTimeLayout           = "2006-01-02 15:04:05"
	maxAlipayNotificationBytes = 64 << 10
)

var alipayChinaLocation = time.FixedZone("CST", 8*60*60)

type alipayPaymentClient interface {
	TradePagePay(alipay.TradePagePay) (*url.URL, error)
	TradeQuery(context.Context, alipay.TradeQuery) (*alipay.TradeQueryRsp, error)
	DecodeNotification(context.Context, url.Values) (*alipay.Notification, error)
}

type alipayPagePaymentProvider struct {
	appID      string
	sellerID   string
	notifyURL  string
	returnURL  string
	client     alipayPaymentClient
	production bool
}

func NewAlipayPagePaymentProviderFromEnv() (CommercialPaymentProvider, []string, error) {
	config := map[string]string{
		"CATS_ALIPAY_APP_ID":           strings.TrimSpace(os.Getenv("CATS_ALIPAY_APP_ID")),
		"CATS_ALIPAY_SELLER_ID":        strings.TrimSpace(os.Getenv("CATS_ALIPAY_SELLER_ID")),
		"CATS_ALIPAY_PRIVATE_KEY_FILE": strings.TrimSpace(os.Getenv("CATS_ALIPAY_PRIVATE_KEY_FILE")),
		"CATS_ALIPAY_PUBLIC_KEY_FILE":  strings.TrimSpace(os.Getenv("CATS_ALIPAY_PUBLIC_KEY_FILE")),
		"CATS_ALIPAY_NOTIFY_URL":       strings.TrimSpace(os.Getenv("CATS_ALIPAY_NOTIFY_URL")),
		"CATS_ALIPAY_RETURN_URL":       strings.TrimSpace(os.Getenv("CATS_ALIPAY_RETURN_URL")),
	}
	missing := []string{}
	for _, name := range []string{
		"CATS_ALIPAY_APP_ID",
		"CATS_ALIPAY_SELLER_ID",
		"CATS_ALIPAY_PRIVATE_KEY_FILE",
		"CATS_ALIPAY_PUBLIC_KEY_FILE",
		"CATS_ALIPAY_NOTIFY_URL",
		"CATS_ALIPAY_RETURN_URL",
	} {
		if config[name] == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return nil, missing, nil
	}
	notifyURL, err := url.Parse(config["CATS_ALIPAY_NOTIFY_URL"])
	if err != nil || notifyURL.Scheme != "https" || notifyURL.Host == "" || notifyURL.RawQuery != "" || notifyURL.Fragment != "" {
		return nil, nil, fmt.Errorf("CATS_ALIPAY_NOTIFY_URL must be an HTTPS URL without query or fragment")
	}
	returnURL, err := url.Parse(config["CATS_ALIPAY_RETURN_URL"])
	if err != nil || returnURL.Scheme != "https" || returnURL.Host == "" || returnURL.Fragment != "" {
		return nil, nil, fmt.Errorf("CATS_ALIPAY_RETURN_URL must be an HTTPS URL without fragment")
	}
	privateKey, err := os.ReadFile(config["CATS_ALIPAY_PRIVATE_KEY_FILE"])
	if err != nil {
		return nil, nil, fmt.Errorf("load Alipay application private key: %w", err)
	}
	publicKey, err := os.ReadFile(config["CATS_ALIPAY_PUBLIC_KEY_FILE"])
	if err != nil {
		return nil, nil, fmt.Errorf("load Alipay public key: %w", err)
	}
	production := envBoolValue("CATS_ALIPAY_PRODUCTION")
	client, err := alipay.New(
		config["CATS_ALIPAY_APP_ID"],
		strings.TrimSpace(string(privateKey)),
		production,
		alipay.WithHTTPClient(&http.Client{Timeout: 15 * time.Second}),
		alipay.WithTimeLocation(alipayChinaLocation),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("initialize Alipay client: %w", err)
	}
	if err := client.LoadAliPayPublicKey(strings.TrimSpace(string(publicKey))); err != nil {
		return nil, nil, fmt.Errorf("load Alipay verification public key: %w", err)
	}
	return &alipayPagePaymentProvider{
		appID:      config["CATS_ALIPAY_APP_ID"],
		sellerID:   config["CATS_ALIPAY_SELLER_ID"],
		notifyURL:  config["CATS_ALIPAY_NOTIFY_URL"],
		returnURL:  config["CATS_ALIPAY_RETURN_URL"],
		client:     client,
		production: production,
	}, nil, nil
}

func (p *alipayPagePaymentProvider) Channel() string { return commercialPaymentChannelAlipayPage }
func (p *alipayPagePaymentProvider) Label() string   { return "支付宝支付" }

func (p *alipayPagePaymentProvider) CreatePayment(ctx context.Context, order *types.CommercialOrder) (*CommercialPaymentIntent, error) {
	if p == nil || p.client == nil || order == nil {
		return nil, fmt.Errorf("Alipay provider is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if order.AmountFen <= 0 || !strings.EqualFold(strings.TrimSpace(order.Currency), "CNY") {
		return nil, fmt.Errorf("Alipay order amount or currency is invalid")
	}
	expiresAt := time.Now().UTC().Add(20 * time.Minute)
	if order.ExpiresAt != nil {
		expiresAt = order.ExpiresAt.UTC()
	}
	subject := truncateUTF8Bytes("CatsCo "+strings.TrimSpace(order.PlanName), 128)
	paymentURL, err := p.client.TradePagePay(alipay.TradePagePay{
		Trade: alipay.Trade{
			NotifyURL:   p.notifyURL,
			ReturnURL:   p.returnURL,
			Subject:     subject,
			OutTradeNo:  strings.TrimSpace(order.OrderNo),
			TotalAmount: formatCNYFen(order.AmountFen),
			ProductCode: alipayProductCodePagePay,
			SellerId:    p.sellerID,
			TimeExpire:  expiresAt.In(alipayChinaLocation).Format(alipayTimeLayout),
			GoodsType:   "0",
		},
		IntegrationType: alipayIntegrationTypePCWeb,
	})
	if err != nil {
		return nil, fmt.Errorf("create Alipay page payment: %w", err)
	}
	if paymentURL == nil || paymentURL.Scheme != "https" || paymentURL.Host == "" {
		return nil, fmt.Errorf("Alipay returned an invalid checkout URL")
	}
	return &CommercialPaymentIntent{CheckoutURL: paymentURL.String(), ExpiresAt: expiresAt}, nil
}

func (p *alipayPagePaymentProvider) ParseNotification(ctx context.Context, request *http.Request) (string, *types.CommercialPaymentConfirmation, error) {
	if p == nil || p.client == nil || request == nil {
		return "", nil, fmt.Errorf("Alipay notification handler is unavailable")
	}
	body, err := readLimitedBody(request.Body, maxAlipayNotificationBytes)
	if err != nil {
		return "", nil, fmt.Errorf("read Alipay notification: %w", err)
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return "", nil, fmt.Errorf("parse Alipay notification: %w", err)
	}
	notification, err := p.client.DecodeNotification(ctx, values)
	if err != nil {
		return "", nil, fmt.Errorf("verify Alipay notification: %w", err)
	}
	return p.confirmationFromNotification(notification, paymentPayloadHash(body))
}

func (p *alipayPagePaymentProvider) QueryPayment(ctx context.Context, order *types.CommercialOrder) (*types.CommercialPaymentConfirmation, bool, error) {
	if p == nil || p.client == nil || order == nil || strings.TrimSpace(order.OrderNo) == "" {
		return nil, false, fmt.Errorf("Alipay order query is unavailable")
	}
	response, err := p.client.TradeQuery(ctx, alipay.TradeQuery{OutTradeNo: strings.TrimSpace(order.OrderNo)})
	if err != nil {
		return nil, false, fmt.Errorf("query Alipay order: %w", err)
	}
	if response == nil {
		return nil, false, fmt.Errorf("Alipay returned an empty query response")
	}
	if !response.IsSuccess() {
		if strings.EqualFold(strings.TrimSpace(response.SubCode), "ACQ.TRADE_NOT_EXIST") {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("Alipay query failed: code=%s sub_code=%s", response.Code, response.SubCode)
	}
	if response.TradeStatus != alipay.TradeStatusSuccess && response.TradeStatus != alipay.TradeStatusFinished {
		return nil, false, nil
	}
	if strings.TrimSpace(response.OutTradeNo) != strings.TrimSpace(order.OrderNo) {
		return nil, false, fmt.Errorf("Alipay queried order mismatch")
	}
	amountFen, err := parseCNYFen(response.TotalAmount)
	if err != nil {
		return nil, false, fmt.Errorf("parse Alipay queried amount: %w", err)
	}
	tradeNo := strings.TrimSpace(response.TradeNo)
	if tradeNo == "" {
		return nil, false, fmt.Errorf("Alipay query is missing trade_no")
	}
	paidAt := parseAlipayTime(response.SendPayDate)
	payload, _ := json.Marshal(response)
	return &types.CommercialPaymentConfirmation{
		Channel:         commercialPaymentChannelAlipayPage,
		EventID:         tradeNo,
		ProviderTradeNo: tradeNo,
		AmountFen:       amountFen,
		Currency:        "CNY",
		PaidAt:          paidAt,
		PayloadHash:     paymentPayloadHash(payload),
	}, true, nil
}

func (p *alipayPagePaymentProvider) confirmationFromNotification(notification *alipay.Notification, payloadHash string) (string, *types.CommercialPaymentConfirmation, error) {
	if p == nil || notification == nil {
		return "", nil, fmt.Errorf("Alipay notification is missing")
	}
	if strings.TrimSpace(notification.AppId) != p.appID {
		return "", nil, fmt.Errorf("Alipay application mismatch")
	}
	if strings.TrimSpace(notification.SellerId) != p.sellerID {
		return "", nil, fmt.Errorf("Alipay seller mismatch")
	}
	if notification.NotifyType != alipay.NotifyTypeTradeStatusSync {
		return "", nil, fmt.Errorf("Alipay notification type is invalid")
	}
	if notification.TradeStatus != alipay.TradeStatusSuccess && notification.TradeStatus != alipay.TradeStatusFinished {
		return "", nil, fmt.Errorf("Alipay transaction is not successful")
	}
	orderNo := strings.TrimSpace(notification.OutTradeNo)
	if orderNo == "" {
		return "", nil, fmt.Errorf("Alipay notification is missing out_trade_no")
	}
	tradeNo := strings.TrimSpace(notification.TradeNo)
	if tradeNo == "" {
		return "", nil, fmt.Errorf("Alipay notification is missing trade_no")
	}
	amountFen, err := parseCNYFen(notification.TotalAmount)
	if err != nil || amountFen <= 0 {
		return "", nil, fmt.Errorf("Alipay notification amount is invalid")
	}
	return orderNo, &types.CommercialPaymentConfirmation{
		Channel:         commercialPaymentChannelAlipayPage,
		EventID:         tradeNo,
		ProviderTradeNo: tradeNo,
		AmountFen:       amountFen,
		Currency:        "CNY",
		PaidAt:          parseAlipayTime(notification.GmtPayment),
		PayloadHash:     payloadHash,
	}, nil
}

func formatCNYFen(amountFen int64) string {
	return fmt.Sprintf("%d.%02d", amountFen/100, amountFen%100)
}

func parseCNYFen(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" || strings.HasPrefix(value, "-") || strings.HasPrefix(value, "+") {
		return 0, fmt.Errorf("invalid CNY amount")
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" {
		return 0, fmt.Errorf("invalid CNY amount")
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid CNY amount")
	}
	fraction := "00"
	if len(parts) == 2 {
		if len(parts[1]) == 0 || len(parts[1]) > 2 {
			return 0, fmt.Errorf("invalid CNY amount")
		}
		fraction = parts[1] + strings.Repeat("0", 2-len(parts[1]))
	}
	cents, err := strconv.ParseInt(fraction, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid CNY amount")
	}
	if whole > ((1<<63-1)-cents)/100 {
		return 0, fmt.Errorf("invalid CNY amount")
	}
	return whole*100 + cents, nil
}

func parseAlipayTime(value string) time.Time {
	if parsed, err := time.ParseInLocation(alipayTimeLayout, strings.TrimSpace(value), alipayChinaLocation); err == nil {
		return parsed.UTC()
	}
	return time.Now().UTC()
}

func readLimitedBody(body io.Reader, maxBytes int64) ([]byte, error) {
	if body == nil {
		return nil, fmt.Errorf("request body is missing")
	}
	limited := io.LimitReader(body, maxBytes+1)
	payload, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > maxBytes {
		return nil, fmt.Errorf("request body is too large")
	}
	return payload, nil
}

func envBoolValue(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on", "enabled":
		return true
	default:
		return false
	}
}

func truncateUTF8Bytes(value string, maxBytes int) string {
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
