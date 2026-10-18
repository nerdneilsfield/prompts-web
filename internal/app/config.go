package app

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// PreserveInstruction is sent as the system prompt on every optimization. It is
// the guarantee that the model keeps the original meaning and does not invent
// facts; the user-supplied scheme is appended after it.
const PreserveInstruction = `你在优化一段 prompt。硬性约束：
1. 完整保留原文的意图、任务、硬性约束和具体事实，不增删要求。
2. 原文没有给出的事实、数字、名称一律用 {{占位符}} 表示，不得编造。
3. 缺失的上下文用明确占位符标注，不要猜测。
4. 只输出优化后的 prompt 正文，不要解释、不要前言、不要 markdown 代码围栏。`

// Config carries every deployment knob. Values come from the environment in the
// main program; this package only reads them here.
type Config struct {
	// Password is the single site password. Required.
	Password string

	// DBPath is the SQLite file. Required.
	DBPath string

	// CookieSecure sets the Secure flag on the session cookie. Must be true in
	// production (HTTPS reverse proxy); false only for plain-HTTP local dev.
	CookieSecure bool

	// SessionTTL is how long a session stays valid. Sessions live in memory, so
	// a restart invalidates them regardless.
	SessionTTL time.Duration

	// AllowedOrigins are the exact scheme://host[:port] origins permitted on
	// write requests. A write with an Origin header that matches none of these
	// is rejected. A request with no Origin header (a CLI client) is allowed.
	AllowedOrigins []string

	// LoginMaxFailures and LoginWindow throttle password guessing per client IP.
	LoginMaxFailures int
	LoginWindow      time.Duration

	// RequestTimeout bounds one upstream model call. 90s in production.
	RequestTimeout time.Duration

	// BaseURL is the OpenAI-compatible base, e.g. https://api.example.com/v1.
	BaseURL string
	// APIKey authenticates upstream. It never leaves this process.
	APIKey string
	// Model is the upstream model id, recorded on every run.
	Model string

	// Sampling parameters actually sent upstream and recorded with the run.
	Temperature float64
	TopP        float64
	MaxTokens   int

	// MaxBodyBytes bounds a request body. Zero means DefaultMaxBodyBytes.
	MaxBodyBytes int64
}

const DefaultMaxBodyBytes = 1 << 20

func DefaultConfig() Config {
	return Config{
		SessionTTL:       24 * time.Hour,
		LoginMaxFailures: 8,
		LoginWindow:      10 * time.Minute,
		RequestTimeout:   90 * time.Second,
		Temperature:      0.2,
		TopP:             1,
		MaxTokens:        2048,
	}
}

func (c Config) validate() error {
	if c.Password == "" {
		return errors.New("Password is required")
	}
	if c.DBPath == "" {
		return errors.New("DBPath is required")
	}
	if c.SessionTTL <= 0 {
		return errors.New("SessionTTL must be positive")
	}
	if c.RequestTimeout <= 0 {
		return errors.New("RequestTimeout must be positive")
	}
	if c.LoginMaxFailures <= 0 || c.LoginWindow <= 0 {
		return errors.New("LoginMaxFailures and LoginWindow must be positive")
	}
	if c.BaseURL == "" || c.APIKey == "" || c.Model == "" {
		return errors.New("BaseURL, APIKey and Model are required")
	}
	for _, o := range c.AllowedOrigins {
		if !strings.HasPrefix(o, "http://") && !strings.HasPrefix(o, "https://") {
			return fmt.Errorf("AllowedOrigins entry %q must start with http:// or https://", o)
		}
	}
	if c.MaxBodyBytes < 0 {
		return errors.New("MaxBodyBytes must not be negative")
	}
	return nil
}

func (c Config) maxBodyBytes() int64 {
	if c.MaxBodyBytes > 0 {
		return c.MaxBodyBytes
	}
	return DefaultMaxBodyBytes
}
