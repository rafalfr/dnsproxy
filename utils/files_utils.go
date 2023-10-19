package utils

import (
	"os"
	"time"
)

func FileExists(name string) (bool, error) {
	_, err := os.Stat(name)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

func GetFileInfo(filePath string) (int64, time.Time, error) {

	// Get the fileinfo
	fileInfo, err := os.Stat(filePath)

	// Checks for the error
	if err != nil {
		return 0, time.Now(), err
	}
	modificationTime := fileInfo.ModTime().UTC()

	// Gives the modification time
	//modificationTime := fileInfo.ModTime()
	//fmt.Println("Name of the file:", fileInfo.Name(),
	//	" Last modified time of the file:",
	///	modificationTime)

	// Gives the size of the file in bytes
	fileSize := fileInfo.Size()
	//fmt.Println("Size of the file:", fileSize)

	return fileSize, modificationTime, nil
}
