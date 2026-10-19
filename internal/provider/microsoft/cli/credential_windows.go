//go:build windows

package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

var advapi = syscall.NewLazyDLL("advapi32.dll")
var credRead = advapi.NewProc("CredReadW")
var credEnumerate = advapi.NewProc("CredEnumerateW")
var credFree = advapi.NewProc("CredFree")

type credential struct {
	Flags, Type             uint32
	TargetName, Comment     *uint16
	LastWritten             syscall.Filetime
	BlobSize                uint32
	Blob                    *byte
	Persist, AttributeCount uint32
	Attributes              uintptr
	TargetAlias, UserName   *uint16
}

func wideString(p *uint16) string {
	if p == nil {
		return ""
	}
	var units []uint16
	for i := uintptr(0); i < 32768; i++ {
		unit := *(*uint16)(unsafe.Add(unsafe.Pointer(p), i*2))
		if unit == 0 {
			break
		}
		units = append(units, unit)
	}
	return string(utf16.Decode(units))
}

func credentialValue(c *credential) (string, error) {
	if c.BlobSize == 0 || c.BlobSize > 256<<10 || c.Blob == nil {
		return "", errors.New("invalid CLI credential")
	}
	data := unsafe.Slice(c.Blob, c.BlobSize)
	if strings.ContainsRune(string(data[:min(30, len(data))]), 0) {
		if len(data)%2 != 0 {
			return "", errors.New("invalid CLI credential encoding")
		}
		units := make([]uint16, len(data)/2)
		for i := range units {
			units[i] = uint16(data[2*i]) | uint16(data[2*i+1])<<8
		}
		return string(utf16.Decode(units)), nil
	}
	return string(data), nil
}

func readCredential(ctx context.Context, account string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	target, err := syscall.UTF16PtrFromString(account + ".copilot-cli")
	if err != nil {
		return "", errors.New("invalid CLI credential account")
	}
	var value *credential
	ok, _, _ := credRead.Call(uintptr(unsafe.Pointer(target)), 1, 0, uintptr(unsafe.Pointer(&value)))
	if ok == 0 {
		return "", errors.New("Copilot CLI work credential is unavailable")
	}
	defer credFree.Call(uintptr(unsafe.Pointer(value)))
	if err := ctx.Err(); err != nil {
		return "", err
	}
	raw, err := credentialValue(value)
	if err != nil || !strings.HasPrefix(raw, "copilot-keychain-chunks-v1:") {
		return raw, err
	}
	parts := strings.Split(raw, ":")
	if len(parts) != 4 {
		return "", errors.New("unsupported CLI credential chunks")
	}
	generation, err := hex.DecodeString(parts[1])
	count, countErr := strconv.Atoi(parts[2])
	checksum, checksumErr := hex.DecodeString(parts[3])
	if err != nil || len(generation) != 16 || countErr != nil || count < 1 || count > 64 || checksumErr != nil || len(checksum) != 32 {
		return "", errors.New("unsupported CLI credential chunks")
	}
	filter, _ := syscall.UTF16PtrFromString("copilot-keychain-chunk.*")
	var length uint32
	var entries **credential
	ok, _, _ = credEnumerate.Call(uintptr(unsafe.Pointer(filter)), 0, uintptr(unsafe.Pointer(&length)), uintptr(unsafe.Pointer(&entries)))
	if ok == 0 {
		return "", errors.New("Copilot CLI credential chunks are unavailable")
	}
	defer credFree.Call(uintptr(unsafe.Pointer(entries)))
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if length > 65536 {
		return "", errors.New("too many CLI credential chunks")
	}
	chunks := make([]string, count)
	for _, entry := range unsafe.Slice(entries, length) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		name := wideString(entry.TargetName)
		if wideString(entry.UserName) != account || !strings.Contains(name, "."+parts[1]+".") {
			continue
		}
		at, indexErr := strconv.Atoi(name[strings.LastIndex(name, ".")+1:])
		if indexErr != nil || at < 0 || at >= count || chunks[at] != "" {
			return "", errors.New("invalid CLI credential chunks")
		}
		chunks[at], err = credentialValue(entry)
		if err != nil {
			return "", err
		}
	}
	for _, chunk := range chunks {
		if chunk == "" {
			return "", errors.New("Copilot CLI is rotating its credential; retry shortly")
		}
	}
	token := strings.Join(chunks, "")
	hash := sha256.Sum256([]byte(token))
	if hex.EncodeToString(hash[:]) != parts[3] {
		return "", errors.New("Copilot CLI credential checksum did not match")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return token, nil
}

func hideProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
