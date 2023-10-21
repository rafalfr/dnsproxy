package proxy

// TODO (rafalfr): nothing

import (
	"bufio"
	"github.com/AdguardTeam/dnsproxy/utils"
	"github.com/AdguardTeam/golibs/log"
	. "github.com/golang-collections/collections/set"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

var TerminationSignal = make(chan os.Signal, 1)
var FinishSignal = make(chan bool, 1)

func reverse[S ~[]E, E any](s S) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

var Bdm = newBlockedDomainsManger()

// BlockedDomainsManager class declaration
type BlockedDomainsManager struct {
	hosts      map[string]*Set
	numDomains int
	mux        sync.Mutex
}

// BlockedDomainsManager constructor
func newBlockedDomainsManger() *BlockedDomainsManager {

	p := BlockedDomainsManager{}
	p.mux.Lock()
	defer p.mux.Unlock()
	p.hosts = make(map[string]*Set)
	p.numDomains = 0
	return &p
}

// BlockedDomainsManager addDomain method which adds a new domain to the manager.
func (r *BlockedDomainsManager) addDomain(domain string) {

	r.mux.Lock()
	defer r.mux.Unlock()

	domainItems := strings.Split(domain, ".")
	reverse(domainItems)

	_, ok := r.hosts[domainItems[0]]
	if !ok {
		r.hosts[domainItems[0]] = New()
	}

	if !r.hosts[domainItems[0]].Has(domain) {
		r.numDomains++
	}
	r.hosts[domainItems[0]].Insert(domain)

}

func (r *BlockedDomainsManager) checkDomain(domain string) bool {

	r.mux.Lock()
	defer r.mux.Unlock()

	if len(r.hosts) > 0 {
		domainItems := strings.Split(domain, ".")

		blockedDomains, ok := r.hosts[domainItems[len(domainItems)-1]]
		if ok {
			if blockedDomains.Has(domain) {
				return true
			}

			for i := 0; i < len(domainItems); i++ {
				tmpDomain := ""
				for j := i; j < len(domainItems); j++ {
					tmpDomain += domainItems[j] + "."
				}
				tmpDomain = strings.TrimSuffix(tmpDomain, ".")
				tmpDomain = "*." + tmpDomain

				if blockedDomains.Has(tmpDomain) {
					return true
				}
			}
			return false
		}
		return false
	} else {
		return false
	}
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
	r.numDomains = 0
}

func UpdateBlockedDomains(r *BlockedDomainsManager, blockedDomainsUrls []string) {

	loadBlockedDomains(r, blockedDomainsUrls)

	for {
		select {
		case <-TerminationSignal:
			FinishSignal <- true
			return
		default:

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
			time.Sleep(time.Second)
		}
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
					return
				}
			}
		} else {
			err := utils.DownloadFromUrl(blockedDomainUrl)
			if err != nil {
				return
			}
		}
	}

	r.clear()

	for _, blockedDomainUrl := range blockedDomainsUrls {
		tokens := strings.Split(blockedDomainUrl, "/")
		filePath := tokens[len(tokens)-1]
		if !strings.HasSuffix(filePath, ".txt") {
			filePath += ".txt"
		}

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
				r.addDomain(line)
			}
		}

		err = f.Close()
		if err != nil {
			return
		}
	}

	log.Info("total number of blocked domains %d", r.getNumDomains())
}

func MonitorLogFile(logFilePath string) {
	for {
		select {
		case <-TerminationSignal:
			return
		default:

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
			time.Sleep(time.Minute)
		}
	}
}
