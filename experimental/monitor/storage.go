// experimental/monitor/storage.go
package monitor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

type fileStore struct {
	mu       sync.Mutex
	basePath string
}

func newFileStore(dbPath string) *fileStore {
	dir := filepath.Dir(dbPath)
	os.MkdirAll(dir, 0755)
	return &fileStore{basePath: dir}
}

func (s *fileStore) save(filename string, v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.basePath, filename), data, 0644)
}

func (s *fileStore) load(filename string, v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(filepath.Join(s.basePath, filename))
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

func (c *Collector) saveAlertRules() {
	c.store.save("alert_rules.json", c.rules)
}

func (c *Collector) loadAlertRules() {
	var rules []AlertRule
	if err := c.store.load("alert_rules.json", &rules); err == nil {
		c.rules = rules
	}
}

func (c *Collector) AddAlertRule(rule AlertRule) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rules == nil {
		c.rules = make([]AlertRule, 0)
	}
	rule.ID = int64(len(c.rules)) + 1
	c.rules = append(c.rules, rule)
	c.saveAlertRules()
	return rule.ID
}

func (c *Collector) DeleteAlertRule(id int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, r := range c.rules {
		if r.ID == id {
			c.rules = append(c.rules[:i], c.rules[i+1:]...)
			break
		}
	}
	c.saveAlertRules()
}

func (c *Collector) GetAlertRules() []AlertRule {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.rules
}
