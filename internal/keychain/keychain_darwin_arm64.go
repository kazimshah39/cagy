//go:build darwin && arm64 && cgo

package keychain

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>
#include <stdlib.h>
#include <string.h>

static CFStringRef cagyString(const char *value) {
    return CFStringCreateWithCString(kCFAllocatorDefault, value, kCFStringEncodingUTF8);
}

static CFMutableDictionaryRef cagyQuery(const char *service, const char *account) {
    CFMutableDictionaryRef query = CFDictionaryCreateMutable(kCFAllocatorDefault, 0,
        &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
    if (!query) return NULL;
    CFStringRef serviceRef = cagyString(service);
    CFStringRef accountRef = cagyString(account);
    if (!serviceRef || !accountRef) {
        if (serviceRef) CFRelease(serviceRef);
        if (accountRef) CFRelease(accountRef);
        CFRelease(query);
        return NULL;
    }
    CFDictionarySetValue(query, kSecClass, kSecClassGenericPassword);
    CFDictionarySetValue(query, kSecAttrService, serviceRef);
    CFDictionarySetValue(query, kSecAttrAccount, accountRef);
    CFRelease(serviceRef);
    CFRelease(accountRef);
    return query;
}

// cagyDisableInteraction makes every background operation fail fast instead of
// opening a macOS Keychain dialog. This is important for CLI/supervisor work:
// a hidden prompt otherwise looks like a stuck spinner forever. Existing items
// keep their ACL; callers receive errSecInteractionNotAllowed when macOS would
// require the login-keychain password.
static void cagyDisableInteraction(CFMutableDictionaryRef query) {
    if (query) {
        CFDictionarySetValue(query, kSecUseAuthenticationUI, kSecUseAuthenticationUIFail);
    }
}

static OSStatus cagyRead(const char *service, const char *account, void **bytes, size_t *length) {
    *bytes = NULL;
    *length = 0;
    CFMutableDictionaryRef query = cagyQuery(service, account);
    if (!query) return errSecAllocate;
    cagyDisableInteraction(query);
    CFDictionarySetValue(query, kSecReturnData, kCFBooleanTrue);
    CFDictionarySetValue(query, kSecMatchLimit, kSecMatchLimitOne);
    CFTypeRef result = NULL;
    OSStatus status = SecItemCopyMatching(query, &result);
    CFRelease(query);
    if (status != errSecSuccess) return status;
    if (!result || CFGetTypeID(result) != CFDataGetTypeID()) {
        if (result) CFRelease(result);
        return errSecInvalidItemRef;
    }
    CFDataRef data = (CFDataRef)result;
    CFIndex size = CFDataGetLength(data);
    if (size < 0) {
        CFRelease(result);
        return errSecDecode;
    }
    if (size > 0) {
        void *copy = malloc((size_t)size);
        if (!copy) {
            CFRelease(result);
            return errSecAllocate;
        }
        memcpy(copy, CFDataGetBytePtr(data), (size_t)size);
        *bytes = copy;
        *length = (size_t)size;
    }
    CFRelease(result);
    return errSecSuccess;
}

static SecAccessRef cagyAccess(const char *label, const char **paths, size_t pathCount, OSStatus *statusOut) {
    (void)paths;
    (void)pathCount;
    *statusOut = errSecSuccess;

    // This is an explicitly user-selected personal-Mac convenience mode. AGM
    // uses the same effective policy with `security ... -A`: credential items
    // do not trigger repeated Keychain approval dialogs for local applications.
    // The item remains in the user's login Keychain and cagy still validates
    // the credential identity before catalog/default mutations.
    CFStringRef descriptor = cagyString(label && label[0] ? label : "cagy agy credential");
    if (!descriptor) {
        *statusOut = errSecAllocate;
        return NULL;
    }
    SecAccessRef access = NULL;
    OSStatus status = SecAccessCreate(descriptor, NULL, &access);
    if (status != errSecSuccess || !access) {
        CFRelease(descriptor);
        *statusOut = status == errSecSuccess ? errSecAllocate : status;
        return NULL;
    }

    CFArrayRef aclList = NULL;
    status = SecAccessCopyACLList(access, &aclList);
    if (status == errSecSuccess && aclList) {
        CFIndex count = CFArrayGetCount(aclList);
        for (CFIndex index = 0; index < count; index++) {
            SecACLRef acl = (SecACLRef)CFArrayGetValueAtIndex(aclList, index);
            status = SecACLSetContents(acl, NULL, descriptor, 0);
            if (status != errSecSuccess) break;
        }
        CFRelease(aclList);
    }
    CFRelease(descriptor);
    if (status != errSecSuccess) {
        CFRelease(access);
        *statusOut = status;
        return NULL;
    }
    *statusOut = errSecSuccess;
    return access;
}

static OSStatus cagySave(const char *service, const char *account,
                         const void *bytes, size_t length,
                         const char *label, const char **paths, size_t pathCount) {
    CFMutableDictionaryRef query = cagyQuery(service, account);
    if (!query) return errSecAllocate;
    cagyDisableInteraction(query);
    CFDataRef data = CFDataCreate(kCFAllocatorDefault, (const UInt8 *)bytes, (CFIndex)length);
    if (!data) {
        CFRelease(query);
        return errSecAllocate;
    }

    // Never change access permissions on an existing item. macOS may ask for
    // the login-keychain password for that operation; preserving the existing
    // ACL keeps cagy non-interactive for already-authorized credentials.
    CFMutableDictionaryRef update = CFDictionaryCreateMutable(kCFAllocatorDefault, 0,
        &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
    if (!update) {
        CFRelease(data);
        CFRelease(query);
        return errSecAllocate;
    }
    CFDictionarySetValue(update, kSecValueData, data);
    OSStatus status = SecItemUpdate(query, update);
    CFRelease(update);
    if (status == errSecItemNotFound) {
        OSStatus addAccessStatus = errSecSuccess;
        SecAccessRef addAccess = cagyAccess(label, paths, pathCount, &addAccessStatus);
        if (!addAccess) {
            CFRelease(data);
            CFRelease(query);
            return addAccessStatus;
        }
        CFDictionarySetValue(query, kSecValueData, data);
        CFDictionarySetValue(query, kSecAttrAccess, addAccess);
        status = SecItemAdd(query, NULL);
        CFRelease(addAccess);
    }
    CFRelease(data);
    CFRelease(query);
    return status;
}

static OSStatus cagyDelete(const char *service, const char *account) {
    CFMutableDictionaryRef query = cagyQuery(service, account);
    if (!query) return errSecAllocate;
    cagyDisableInteraction(query);
    OSStatus status = SecItemDelete(query);
    CFRelease(query);
    return status;
}

static void cagyClearFree(void *bytes, size_t length) {
    if (!bytes) return;
    if (length > 0) memset_s(bytes, length, 0, length);
    free(bytes);
}
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
	"unsafe"
)

// DarwinStore stores generic passwords through Apple's Security framework.
type DarwinStore struct {
	runSecurity func(context.Context, ...string) error
}

const keychainCallTimeout = 3 * time.Second

// The Security framework can block inside a legacy ACL prompt even when the
// caller requested non-interactive mode. Keep at most one such call alive and
// make the Go API fail fast so a supervisor never spins forever.
var keychainCallGate = make(chan struct{}, 1)

func beginKeychainCall(ctx context.Context, op string) error {
	select {
	case keychainCallGate <- struct{}{}:
		return nil
	default:
		return statusError(op, C.errSecInteractionNotAllowed)
	}
}

func endKeychainCall() { <-keychainCallGate }

func keychainCallExpired(ctx context.Context, op string, result <-chan struct{}) error {
	timer := time.NewTimer(keychainCallTimeout)
	defer timer.Stop()
	select {
	case <-result:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return statusError(op, C.errSecInteractionNotAllowed)
	}
}

func New() Store { return DarwinStore{} }

func (s DarwinStore) security(ctx context.Context, args ...string) error {
	if s.runSecurity != nil {
		return s.runSecurity(ctx, args...)
	}
	command := exec.CommandContext(ctx, "/usr/bin/security", args...)
	command.Stdin = nil
	command.Stdout = nil
	command.Stderr = nil
	return command.Run()
}

// ReplaceCanonical follows agy's known-working AGM-compatible Keychain flow:
// remove the old item, then create a new allow-all item in the exact payload
// format expected by agy. Updating the legacy item in place can trigger a
// macOS ACL password prompt, so this path intentionally never does that.
func (s DarwinStore) ReplaceCanonical(ctx context.Context, secret []byte, _ Access) error {
	payload, err := canonicalKeychainPayload(secret)
	if err != nil {
		return err
	}
	defer Zero(payload)

	operationCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for index := 0; index < 5; index++ {
		if err := s.security(operationCtx, "delete-generic-password", "-s", CanonicalService, "-a", CanonicalAccount); err != nil {
			break
		}
	}
	if err := operationCtx.Err(); err != nil {
		return errors.New("replace agy credential timed out")
	}
	if err := s.security(operationCtx,
		"add-generic-password",
		"-U",
		"-s", CanonicalService,
		"-a", CanonicalAccount,
		"-w", string(payload),
		"-T", "/usr/bin/security",
		"-T", "/usr/bin/codesign",
		"-T", "",
		"-A",
	); err != nil {
		if operationCtx.Err() != nil {
			return errors.New("replace agy credential timed out")
		}
		return errors.New("replace agy credential failed")
	}
	return nil
}

func (DarwinStore) Read(ctx context.Context, item Item) ([]byte, error) {
	if err := item.validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := beginKeychainCall(ctx, "read"); err != nil {
		return nil, err
	}
	service := C.CString(item.Service)
	account := C.CString(item.Account)
	type result struct {
		data   []byte
		status C.OSStatus
		err    error
	}
	results := make(chan result, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer endKeychainCall()
		defer C.free(unsafe.Pointer(service))
		defer C.free(unsafe.Pointer(account))
		var raw unsafe.Pointer
		var length C.size_t
		status := C.cagyRead(service, account, &raw, &length)
		if status == C.errSecSuccess {
			defer C.cagyClearFree(raw, length)
			if uint64(length) > uint64(^uint(0)>>1) {
				results <- result{status: status, err: errors.New("keychain read returned an oversized item")}
				return
			}
			if length == 0 {
				results <- result{status: status, data: []byte{}}
				return
			}
			results <- result{status: status, data: C.GoBytes(raw, C.int(length))}
			return
		}
		results <- result{status: status}
	}()
	if err := keychainCallExpired(ctx, "read", done); err != nil {
		return nil, err
	}
	res := <-results
	if res.err != nil {
		return nil, res.err
	}
	if res.status != C.errSecSuccess {
		return nil, statusError("read", res.status)
	}
	return res.data, nil
}

func (DarwinStore) Exists(ctx context.Context, item Item) (bool, error) {
	if err := item.validate(); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := beginKeychainCall(ctx, "inspect"); err != nil {
		return false, err
	}
	service := C.CString(item.Service)
	account := C.CString(item.Account)
	type result struct{ status C.OSStatus }
	results := make(chan result, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer endKeychainCall()
		defer C.free(unsafe.Pointer(service))
		defer C.free(unsafe.Pointer(account))
		var raw unsafe.Pointer
		var length C.size_t
		status := C.cagyRead(service, account, &raw, &length)
		if status == C.errSecSuccess {
			C.cagyClearFree(raw, length)
		}
		results <- result{status: status}
	}()
	if err := keychainCallExpired(ctx, "inspect", done); err != nil {
		return false, err
	}
	res := <-results
	if res.status == C.errSecItemNotFound {
		return false, nil
	}
	if res.status != C.errSecSuccess {
		return false, statusError("inspect", res.status)
	}
	return true, nil
}

func (DarwinStore) Save(ctx context.Context, item Item, secret []byte, access Access) error {
	if err := item.validate(); err != nil {
		return err
	}
	if len(secret) == 0 {
		return errors.New("keychain secret cannot be empty")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := beginKeychainCall(ctx, "save"); err != nil {
		return err
	}
	service := C.CString(item.Service)
	account := C.CString(item.Account)
	label := C.CString(access.Label)
	secretCopy := C.CBytes(secret)
	paths := make([]*C.char, 0, len(access.TrustedPaths))
	for _, path := range access.TrustedPaths {
		if path != "" {
			paths = append(paths, C.CString(path))
		}
	}
	var pathPointer **C.char
	if len(paths) > 0 {
		pathPointer = &paths[0]
	}
	done := make(chan struct{})
	result := make(chan C.OSStatus, 1)
	go func() {
		defer close(done)
		defer endKeychainCall()
		defer C.free(unsafe.Pointer(service))
		defer C.free(unsafe.Pointer(account))
		defer C.free(unsafe.Pointer(label))
		defer C.cagyClearFree(secretCopy, C.size_t(len(secret)))
		for _, path := range paths {
			defer C.free(unsafe.Pointer(path))
		}
		result <- C.cagySave(service, account, secretCopy, C.size_t(len(secret)), label,
			(**C.char)(unsafe.Pointer(pathPointer)), C.size_t(len(paths)))
	}()
	if err := keychainCallExpired(ctx, "save", done); err != nil {
		return err
	}
	status := <-result
	if status != C.errSecSuccess {
		return statusError("save", status)
	}
	return nil
}

func (DarwinStore) Delete(ctx context.Context, item Item) error {
	if err := item.validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := beginKeychainCall(ctx, "delete"); err != nil {
		return err
	}
	service, account := C.CString(item.Service), C.CString(item.Account)
	done := make(chan struct{})
	result := make(chan C.OSStatus, 1)
	go func() {
		defer close(done)
		defer endKeychainCall()
		defer C.free(unsafe.Pointer(service))
		defer C.free(unsafe.Pointer(account))
		result <- C.cagyDelete(service, account)
	}()
	if err := keychainCallExpired(ctx, "delete", done); err != nil {
		return err
	}
	status := <-result
	if status == C.errSecItemNotFound {
		return nil
	}
	if status != C.errSecSuccess {
		return statusError("delete", status)
	}
	return nil
}

func statusError(op string, status C.OSStatus) error {
	kind := ErrorSystem
	switch status {
	case C.errSecItemNotFound:
		kind = ErrorNotFound
	case C.errSecDuplicateItem:
		kind = ErrorDuplicate
	case C.errSecAuthFailed, C.errSecInteractionNotAllowed:
		kind = ErrorDenied
	case C.errSecUserCanceled:
		kind = ErrorInteractionNeeded
	case C.errSecInvalidItemRef, C.errSecDecode, C.errSecParam:
		kind = ErrorInvalidItem
	}
	return &Error{Op: op, Kind: kind, Status: int32(status)}
}

var _ Store = DarwinStore{}
var _ = fmt.Sprintf
