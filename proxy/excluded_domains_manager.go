package proxy

// TODO(rafalfr): nothing to do

import "sync"

var Edm = NewExcludedDomainsManager()

type ExcludedDomainsManager struct {
	hosts      []string
	numDomains int
	mux        sync.Mutex
}

func NewExcludedDomainsManager() *ExcludedDomainsManager {
	return &ExcludedDomainsManager{
		hosts:      []string{},
		numDomains: 0,
	}
}

func (r *ExcludedDomainsManager) AddDomain(domain string) {
	r.mux.Lock()
	for _, host := range r.hosts {
		if host == domain {
			r.mux.Unlock()
			return
		}
	}
	r.hosts = append(r.hosts, domain)
	r.numDomains++
	r.mux.Unlock()
}

func (r *ExcludedDomainsManager) checkDomain(domain string) bool {
	r.mux.Lock()
	for _, host := range r.hosts {
		if host == domain {
			r.mux.Unlock()
			return true
		}
	}
	r.mux.Unlock()
	return false
}

// Output: 0
func (r *ExcludedDomainsManager) getNumDomains() int {
	return r.numDomains
}

func (r *ExcludedDomainsManager) clear() {
	r.mux.Lock()
	r.hosts = []string{}
	r.numDomains = 0
	r.mux.Unlock()
}
