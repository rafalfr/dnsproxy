package proxy

// TODO(rafal): nothing to do

import "sync"

// Edm is a pointer to the ExcludedDomainsManager instance.
var Edm = NewExcludedDomainsManager()

// ExcludedDomainsManager is a struct that keeps track of the excluded domains. It is used to keep track of the number of excluded domains.
type ExcludedDomainsManager struct {
	hosts      []string
	numDomains int
	mux        sync.Mutex
}

// NewExcludedDomainsManager creates a new ExcludedDomainsManager instance and returns it. It initializes the ExcludedDomainsManager with an empty slice of hosts and sets the number of domains to 0. The function returns a pointer to the created instance.
func NewExcludedDomainsManager() *ExcludedDomainsManager {
	return &ExcludedDomainsManager{
		hosts:      []string{},
		numDomains: 0,
	}
}

// AddDomain is a method of the ExcludedDomainsManager class. It adds a domain to the list of excluded domains. It locks the mutex to ensure thread safety. It checks if the domain already exists in the list of excluded domains. If the domain does not exist, it appends the domain to the list of excluded domains and increments the number of domains.
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

// CheckDomain checks if the domain is in the list of excluded domains. It locks the mutex to ensure thread safety. It returns true if the domain exists in the list of excluded domains, false otherwise.
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

// GetNumDomains returns the number of domains currently stored in the ExcludedDomainsManager. It locks the mutex to ensure thread safety. It returns the number of domains.
func (r *ExcludedDomainsManager) getNumDomains() int {
	return r.numDomains
}

// Clear method clears the list of excluded domains in the ExcludedDomainsManager. It locks the mutex to ensure thread safety. It resets the number of domains to zero.
func (r *ExcludedDomainsManager) clear() {
	r.mux.Lock()
	r.hosts = []string{}
	r.numDomains = 0
	r.mux.Unlock()
}
