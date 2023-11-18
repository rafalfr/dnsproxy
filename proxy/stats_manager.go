package proxy

// TODO (rafalfr): nothing

import (
	"encoding/json"
	"sync"
)

var SM = NewStatsManager()

type StatsManager struct {
	stats map[string]any
	mux   sync.Mutex
}

func NewStatsManager() *StatsManager {
	return &StatsManager{
		stats: make(map[string]any),
	}
}

func (r *StatsManager) Set(key string, value any) {
	r.mux.Lock()
	r.stats[key] = value
	r.mux.Unlock()
}

func (r *StatsManager) Get(key string) any {
	r.mux.Lock()
	value := r.stats[key]
	r.mux.Unlock()
	return value
}

func (r *StatsManager) AsJsonPretty() ([]byte, error) {
	r.mux.Lock()
	defer r.mux.Unlock()

	return json.MarshalIndent(r.stats, "", "  ")
}

func (r *StatsManager) Exists(key string) bool {
	r.mux.Lock()
	_, ok := r.stats[key]
	r.mux.Unlock()
	return ok
}

func (r *StatsManager) GetStats() map[string]any {
	r.mux.Lock()
	defer r.mux.Unlock()
	return r.stats
}
