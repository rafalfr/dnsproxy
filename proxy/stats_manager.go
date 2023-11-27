package proxy

// TODO (rafalfr): nothing

import (
	"encoding/json"
	"github.com/AdguardTeam/golibs/log"
	"os"
	"strings"
	"sync"
	"time"
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
		if _, ok := r.stats[keyParts[0]]; ok {
			return r.stats[keyParts[0]]
		} else {
			return nil
		}
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

func (r *StatsManager) GetStatsPtr() *map[string]any {
	r.mux.Lock()
	defer r.mux.Unlock()

	return &r.stats
}

// SetStats sets the stats map of the StatsManager to the given map[string]any instance and returns it
func (r *StatsManager) SetStats(stats *map[string]any) {
	r.mux.Lock()
	defer r.mux.Unlock()

	r.stats = *stats
}

// LoadStats loads the stats map of the StatsManager from the given file path
func (r *StatsManager) LoadStats(filePath string) {
	r.mux.Lock()
	defer r.mux.Unlock()

	// write the code to check if the file exists
	if _, err := os.Stat(filePath); err == nil {
		// File exists
		// write the code to read the file contents into bytes slice
		bytes, err := os.ReadFile(filePath)
		if err != nil {
			log.Error("Error reading file: %s", filePath)
			return
		}

		var stats map[string]any
		err = json.Unmarshal(bytes, &stats)

		if err != nil {
			return
		}
		r.CopyStats(&stats, &r.stats)

	} else if os.IsNotExist(err) {
		// File does not exist
		log.Error("File %s does not exist", filePath)
	} else {
		// Error occurred while checking file existence
		log.Error("Error occurred while checking file existence: %s", filePath)
	}
		
	//if r.Get("time::since") == nil {
	//	currentTime := time.Now().Format("2006-01-02 15:04:05")
	//	r.Set("time::since", currentTime)
	//}
}

// SaveStats saves the stats map of the StatsManager to the given file path
func (r *StatsManager) SaveStats(filePath string) {
	r.mux.Lock()
	defer r.mux.Unlock()

	bytes, err := json.Marshal(&r.stats)
	if err != nil {
		log.Error("Error converting stats to JSON: %s", filePath)
		return
	}
	err = os.WriteFile(filePath, bytes, 0644)
	if err != nil {
		log.Error("Error writing JSON to file: %s", filePath)
		return
	}
}

// CopyStats copies the stats map of the srcStats map to the dstStats map
func (r *StatsManager) CopyStats(srcStats *map[string]interface{}, dstStats *map[string]interface{}) {
	for key, value := range *srcStats {
		if m, ok := value.(map[string]interface{}); ok {
			var stats map[string]interface{}
			stats = make(map[string]interface{})
			(*dstStats)[key] = stats
			r.CopyStats(&m, &stats)
		} else {
			if f, ok := value.(float64); ok {
				(*dstStats)[key] = uint64(f)
			} else {
				(*dstStats)[key] = value
			}
		}
	}
}
