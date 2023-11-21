package proxy

// TODO (rafalfr): nothing

import (
	"encoding/json"
	"strings"
	"sync"
)

var SM = NewStatsManager()

// StatsManager is a map of stats. It is used to keep track of stats for the proxy. It is used to keep track of the number of queries, answers, cache hits, etc.
type StatsManager struct {
	stats map[string]any
	mux   sync.Mutex
}

// NewStatsManager creates a new StatsManager instance and returns it.
func NewStatsManager() *StatsManager {
	return &StatsManager{
		stats: make(map[string]any),
	}
}

// Set sets a value in the StatsManager with the given key and value or creates a new entry with the given key and value if the key does not exist in the StatsManager
func (r *StatsManager) Set(key string, value any) {
	r.mux.Lock()
	defer r.mux.Unlock()

	keyParts := strings.Split(key, "::")
	if len(keyParts) == 1 {
		r.stats[keyParts[0]] = value
	} else {
		stats := r.stats
		for i := 0; i < len(keyParts)-1; i++ {
			if _, ok := stats[keyParts[i]]; !ok {
				stats[keyParts[i]] = make(map[string]any)
			}
			stats = stats[keyParts[i]].(map[string]any)
		}
		stats[keyParts[len(keyParts)-1]] = value
	}
}

// Get gets a value from the StatsManager with the given key and returns it or nil if not found
func (r *StatsManager) Get(key string) any {
	r.mux.Lock()
	defer r.mux.Unlock()

	keyParts := strings.Split(key, "::")
	if len(keyParts) == 1 {
		return r.stats[keyParts[0]]
	} else {
		stats := r.stats
		for i := 0; i < len(keyParts)-1; i++ {
			if _, ok := stats[keyParts[i]]; !ok {
				return nil
			} else {
				stats = stats[keyParts[i]].(map[string]any)
			}
		}
		return stats[keyParts[len(keyParts)-1]]
	}
}

// AsJsonPretty returns a JSON representation of the StatsManager as a byte array instance using the json.Marshal function and the json.MarshalIndent function
func (r *StatsManager) AsJsonPretty() ([]byte, error) {
	r.mux.Lock()
	defer r.mux.Unlock()

	return json.MarshalIndent(r.stats, "", "  ")
}

// Exists checks if a value exists in the StatsManager with the given key and returns true if it does and false otherwise
func (r *StatsManager) Exists(key string) bool {
	r.mux.Lock()
	defer r.mux.Unlock()

	keyParts := strings.Split(key, "::")
	if len(keyParts) == 1 {
		_, ok := r.stats[keyParts[0]]
		return ok
	} else {
		stats := r.stats
		for i := 0; i < len(keyParts)-1; i++ {
			if _, ok := stats[keyParts[i]]; !ok {
				return false
			} else {
				stats = stats[keyParts[i]].(map[string]any)
			}
		}
		_, ok := stats[keyParts[len(keyParts)-1]]
		return ok
	}
}

// GetStats returns the stats map of the StatsManager as a map[string]any instance
func (r *StatsManager) GetStats() map[string]any {
	r.mux.Lock()
	defer r.mux.Unlock()

	return r.stats
}
