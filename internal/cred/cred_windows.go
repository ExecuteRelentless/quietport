package cred

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"

	"quietport.app/quietport/internal/cryptobox"
)

// Windows: the vault key is a DPAPI blob (CryptProtectData, current-user scope, FR-61) stored at
// %LOCALAPPDATA%\Quietport\vault.key. Only this user's account can unprotect it.

const cryptprotectUIForbidden = 0x1

func dpapi(fn func(in *windows.DataBlob, out *windows.DataBlob) error, data []byte) ([]byte, error) {
	var in, out windows.DataBlob
	if len(data) > 0 {
		in.Data = &data[0]
		in.Size = uint32(len(data))
	}
	if err := fn(&in, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	res := make([]byte, out.Size)
	copy(res, unsafe.Slice(out.Data, out.Size))
	return res, nil
}

func protect(b []byte) ([]byte, error) {
	desc, _ := windows.UTF16PtrFromString("Quietport vault key")
	return dpapi(func(in, out *windows.DataBlob) error {
		return windows.CryptProtectData(in, desc, nil, 0, nil, cryptprotectUIForbidden, out)
	}, b)
}

func unprotect(b []byte) ([]byte, error) {
	return dpapi(func(in, out *windows.DataBlob) error {
		return windows.CryptUnprotectData(in, nil, nil, 0, nil, cryptprotectUIForbidden, out)
	}, b)
}

func loadOrCreate(appDir, service string) ([]byte, error) {
	p := fileKeyPath(appDir)
	if blob, err := os.ReadFile(p); err == nil {
		if k, err := unprotect(blob); err == nil && len(k) == 32 {
			return k, nil
		}
	}
	k := cryptobox.RandomBytes(32)
	blob, err := protect(k)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(p, blob, 0o600); err != nil {
		return nil, err
	}
	return k, nil
}

func destroy(appDir, service string) error { return os.Remove(fileKeyPath(appDir)) }
