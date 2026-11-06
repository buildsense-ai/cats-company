package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

func repairMaterialReceipt(t *testing.T, audience string, raw []byte) store.SkillHubPublicationCloudReceipt {
	t.Helper()
	r := cancellationTestLease(audience).Receipt
	r.Query.RequestHash = publicationDescriptionSHA256Hex(string(raw))
	r.Query.RequestBytes = int64(len(raw))
	if r.Validate() != nil {
		t.Fatal("invalid material receipt fixture")
	}
	return r
}
func repairMaterialConfig(r store.SkillHubPublicationCloudReceipt) publicationRepairMaterialConfig {
	return publicationRepairMaterialConfig{Enabled: true, Issuer: r.Query.Issuer, Audience: r.Query.Audience, PeerSecret: "pubmaterial_" + strings.Repeat("a", 64)}
}
func repairMaterialResponse(raw []byte) *http.Response {
	return &http.Response{StatusCode: 200, ContentLength: int64(len(raw)), Header: http.Header{
		"Content-Type": []string{"application/octet-stream"}, "Content-Length": []string{strconv.Itoa(len(raw))},
		"Cache-Control": []string{"no-store"}, "X-Content-Type-Options": []string{"nosniff"},
	}, Body: io.NopCloser(strings.NewReader(string(raw)))}
}

type repairMaterialRoundTrip func(*http.Request) (*http.Response, error)

func (f repairMaterialRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPublicationRepairMaterialClientDefaultOffAndConfig(t *testing.T) {
	r := repairMaterialReceipt(t, "https://hub.example", []byte("private"))
	for _, client := range []*publicationRepairMaterialClient{nil, {}} {
		if b, err := client.readOriginal(context.Background(), r); b != nil || !errors.Is(err, errPublicationRepairMaterialDisabled) {
			t.Fatal("disabled read", err)
		}
	}
	for _, mode := range []string{"issuer", "audience", "same_origin", "secret", "secret_upper", "secret_newline", "nil_transport"} {
		t.Run(mode, func(t *testing.T) {
			cfg := repairMaterialConfig(r)
			var transport http.RoundTripper = http.DefaultTransport
			switch mode {
			case "issuer":
				cfg.Issuer = "http://catsco.example"
			case "audience":
				cfg.Audience = "https://hub.example/path?secret=PRIVATE"
			case "same_origin":
				cfg.Issuer = cfg.Audience
			case "secret":
				cfg.PeerSecret = strings.Repeat("a", 64)
			case "secret_upper":
				cfg.PeerSecret = "pubmaterial_" + strings.Repeat("A", 64)
			case "secret_newline":
				cfg.PeerSecret += "\n"
			case "nil_transport":
				transport = nil
			}
			if c, err := newPublicationRepairMaterialClientWithTransport(cfg, transport); c != nil || !errors.Is(err, store.ErrSkillHubPublicationInvalid) {
				t.Fatal("invalid config", err)
			}
		})
	}
	c, err := newPublicationRepairMaterialClient(repairMaterialConfig(r))
	if err != nil {
		t.Fatal(err)
	}
	tr := c.client.Transport.(*http.Transport)
	if tr.Proxy != nil || !tr.DisableCompression || !tr.DisableKeepAlives || c.client.Jar != nil || c.client.Timeout != 15*time.Second {
		t.Fatal("unsafe production transport")
	}
}

func TestPublicationRepairMaterialClientExactPrivateHTTPS(t *testing.T) {
	raw := []byte("{\"private\":\"original unparsed material\"}")
	var receipt store.SkillHubPublicationCloudReceipt
	var requests atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != "POST" || r.URL.Path != publicationRepairMaterialPath || r.URL.RawQuery != "" ||
			r.Header.Get("Authorization") != "Bearer pubmaterial_"+strings.Repeat("a", 64) || r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" ||
			r.Header.Get("Accept") != "application/octet-stream" || r.Header.Get("Accept-Encoding") != "identity" || r.Header.Get("Cache-Control") != "no-store" {
			t.Error("private request boundary")
		}
		body, _ := io.ReadAll(r.Body)
		var actual map[string]json.RawMessage
		if json.Unmarshal(body, &actual) != nil || len(actual) != 2 || string(actual["schemaVersion"]) != "1" {
			t.Error("request shape")
		}
		want, _ := json.Marshal(publicationControlReceipt(receipt))
		if !reflect.DeepEqual([]byte(actual["receipt"]), want) {
			t.Error("receipt binding")
		}
		for k, values := range repairMaterialResponse(raw).Header {
			w.Header()[k] = values
		}
		w.Write(raw)
	}))
	defer s.Close()
	receipt = repairMaterialReceipt(t, s.URL, raw)
	c, err := newPublicationRepairMaterialClientWithTransport(repairMaterialConfig(receipt), s.Client().Transport)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		b, err := c.readOriginal(context.Background(), receipt)
		if err != nil || !reflect.DeepEqual(b, raw) {
			t.Fatal("exact byte read", err)
		}
		b[0] = 0
	}
	if requests.Load() != 2 {
		t.Fatal("read replay request count")
	}
	// Default trust must refuse the untrusted fixture certificate, not leak URL.
	untrusted, _ := newPublicationRepairMaterialClient(repairMaterialConfig(receipt))
	if b, err := untrusted.readOriginal(context.Background(), receipt); b != nil || err != errPublicationRepairMaterialPeer || strings.Contains(err.Error(), s.URL) {
		t.Fatal("TLS failure leak", err)
	}
}

func TestPublicationRepairMaterialClientRejectsMalformedPeer(t *testing.T) {
	raw := []byte("PRIVATE retained candidate")
	receipt := repairMaterialReceipt(t, "https://hub.example", raw)
	for _, mode := range []string{"status", "redirect", "type", "duplicate_type", "missing_length", "wrong_length", "duplicate_length", "leading_zero_length",
		"cache", "duplicate_cache", "nosniff", "duplicate_nosniff", "encoding", "empty_encoding", "uncompressed", "chunked", "short", "extra", "hash", "read_error", "transport_error", "cancelled", "late"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			c, err := newPublicationRepairMaterialClientWithTransport(repairMaterialConfig(receipt), repairMaterialRoundTrip(func(*http.Request) (*http.Response, error) {
				calls++
				r := repairMaterialResponse(raw)
				switch mode {
				case "status":
					r.StatusCode = 403
				case "redirect":
					r.StatusCode = 302
					r.Header.Set("Location", "https://other.example/PRIVATE")
				case "type":
					r.Header.Set("Content-Type", "application/json")
				case "duplicate_type":
					r.Header.Add("Content-Type", "application/octet-stream")
				case "missing_length":
					r.Header.Del("Content-Length")
				case "wrong_length":
					r.ContentLength++
				case "duplicate_length":
					r.Header.Add("Content-Length", strconv.Itoa(len(raw)))
				case "leading_zero_length":
					r.Header.Set("Content-Length", "0"+strconv.Itoa(len(raw)))
				case "cache":
					r.Header.Set("Cache-Control", "public")
				case "duplicate_cache":
					r.Header.Add("Cache-Control", "no-store")
				case "nosniff":
					r.Header.Del("X-Content-Type-Options")
				case "duplicate_nosniff":
					r.Header.Add("X-Content-Type-Options", "nosniff")
				case "encoding":
					r.Header.Set("Content-Encoding", "gzip")
				case "empty_encoding":
					r.Header["Content-Encoding"] = []string{""}
				case "uncompressed":
					r.Uncompressed = true
				case "chunked":
					r.TransferEncoding = []string{"chunked"}
				case "short":
					r.Body = io.NopCloser(strings.NewReader(string(raw[1:])))
				case "extra":
					r.Body = io.NopCloser(strings.NewReader(string(raw) + "x"))
				case "hash":
					r.Body = io.NopCloser(strings.NewReader(strings.Repeat("x", len(raw))))
				case "read_error":
					r.Body = io.NopCloser(io.MultiReader(strings.NewReader("PRIVATE"), repairMaterialErrorReader{}))
				case "transport_error":
					return nil, errors.New("PRIVATE transport credentials")
				case "late":
					cancel()
				}
				return r, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			if mode == "cancelled" {
				cancel()
			}
			got, err := c.readOriginal(ctx, receipt)
			if got != nil || err == nil || strings.Contains(err.Error(), "PRIVATE") || calls > 1 {
				t.Fatal("malformed peer returned bytes/retried/leaked", err, calls)
			}
		})
	}
}

type repairMaterialErrorReader struct{}

func (repairMaterialErrorReader) Read([]byte) (int, error) {
	return 0, errors.New("PRIVATE partial body")
}

func TestPublicationRepairMaterialClientReceiptValidationBeforeRequest(t *testing.T) {
	raw := []byte("private")
	receipt := repairMaterialReceipt(t, "https://hub.example", raw)
	calls := 0
	c, _ := newPublicationRepairMaterialClientWithTransport(repairMaterialConfig(receipt), repairMaterialRoundTrip(func(*http.Request) (*http.Response, error) { calls++; return repairMaterialResponse(raw), nil }))
	for _, mode := range []string{"issuer", "audience", "size", "hash", "entry", "revision"} {
		t.Run(mode, func(t *testing.T) {
			r := receipt
			switch mode {
			case "issuer":
				r.Query.Issuer = "https://other.example"
			case "audience":
				r.Query.Audience = "https://other.example"
			case "size":
				r.Query.RequestBytes = publicationCaptureRuntimeMaxBytes + 1
			case "hash":
				r.Query.RequestHash = "bad"
			case "entry":
				r.Query.EntryID = "bad"
			case "revision":
				r.Query.Revision = 2
			}
			if b, err := c.readOriginal(context.Background(), r); b != nil || err != store.ErrSkillHubPublicationInvalid {
				t.Fatal("invalid receipt read", err)
			}
		})
	}
	if calls != 0 {
		t.Fatal("invalid receipt sent")
	}
}

func TestPublicationRepairMaterialClientBoundsInFlightAndDisallowsRedirect(t *testing.T) {
	raw := []byte("private")
	receipt := repairMaterialReceipt(t, "https://hub.example", raw)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	c, _ := newPublicationRepairMaterialClientWithTransport(repairMaterialConfig(receipt), repairMaterialRoundTrip(func(*http.Request) (*http.Response, error) {
		entered <- struct{}{}
		<-release
		return repairMaterialResponse(raw), nil
	}))
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := c.readOriginal(context.Background(), receipt); done <- err }()
	}
	<-entered
	<-entered
	if b, err := c.readOriginal(context.Background(), receipt); b != nil || err != store.ErrSkillHubPublicationBusy {
		t.Fatal("read slots unbounded", err)
	}
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	var redirected atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == publicationRepairMaterialPath {
			http.Redirect(w, r, "/PRIVATE", 302)
			return
		}
		redirected.Add(1)
	}))
	defer s.Close()
	receipt = repairMaterialReceipt(t, s.URL, raw)
	c, _ = newPublicationRepairMaterialClientWithTransport(repairMaterialConfig(receipt), s.Client().Transport)
	if b, err := c.readOriginal(context.Background(), receipt); b != nil || err != errPublicationRepairMaterialPeer || redirected.Load() != 0 {
		t.Fatal("followed redirect", err)
	}
}
