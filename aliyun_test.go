package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Golden vector computed INDEPENDENTLY of this Go code via .NET HMACSHA1
// (PowerShell), against Aliyun's documented RPC signature algorithm. If this
// test fails, the signing implementation drifted from the spec.
func TestSignQueryGolden(t *testing.T) {
	params := map[string]string{
		"AccessKeyId": "testid",
		"Format":      "JSON",
		"Version":     "2017-05-25",
	}
	const want = "AccessKeyId=testid&Format=JSON&Version=2017-05-25&Signature=H2dJg8mosuHtwIS3eVj4lxRwBQE%3D"
	if got := signQuery(params, "testsecret"); got != want {
		t.Errorf("signQuery mismatch:\n got: %s\nwant: %s", got, want)
	}
}

func TestPercentEncode(t *testing.T) {
	cases := map[string]string{
		"/":       "%2F",
		"=":       "%3D",
		"&":       "%26",
		" ":       "%20",
		"+":       "%2B",
		"*":       "%2A",
		"~":       "~",
		"a1-_.~Z": "a1-_.~Z",
		"中":       "%E4%B8%AD", // UTF-8 bytes E4 B8 AD
		"ab c=d":  "ab%20c%3Dd",
	}
	for in, want := range cases {
		if got := percentEncode(in); got != want {
			t.Errorf("percentEncode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSignQueryDeterministicAndSorted(t *testing.T) {
	params := map[string]string{
		"AccessKeyId": "id",
		"B":           "2",
		"A":           "1",
	}
	first := signQuery(params, "s")
	second := signQuery(params, "s")
	if first != second {
		t.Errorf("signQuery not deterministic:\n first: %s\nsecond: %s", first, second)
	}
	// keys must appear sorted by encoded key, each value percent-encoded.
	if !strings.HasPrefix(first, "A=1&AccessKeyId=id&B=2&Signature=") {
		t.Errorf("unexpected ordering/encoding: %s", first)
	}
}

func TestSendSms_OK(t *testing.T) {
	var gotMethod, gotPath, gotAction, gotPhone, gotSign, gotTpl string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAction = r.URL.Query().Get("Action")
		gotPhone = r.URL.Query().Get("PhoneNumbers")
		gotSign = r.URL.Query().Get("Signature")
		gotTpl = r.URL.Query().Get("TemplateParam")
		_, _ = io.WriteString(w, `{"Code":"OK","Message":"OK","BizId":"123456","RequestId":"req-1"}`)
	}))
	defer srv.Close()

	cfg := AliyunConfig{
		AccessKeyID: "id", AccessKeySecret: "secret",
		SignName: "S", TemplateCode: "T", TemplateParamKey: "content",
		RegionID: "cn-hangzhou", Endpoint: srv.URL,
	}
	res, err := SendSms(context.Background(), srv.Client(), cfg, "13800138000", `{"content":"hi"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Code != "OK" || res.BizId != "123456" || res.RequestId != "req-1" {
		t.Errorf("unexpected result: %+v", res)
	}
	if gotMethod != http.MethodGet || gotPath != "/" || gotAction != "SendSms" {
		t.Errorf("request wrong: method=%q path=%q action=%q", gotMethod, gotPath, gotAction)
	}
	if gotPhone != "13800138000" {
		t.Errorf("phone mismatch: %q", gotPhone)
	}
	if gotSign == "" {
		t.Error("expected non-empty Signature on outgoing request")
	}
	if gotTpl != `{"content":"hi"}` {
		t.Errorf("template param not forwarded verbatim: %q", gotTpl)
	}
}

func TestSendSms_BusinessError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"Code":"isv.BUSINESS_LIMIT_CONTROL","Message":"limit","RequestId":"req-2"}`)
	}))
	defer srv.Close()
	cfg := AliyunConfig{AccessKeyID: "id", AccessKeySecret: "secret", SignName: "S", TemplateCode: "T", Endpoint: srv.URL}
	res, err := SendSms(context.Background(), srv.Client(), cfg, "13800138000", `{"content":"hi"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Code != "isv.BUSINESS_LIMIT_CONTROL" {
		t.Errorf("expected error code, got %q", res.Code)
	}
}

func TestSendSms_UnparseableBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `not json`)
	}))
	defer srv.Close()
	cfg := AliyunConfig{AccessKeyID: "id", AccessKeySecret: "secret", SignName: "S", TemplateCode: "T", Endpoint: srv.URL}
	if _, err := SendSms(context.Background(), srv.Client(), cfg, "13800138000", `{"content":"hi"}`); err == nil {
		t.Error("expected error for unparseable body")
	}
}
