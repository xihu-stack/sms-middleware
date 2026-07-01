package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// Config is loaded entirely from environment variables.
type Config struct {
	Listen    string
	Allowlist []netip.Prefix
	Aliyun    AliyunConfig
	Timeout   time.Duration
}

func loadConfig() (Config, error) {
	cfg := Config{
		Listen:  getenv("LISTEN", ":8080"),
		Timeout: getDuration("ALIYUN_TIMEOUT", 5*time.Second),
		Aliyun: AliyunConfig{
			AccessKeyID:      os.Getenv("ALIYUN_ACCESS_KEY_ID"),
			AccessKeySecret:  os.Getenv("ALIYUN_ACCESS_KEY_SECRET"),
			SignName:         os.Getenv("ALIYUN_SIGN_NAME"),
			TemplateCode:     os.Getenv("ALIYUN_TEMPLATE_CODE"),
			TemplateParamKey: getenv("ALIYUN_TEMPLATE_PARAM_KEY", "content"),
			RegionID:         getenv("ALIYUN_REGION", "cn-hangzhou"),
			Endpoint:         getenv("ALIYUN_ENDPOINT", "https://dysmsapi.aliyuncs.com"),
		},
	}

	var missing []string
	for k, v := range map[string]string{
		"ALIYUN_ACCESS_KEY_ID":     cfg.Aliyun.AccessKeyID,
		"ALIYUN_ACCESS_KEY_SECRET": cfg.Aliyun.AccessKeySecret,
		"ALIYUN_SIGN_NAME":         cfg.Aliyun.SignName,
		"ALIYUN_TEMPLATE_CODE":     cfg.Aliyun.TemplateCode,
	} {
		if strings.TrimSpace(v) == "" {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return cfg, fmt.Errorf("missing required env vars: %s", strings.Join(missing, ", "))
	}

	allow, err := parseAllowlist(os.Getenv("IP_ALLOWLIST"))
	if err != nil {
		return cfg, err
	}
	cfg.Allowlist = allow
	return cfg, nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

// parseAllowlist parses a comma-separated list of IPs and/or CIDRs.
// A bare IP is treated as a /32 (v4) or /128 (v6) prefix.
func parseAllowlist(s string) ([]netip.Prefix, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var out []netip.Prefix
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		pfx, err := parsePrefix(part)
		if err != nil {
			return nil, fmt.Errorf("invalid IP/CIDR %q: %w", part, err)
		}
		out = append(out, pfx)
	}
	return out, nil
}

func parsePrefix(s string) (netip.Prefix, error) {
	if pfx, err := netip.ParsePrefix(s); err == nil {
		return pfx.Masked(), nil
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return addr.Prefix(addr.BitLen())
}

// ipAllowed reports whether ip is within any allowed prefix.
// An empty allowlist denies everything (fail-closed), because the allowlist is
// the middleware's only authentication mechanism.
func ipAllowed(ip netip.Addr, allow []netip.Prefix) bool {
	if len(allow) == 0 {
		return false
	}
	for _, p := range allow {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// clientIP extracts the peer IP from a "host:port" RemoteAddr.
func clientIP(remoteAddr string) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr, true
}

// maskPhone masks the middle digits of a phone number, e.g. 13800138000 -> 138****8000.
// Numbers with 7 or fewer characters are fully masked.
func maskPhone(phone string) string {
	r := []rune(phone)
	if len(r) <= 7 {
		return strings.Repeat("*", len(r))
	}
	return string(r[:3]) + strings.Repeat("*", len(r)-7) + string(r[len(r)-4:])
}

// app holds runtime dependencies shared by HTTP handlers.
type app struct {
	cfg    Config
	log    *slog.Logger
	client *http.Client
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", a.health)
	mux.HandleFunc("POST /sms/send", a.ipFilter(a.send))
	return a.accessLog(mux)
}

// statusWriter captures the response status code and bytes written for logging.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(s int) {
	if w.status == 0 {
		w.status = s
	}
	w.ResponseWriter.WriteHeader(s)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// accessLog emits one structured log line per request (WARN when status >= 400).
func (a *app) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		lvl := slog.LevelInfo
		if sw.status >= 400 {
			lvl = slog.LevelWarn
		}
		a.log.Log(context.Background(), lvl, "http request",
			"remote", r.RemoteAddr, "method", r.Method, "path", r.URL.Path,
			"status", sw.status, "bytes", sw.bytes, "duration_ms", time.Since(start).Milliseconds())
	})
}

// ipFilter rejects requests whose source IP is not in the allowlist.
func (a *app) ipFilter(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip, ok := clientIP(r.RemoteAddr)
		if !ok || !ipAllowed(ip, a.cfg.Allowlist) {
			a.log.Warn("request denied by ip allowlist", "remote", r.RemoteAddr, "path", r.URL.Path)
			writeJSON(w, http.StatusForbidden, apiError("FORBIDDEN"))
			return
		}
		next(w, r)
	}
}

func (a *app) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type sendRequest struct {
	To      string `json:"to"`
	Content string `json:"content"`
}

func (a *app) send(w http.ResponseWriter, r *http.Request) {
	var req sendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		a.log.Warn("bad request", "reason", "invalid JSON", "err", err, "remote", r.RemoteAddr)
		writeJSON(w, http.StatusBadRequest, apiError("BAD_REQUEST", "invalid JSON body"))
		return
	}
	req.To = strings.TrimSpace(req.To)
	req.Content = strings.TrimSpace(req.Content)
	if req.To == "" || req.Content == "" {
		missing := "'to' and 'content' are required"
		switch {
		case req.To == "" && req.Content != "":
			missing = "'to' is empty"
		case req.Content == "" && req.To != "":
			missing = "'content' is empty"
		}
		a.log.Warn("bad request", "reason", missing, "remote", r.RemoteAddr)
		writeJSON(w, http.StatusBadRequest, apiError("BAD_REQUEST", "'to' and 'content' are required"))
		return
	}

	key := a.cfg.Aliyun.TemplateParamKey
	if key == "" {
		key = "content"
	}
	templateParam, _ := json.Marshal(map[string]string{key: req.Content})

	ctx, cancel := context.WithTimeout(r.Context(), a.cfg.Timeout)
	defer cancel()

	t0 := time.Now()
	res, err := SendSms(ctx, a.client, a.cfg.Aliyun, req.To, string(templateParam))
	aliyunMs := time.Since(t0).Milliseconds()
	if err != nil {
		a.log.Error("aliyun call failed", "to", maskPhone(req.To), "err", err, "aliyun_ms", aliyunMs)
		writeJSON(w, http.StatusBadGateway, apiError("UPSTREAM_UNAVAILABLE"))
		return
	}
	if res.Code != "OK" {
		a.log.Error("aliyun business error",
			"to", maskPhone(req.To), "code", res.Code, "message", res.Message,
			"request_id", res.RequestId, "aliyun_ms", aliyunMs)
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"code":        "UPSTREAM_ERROR",
			"aliyun_code": res.Code,
			"message":     res.Message,
			"request_id":  res.RequestId,
		})
		return
	}

	a.log.Info("sms sent",
		"to", maskPhone(req.To), "len", len(req.Content),
		"biz_id", res.BizId, "request_id", res.RequestId, "aliyun_ms", aliyunMs)
	writeJSON(w, http.StatusOK, map[string]any{
		"code":       "OK",
		"biz_id":     res.BizId,
		"request_id": res.RequestId,
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// apiError builds a JSON error body. Pass only a code, or a code plus a message.
func apiError(code string, msg ...string) any {
	if len(msg) == 0 {
		return map[string]string{"code": code}
	}
	return map[string]string{"code": code, "message": strings.Join(msg, ": ")}
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := loadConfig()
	if err != nil {
		log.Error("config error", "err", err)
		os.Exit(1)
	}
	if len(cfg.Allowlist) == 0 {
		log.Warn("IP_ALLOWLIST is empty: all requests will be denied (fail-closed)")
	}

	a := &app{
		cfg:    cfg,
		log:    log,
		client: &http.Client{Timeout: cfg.Timeout},
	}

	srv := &http.Server{
		Addr:         cfg.Listen,
		Handler:      a.routes(),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		log.Info("listening", "addr", cfg.Listen)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	log.Info("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	log.Info("stopped")
}
