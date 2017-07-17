package acpi

import (
	"gopheros/device"
	"gopheros/kernel"
	"gopheros/kernel/kfmt"
	"gopheros/kernel/mem"
	"gopheros/kernel/mem/pmm"
	"gopheros/kernel/mem/pmm/allocator"
	"gopheros/kernel/mem/vmm"
	"io"
	"unsafe"
)

var (
	errMissingRSDP           = &kernel.Error{Module: "acpi", Message: "could not locate ACPI RSDP"}
	errTableChecksumMismatch = &kernel.Error{Module: "acpi", Message: "detected checksum mismatch while parsing ACPI table header"}

	mapFn   = vmm.Map
	unmapFn = vmm.Unmap
)

type acpiDriver struct {
	// mappedPages keeps track of all pages mapped while parsing the ACPI
	// tables so they can be unmapped after parsing is complete.
	mappedPages map[vmm.Page]struct{}

	// rsdtAddr holds the address to the root system descriptor table.
	rsdtAddr uintptr

	// useXSDT specifies if the driver must use the XSDT or the RSDT table.
	useXSDT bool
}

// DriverInit initializes this driver.
func (drv *acpiDriver) DriverInit(w io.Writer) *kernel.Error {
	drv.mappedPages = make(map[vmm.Page]struct{})
	defer func() {
		var gotUnmapErr bool
		for page := range drv.mappedPages {
			if err := unmapFn(page); err != nil {
				gotUnmapErr = true
			}
		}

		// Reclaim memory used by ACPI tables
		if !gotUnmapErr {
			allocator.ReclaimRegions()
		}
	}()

	return drv.parseRSDT(w)
}

func (drv *acpiDriver) parseRSDT(w io.Writer) *kernel.Error {
	header, sizeofHeader, err := drv.mapACPITable(drv.rsdtAddr)
	if err != nil {
		return err
	}

	var (
		payloadLen   = header.length - uint32(sizeofHeader)
		sdtAddresses []uintptr
	)

	// RSDT uses 4-byte long pointers whereas the XSDT uses 8-byte long.
	switch drv.useXSDT {
	case true:
		sdtAddresses = make([]uintptr, payloadLen>>3)
		for curPtr, i := drv.rsdtAddr+sizeofHeader, 0; i < len(sdtAddresses); curPtr, i = curPtr+8, i+1 {
			sdtAddresses[i] = uintptr(*(*uint64)(unsafe.Pointer(curPtr)))
		}
	default:
		sdtAddresses = make([]uintptr, payloadLen>>2)
		for curPtr, i := drv.rsdtAddr+sizeofHeader, 0; i < len(sdtAddresses); curPtr, i = curPtr+4, i+1 {
			sdtAddresses[i] = uintptr(*(*uint32)(unsafe.Pointer(curPtr)))
		}
	}

	for _, addr := range sdtAddresses {
		if header, _, err = drv.mapACPITable(addr); err != nil {
			switch err {
			case errTableChecksumMismatch:
				continue
			default:
				return err
			}
		}

		signature := header.signature[:]
		switch signature {
		default:
			kfmt.Fprintf(w, "found %s at 0x%16x, len: %6d\n", signature, addr, header.length)
		}

	}

	return nil
}

// mapACPITable attempts to map and parse the header for the ACPI table starting
// at the given address. It then uses the length field for the header to expand
// the mapping to cover the table contents and verifies the checksum before
// returning a pointer to the table header.
func (drv *acpiDriver) mapACPITable(tableAddr uintptr) (header *sdtHeader, sizeofHeader uintptr, err *kernel.Error) {
	// First map enough pages to access the header
	sizeofHeader = unsafe.Sizeof(sdtHeader{})
	if err = drv.mapRegion(tableAddr, mem.Size(sizeofHeader)); err != nil {
		return nil, sizeofHeader, err
	}

	// Expand mapping to cover the table contents
	header = (*sdtHeader)(unsafe.Pointer(tableAddr))
	if err = drv.mapRegion(tableAddr, mem.Size(header.length)); err != nil {
		return nil, sizeofHeader, err
	}

	if !validTable(tableAddr, header.length) {
		return nil, sizeofHeader, errTableChecksumMismatch
	}

	return header, sizeofHeader, nil
}

// mapRegion ensures that a virtuel memory mapping exists for the memory region
// starting at startAddr with the size. The mapped pages are kept in a reservation
// map so they can be safely unmapped.
func (drv *acpiDriver) mapRegion(startAddr uintptr, size mem.Size) *kernel.Error {
	// Convert range into pages by rounding up (startAddr + size) to the nearest
	// page and rounding down startAddr to the nearest page.
	pageSizeMinus1 := uintptr(mem.PageSize - 1)
	endAddr := (startAddr + uintptr(size) + pageSizeMinus1) & ^pageSizeMinus1
	startAddr = startAddr & ^pageSizeMinus1

	for curPage := vmm.PageFromAddress(startAddr); curPage <= vmm.PageFromAddress(endAddr); curPage++ {
		if _, exists := drv.mappedPages[curPage]; exists {
			continue
		}

		if err := mapFn(curPage, pmm.Frame(curPage), vmm.FlagPresent); err != nil {
			return err
		}

		drv.mappedPages[curPage] = struct{}{}
	}

	return nil
}

// DriverName returns the name of this driver.
func (*acpiDriver) DriverName() string {
	return "ACPI"
}

// DriverVersion returns the version of this driver.
func (*acpiDriver) DriverVersion() (uint16, uint16, uint16) {
	return 0, 0, 1
}

// locateRSDT scans the memory region [rsdpLocationLow, rsdpLocationHi] looking
// for the signature of the root system descriptor pointer (RSDP). If the RSDP
// is found and is valid, locateRSDT returns the physical address of the root
// system descriptor table (RSDT) or the extended system descriptor table (XSDT)
// if the system supports ACPI 2.0+.
func locateRSDT() (uintptr, bool, *kernel.Error) {
	var (
		rsdp  *rsdpDescriptor
		rsdp2 *rsdpDescriptor2
	)

	// Cleanup temporary identity mappings when the function returns
	defer func() {
		for curPage := vmm.PageFromAddress(rsdpLocationLow); curPage <= vmm.PageFromAddress(rsdpLocationHi); curPage++ {
			unmapFn(curPage)
		}
	}()

	// Setup temporary identity mapping so we can scan for the header
	for curPage := vmm.PageFromAddress(rsdpLocationLow); curPage <= vmm.PageFromAddress(rsdpLocationHi); curPage++ {
		if err := mapFn(curPage, pmm.Frame(curPage), vmm.FlagPresent); err != nil {
			return 0, false, err
		}
	}

	// The RSDP should be aligned on a 16-byte boundary
checkNextBlock:
	for curPtr := rsdpLocationLow; curPtr < rsdpLocationHi; curPtr += 16 {
		rsdp = (*rsdpDescriptor)(unsafe.Pointer(curPtr))
		for i, b := range rsdtSignature {
			if rsdp.signature[i] != b {
				continue checkNextBlock
			}
		}

		if rsdp.revision == rsdpRevisionACPI1 {
			if !validTable(curPtr, uint32(unsafe.Sizeof(*rsdp))) {
				continue
			}

			return uintptr(rsdp.rsdtAddr), false, nil
		}

		// System uses ACPI revision > 1 and provides an extended RSDP
		// which can be accessed at the same place.
		rsdp2 = (*rsdpDescriptor2)(unsafe.Pointer(curPtr))
		if !validTable(curPtr, uint32(unsafe.Sizeof(*rsdp2))) {
			continue
		}

		return uintptr(rsdp2.xsdtAddr), true, nil
	}

	return 0, false, errMissingRSDP
}
func probeForACPI() device.Driver {
	if rsdtAddr, useXSDT, err := locateRSDT(); err == nil {
		return &acpiDriver{
			rsdtAddr: rsdtAddr,
			useXSDT:  useXSDT,
		}
	}

	return nil
}

func init() {
	ProbeFuncs = append(ProbeFuncs, probeForACPI)
}
