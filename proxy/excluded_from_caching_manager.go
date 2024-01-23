package proxy

// TODO (rafalfr): nothing

import (
	"github.com/barweiss/go-tuple"
	. "github.com/golang-collections/collections/set"
	"strings"
	"sync"
)

// Efcm is a global instance of the ExcludedFromCachingManager struct.
var Efcm = newExcludedFromCachingManager()

// ExcludedFromCachingManager is a class that manages blocked domains.
type ExcludedFromCachingManager struct {
	hosts             map[string]*Set
	domainToListIndex map[string]int
	blockedLists      []string
	numDomains        int
	mux               sync.Mutex
}

func newExcludedFromCachingManager() *ExcludedFromCachingManager {

	p := ExcludedFromCachingManager{}
	p.mux.Lock()
	defer p.mux.Unlock()
	p.hosts = make(map[string]*Set)
	p.domainToListIndex = make(map[string]int)
	p.blockedLists = make([]string, 0)
	p.numDomains = 0
	return &p
}

func (r *ExcludedFromCachingManager) AddDomain(domain tuple.T2[string, string]) {
	r.mux.Lock()
	defer r.mux.Unlock()

	domainItems := strings.Split(domain.V1, ".")
	reverse(domainItems)

	_, ok := r.hosts[domainItems[0]]
	if !ok {
		r.hosts[domainItems[0]] = New()
	}

	if !r.hosts[domainItems[0]].Has(domain.V1) {
		r.numDomains++
	}
	r.hosts[domainItems[0]].Insert(domain.V1)

	if len(r.blockedLists) == 0 {
		r.blockedLists = append(r.blockedLists, domain.V2)
	}

	for i := 0; i < len(r.blockedLists); i++ {
		if r.blockedLists[i] == domain.V2 {
			r.domainToListIndex[domain.V1] = i
			break
		}
	}
}

func (r *ExcludedFromCachingManager) checkDomain(domain string) (bool, string) {

	r.mux.Lock()
	defer r.mux.Unlock()

	if len(r.hosts) > 0 {
		domainItems := strings.Split(domain, ".")

		blockedDomains, ok := r.hosts[domainItems[len(domainItems)-1]]
		if ok {
			if blockedDomains.Has(domain) {
				return true, domain
			}

			for i := 0; i < len(domainItems); i++ {
				tmpDomain := ""
				for j := i; j < len(domainItems); j++ {
					tmpDomain += domainItems[j] + "."
				}
				tmpDomain = strings.TrimSuffix(tmpDomain, ".")
				tmpDomain = "*." + tmpDomain

				if blockedDomains.Has(tmpDomain) {
					return true, tmpDomain
				}
			}
			return false, domain
		}
		return false, domain
	} else {
		return false, domain
	}
}
