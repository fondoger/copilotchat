package wam

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/fondoger/copilotchat/internal/provider/microsoft/entra"
)

var (
	combase        = syscall.NewLazyDLL("combase.dll")
	roInitialize   = combase.NewProc("RoInitialize")
	roUninitialize = combase.NewProc("RoUninitialize")
	roFactory      = combase.NewProc("RoGetActivationFactory")
	createString   = combase.NewProc("WindowsCreateString")
	deleteString   = combase.NewProc("WindowsDeleteString")
	stringBuffer   = combase.NewProc("WindowsGetStringRawBuffer")
	iidStatics     = guid("6aca7c92-a581-4479-9c10-752eff44fd34")
	iidInterop     = guid("f4b8e804-811e-4436-b69c-44cb67b72084")
	iidFactory     = guid("6cf2141c-0ff0-4c67-b84f-99ddbe4a72c9")
	iidAsyncInfo   = guid("00000036-0000-0000-c000-000000000046")
	iidAsyncToken  = guid("0a815852-7c44-5674-b3d2-fa2e4c1e46c9")
	iidAccount2    = guid("7b56d6f8-990b-4eb5-94a7-5621f3a8b824")
)

func guid(s string) syscall.GUID {
	b, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil || len(b) != 16 {
		panic("invalid constant GUID")
	}
	g := syscall.GUID{Data1: binary.BigEndian.Uint32(b), Data2: binary.BigEndian.Uint16(b[4:]), Data3: binary.BigEndian.Uint16(b[6:])}
	copy(g.Data4[:], b[8:])
	return g
}

// Calls use only Windows-provided WinRT APIs. All COM objects belong to
// this fixed OS thread; the UI thread remains free to run its native event loop.
type nativeCall struct {
	ctx     context.Context
	err     error
	objects []unsafe.Pointer
	strings []uintptr
}

//go:uintptrescapes
func comCall(obj unsafe.Pointer, slot int, args ...uintptr) uintptr {
	vtable := *(**[16]uintptr)(obj)
	hr, _, _ := syscall.SyscallN(vtable[slot], append([]uintptr{uintptr(obj)}, args...)...)
	return hr
}

func (n *nativeCall) check(hr uintptr, operation string) {
	if int32(hr) < 0 && n.err == nil {
		n.err = fmt.Errorf("%s: HRESULT 0x%08x", operation, uint32(hr))
	}
}

func (n *nativeCall) cleanup() {
	for i := len(n.objects) - 1; i >= 0; i-- {
		comCall(n.objects[i], 2)
	}
	for _, h := range n.strings {
		deleteString.Call(h)
	}
}

func (n *nativeCall) str(s string) uintptr {
	if n.err != nil {
		return 0
	}
	u, err := syscall.UTF16FromString(s)
	if err != nil {
		n.err = err
		return 0
	}
	var h uintptr
	hr, _, _ := createString.Call(uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&h)))
	n.check(hr, "WindowsCreateString")
	if h != 0 {
		n.strings = append(n.strings, h)
	}
	return h
}

func (n *nativeCall) keep(p unsafe.Pointer) unsafe.Pointer {
	if p != nil {
		n.objects = append(n.objects, p)
	}
	return p
}

func (n *nativeCall) factory(class string, iid *syscall.GUID) unsafe.Pointer {
	h := n.str(class)
	if n.err != nil {
		return nil
	}
	var p unsafe.Pointer
	hr, _, _ := roFactory.Call(h, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&p)))
	n.check(hr, "RoGetActivationFactory")
	return n.keep(p)
}

//go:uintptrescapes
func (n *nativeCall) object(obj unsafe.Pointer, slot int, args ...uintptr) unsafe.Pointer {
	if n.err != nil {
		return nil
	}
	if obj == nil {
		n.err = fmt.Errorf("missing COM object for slot %d", slot)
		return nil
	}
	var p unsafe.Pointer
	n.check(comCall(obj, slot, append(args, uintptr(unsafe.Pointer(&p)))...), fmt.Sprintf("COM slot %d", slot))
	return n.keep(p)
}

func (n *nativeCall) number(obj unsafe.Pointer, slot int) uint32 {
	if n.err != nil {
		return 0
	}
	if obj == nil {
		n.err = fmt.Errorf("Windows WAM returned no value")
		return 0
	}
	var value uint32
	n.check(comCall(obj, slot, uintptr(unsafe.Pointer(&value))), fmt.Sprintf("COM scalar slot %d", slot))
	return value
}

func (n *nativeCall) text(obj unsafe.Pointer, slot int) string {
	if n.err != nil {
		return ""
	}
	if obj == nil {
		n.err = fmt.Errorf("Windows WAM returned no account")
		return ""
	}
	var h uintptr
	n.check(comCall(obj, slot, uintptr(unsafe.Pointer(&h))), "COM text")
	if h == 0 {
		return ""
	}
	defer deleteString.Call(h)
	if n.err != nil {
		return ""
	}
	var count uint32
	p, _, _ := stringBuffer.Call(h, uintptr(unsafe.Pointer(&count)))
	if count > 128*1024 {
		n.err = fmt.Errorf("oversized WAM string")
		return ""
	}
	if count == 0 || p == 0 {
		return ""
	}
	// The ABI returns this pointer in a register. Reinterpret its pointer-sized
	// bits; the buffer belongs to Windows and remains valid until h is deleted.
	buffer := *(*unsafe.Pointer)(unsafe.Pointer(&p))
	return syscall.UTF16ToString(unsafe.Slice((*uint16)(buffer), count))
}

func (n *nativeCall) wait(op unsafe.Pointer) unsafe.Pointer {
	info := n.object(op, 0, uintptr(unsafe.Pointer(&iidAsyncInfo)))
	if n.err != nil {
		return nil
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for n.err == nil {
		if err := n.ctx.Err(); err != nil {
			comCall(info, 9)
			n.err = err
			break
		}
		switch n.number(info, 7) {
		case 1:
			return n.object(op, 8)
		case 2:
			n.err = context.Canceled
		case 3:
			code := n.number(info, 8)
			n.err = fmt.Errorf("WAM async failed: HRESULT 0x%08x", code)
		}
		if n.err != nil {
			break
		}
		select {
		case <-n.ctx.Done():
			comCall(info, 9)
			n.err = n.ctx.Err()
		case <-ticker.C:
		}
	}
	return nil
}

func (n *nativeCall) property(props unsafe.Pointer, key, value string) {
	k, v := n.str(key), n.str(value)
	if n.err != nil {
		return
	}
	if props == nil {
		n.err = fmt.Errorf("Windows WAM returned no request properties")
		return
	}
	var replaced byte
	n.check(comCall(props, 10, k, v, uintptr(unsafe.Pointer(&replaced))), "WAM request property")
}

const managerClass = "Windows.Security.Authentication.Web.Core.WebAuthenticationCoreManager"

// The public client used by Copilot CLI's Entra broker. WAM and the service
// enforce application consent, tenant policies and account entitlement.
const clientID = "aebc6443-996d-45c2-90f0-388ff96faa56"

// acquire returns only in-memory values. A blank accountID requests the native
// account picker; a supplied opaque ID selects exactly that account silently.
func acquire(ctx context.Context, hwnd uintptr, accountID string) (token, selectedID string, err error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if accountID == "" && hwnd == 0 {
		return "", "", fmt.Errorf("Windows sign-in needs an application window")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	n := &nativeCall{ctx: ctx}
	hr, _, _ := roInitialize.Call(1)
	n.check(hr, "RoInitialize")
	if n.err != nil {
		return "", "", n.err
	}
	defer roUninitialize.Call()
	defer n.cleanup()
	statics := n.factory(managerClass, &iidStatics)
	provider := n.wait(n.object(statics, 12, n.str("https://login.microsoft.com"), n.str("organizations")))
	factory := n.factory("Windows.Security.Authentication.Web.Core.WebTokenRequest", &iidFactory)
	var prompt uintptr
	if accountID == "" {
		prompt = 1 // Force the broker UI, with select_account below, for each new provider.
	}
	request := n.object(factory, 7, uintptr(provider), n.str(entra.Resource+"/.default openid profile offline_access"), n.str(clientID), prompt)
	properties := n.object(request, 10)
	// MSAL's direct WAM protocol uses OAuth 2.0 scopes, not a v1 resource property.
	n.property(properties, "wam_compat", "2.0")
	var result unsafe.Pointer
	if accountID == "" {
		n.property(properties, "prompt", "select_account")
		interop := n.factory(managerClass, &iidInterop)
		result = n.wait(n.object(interop, 6, hwnd, uintptr(request), uintptr(unsafe.Pointer(&iidAsyncToken))))
	} else {
		account := n.wait(n.object(statics, 10, uintptr(provider), n.str(accountID)))
		if n.err == nil && account == nil {
			n.err = fmt.Errorf("Windows account is unavailable; reconnect this provider")
		}
		result = n.wait(n.object(statics, 7, uintptr(request), uintptr(account)))
	}
	if n.err != nil {
		return "", "", n.err
	}
	status := n.number(result, 7)
	if n.err != nil {
		return "", "", n.err
	}
	if status == 1 {
		return "", "", context.Canceled
	}
	if status == 3 {
		return "", "", fmt.Errorf("Windows needs you to sign in again; reconnect this provider")
	}
	if status != 0 {
		providerError := n.object(result, 8)
		code := uint32(0)
		if providerError != nil {
			code = n.number(providerError, 6)
		}
		return "", "", fmt.Errorf("WAM response status=%d, code=0x%08x", status, code)
	}
	responses := n.object(result, 6)
	size := n.number(responses, 7)
	if n.err != nil {
		return "", "", n.err
	}
	if size != 1 {
		return "", "", fmt.Errorf("unexpected WAM response count %d", size)
	}
	response := n.object(responses, 6, 0)
	token = n.text(response, 6)
	account := n.object(response, 8)
	account2 := n.object(account, 0, uintptr(unsafe.Pointer(&iidAccount2)))
	selectedID = n.text(account2, 6)
	return token, selectedID, n.err
}
