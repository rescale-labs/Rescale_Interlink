//go:build darwin

package platform

/*
#cgo LDFLAGS: -framework IOKit -framework CoreFoundation
#include <IOKit/pwr_mgt/IOPMLib.h>
#include <CoreFoundation/CoreFoundation.h>

static IOReturn createAssertion(CFStringRef reason, IOPMAssertionID *id) {
    return IOPMAssertionCreateWithName(
        kIOPMAssertionTypeNoIdleSleep,
        kIOPMAssertionLevelOn,
        reason,
        id
    );
}
*/
import "C"

import (
	"fmt"
	"sync"
	"unsafe"
)

func inhibitSleep(reason string) (func(), error) {
	cReason := C.CString(reason)
	cfReason := C.CFStringCreateWithCString(C.kCFAllocatorDefault, cReason, C.kCFStringEncodingUTF8)
	C.free(unsafe.Pointer(cReason))
	defer C.CFRelease(C.CFTypeRef(cfReason))

	var assertionID C.IOPMAssertionID
	ret := C.createAssertion(cfReason, &assertionID)
	if ret != C.kIOReturnSuccess {
		return func() {}, fmt.Errorf("IOPMAssertionCreateWithName failed: 0x%x", int(ret))
	}

	var once sync.Once
	release := func() {
		once.Do(func() {
			C.IOPMAssertionRelease(assertionID)
		})
	}
	return release, nil
}
