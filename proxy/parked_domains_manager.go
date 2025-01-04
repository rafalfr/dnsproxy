package proxy

import (
	"encoding/json"
	"github.com/AdguardTeam/dnsproxy/utils"
	"github.com/AdguardTeam/golibs/log"
	"os"
	"regexp"
	"sync"
	"time"
)

type Pair struct {
	values [2]interface{}
}

func MakePair(k, v interface{}) Pair {
	return Pair{values: [2]interface{}{k, v}}
}

func (p Pair) Get(i int) interface{} {
	return p.values[i]
}

func (p Pair) Deconstruct() (interface{}, interface{}) {
	return p.values[0], p.values[1]
}

type DomainsData struct {
	Domains []DomainData `json:"domains"`
}

type DomainData struct {
	Name    string `json:"name"`
	MNAME   string `json:"mname"`
	RNAME   string `json:"rname"`
	Serial  uint32 `json:"serial"`
	Refresh uint32 `json:"refresh"`
	Retry   uint32 `json:"retry"`
	Expire  uint32 `json:"expire"`
	TTL     uint32 `json:"ttl"`
	Mbox    string `json:"mbox"`
	NS      string `json:"ns"`
	MX      string `json:"mx"`
	A       string `json:"a"`
	AAAA    string `json:"aaaa"`
}

var Pdm = NewParkedDomainsManager()

type ParkedDomainsManager struct {
	domains    []Pair
	SOAs       map[int64]DomainData
	numDomains int
	mux        sync.Mutex
}

func NewParkedDomainsManager() *ParkedDomainsManager {
	return &ParkedDomainsManager{
		domains:    []Pair{},
		SOAs:       make(map[int64]DomainData),
		numDomains: 0,
		mux:        sync.Mutex{},
	}
}

func (p *ParkedDomainsManager) AddDomain(domain string, soa DomainData) {
	p.mux.Lock()
	for _, host := range p.domains {
		if host.Get(0) == domain {
			p.mux.Unlock()
			return
		}
	}
	domainRegEx, err := regexp.Compile(domain)
	if err != nil {
		p.mux.Unlock()
		return
	}
	id := time.Now().UnixNano()
	p.domains = append(p.domains, MakePair(domainRegEx, id))
	p.SOAs[id] = soa
	p.numDomains++
	p.mux.Unlock()
}

func (p *ParkedDomainsManager) CheckDomain(domain string) bool {
	p.mux.Lock()
	for _, host := range p.domains {
		if host.Get(0).(*regexp.Regexp).MatchString(domain) {
			p.mux.Unlock()
			return true
		}
	}
	p.mux.Unlock()
	return false
}

func (p *ParkedDomainsManager) GetDomainData(domain string) (DomainData, bool) {
	p.mux.Lock()
	defer p.mux.Unlock()
	for _, host := range p.domains {
		if host.Get(0).(*regexp.Regexp).MatchString(domain) {
			return p.SOAs[host.Get(1).(int64)], true
		}
	}
	return DomainData{}, false
}

func (p *ParkedDomainsManager) Clear() {
	p.mux.Lock()
	p.domains = []Pair{}
	p.numDomains = 0
	p.mux.Unlock()
}

func (p *ParkedDomainsManager) GetNumDomains() int {
	p.mux.Lock()
	defer p.mux.Unlock()
	return p.numDomains
}

func (p *ParkedDomainsManager) LoadParkedDomains(parkedDomainsPath string) {
	p.mux.Lock()
	defer p.mux.Unlock()

	ok, _ := utils.FileExists(parkedDomainsPath)
	if ok {
		// read the yaml file parkedDomainsPath and parse it
		file, err := os.Open(parkedDomainsPath)
		if err != nil {
			log.Error("Failed to open file %s: %v", parkedDomainsPath, err)
			return
		}
		defer file.Close()
		b, err := os.ReadFile(parkedDomainsPath)
		if err != nil {
			log.Error("Failed to read file %s: %v", parkedDomainsPath, err)
			return
		}

		var domains DomainsData
		err = json.Unmarshal(b, &domains)
		if err != nil {
			log.Error("Failed to unmarshal file %s: %v", parkedDomainsPath, err)
			return
		}

		for _, domain := range domains.Domains {
			p.AddDomain(domain.Name, domain)
		}
	}
}
