// Package credentials manages model API credentials (PRD 3.2, F2). The
// provider, base URL and label live in credentials.json; the key lives only
// in the system credential store (Windows Credential Manager, macOS login
// Keychain) and reaches dsh as an environment variable when a runtime starts.
package credentials

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/internal/core/fsutil"
)

// SecretStore is the system credential store. MygoSecrets is the real one.
type SecretStore interface {
	Set(key string, value []byte) error
	Get(key string) ([]byte, error)
	Delete(key string) error
}

// ErrNotFound is returned by a SecretStore for a missing key.
var ErrNotFound = errors.New("secret not found")

// Provider says how a dsh provider takes its key and base URL, and which
// models it serves.
type Provider struct {
	ID      string // dsh provider route, e.g. deepseek-official
	Name    string // what the settings page calls it
	KeyEnv  string
	BaseEnv string // "" when the route takes no base URL from the environment
	// Models are a few of the route's models to pick from, the default
	// first; any other model id of the route works too.
	Models []string
	// Base is where the service lists its models (pi-ai's catalog address
	// of the route) and Listing how (see RemoteModels); "" when the
	// service has no listing Alcove knows.
	Base, Listing string
}

// Providers dsh 0.2.1-rc.5 accepts through environment variables, in the
// order the settings page lists them. deepseek-official is dsh's own
// DeepSeek adapter; the others are dsh-llm-pi-ai routes the bridge bundle
// configures (bridge/cordis.patch.yml), reading their key from pi-ai's own
// variable (its env-api-keys map). Catalog routes take their base URL from
// the catalog only; Custom takes its address, protocol and model from the
// credential.
var Providers = []Provider{
	{ID: "deepseek-official", Name: "DeepSeek", KeyEnv: "DEEPSEEK_API_KEY", BaseEnv: "DEEPSEEK_BASE_URL", Models: []string{"deepseek-v4-flash", "deepseek-v4-pro"}, Base: "https://api.deepseek.com", Listing: ListOpenAI},
	{ID: "openai", Name: "OpenAI", KeyEnv: "OPENAI_API_KEY", Models: []string{"gpt-5.5", "gpt-6.1-sol", "gpt-5.4-mini"}, Base: "https://api.openai.com/v1", Listing: ListOpenAI},
	{ID: "anthropic", Name: "Anthropic Claude", KeyEnv: "ANTHROPIC_API_KEY", Models: []string{"claude-sonnet-5-5", "claude-opus-5-5", "claude-haiku-5-5"}, Base: "https://api.anthropic.com", Listing: ListAnthropic},
	{ID: "google", Name: "Google Gemini", KeyEnv: "GEMINI_API_KEY", Models: []string{"gemini-pro-latest", "gemini-flash-latest"}, Base: "https://generativelanguage.googleapis.com/v1beta", Listing: ListGoogle},
	{ID: "xai", Name: "xAI Grok", KeyEnv: "XAI_API_KEY", Models: []string{"grok-4.7", "grok-4.6"}, Base: "https://api.x.ai/v1", Listing: ListOpenAI},
	{ID: "openrouter", Name: "OpenRouter", KeyEnv: "OPENROUTER_API_KEY", Models: []string{"~anthropic/claude-sonnet-latest", "~openai/gpt-sol-latest", "~google/gemini-pro-latest", "~deepseek/deepseek-pro-latest"}, Base: "https://openrouter.ai/api/v1", Listing: ListOpenAI},
	{ID: "moonshotai-cn", Name: "Kimi（月之暗面）", KeyEnv: "MOONSHOT_API_KEY", Models: []string{"kimi-k3", "kimi-k3.1-pro", "kimi-k2.7-code"}, Base: "https://api.moonshot.cn/v1", Listing: ListOpenAI},
	{ID: "zai-coding-cn", Name: "智谱 GLM（Coding 套餐）", KeyEnv: "ZAI_CODING_CN_API_KEY", Models: []string{"glm-5.3", "glm-5.3-flash"}, Base: "https://open.bigmodel.cn/api/coding/paas/v4", Listing: ListOpenAI},
	{ID: "qwen-token-plan-cn", Name: "阿里云百炼（Token Plan）", KeyEnv: "QWEN_TOKEN_PLAN_CN_API_KEY", Models: []string{"qwen3.8-max", "qwen3.8-flash", "qwen3.7-plus"}, Base: "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1", Listing: ListOpenAI},
	{ID: "minimax-cn", Name: "MiniMax", KeyEnv: "MINIMAX_CN_API_KEY", Models: []string{"MiniMax-M3", "MiniMax-M3.1-Flash"}},
	{ID: "xiaomi", Name: "小米 MiMo", KeyEnv: "XIAOMI_API_KEY", Models: []string{"mimo-v2.6-pro", "mimo-v2.6-flash"}, Base: "https://api.xiaomimimo.com/v1", Listing: ListOpenAI},
	{ID: "mistral", Name: "Mistral", KeyEnv: "MISTRAL_API_KEY", Models: []string{"mistral-large-latest", "devstral-latest"}, Base: "https://api.mistral.ai/v1", Listing: ListOpenAI},
	{ID: "groq", Name: "Groq", KeyEnv: "GROQ_API_KEY", Models: []string{"openai/gpt-oss-120b", "qwen/qwen3.8-27b"}, Base: "https://api.groq.com/openai/v1", Listing: ListOpenAI},
	{ID: Custom, Name: "自定义（兼容接口）", KeyEnv: "ALCOVE_CUSTOM_API_KEY", BaseEnv: "ALCOVE_CUSTOM_BASE_URL"},
}

// Custom is the route of an endpoint the user names, speaking one of
// Protocols. The bridge registers it only for a runtime started with
// ALCOVE_CUSTOM_BASE_URL set; its protocol and only model come from
// ALCOVE_CUSTOM_API and ALCOVE_CUSTOM_MODEL.
const Custom = "custom"

// Protocol is a wire protocol a custom endpoint may speak.
type Protocol struct {
	ID   string // dsh-llm-pi-ai's api name
	Name string
}

// Protocols are the ones dsh-llm-pi-ai serves for a declared route, the
// most common first.
var Protocols = []Protocol{
	{ID: "openai-completions", Name: "OpenAI 兼容（Chat Completions）"},
	{ID: "openai-responses", Name: "OpenAI Responses"},
	{ID: "anthropic-messages", Name: "Anthropic 兼容（Messages）"},
}

// ProtocolOf returns the protocol named id.
func ProtocolOf(id string) (Protocol, bool) {
	for _, p := range Protocols {
		if p.ID == id {
			return p, true
		}
	}
	return Protocol{}, false
}

// ProviderOf returns the provider of route id.
func ProviderOf(id string) (Provider, bool) {
	for _, p := range Providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

// ModelOf is the model c runs: its own, else its provider's default.
func ModelOf(c Credential) string {
	if c.Model != "" {
		return c.Model
	}
	if p, ok := ProviderOf(c.Provider); ok && len(p.Models) > 0 {
		return p.Models[0]
	}
	return ""
}

// Credential is one stored credential, without its key.
type Credential struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Provider string `json:"provider"`
	BaseURL  string `json:"baseUrl,omitempty"`
	Model    string `json:"model,omitempty"` // "" is the provider's default
	// Models are the ones the user picked to switch between; none means
	// the provider's suggestions (EnabledModels).
	Models []string `json:"models,omitempty"`
	// Context is how much context a conversation of a model gets before
	// it is compacted, in tokens by model: below the runtime's window it
	// caps it, above it widens it (up to the widest a catalog states);
	// none is the runtime's window.
	Context   map[string]int `json:"context,omitempty"`
	API       string         `json:"api,omitempty"` // a Custom endpoint's protocol
	CreatedAt time.Time      `json:"createdAt"`
}

// Store keeps credential records in Path and keys in Secrets.
type Store struct {
	Path    string
	Secrets SecretStore
	mu      sync.Mutex
}

func secretKey(id string) string { return "credential/" + id }

// Add stores a new credential of a listed provider. The key goes to the
// secret store first, so a record never points at a missing key.
func (s *Store) Add(label, provider, baseURL, key, model string) (*Credential, error) {
	p, ok := ProviderOf(provider)
	switch {
	case !ok:
		return nil, fmt.Errorf("unknown provider %q", provider)
	case p.ID == Custom:
		return nil, errors.New("a custom endpoint is added with AddCustom")
	case baseURL != "" && p.BaseEnv == "":
		return nil, fmt.Errorf("%s takes no base URL", p.Name)
	}
	return s.add(Credential{Label: label, Provider: provider, BaseURL: baseURL, Model: strings.TrimSpace(model)}, key)
}

// AddCustom stores a credential of an endpoint speaking protocol api at
// baseURL, running model.
func (s *Store) AddCustom(label, baseURL, api, key, model string) (*Credential, error) {
	model = strings.TrimSpace(model)
	switch _, ok := ProtocolOf(api); {
	case !ok:
		return nil, fmt.Errorf("unknown protocol %q", api)
	case baseURL == "":
		return nil, errors.New("a custom endpoint needs a base URL")
	case model == "":
		return nil, errors.New("a custom endpoint needs a model")
	}
	return s.add(Credential{Label: label, Provider: Custom, BaseURL: baseURL, Model: model, API: api}, key)
}

func (s *Store) add(c Credential, key string) (*Credential, error) {
	if strings.TrimSpace(key) == "" {
		return nil, errors.New("key is empty")
	}
	if c.BaseURL != "" {
		u, err := url.Parse(c.BaseURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, fmt.Errorf("base URL %q is not an http(s) URL", c.BaseURL)
		}
	}
	raw := make([]byte, 8)
	rand.Read(raw)
	c.ID, c.CreatedAt = hex.EncodeToString(raw), time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return nil, err
	}
	if err := s.Secrets.Set(secretKey(c.ID), []byte(key)); err != nil {
		return nil, fmt.Errorf("store key: %w", err)
	}
	all = append(all, c)
	if err := fsutil.WriteJSON(s.Path, all, 0o600); err != nil {
		s.Secrets.Delete(secretKey(c.ID))
		return nil, err
	}
	return &c, nil
}

// List returns the records sorted by label.
func (s *Store) List() ([]Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	sort.Slice(all, func(i, j int) bool { return all[i].Label < all[j].Label })
	return all, err
}

// Keys returns every stored key, for the diagnostics bundle to take out.
// Keys the secret store cannot read are skipped.
func (s *Store) Keys() ([][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return nil, err
	}
	var out [][]byte
	for _, c := range all {
		v, err := s.Secrets.Get(secretKey(c.ID))
		switch {
		case errors.Is(err, ErrNotFound):
		case err != nil:
			return nil, fmt.Errorf("read the key of %s: %w", c.Label, err)
		default:
			out = append(out, v)
		}
	}
	return out, nil
}

// SetModel changes the model credential id runs; "" is its provider's
// default, which a Custom endpoint does not have.
func (s *Store) SetModel(id, model string) error {
	model = strings.TrimSpace(model)
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return err
	}
	for i := range all {
		if all[i].ID == id {
			if model == "" && all[i].Provider == Custom {
				return errors.New("a custom endpoint needs a model")
			}
			all[i].Model = model
			return fsutil.WriteJSON(s.Path, all, 0o600)
		}
	}
	return fmt.Errorf("no credential %s", id)
}

// SetModels sets the models credential id switches between.
func (s *Store) SetModels(id string, models []string) error {
	var keep []string
	seen := map[string]bool{}
	for _, m := range models {
		if m = strings.TrimSpace(m); m != "" && !seen[m] {
			seen[m] = true
			keep = append(keep, m)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return err
	}
	for i := range all {
		if all[i].ID == id {
			all[i].Models = keep
			return fsutil.WriteJSON(s.Path, all, 0o600)
		}
	}
	return fmt.Errorf("no credential %s", id)
}

// Get returns the credential id.
func (s *Store) Get(id string) (Credential, error) {
	s.mu.Lock()
	all, err := s.load()
	s.mu.Unlock()
	if err != nil {
		return Credential{}, err
	}
	for _, c := range all {
		if c.ID == id {
			return c, nil
		}
	}
	return Credential{}, fmt.Errorf("no credential %s", id)
}

// SetContext sets the context of model on credential id to tokens; 0
// goes back to the runtime's window.
func (s *Store) SetContext(id, model string, tokens int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return err
	}
	for i := range all {
		if all[i].ID != id {
			continue
		}
		if tokens > 0 {
			if all[i].Context == nil {
				all[i].Context = map[string]int{}
			}
			all[i].Context[model] = tokens
		} else {
			delete(all[i].Context, model)
		}
		return fsutil.WriteJSON(s.Path, all, 0o600)
	}
	return fmt.Errorf("no credential %s", id)
}

// EnabledModels are the models c switches between: the picked ones, else
// its provider's suggestions (a Custom endpoint's own model), with the
// model it runs always among them.
func EnabledModels(c Credential) []string {
	list := c.Models
	if len(list) == 0 {
		if p, ok := ProviderOf(c.Provider); ok && p.ID != Custom {
			list = p.Models
		}
	}
	cur := ModelOf(c)
	for _, m := range list {
		if m == cur {
			return list
		}
	}
	if cur == "" {
		return list
	}
	return append([]string{cur}, list...)
}

// Remove deletes the key, then the record.
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return err
	}
	keep := all[:0]
	for _, c := range all {
		if c.ID != id {
			keep = append(keep, c)
		}
	}
	if len(keep) == len(all) {
		return fmt.Errorf("no credential %s", id)
	}
	// Key first: if the store refuses, the record stays and Remove can be
	// retried, so no key is left behind without a record pointing at it.
	if err := s.Secrets.Delete(secretKey(id)); err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("delete key: %w", err)
	}
	return fsutil.WriteJSON(s.Path, keep, 0o600)
}

// LaunchEnv returns the provider id, the model (ModelOf) and the KEY=value
// pairs that give dsh this credential. Call it only when starting a runtime;
// the env must not be logged or written anywhere.
func (s *Store) LaunchEnv(id string) (provider, model string, env []string, err error) {
	s.mu.Lock()
	all, err := s.load()
	s.mu.Unlock()
	if err != nil {
		return "", "", nil, err
	}
	for _, c := range all {
		if c.ID != id {
			continue
		}
		p, _ := ProviderOf(c.Provider)
		key, err := s.Secrets.Get(secretKey(id))
		if err != nil {
			return "", "", nil, fmt.Errorf("read key for %s: %w", c.Label, err)
		}
		env = []string{p.KeyEnv + "=" + string(key)}
		if c.BaseURL != "" {
			env = append(env, p.BaseEnv+"="+c.BaseURL)
		}
		if p.ID == Custom {
			env = append(env, "ALCOVE_CUSTOM_API="+c.API, "ALCOVE_CUSTOM_MODEL="+c.Model)
		}
		return p.ID, ModelOf(c), env, nil
	}
	return "", "", nil, fmt.Errorf("no credential %s", id)
}

func (s *Store) load() ([]Credential, error) {
	var all []Credential
	if err := fsutil.ReadJSON(s.Path, &all); err != nil {
		return nil, fmt.Errorf("credentials file %s: %w", s.Path, err)
	}
	return all, nil
}
