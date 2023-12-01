package proxy

// TODO (rafalfr): nothing

import (
	"bufio"
	"github.com/AdguardTeam/dnsproxy/utils"
	"github.com/AdguardTeam/golibs/log"
	"github.com/barweiss/go-tuple"
	. "github.com/golang-collections/collections/set"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var FinishSignal = make(chan bool, 1)

// reverse reverses the given slice of strings.
func reverse(s []string) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

// Bdm is a global instance of the BlockedDomainsManager struct.
var Bdm = newBlockedDomainsManger()

// BlockedDomainsManager is a class that manages blocked domains.
type BlockedDomainsManager struct {
	hosts             map[string]*Set
	domainToListIndex map[string]int
	blockedLists      []string
	numDomains        int
	mux               sync.Mutex
}

func newBlockedDomainsManger() *BlockedDomainsManager {

	p := BlockedDomainsManager{}
	p.mux.Lock()
	defer p.mux.Unlock()
	p.hosts = make(map[string]*Set)
	p.domainToListIndex = make(map[string]int)
	p.blockedLists = make([]string, 0)
	p.numDomains = 0
	return &p
}

func (r *BlockedDomainsManager) addDomain(domain tuple.T2[string, string]) {

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

func (r *BlockedDomainsManager) checkDomain(domain string) (bool, string) {

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

func (r *BlockedDomainsManager) getDomainListName(domain string) string {
	r.mux.Lock()
	defer r.mux.Unlock()

	if listIndex, ok := r.domainToListIndex[domain]; ok {

		if listIndex < len(r.blockedLists) {
			return r.blockedLists[listIndex]
		} else {
			return "unknown"
		}
	}

	return "unknown"
}

func (r *BlockedDomainsManager) getNumDomains() int {

	r.mux.Lock()
	defer r.mux.Unlock()

	return r.numDomains
}

func (r *BlockedDomainsManager) clear() {

	r.mux.Lock()
	defer r.mux.Unlock()

	clear(r.hosts)
	clear(r.domainToListIndex)
	clear(r.blockedLists)
	r.numDomains = 0
}

func UpdateBlockedDomains(r *BlockedDomainsManager, blockedDomainsUrls []string) {

	//log.Info("updating domains")
	loadBlockedDomains(r, blockedDomainsUrls)

	downloadDomains := false

	for _, blockedDomainUrl := range blockedDomainsUrls {

		tokens := strings.Split(blockedDomainUrl, "/")
		filePath := tokens[len(tokens)-1]
		if !strings.HasSuffix(filePath, ".txt") {
			filePath += ".txt"
		}

		fileSize, modificationTime, err := utils.GetFileInfo(filePath)

		if err != nil {
			downloadDomains = true
		} else {
			// TODO (rafalfr): blocked domains update interval
			if time.Now().Sub(modificationTime).Seconds() > 6*3600 || fileSize == 0 {
				if utils.CheckRemoteFileExists(blockedDomainUrl) {
					e := os.Remove(filePath)
					if e != nil {
						log.Fatal(e)
					}
				}
				downloadDomains = true
			}
		}
	}
	if downloadDomains {
		downloadDomains = false
		loadBlockedDomains(r, blockedDomainsUrls)
	}
}

func loadBlockedDomains(r *BlockedDomainsManager, blockedDomainsUrls []string) {

	// https://github.com/xpzouying/go-practice/blob/master/read_file_line_by_line/main.go

	for _, blockedDomainUrl := range blockedDomainsUrls {
		tokens := strings.Split(blockedDomainUrl, "/")
		filePath := tokens[len(tokens)-1]
		if !strings.HasSuffix(filePath, ".txt") {
			filePath += ".txt"
		}

		ok, _ := utils.FileExists(filePath)
		if ok {
			fileSize, _, _ := utils.GetFileInfo(filePath)
			if fileSize == 0 {
				err := utils.DownloadFromUrl(blockedDomainUrl)
				if err != nil {
					log.Fatal(err)
					return
				}
			}
		} else {
			err := utils.DownloadFromUrl(blockedDomainUrl)
			if err != nil {
				log.Fatal(err)
				return
			}
		}
	}

	r.clear()

	allDomains := make([]tuple.T2[string, string], 0)

	for _, blockedDomainUrl := range blockedDomainsUrls {
		tokens := strings.Split(blockedDomainUrl, "/")
		filePath := tokens[len(tokens)-1]
		if !strings.HasSuffix(filePath, ".txt") {
			filePath += ".txt"
		}

		fileName := strings.TrimSuffix(filePath, filepath.Ext(filePath))
		r.blockedLists = append(r.blockedLists, fileName)

		f, err := os.OpenFile(filePath, os.O_RDONLY, os.ModePerm)
		if err != nil {
			log.Fatalf("open file error: %v", err)
			return
		}

		rd := bufio.NewReader(f)
		for {
			line, err := rd.ReadString('\n')
			if err != nil {
				if err == io.EOF {
					break
				}
				log.Fatalf("read file line error: %v", err)
				return
			}
			if !strings.HasPrefix(line, "#") {
				line = strings.Trim(line, "\n ")
				allDomains = append(allDomains, tuple.New2(line, fileName))
			}
		}

		err = f.Close()
		if err != nil {
			log.Fatalf("close file error: %v", err)
			return
		}
	}

	sort.Slice(allDomains, func(i, j int) bool {
		return len(allDomains[i].V1) < len(allDomains[j].V1)
	})

	numDuplicatedDomains := 0
	for _, domain := range allDomains {
		if Edm.checkDomain(domain.V1) == false {
			ok, _ := r.checkDomain(domain.V1)
			if ok == false {
				r.addDomain(domain)
			} else {
				numDuplicatedDomains++
			}
		}
	}

	SM.Set("blocked_domains::num_domains", r.getNumDomains())
	log.Info("total number of blocked domains %d", r.getNumDomains())
	log.Info("number of duplicated domains %d", numDuplicatedDomains)
}

func MonitorLogFile(logFilePath string) {

	ok, err := utils.FileExists(logFilePath)
	if ok && err == nil {
		fileSize, _, err := utils.GetFileInfo(logFilePath)
		if fileSize > 128*1024*1024 && err == nil {
			e := os.Remove(logFilePath)
			if e != nil {
				log.Fatal(e)
			}
		}
	}
}
