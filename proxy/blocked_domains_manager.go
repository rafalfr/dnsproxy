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
	hosts            map[string]*Set
	domainToListName map[string]string
	numDomains       int
	mux              sync.Mutex
}

/**
 * newBlockedDomainsManager is a function that creates a new instance of the
 * BlockedDomainsManager struct. It initializes the struct with an empty map of
 * hosts and sets the number of domains to 0. The function returns a pointer to
 * the created instance.
 */
func newBlockedDomainsManger() *BlockedDomainsManager {

	p := BlockedDomainsManager{}
	p.mux.Lock()
	defer p.mux.Unlock()
	p.hosts = make(map[string]*Set)
	p.domainToListName = make(map[string]string)
	p.numDomains = 0
	return &p
}

/**
 * addDomain is a method of the BlockedDomainsManager class. It adds a domain to
 * the list of blocked domains.
 *
 * Parameters:
 * - domain (string): The domain to be added.
 *
 * Locks:
 * - r.mux: Locks the mutex to ensure thread safety.
 *
 * Returns:
 * - None
 *
 * Behavior:
 * - Splits the domain string into individual items using the dot (".") as the
 * separator.
 * - Reverses the order of the domain items.
 * - Checks if the first item of the reversed domain items exists in the r.hosts
 * map.
 * - If the first item does not exist, creates a new instance of the map and
 * assigns it to r.hosts[domainItems[0]].
 * - Checks if the domain already exists in the map associated with the first item.
 * - If the domain does not exist, increments the count of the number of domains
 * (r.numDomains).
 * - Inserts the domain into the map associated with the first item.
 *
 * Note: This method ensures thread safety by using a mutex to lock the critical
 * section of code.
 */
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

	r.domainToListName[domain.V1] = domain.V2
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

	if listName, ok := r.domainToListName[domain]; ok {
		return listName
	}

	return "unknown"
}

/**
 * getNumDomains returns the number of domains currently stored in the
 * BlockedDomainsManager.
 *
 * Parameters:
 * - None
 *
 * Returns:
 * - int: The number of domains stored in the BlockedDomainsManager.
 *
 * Concurrency:
 * - This method is thread-safe and uses a mutex to ensure exclusive access to the
 * numDomains variable.
 */
func (r *BlockedDomainsManager) getNumDomains() int {

	r.mux.Lock()
	defer r.mux.Unlock()

	return r.numDomains
}

/**
 * clear method clears the list of blocked domains in the BlockedDomainsManager. It
 * acquires a lock on the mutex to ensure exclusive access to the data, and
 * releases the lock before returning. The method also resets the count of blocked
 * domains to zero.
 */
func (r *BlockedDomainsManager) clear() {

	r.mux.Lock()
	defer r.mux.Unlock()

	clear(r.hosts)
	r.numDomains = 0

	clear(r.domainToListName)
}

/**
 * UpdateBlockedDomains is a function that updates the list of blocked domains in
 * the BlockedDomainsManager. It takes a pointer to a BlockedDomainsManager object
 * (r) and a slice of strings (blockedDomainsUrls) as input parameters.
 *
 * The function first calls the loadBlockedDomains function to load the blocked
 * domains from the specified URLs into the BlockedDomainsManager.
 *
 * Then, it iterates over each blocked domain URL in the blockedDomainsUrls slice.
 * It extracts the file name from the URL and appends ".txt" if it doesn't already
 * have a file extension. It then checks if the file exists locally and retrieves
 * its size and modification time using the GetFileInfo function from the utils
 * package.
 *
 * If the file doesn't exist locally or if its modification time is older than 6
 * hours or if the file size is 0, it checks if the remote file exists using the
 * CheckRemoteFileExists function from the utils package. If the remote file
 * exists, it removes the local file using os.Remove.
 *
 * After iterating over all the blocked domain URLs, if any of the conditions for
 * downloading the domains are met, the function calls the loadBlockedDomains
 * function again to update the BlockedDomainsManager with the latest blocked
 * domains.
 *
 * The function does not have an infinite loop commented out, so it will not
 * continuously update the blocked domains.
 */
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

/**
 * func MonitorLogFile(logFilePath string)
 *
 * MonitorLogFile is a function that monitors a log file specified by the
 * logFilePath parameter.
 *
 * Parameters:
 * - logFilePath (string): The path of the log file to be monitored.
 *
 * Description:
 * This function continuously monitors the specified log file. It checks if the
 * file exists and if its size exceeds 128 MB. If the file exists and its size is
 * larger than 128 MB, it is deleted.
 *
 * Note:
 * - The function does not return any value.
 * - The monitoring process can be terminated by sending a termination signal to
 * the function.
 */
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
