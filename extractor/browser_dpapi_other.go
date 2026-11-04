//go:build !windows

package extractor

import "errors"

func unprotectBrowserData([]byte) ([]byte, error) {
	return nil, errors.New("DPAPI cookies can only be decrypted on Windows by the browser's OS user")
}
