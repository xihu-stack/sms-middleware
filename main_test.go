package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func mustAllow(t *testing.T, s string) []netip.Prefix {
	t.Helper()
	p, err := parseAllowlist(s)
	if err != nil {
		t.Fatalf("parseAllowlist(%q) error: %v", s, err)
	}
	return p
}

func TestParseAllowlist(t *testing.T) {
	pfx, err := parseAllowlist("10.0.0.0/8, 192.168.1.1 , 8.8.8.8")
	if err != nil {
		t.Fatal(err)
	}
	if len(pfx) != 3 {
		t.Fatalf("got %d prefixes, want 3", len(pfx))
	}
	// bare IPv4 -> /32
	bare := pfx[2]
	if bare.Bits() != 32 {
		t.Errorf("bare IP should be /32, got bits=%d", bare.Bits())
	}
	if !bare.Contains(netipMustAddr(t, "8.8.8.8")) {
		t.Errorf("8.8.8.8 not contained in %v", bare)
	}
	// CIDR should be masked to network bits
	if pfx[0].Bits() != 8 {
		t.Errorf("10.0.0.0/8 bits=%d", pfx[0].Bits())
	}
}

func netipMustAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("parse addr %q: %v", s, err)
	}
	return a
}

func TestParseAllowlist_Empty(t *testing.T) {
	pfx, err := parseAllowlist("   ")
	if err != nil {
		t.Fatal(err)
	}
	if pfx != nil {
		t.Errorf("expected nil for empty allowlist, got %v", pfx)
	}
}

func TestParseAllowlist_Invalid(t *testing.T) {
	if _, err := parseAllowlist("not-an-ip"); err == nil {
		t.Error("expected error for invalid entry")
	}
}

func TestIPAllowed(t *testing.T) {
	allow, _ := parseAllowlist("10.0.0.0/8,8.8.8.8")
	yes := func(remote string) bool {
		ip, ok := clientIP(remote)
		if !ok {
			return false
		}
		return ipAllowed(ip, allow)
	}
	if !yes("10.1.2.3:1") {
		t.Error("10.1.2.3 should be allowed by 10.0.0.0/8")
	}
	if !yes("8.8.8.8:1") {
		t.Error("8.8.8.8 should be allowed")
	}
	if yes("1.2.3.4:1") {
		t.Error("1.2.3.4 should be denied")
	}
	// fail-closed: empty allowlist denies everything
	if ipAllowed(netip.Addr{}, nil) {
		t.Error("empty allowlist must deny (fail-closed)")
	}
}

func TestClientIP(t *testing.T) {
	ip, ok := clientIP("1.2.3.4:5678")
	if !ok || ip.String() != "1.2.3.4" {
		t.Errorf("clientIP parse failed: %v %v", ip, ok)
	}
}

func TestMaskPhone(t *testing.T) {
	cases := map[string]string{
		"13800138000": "138****8000",
		"1234567":     "*******", // <=7 -> fully masked
		"123456":      "******",
		"":            "",
		"8613800138000": "861******8000", // 13 chars: first 3, last 4, mask 6
	}
	for in, want := range cases {
		if got := maskPhone(in); got != want {
			t.Errorf("maskPhone(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- HTTP handler tests ---

func TestHealth(t *testing.T) {
	a := &app{cfg: Config{}, log: discardLogger()}
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Errorf("unexpected body: %s", rec.Body.String())
	}
}

func TestIPFilter_DeniesWhenNotAllowed(t *testing.T) {
	a := &app{cfg: Config{Allowlist: nil}, log: discardLogger()} // fail-closed
	req := httptest.NewRequest(http.MethodPost, "/sms/send",
		strings.NewReader(`{"to":"13800138000","content":"hi"}`))
	req.RemoteAddr = "9.9.9.9:1"
	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("got %d, want 403", rec.Code)
	}
}

func TestSendHandler_BadRequest(t *testing.T) {
	a := &app{cfg: Config{Allowlist: mustAllow(t, "127.0.0.1")}, log: discardLogger()}
	// missing content
	req := httptest.NewRequest(http.MethodPost, "/sms/send",
		strings.NewReader(`{"to":"13800138000"}`))
	req.RemoteAddr = "127.0.0.1:1"
	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	// invalid JSON
	req2 := httptest.NewRequest(http.MethodPost, "/sms/send", strings.NewReader(`{bad`))
	req2.RemoteAddr = "127.0.0.1:1"
	rec2 := httptest.NewRecorder()
	a.routes().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusBadRequest {
		t.Errorf("got %d, want 400 for invalid json", rec2.Code)
	}
}

func TestSendHandler_SuccessAndUpstreamError(t *testing.T) {
	// stub that returns OK for phone A and a business error for phone B
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		phone := r.URL.Query().Get("PhoneNumbers")
		if phone == "13800000000" {
			_, _ = io.WriteString(w, `{"Code":"OK","BizId":"b1","RequestId":"r1"}`)
			return
		}
		_, _ = io.WriteString(w, `{"Code":"isv.MOBILE_NUMBER_ILLEGAL","Message":"bad","RequestId":"r2"}`)
	}))
	defer srv.Close()

	cfg := Config{
		Allowlist: mustAllow(t, "127.0.0.1"),
		Timeout:   2 * time.Second,
		Aliyun: AliyunConfig{
			AccessKeyID: "id", AccessKeySecret: "s",
			SignName: "S", TemplateCode: "T", TemplateParamKey: "content",
			Endpoint: srv.URL,
		},
	}
	a := &app{cfg: cfg, log: discardLogger(), client: srv.Client()}

	// success
	req := httptest.NewRequest(http.MethodPost, "/sms/send",
		strings.NewReader(`{"to":"13800000000","content":"告警：CPU 95%"}`))
	req.RemoteAddr = "127.0.0.1:1"
	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("OK case: got %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	// upstream business error -> 502
	req2 := httptest.NewRequest(http.MethodPost, "/sms/send",
		strings.NewReader(`{"to":"13811111111","content":"告警"}`))
	req2.RemoteAddr = "127.0.0.1:1"
	rec2 := httptest.NewRecorder()
	a.routes().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusBadGateway {
		t.Errorf("error case: got %d, want 502 (body=%s)", rec2.Code, rec2.Body.String())
	}
	if !strings.Contains(rec2.Body.String(), "isv.MOBILE_NUMBER_ILLEGAL") {
		t.Errorf("error body should expose aliyun code: %s", rec2.Body.String())
	}
}

func TestSendHandler_TemplateParamKeyConfigurable(t *testing.T) {
	var gotTpl string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTpl = r.URL.Query().Get("TemplateParam")
		_, _ = io.WriteString(w, `{"Code":"OK","BizId":"b","RequestId":"r"}`)
	}))
	defer srv.Close()
	cfg := Config{
		Allowlist: mustAllow(t, "127.0.0.1"),
		Timeout:   2 * time.Second,
		Aliyun: AliyunConfig{
			AccessKeyID: "id", AccessKeySecret: "s",
			SignName: "S", TemplateCode: "T", TemplateParamKey: "msg",
			Endpoint: srv.URL,
		},
	}
	a := &app{cfg: cfg, log: discardLogger(), client: srv.Client()}
	req := httptest.NewRequest(http.MethodPost, "/sms/send",
		strings.NewReader(`{"to":"13800000000","content":"hello"}`))
	req.RemoteAddr = "127.0.0.1:1"
	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", rec.Code)
	}
	if gotTpl != `{"msg":"hello"}` {
		t.Errorf("TemplateParam should use configured key 'msg', got %q", gotTpl)
	}
}

func bufferLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, nil)), &buf
}

func TestStatusWriter(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: rec}
	sw.WriteHeader(http.StatusNotFound)
	if sw.status != http.StatusNotFound {
		t.Errorf("status=%d want 404", sw.status)
	}
	if _, err := sw.Write([]byte("hi")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if sw.bytes != 2 {
		t.Errorf("bytes=%d want 2", sw.bytes)
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("underlying status=%d want 404", rec.Code)
	}
}

func TestAccessLog(t *testing.T) {
	log, buf := bufferLogger()
	a := &app{cfg: Config{}, log: log}
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	a.accessLog(http.HandlerFunc(a.health)).ServeHTTP(rec, req)
	if !strings.Contains(buf.String(), "http request") {
		t.Errorf("missing access log line: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "status=200") {
		t.Errorf("missing status=200: %s", buf.String())
	}
}

func TestAccessLog_WarnsOn4xx(t *testing.T) {
	log, buf := bufferLogger()
	a := &app{cfg: Config{Allowlist: nil}, log: log} // deny all -> 403
	req := httptest.NewRequest(http.MethodPost, "/sms/send", strings.NewReader(`{"to":"1","content":"x"}`))
	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, req)
	if !strings.Contains(buf.String(), "status=403") {
		t.Errorf("expected status=403: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "level=WARN") {
		t.Errorf("expected WARN level for 4xx: %s", buf.String())
	}
}

func TestSendHandler_LogsBadRequest(t *testing.T) {
	log, buf := bufferLogger()
	a := &app{cfg: Config{Allowlist: mustAllow(t, "127.0.0.1")}, log: log}
	req := httptest.NewRequest(http.MethodPost, "/sms/send", strings.NewReader(`{bad`))
	req.RemoteAddr = "127.0.0.1:1"
	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d want 400", rec.Code)
	}
	if !strings.Contains(buf.String(), "bad request") {
		t.Errorf("expected 'bad request' log: %s", buf.String())
	}
}

func TestTruncateContent(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"abc", 0, "abc"},      // no limit
		{"abc", 5, "abc"},      // under limit
		{"abcde", 5, "abcde"},  // equal -> no truncation
		{"abcdef", 5, "abcd…"}, // over -> max-1 runes + ellipsis
		{"ab", 1, "…"},
		{"服务器CPU使用率95%超过阈值请立刻处理", 6, "服务器CP…"}, // rune-aware (中文按字符计)
	}
	for _, c := range cases {
		if got := truncateContent(c.in, c.max); got != c.want {
			t.Errorf("truncateContent(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}

func TestSendHandler_TruncatesLongContent(t *testing.T) {
	var gotTpl string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTpl = r.URL.Query().Get("TemplateParam")
		_, _ = io.WriteString(w, `{"Code":"OK","BizId":"b","RequestId":"r"}`)
	}))
	defer srv.Close()

	cfg := Config{
		Allowlist: mustAllow(t, "127.0.0.1"),
		Timeout:   2 * time.Second,
		SMSMaxLen: 10,
		Aliyun: AliyunConfig{
			AccessKeyID: "id", AccessKeySecret: "s",
			SignName: "S", TemplateCode: "T", TemplateParamKey: "content",
			Endpoint: srv.URL,
		},
	}
	log, buf := bufferLogger()
	a := &app{cfg: cfg, log: log, client: srv.Client()}

	req := httptest.NewRequest(http.MethodPost, "/sms/send",
		strings.NewReader(`{"to":"13800000000","content":"abcdefghijklmn"}`)) // 14 chars
	req.RemoteAddr = "127.0.0.1:1"
	rec := httptest.NewRecorder()
	a.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("got %d want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	// 14 chars, max 10 -> 9 chars + "…"
	want := `{"content":"abcdefghi…"}`
	if gotTpl != want {
		t.Errorf("TemplateParam = %q, want %q", gotTpl, want)
	}
	if !strings.Contains(buf.String(), "content truncated") {
		t.Errorf("expected truncation log: %s", buf.String())
	}
}
