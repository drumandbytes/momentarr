package model

const (
	StatusOK    = "ok"
	StatusError = "error"
	Version     = "1.0.1" // x-release-please-version
)

type Proxy struct {
	URL      string `json:"url,omitempty"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

type Cookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain,omitempty"`
	Path     string  `json:"path,omitempty"`
	Expires  float64 `json:"expires,omitempty"`
	HTTPOnly bool    `json:"httpOnly,omitempty"`
	Secure   bool    `json:"secure,omitempty"`
	SameSite string  `json:"sameSite,omitempty"`
}

type V1Request struct {
	Cmd               string   `json:"cmd"`
	URL               string   `json:"url,omitempty"`
	Cookies           []Cookie `json:"cookies,omitempty"`
	MaxTimeout        int      `json:"maxTimeout,omitempty"`
	Proxy             *Proxy   `json:"proxy,omitempty"`
	Session           string   `json:"session,omitempty"`
	SessionTTLMinutes int      `json:"session_ttl_minutes,omitempty"`
	PostData          string   `json:"postData,omitempty"`
	ReturnOnlyCookies bool     `json:"returnOnlyCookies,omitempty"`
	ReturnScreenshot  bool     `json:"returnScreenshot,omitempty"`
	WaitInSeconds     int      `json:"waitInSeconds,omitempty"`
	DisableMedia      *bool    `json:"disableMedia,omitempty"`
	TabsTillVerify    *int     `json:"tabs_till_verify,omitempty"`
}

type Solution struct {
	URL            string            `json:"url"`
	Status         int               `json:"status"`
	Headers        map[string]string `json:"headers"`
	Response       string            `json:"response,omitempty"`
	Cookies        []Cookie          `json:"cookies"`
	UserAgent      string            `json:"userAgent"`
	Screenshot     string            `json:"screenshot,omitempty"`
	TurnstileToken string            `json:"turnstile_token,omitempty"`
}

type V1Response struct {
	Status         string    `json:"status"`
	Message        string    `json:"message"`
	Session        string    `json:"session,omitempty"`
	Sessions       []string  `json:"sessions,omitempty"`
	StartTimestamp int64     `json:"startTimestamp"`
	EndTimestamp   int64     `json:"endTimestamp"`
	Version        string    `json:"version"`
	Solution       *Solution `json:"solution,omitempty"`
}

type IndexResponse struct {
	Msg       string `json:"msg"`
	Version   string `json:"version"`
	UserAgent string `json:"userAgent"`
}

type HealthResponse struct {
	Status string `json:"status"`
}
