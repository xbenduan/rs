//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const dpapiPasswordPrefix = "dpapi:"

func protectNodePassword(password string) (string, error) {
	encrypted, err := dpapi([]byte(password), true)
	if err != nil {
		return "", err
	}
	return dpapiPasswordPrefix + base64.RawStdEncoding.EncodeToString(encrypted), nil
}

func unprotectNodePassword(password string) (string, error) {
	encoded, ok := strings.CutPrefix(password, dpapiPasswordPrefix)
	if !ok {
		return "", errors.New("password is not DPAPI protected")
	}
	encrypted, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decode DPAPI password: %w", err)
	}
	plaintext, err := dpapi(encrypted, false)
	if err != nil {
		return "", fmt.Errorf("DPAPI decrypt: %w", err)
	}
	return string(plaintext), nil
}

func lockNodeStorage(path string) (func() error, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	name, err := windows.UTF16PtrFromString(fmt.Sprintf(`Local\rs-node-%x`, sha256.Sum256([]byte(absolute))))
	if err != nil {
		return nil, err
	}
	mutex, err := windows.CreateMutex(nil, false, name)
	if err != nil {
		return nil, err
	}
	result, err := windows.WaitForSingleObject(mutex, windows.INFINITE)
	if err != nil {
		_ = windows.CloseHandle(mutex)
		return nil, err
	}
	if result != windows.WAIT_OBJECT_0 && result != windows.WAIT_ABANDONED {
		_ = windows.CloseHandle(mutex)
		return nil, fmt.Errorf("wait for node mutex returned %d", result)
	}
	return func() error {
		return errors.Join(windows.ReleaseMutex(mutex), windows.CloseHandle(mutex))
	}, nil
}

func dpapi(input []byte, protect bool) ([]byte, error) {
	if len(input) == 0 {
		return nil, errors.New("DPAPI input is empty")
	}
	inputBlob := windows.DataBlob{Size: uint32(len(input)), Data: &input[0]}
	var outputBlob windows.DataBlob
	var err error
	if protect {
		err = windows.CryptProtectData(
			&inputBlob,
			nil,
			nil,
			0,
			nil,
			windows.CRYPTPROTECT_UI_FORBIDDEN,
			&outputBlob,
		)
	} else {
		err = windows.CryptUnprotectData(
			&inputBlob,
			nil,
			nil,
			0,
			nil,
			windows.CRYPTPROTECT_UI_FORBIDDEN,
			&outputBlob,
		)
	}
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(outputBlob.Data)))
	return append([]byte(nil), unsafe.Slice(outputBlob.Data, outputBlob.Size)...), nil
}
