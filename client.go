package bizgo

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Environment is a Bizgo API server.
type Environment string

const (
	// Production sends real messages. It is the default.
	Production Environment = "https://mars.ibapi.kr"
	// Sandbox uses the same API key as production but delivers nothing.
	Sandbox Environment = "https://sandbox-mars.ibapi.kr"
)

const (
	// APIKeyEnv is the environment variable read when no API key option is given.
	APIKeyEnv = "BIZGO_API_KEY" //nolint:gosec // G101: the name of an environment variable, not a credential

	// DefaultTimeout is the time limit for one attempt of a request.
	DefaultTimeout = 30 * time.Second
	// DefaultConnectTimeout is the time limit for opening a connection (default HTTP client only).
	DefaultConnectTimeout = 5 * time.Second
	// DefaultMaxRetries is the number of retries after the first attempt.
	DefaultMaxRetries = 2
)

// secret keeps the API key out of fmt and reflection-based dumps.
type secret string

func (secret) String() string   { return "[REDACTED]" }
func (secret) GoString() string { return `"[REDACTED]"` }

// Client is a Bizgo Communication API client. Create it with [NewClient].
//
// A Client is safe for concurrent use. Its settings, including the API key and the rate limit
// buckets, belong to the instance; nothing is shared between clients.
//
// The resources are the fields of the embedded [Services], one per resource of the API:
// client.Send, client.Files, client.Reports, client.Messages, client.Reservations,
// client.Alimtalk.Templates, client.BrandMessage.GroupSends, client.RCS.Templates,
// client.Insights.Alimtalk, client.Counsel.Messages, ... ([Operations] lists every operation).
type Client struct {
	Services

	t *transport
}

// Option configures a [Client].
type Option func(*config) error

type config struct {
	apiKey      *string
	baseURL     string
	timeout     time.Duration
	maxRetries  int
	httpClient  *http.Client
	logger      *slog.Logger
	environment Environment
	app         string
	rateLimit   bool
	sendRate    float64
	otherRate   float64
	hooks       []Hooks
	trustedHTTP bool
	proxy       *url.URL
	rootCAs     *x509.CertPool
}

// WithAPIKey sets the API key issued in the Bizgo console. Without it, [APIKeyEnv] is used.
// Pass the key only, without a "Bearer " or "ApiKey " prefix. Never hard-code it.
func WithAPIKey(key string) Option {
	return func(c *config) error {
		c.apiKey = &key
		return nil
	}
}

// WithEnvironment selects [Production] (default) or [Sandbox].
func WithEnvironment(env Environment) Option {
	return func(c *config) error {
		if env != Production && env != Sandbox {
			return &ConfigurationError{msg: "알 수 없는 환경입니다. bizgo.Production 또는 bizgo.Sandbox를 쓰세요(다른 서버는 WithBaseURL)"}
		}
		c.environment = env
		return nil
	}
}

// WithBaseURL overrides the server URL. It must be https (http is allowed only for localhost,
// for mock servers in tests) and must not contain user info, a query or a fragment.
func WithBaseURL(baseURL string) Option {
	return func(c *config) error {
		c.baseURL = baseURL
		return nil
	}
}

// WithTimeout sets the time limit for one attempt of a request (default 30s).
func WithTimeout(d time.Duration) Option {
	return func(c *config) error {
		if d <= 0 {
			return &ConfigurationError{msg: "timeout은 0보다 커야 합니다"}
		}
		c.timeout = d
		return nil
	}
}

// WithMaxRetries sets how many times a failed request is retried (default 2, 0 disables).
// Sends without an idempotency key are retried only on HTTP 429.
func WithMaxRetries(n int) Option {
	return func(c *config) error {
		if n < 0 {
			return &ConfigurationError{msg: "maxRetries는 0 이상이어야 합니다"}
		}
		c.maxRetries = n
		return nil
	}
}

// WithHTTPClient uses your own *http.Client (SDK-DESIGN.md §12.6). For a proxy or custom CA
// certificates prefer [WithProxy] and [WithRootCAs] / [WithRootCAsFile], which keep the SDK's own
// transport.
//
// The SDK accepts only a client it can check: its Transport must be nil (http.DefaultTransport) or
// a *http.Transport whose TLS configuration does not skip certificate verification (or the
// bizgotest fake), and it must have no cookie Jar and no CheckRedirect function. Anything else is a
// [*ConfigurationError]; a RoundTripper the SDK cannot inspect needs [WithTrustedHTTPClient].
//
// The client is copied: your value is not modified. The copy never follows redirects (so the
// Authorization header can never be sent to another URL). Its Timeout is kept if set; the
// per-attempt timeout of [WithTimeout] applies as well. The SDK sets Authorization, User-Agent,
// X-Bizgo-Client, Accept and Content-Type on every request itself, last.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *config) error {
		if hc == nil {
			return &ConfigurationError{msg: "httpClient가 nil입니다"}
		}
		c.httpClient, c.trustedHTTP = hc, false
		return nil
	}
}

// WithTrustedHTTPClient uses your own *http.Client without checking its Transport, for
// instrumentation wrappers and other RoundTrippers the SDK cannot inspect.
//
// You guarantee that the Transport does not follow redirects, retry requests, add or change
// authentication, or send the requests anywhere else. The SDK cannot enforce this: a RoundTripper
// that retries can deliver a message more than once, and one that logs or forwards requests sees
// the Authorization header (the API key) and the phone numbers in the bodies. The SDK still refuses
// cookie jars and CheckRedirect functions, never follows 3xx responses, and sets its headers last.
func WithTrustedHTTPClient(hc *http.Client) Option {
	return func(c *config) error {
		if hc == nil {
			return &ConfigurationError{msg: "httpClient가 nil입니다"}
		}
		c.httpClient, c.trustedHTTP = hc, true
		return nil
	}
}

// WithProxy sends the requests through an HTTP(S) or SOCKS5 proxy, for example
// "http://proxy.example.internal:3128" (user info in the URL is used for proxy authentication).
// It applies to the SDK's own transport and cannot be combined with WithHTTPClient.
func WithProxy(proxyURL string) Option {
	return func(c *config) error {
		u, err := url.Parse(proxyURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5") {
			return &ConfigurationError{msg: "WithProxy: http://, https:// 또는 socks5:// 프록시 주소를 넣으세요"}
		}
		c.proxy = u
		return nil
	}
}

// WithRootCAs verifies the server certificate with these CA certificates instead of the system
// store (for TLS-inspecting corporate proxies). Verification can never be turned off.
func WithRootCAs(pool *x509.CertPool) Option {
	return func(c *config) error {
		if pool == nil {
			return &ConfigurationError{msg: "WithRootCAs: CertPool이 nil입니다"}
		}
		c.rootCAs = pool
		return nil
	}
}

// WithRootCAsFile is [WithRootCAs] with the certificates of a PEM file.
func WithRootCAsFile(path string) Option {
	return func(c *config) error {
		data, err := os.ReadFile(path) //nolint:gosec // G304: a CA file chosen by the developer
		if err != nil {
			return &ConfigurationError{msg: "WithRootCAsFile: " + fileProblem(err) + ": " + filepath.Base(path)}
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(data) {
			return &ConfigurationError{msg: "WithRootCAsFile: PEM 인증서가 없습니다: " + filepath.Base(path)}
		}
		c.rootCAs = pool
		return nil
	}
}

// fakeTransport is implemented by the bizgotest fake, so that WithHTTPClient accepts it.
type fakeTransport interface{ BizgoTestTransport() }

// checkHTTPClient refuses clients whose settings the SDK cannot check or make safe.
func checkHTTPClient(hc *http.Client, trusted bool) error {
	if hc.Jar != nil {
		return &ConfigurationError{msg: "httpClient에 쿠키 Jar가 있습니다. Jar 없이 넘기세요"}
	}
	if hc.CheckRedirect != nil {
		return &ConfigurationError{msg: "httpClient에 CheckRedirect가 있습니다. SDK는 리다이렉트를 따르지 않으므로 CheckRedirect 없이 넘기세요"}
	}
	if trusted {
		return nil
	}
	switch tr := hc.Transport.(type) {
	case nil, fakeTransport:
		return nil
	case *http.Transport:
		if tr.TLSClientConfig != nil && tr.TLSClientConfig.InsecureSkipVerify {
			return &ConfigurationError{msg: "httpClient의 Transport가 TLS 인증서 검증을 끕니다(InsecureSkipVerify). 사내 CA는 WithRootCAs를 쓰세요"}
		}
		return nil
	}
	return &ConfigurationError{msg: "SDK가 점검할 수 없는 httpClient Transport입니다. 프록시·CA는 WithProxy·WithRootCAs를 쓰고, " +
		"꼭 필요하면 위험(재시도·리다이렉트·인증 변경을 하지 않아야 함)을 확인한 뒤 WithTrustedHTTPClient를 쓰세요"}
}

// WithLogger logs one line per HTTP attempt at debug level:
//
//	POST /api/comm/v1/send/omni -> 200 (35 ms, attempt 1)
//
// Nothing else is logged: no API key, body, query string or phone number. Default: silent. A
// panic in the log handler is recovered and ignored.
func WithLogger(l *slog.Logger) Option {
	return func(c *config) error {
		c.logger = l
		return nil
	}
}

// WithAppInfo adds your application to the User-Agent header (" app/<name>-<version>"), so that
// Bizgo can tell which of your applications sent a request. name must match ^[A-Za-z0-9._-]{1,50}$
// and version ^[A-Za-z0-9._+-]{1,30}$, otherwise NewClient returns a [*ConfigurationError]. Do not
// put e-mail addresses, phone numbers or other personal data in them.
func WithAppInfo(name, version string) Option {
	return func(c *config) error {
		if !validToken(name, 50, "._-") || !validToken(version, 30, "._+-") {
			return &ConfigurationError{msg: "WithAppInfo: name은 영문·숫자·._- 1~50자, version은 영문·숫자·._+- 1~30자만 쓸 수 있습니다"}
		}
		c.app = name + "-" + version
		return nil
	}
}

func validToken(s string, maxLen int, extra string) bool {
	if s == "" || len(s) > maxLen {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && !strings.ContainsRune(extra, r) {
			return false
		}
	}
	return true
}

// WithRateLimit sets the client-side rate limits. Default (on): [DefaultSendRate] messages per
// second for the send operations, counted per recipient, and [DefaultOtherRate] requests per second
// for every other operation. Both must be finite and greater than 0.
//
// The limits pace this Client in this process only. Bizgo counts per account: if several clients,
// processes or servers share the API key, lower the rates or coordinate them yourself. HTTP 429 is
// still retried.
func WithRateLimit(sendPerSecond, otherPerSecond float64) Option {
	return func(c *config) error {
		if !validRate(sendPerSecond) || !validRate(otherPerSecond) {
			return &ConfigurationError{msg: "WithRateLimit: 초당 한도는 0보다 큰 유한한 숫자여야 합니다"}
		}
		c.rateLimit, c.sendRate, c.otherRate = true, sendPerSecond, otherPerSecond
		return nil
	}
}

// WithoutRateLimit turns the client-side rate limit off (HTTP 429 is still retried).
func WithoutRateLimit() Option {
	return func(c *config) error {
		c.rateLimit = false
		return nil
	}
}

// WithHooks adds observability hooks (see [Hooks]). The option can be given more than once; hooks run in order.
func WithHooks(hooks ...Hooks) Option {
	return func(c *config) error {
		c.hooks = append(c.hooks, hooks...)
		return nil
	}
}

// NewClient creates a client.
//
//	client, err := bizgo.NewClient(bizgo.WithEnvironment(bizgo.Sandbox)) // key from BIZGO_API_KEY
//
// It returns a [*ConfigurationError] when the API key is missing or is not printable ASCII without
// spaces, or the base URL is not https.
func NewClient(opts ...Option) (*Client, error) {
	c := &config{timeout: DefaultTimeout, maxRetries: DefaultMaxRetries, environment: Production,
		rateLimit: true, sendRate: DefaultSendRate, otherRate: DefaultOtherRate}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(c); err != nil {
			return nil, err
		}
	}
	if c.httpClient != nil {
		if c.proxy != nil || c.rootCAs != nil {
			return nil, &ConfigurationError{msg: "WithProxy·WithRootCAs는 SDK의 기본 HTTP 클라이언트에만 쓸 수 있습니다(WithHTTPClient와 함께 쓸 수 없음)"}
		}
		if err := checkHTTPClient(c.httpClient, c.trustedHTTP); err != nil {
			return nil, err
		}
	}
	key, err := resolveAPIKey(c.apiKey)
	if err != nil {
		return nil, err
	}
	raw := c.baseURL
	if raw == "" {
		raw = string(c.environment)
	}
	base, err := resolveBaseURL(raw)
	if err != nil {
		return nil, err
	}
	t := &transport{
		apiKey:     secret(key),
		baseURL:    base,
		timeout:    c.timeout,
		maxRetries: c.maxRetries,
		http:       httpClient(c.httpClient, c.proxy, c.rootCAs),
		logger:     c.logger,
		sleep:      sleepContext,
		userAgent:  buildUserAgent(c.app),
		hooks:      c.hooks,
	}
	if c.rateLimit {
		t.limiter = newRateLimiter(c.sendRate, c.otherRate, time.Now)
	}
	return &Client{Services: newServices(t), t: t}, nil
}

// BaseURL returns the server URL the client sends to ("" for a zero Client).
func (c *Client) BaseURL() string {
	if c == nil || c.t == nil {
		return ""
	}
	return c.t.baseURL
}

// String describes the client without the API key. It is safe on a nil or zero Client.
func (c *Client) String() string {
	if c == nil || c.t == nil {
		return "bizgo.Client(not initialized: use bizgo.NewClient)"
	}
	return "bizgo.Client(" + c.t.baseURL + ")"
}

// GoString describes the client without the API key.
func (c *Client) GoString() string { return c.String() }

// Format prints a Client, or a dereferenced Client value, as String, so that no field (not even the
// transport pointer) is printed with %v, %+v or %#v.
func (c Client) Format(f fmt.State, _ rune) { _, _ = fmt.Fprint(f, (&c).String()) }

// sdkClient is the X-Bizgo-Client header (SDK-DESIGN.md §2.1).
const sdkClient = "bizgo-sdk-comm-go/" + Version

// buildUserAgent is "bizgo-sdk-comm-go/<ver> go/<ver> (<os>; <arch>)[ app/<name>-<ver>]". Only
// coarse OS and architecture names are sent (no host name, user name or kernel version).
func buildUserAgent(app string) string {
	ua := sdkClient + " go/" + strings.TrimPrefix(runtime.Version(), "go") + " (" + osName(runtime.GOOS) + "; " + archName(runtime.GOARCH) + ")"
	if app != "" {
		ua += " app/" + app
	}
	return ua
}

func osName(goos string) string {
	switch goos {
	case "linux", "windows", "darwin", "freebsd":
		return goos
	}
	return "other"
}

func archName(goarch string) string {
	switch goarch {
	case "amd64":
		return "x64"
	case "arm64":
		return "arm64"
	case "386":
		return "x86"
	case "arm":
		return "arm"
	}
	return "other"
}

func resolveAPIKey(explicit *string) (string, error) {
	var key string
	if explicit != nil {
		key = *explicit
	} else {
		key = os.Getenv(APIKeyEnv)
	}
	if key == "" {
		return "", &ConfigurationError{msg: "API Key가 없습니다. WithAPIKey로 넘기거나 환경변수 " + APIKeyEnv +
			"에 설정하세요. 키는 코드에 직접 쓰지 말고 환경변수나 시크릿 저장소에서 읽어 오세요"}
	}
	// ^[\x21-\x7e]+$, not trimmed silently: a key with a trailing newline usually means a broken secret file
	for i := 0; i < len(key); i++ {
		switch b := key[i]; {
		case b == ' ' || b == '\t' || b == '\n' || b == '\r':
			return "", &ConfigurationError{msg: "API Key에 공백이나 줄바꿈이 있습니다. 'Bearer '나 'ApiKey ' 같은 접두어 없이 키만 넣고, " +
				"앞뒤 공백·줄바꿈을 지우세요"}
		case b < 0x21 || b > 0x7e:
			return "", &ConfigurationError{msg: "API Key에 쓸 수 없는 문자가 있습니다(출력 가능한 ASCII만 허용)"}
		}
	}
	return key, nil
}

var localHosts = map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}

func resolveBaseURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || u.Host == "" || u.Opaque != "" {
		return "", &ConfigurationError{msg: "baseURL이 올바른 URL이 아닙니다"}
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || strings.Contains(raw, "#") {
		return "", &ConfigurationError{msg: "baseURL에는 사용자 정보, 쿼리, fragment를 넣을 수 없습니다"}
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !localHosts[u.Hostname()]) {
		// The API key travels in a header, so it must never be sent over plain HTTP.
		return "", &ConfigurationError{msg: fmt.Sprintf("baseURL은 https여야 합니다: %s://%s", u.Scheme, u.Hostname())}
	}
	return u.String(), nil
}

// refuseRedirect keeps the Authorization header from ever leaving the base URL.
func refuseRedirect(req *http.Request, _ []*http.Request) error {
	status := 0
	if req.Response != nil {
		status = req.Response.StatusCode
	}
	return &redirectError{status: status}
}

func httpClient(user *http.Client, proxy *url.URL, rootCAs *x509.CertPool) *http.Client {
	if user != nil {
		cp := *user // shallow copy: the caller's client is not modified
		cp.CheckRedirect = refuseRedirect
		cp.Jar = nil
		return &cp
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	var tr *http.Transport
	if ok {
		tr = base.Clone()
	} else {
		tr = &http.Transport{Proxy: http.ProxyFromEnvironment}
	}
	tr.DialContext = (&net.Dialer{Timeout: DefaultConnectTimeout, KeepAlive: 30 * time.Second}).DialContext
	tr.TLSHandshakeTimeout = DefaultConnectTimeout * 2
	if proxy != nil {
		tr.Proxy = http.ProxyURL(proxy)
	}
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: rootCAs} // RootCAs nil: system store
	return &http.Client{Transport: tr, CheckRedirect: refuseRedirect}
}
