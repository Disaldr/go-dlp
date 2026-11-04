package extractor

import (
	"errors"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

func unprotectBrowserData(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("empty DPAPI data")
	}
	in := windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
	var out windows.DataBlob
	err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	runtime.KeepAlive(data)
	if err != nil {
		return nil, errors.New("DPAPI decryption failed; run as the same Windows user as the browser, or use --cookies")
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}
