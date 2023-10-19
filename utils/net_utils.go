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

func DownloadFromUrl(url string, opFilePath ...string) error {

	filePath := ""

	if len(opFilePath) > 0 {
		filePath = opFilePath[0]
	} else {
		tokens := strings.Split(url, "/")
		filePath = tokens[len(tokens)-1]
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
