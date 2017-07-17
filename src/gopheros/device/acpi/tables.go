package acpi

import "unsafe"

const (
	// RDSP must be located in the physical memory region 0xe0000 to 0xfffff
	rsdpLocationLow uintptr = 0xe0000
	rsdpLocationHi  uintptr = 0xfffff

	rsdpRevisionACPI1 uint8 = 0
)

var (
	rsdtSignature = [8]byte{'R', 'S', 'D', ' ', 'P', 'T', 'R', ' '}
)

// rsdpDescriptor defines the root system descriptor pointer for ACPI 1.0. This
// is used as the entry-point for parsing ACPI data.
type rsdpDescriptor struct {
	// The signature must contain "RSD PTR " (last byte is a space).
	signature [8]byte

	// A value that when added to the sum of all other bytes in the 32-bit
	// RSDT should result in the value 0.
	checksum uint8

	oemID [6]byte

	// ACPI revision number. It is 0 for ACPI1.0 and 2 for versions 2.0 to 6.1.
	revision uint8

	// Physical address of 32-bit root system descriptor table.
	rsdtAddr uint32
}

// rsdpDescriptor2 extends rsdpDescriptor with additional fields. It is used
// when rsdpDescriptor.revision > 1.
type rsdpDescriptor2 struct {
	rsdpDescriptor

	// The size of the 64-bit root system descriptor table.
	length uint32

	// Physical address of 64-bit root system descriptor table.
	xsdtAddr uint64

	// A value that when added to the sum of all bytes in the 64-bit RSDT
	// should result in the value 0.
	extendedChecksum uint8

	reserved [3]byte
}

// sdtHeader defines the common header for all ACPI-related tables.
type sdtHeader struct {
	// The signature defines the table type.
	signature [4]byte

	// The length of the table
	length uint32

	revision uint8

	// A value that when added to the sum of all other bytes in the table
	// should result in the value 0.
	checksum uint8

	oemID       [6]byte
	oemTableID  [8]byte
	oemRevision uint32

	creatorID       uint32
	creatorRevision uint32
}

// validTable calculates the checksum for an ACPI table of length tableLength
// that starts at tablePtr and returns true if the table is valid.
func validTable(tablePtr uintptr, tableLength uint32) bool {
	var (
		i   uint32
		sum uint8
	)

	for i = 0; i < tableLength; i++ {
		sum += *(*uint8)(unsafe.Pointer(tablePtr + uintptr(i)))
	}

	return sum == 0
}
