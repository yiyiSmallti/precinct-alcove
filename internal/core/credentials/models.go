package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// How a service lists its models (Provider.Listing).
const (
	ListOpenAI    = "openai"    // GET {base}/models, bearer key: data[].id
	ListAnthropic = "anthropic" // GET {base}/v1/models, x-api-key: data[].id
	ListGoogle    = "google"    // GET {base}/models, x-goog-api-key: models[].name
)

// listing is where and how c's service lists its models.
func listing(c Credential) (base, how string, ok bool) {
	p, known := ProviderOf(c.Provider)
	if !known {
		return "", "", false
	}
	if p.ID == Custom {
		if c.API == "anthropic-messages" {
			return c.BaseURL, ListAnthropic, true
		}
		return c.BaseURL, ListOpenAI, true
	}
	base = p.Base
	if c.BaseURL != "" {
		base = c.BaseURL
	}
	return base, p.Listing, p.Listing != ""
}

// CanList reports whether Alcove can ask c's service for its models.
func CanList(c Credential) bool {
	_, _, ok := listing(c)
	return ok
}

// RemoteModels asks credential id's service which models its key may use,
// sorted by id. It reads the key from the secret store.
func (s *Store) RemoteModels(ctx context.Context, id string) ([]string, error) {
	s.mu.Lock()
	all, err := s.load()
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	for _, c := range all {
		if c.ID != id {
			continue
		}
		key, err := s.Secrets.Get(secretKey(id))
		if err != nil {
			return nil, fmt.Errorf("read key for %s: %w", c.Label, err)
		}
		return listModels(ctx, c, string(key))
	}
	return nil, fmt.Errorf("no credential %s", id)
}

func listModels(ctx context.Context, c Credential, key string) ([]string, error) {
	base, how, ok := listing(c)
	if !ok {
		return nil, errors.New("this service does not list its models")
	}
	base = strings.TrimRight(base, "/")
	var target string
	header := http.Header{}
	switch how {
	case ListAnthropic:
		// Anthropic-style bases stop before /v1 (pi-ai's catalog, and
		// gateways that copy it); some custom ones include it.
		target = base + "/v1/models?limit=1000"
		if strings.HasSuffix(base, "/v1") {
			target = base + "/models?limit=1000"
		}
		header.Set("x-api-key", key)
		header.Set("anthropic-version", "2023-06-01")
	case ListGoogle:
		target = base + "/models?pageSize=1000"
		header.Set("x-goog-api-key", key)
	default:
		target = base + "/models"
		header.Set("Authorization", "Bearer "+key)
	}
	if _, err := url.Parse(target); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header = header
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%s: %s", res.Status, errorText(body))
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			Name    string   `json:"name"`
			Methods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("the model list is not JSON: %w", err)
	}
	var out []string
	for _, m := range list.Data {
		if m.ID != "" {
			out = append(out, m.ID)
		}
	}
	for _, m := range list.Models {
		// Gemini lists embedding and other models too; only those that
		// generate content can run a task.
		for _, how := range m.Methods {
			if how == "generateContent" {
				out = append(out, strings.TrimPrefix(m.Name, "models/"))
				break
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// errorText is the message of an API error body, or its start.
func errorText(body []byte) string {
	var e struct {
		Error any `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != nil {
		switch v := e.Error.(type) {
		case string:
			return v
		case map[string]any:
			if m, ok := v["message"].(string); ok {
				return m
			}
		}
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
