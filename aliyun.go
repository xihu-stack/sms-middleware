package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// AliyunConfig holds the credentials and template settings used to call Aliyun SMS.
type AliyunConfig struct {
	AccessKeyID      string
	AccessKeySecret  string
	SignName         string
	TemplateCode     string
	TemplateParamKey string // variable name declared in the SMS template, e.g. "content"
	RegionID         string
	Endpoint         string // e.g. https://dysmsapi.aliyuncs.com
}

// SendResult is the parsed response returned by Aliyun SendSms.
type SendResult struct {
	Code      string `json:"Code"`      // "OK" on success; otherwise an error code (e.g. isv.BUSINESS_LIMIT_CONTROL)
	Message   string `json:"Message"`
	BizId     string `json:"BizId"`
	RequestId string `json:"RequestId"`
}

// buildParams assembles the full RPC parameter map for one SendSms call.
// nonce/timestamp are generated here; for deterministic tests call signQuery directly.
func buildParams(cfg AliyunConfig, phone, templateParam string) map[string]string {
	return map[string]string{
		"AccessKeyId":      cfg.AccessKeyID,
		"Format":           "JSON",
		"Version":          "2017-05-25",
		"SignatureMethod":  "HMAC-SHA1",
		"SignatureVersion": "1.0",
		"SignatureNonce":   newNonce(),
		"Timestamp":        time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		"Action":           "SendSms",
		"RegionId":         cfg.RegionID,
		"PhoneNumbers":     phone,
		"SignName":         cfg.SignName,
		"TemplateCode":     cfg.TemplateCode,
		"TemplateParam":    templateParam,
	}
}

// newNonce returns a random hex string used as SignatureNonce.
func newNonce() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// percentEncode encodes a string per Aliyun's RPC signature spec:
// keep A-Z a-z 0-9 - _ . ~ unescaped; every other byte becomes %HH (uppercase).
// It operates on bytes, so UTF-8 multibyte characters (e.g. Chinese) are encoded
// byte-by-byte, which is what Aliyun expects.
func percentEncode(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			b.WriteString(fmt.Sprintf("%%%02X", c))
		}
	}
	return b.String()
}

// signQuery builds the percent-encoded query string including the Signature
// parameter, following Aliyun's RPC signature algorithm (HMAC-SHA1).
// Pure function: same inputs always produce the same output.
func signQuery(params map[string]string, secret string) string {
	encKeys := make([]string, 0, len(params))
	encValues := make(map[string]string, len(params))
	for k, v := range params {
		ek := percentEncode(k)
		encKeys = append(encKeys, ek)
		encValues[ek] = percentEncode(v)
	}
	sort.Strings(encKeys)

	var canonical strings.Builder
	for i, ek := range encKeys {
		if i > 0 {
			canonical.WriteByte('&')
		}
		canonical.WriteString(ek)
		canonical.WriteByte('=')
		canonical.WriteString(encValues[ek])
	}

	// stringToSign = HTTP_METHOD&percentEncode("/")&percentEncode(canonical)
	stringToSign := "GET&" + percentEncode("/") + "&" + percentEncode(canonical.String())

	mac := hmac.New(sha1.New, []byte(secret+"&"))
	mac.Write([]byte(stringToSign))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	var q strings.Builder
	for _, ek := range encKeys {
		q.WriteString(ek)
		q.WriteByte('=')
		q.WriteString(encValues[ek])
		q.WriteByte('&')
	}
	q.WriteString("Signature=")
	q.WriteString(percentEncode(signature))
	return q.String()
}

// SendSms calls Aliyun SendSms and returns the parsed result. The HTTP client is
// injected so tests can point it at an httptest server.
func SendSms(ctx context.Context, client *http.Client, cfg AliyunConfig, phone, templateParam string) (*SendResult, error) {
	params := buildParams(cfg, phone, templateParam)
	rawURL := strings.TrimRight(cfg.Endpoint, "/") + "/?" + signQuery(params, cfg.AccessKeySecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var r SendResult
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("aliyun response parse failed: %w (body=%q)", err, string(body))
	}
	return &r, nil
}
