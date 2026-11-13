// Package catalog reads what the bundled runtime knows about each model:
// its context window and the reasoning efforts dsh accepts for it. The
// facts come from the same files dsh resolves models with (pi-ai's provider
// catalog, and dsh-llm-deepseek's fixed facts for the official route), so a
// level offered here is one initialize accepts.
package catalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// DefaultContext is the window dsh gives a model no catalog describes
// (dsh-llm-pi-ai and dsh-llm-deepseek's defaultContextWindow).
const DefaultContext = 262144

// Efforts are dsh's reasoning efforts, least thinking first.
var Efforts = []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// Model is what the runtime knows about one model of a route.
type Model struct {
	ID string
	// Context is the window the runtime gives the model unless told
	// otherwise; Max is the largest any bundled catalog states for it (the
	// same model through another service), which a pi-ai route can be told
	// to use instead. Max is Context where no route can be told.
	Context, Max int
	// Efforts the model accepts, in Efforts order; none means it takes no
	// reasoning effort and runs at its provider's default.
	Efforts []string
	// Listed reports whether the route serves the model as it is; an
	// unlisted model of a catalog route must be declared to the runtime.
	Listed bool
}

// Catalog reads pi-ai's provider files under Dir on first use.
type Catalog struct {
	// Dir is pi-ai's dist/providers/data directory; "" knows no model.
	Dir string

	mu     sync.Mutex
	routes map[string]map[string]Model
	widest map[string]int // by bare model id, over every provider file
}

// FromDSHBin is the catalog beside the dsh entry script bin
// (node_modules/@deepseek-ai/dsh/lib/bin.js).
func FromDSHBin(bin string) *Catalog {
	if bin == "" {
		return &Catalog{}
	}
	modules := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(bin))))
	return &Catalog{Dir: filepath.Join(modules, "@earendil-works", "pi-ai", "dist", "providers", "data")}
}

// The official DeepSeek route is dsh's own adapter, not pi-ai's: it serves
// any model id at these efforts, with a 1M window for the models it lists
// and the SDK profile's defaultContextWindow (1,000,000) for the rest.
const deepseekRoute = "deepseek-official"

var deepseekEfforts = []string{"off", "low", "high", "max"}

// Lookup returns what the runtime knows of model on route. A model no
// catalog describes comes back with ok false and the default window.
func (c *Catalog) Lookup(route, model string) (m Model, ok bool) {
	if route == deepseekRoute {
		window := 1000000
		if model == "deepseek-flash" || model == "deepseek-v4-pro" {
			window = 1048576
		}
		return Model{ID: model, Context: window, Max: window, Efforts: deepseekEfforts, Listed: true}, true
	}
	m, ok = Model{ID: model, Context: DefaultContext}, false
	if c != nil {
		if found, listed := c.route(route)[model]; listed {
			m, ok = found, true
		}
	}
	m.Max = m.Context
	if c.Routes(route) {
		m.Max = max(m.Context, c.widestOf(model))
	}
	return m, ok
}

// widestOf is the largest window any provider file states for model, by
// its bare id (openrouter's "openai/gpt-6.1-sol" is "gpt-6.1-sol"); 0 for
// a model none lists.
func (c *Catalog) widestOf(model string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.widest == nil {
		c.widest = map[string]int{}
		files, _ := filepath.Glob(filepath.Join(c.Dir, "*.json"))
		for _, f := range files {
			var apis map[string]map[string]entry
			if b, err := os.ReadFile(f); err != nil || json.Unmarshal(b, &apis) != nil {
				continue
			}
			for _, models := range apis {
				for _, e := range models {
					id := e.ID[strings.LastIndex(e.ID, "/")+1:]
					if e.Type == "chat" || e.Type == "" {
						c.widest[id] = max(c.widest[id], e.ContextWindow)
					}
				}
			}
		}
	}
	return c.widest[model[strings.LastIndex(model, "/")+1:]]
}

// Routes reports whether route is one the catalog describes, so an
// unlisted model of it has to be declared.
func (c *Catalog) Routes(route string) bool {
	return c != nil && len(c.route(route)) > 0
}

func (c *Catalog) route(route string) map[string]Model {
	c.mu.Lock()
	defer c.mu.Unlock()
	if m, ok := c.routes[route]; ok {
		return m
	}
	if c.routes == nil {
		c.routes = map[string]map[string]Model{}
	}
	m := c.read(route)
	c.routes[route] = m
	return m
}

// entry is one model of a pi-ai provider file.
type entry struct {
	ID            string             `json:"id"`
	Type          string             `json:"type"`
	Reasoning     bool               `json:"reasoning"`
	ContextWindow int                `json:"contextWindow"`
	LevelMap      map[string]*string `json:"thinkingLevelMap"`
}

// read loads route's file: {api: {"kind:id": entry}}. A missing or broken
// file knows no model; the runtime then reports the model itself.
func (c *Catalog) read(route string) map[string]Model {
	out := map[string]Model{}
	if c.Dir == "" || route == "" || strings.ContainsAny(route, `/\.`) {
		return out
	}
	b, err := os.ReadFile(filepath.Join(c.Dir, route+".json"))
	if err != nil {
		return out
	}
	var apis map[string]map[string]entry
	if json.Unmarshal(b, &apis) != nil {
		return out
	}
	for _, models := range apis {
		for _, e := range models {
			if e.ID == "" || (e.Type != "" && e.Type != "chat") {
				continue
			}
			out[e.ID] = Model{ID: e.ID, Context: e.ContextWindow, Efforts: levels(e), Listed: true}
		}
	}
	return out
}

// levels are the efforts dsh-llm-pi-ai offers for e
// (getSupportedThinkingLevels): none for a model without reasoning; a level
// mapped to null is unsupported, and xhigh and max must be mapped to exist.
// A model offering only "off" offers nothing (dsh then omits the control).
func levels(e entry) []string {
	if !e.Reasoning {
		return nil
	}
	var out []string
	for _, l := range Efforts {
		wire, set := e.LevelMap[l]
		if set && wire == nil {
			continue
		}
		if (l == "xhigh" || l == "max") && !set {
			continue
		}
		out = append(out, l)
	}
	if len(out) == 1 && out[0] == "off" {
		return nil
	}
	return out
}

// Supports reports whether m accepts effort.
func (m Model) Supports(effort string) bool {
	for _, e := range m.Efforts {
		if e == effort {
			return true
		}
	}
	return false
}
