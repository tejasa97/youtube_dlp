//go:build windows

package chromiumwindows

import (
	"context"
	"unsafe"

	"golang.org/x/sys/windows"
)

type DPAPI struct{}

func (DPAPI) Unprotect(ctx context.Context, encrypted []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(encrypted) == 0 {
		return nil, ErrDecrypt
	}
	input := append([]byte(nil), encrypted...)
	defer clear(input)
	in := windows.DataBlob{Size: uint32(len(input)), Data: &input[0]}
	var out windows.DataBlob
	const cryptProtectUIForbidden = 0x1
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, cryptProtectUIForbidden, &out); err != nil || out.Data == nil || out.Size == 0 {
		return nil, ErrKeyUnavailable
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	result := append([]byte(nil), unsafe.Slice(out.Data, int(out.Size))...)
	if err := ctx.Err(); err != nil {
		clear(result)
		return nil, err
	}
	return result, nil
}

func defaultProtector() DataProtector { return DPAPI{} }
