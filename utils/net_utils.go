package utils

// TODO (rafalfr): nothing

import (
	"errors"
	"github.com/AdguardTeam/golibs/log"
	"io"
	"net/http"
	"os"
	"strings"
)

// DownloadFromUrl example.com/file.txt", "/path/to/save/file.txt")
// handle error
func DownloadFromUrl(url string, opFilePath ...string) error {

	filePath := ""

	if len(opFilePath) > 0 {
		filePath = opFilePath[0]
	} else {
		tokens := strings.Split(url, "/")
		filePath = tokens[len(tokens)-1]
		if !strings.HasSuffix(filePath, ".txt") {
			filePath += ".txt"
		}
	}

	output, err := os.Create(filePath)
	if err != nil {
		log.Error("Error while creating", filePath, "-", err)
		return err
	}
	defer func(output *os.File) {
		err := output.Close()
		if err != nil {
			log.Error("Error while closing output file ", filePath, "-", err)
			return
		}
	}(output)

	response, err := http.Get(url)
	if err != nil {
		log.Error("Error while downloading", url, "-", err)
		return err
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			log.Error("Error while closing output file ", filePath, "-", err)
		}
	}(response.Body)

	// Check server response
	if response.StatusCode != http.StatusOK {
		log.Error("bad status: %s\n", response.Status)
		return errors.New("")
	}

	_, err = io.Copy(output, response.Body)
	if err != nil {
		log.Error("Error while downloading", url, "-", err)
		return err
	}

	return nil
}

/**
 * CheckRemoteFileExists is a function that takes a fileUrl string as input and
 * returns a boolean value indicating whether the remote file exists or not. It
 * sends a HTTP HEAD request to the specified fileUrl and checks the response
 * status code. If the status code is not 200 (OK), it returns false indicating
 * that the file does not exist. Otherwise, it returns true indicating that the
 * file exists.
 */
func CheckRemoteFileExists(fileUrl string) bool {
	resp, err := http.Head(fileUrl)
	if err != nil {
		return false
	}
	if resp.StatusCode != http.StatusOK {
		return false
	}
	return true
}
