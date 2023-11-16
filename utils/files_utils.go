package utils

// TODO (rafalfr): nothing

import (
	"os"
	"time"
)

/**
 * FileExists checks if a file with the given name exists.
 *
 * Parameters:
 * - name (string): The name of the file to check.
 *
 * Returns:
 * - bool: A boolean value indicating whether the file exists or not.
 * - error: An error value, if any occurred during the file existence check.
 */
func FileExists(name string) (bool, error) {
	_, err := os.Stat(name)
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

// handle error
// use fileSize and modificationTime
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
