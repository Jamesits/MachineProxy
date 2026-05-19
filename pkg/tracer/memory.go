//go:build linux

package tracer

import (
	"bytes"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ReadString reads a NUL-terminated string from the tracee's address space.
func ReadString(pid int, addr uintptr) (string, error) {
	if addr == 0 {
		return "", nil
	}

	var buf [4096]byte
	local := unix.Iovec{Base: &buf[0]}
	local.SetLen(len(buf))
	remote := unix.RemoteIovec{Base: addr, Len: len(buf)}

	n, err := unix.ProcessVMReadv(pid, []unix.Iovec{local}, []unix.RemoteIovec{remote}, 0)
	if err != nil {
		return "", err
	}

	if idx := bytes.IndexByte(buf[:n], 0); idx >= 0 {
		return string(buf[:idx]), nil
	}
	return string(buf[:n]), nil
}

// ReadPointer reads a single pointer-sized value from the tracee.
func ReadPointer(pid int, addr uintptr) (uintptr, error) {
	var val uintptr
	local := unix.Iovec{
		Base: (*byte)(unsafe.Pointer(&val)),
	}
	local.SetLen(int(unsafe.Sizeof(val)))
	remote := unix.RemoteIovec{Base: addr, Len: int(unsafe.Sizeof(val))}

	_, err := unix.ProcessVMReadv(pid, []unix.Iovec{local}, []unix.RemoteIovec{remote}, 0)
	return val, err
}

// ReadStringArray reads a NULL-terminated array of string pointers from the
// tracee (e.g. argv or envp).
func ReadStringArray(pid int, addr uintptr) ([]string, error) {
	if addr == 0 {
		return nil, nil
	}

	var result []string
	ptrSize := unsafe.Sizeof(uintptr(0))

	for i := 0; ; i++ {
		ptr, err := ReadPointer(pid, addr+uintptr(i)*ptrSize)
		if err != nil {
			return nil, err
		}
		if ptr == 0 {
			break
		}

		s, err := ReadString(pid, ptr)
		if err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, nil
}

// WriteBytes writes data into the tracee's address space at the given address.
func WriteBytes(pid int, addr uintptr, data []byte) error {
	if len(data) == 0 {
		return nil
	}
	local := unix.Iovec{Base: &data[0]}
	local.SetLen(len(data))
	remote := unix.RemoteIovec{Base: addr, Len: len(data)}
	_, err := unix.ProcessVMWritev(pid, []unix.Iovec{local}, []unix.RemoteIovec{remote}, 0)
	return err
}

// WriteStringArray writes a NULL-terminated string pointer array and all
// string data into the tracee at addr. Returns the total bytes consumed.
// Layout: [ptr0, ptr1, ..., NULL, "str0\0", "str1\0", ...]
func WriteStringArray(pid int, addr uintptr, strs []string) (int, error) {
	ptrSize := int(unsafe.Sizeof(uintptr(0)))

	// Calculate layout: pointer table + null terminator, then string data.
	tableSize := (len(strs) + 1) * ptrSize
	dataStart := addr + uintptr(tableSize)

	// Build the buffer with both the pointer table and string data.
	var stringData []byte
	ptrs := make([]uintptr, len(strs)+1)

	offset := 0
	for i, s := range strs {
		ptrs[i] = dataStart + uintptr(offset)
		stringData = append(stringData, []byte(s)...)
		stringData = append(stringData, 0)
		offset += len(s) + 1
	}
	ptrs[len(strs)] = 0 // NULL terminator

	// Write pointer table.
	ptrBuf := unsafe.Slice((*byte)(unsafe.Pointer(&ptrs[0])), tableSize)
	if err := WriteBytes(pid, addr, ptrBuf); err != nil {
		return 0, err
	}

	// Write string data.
	if err := WriteBytes(pid, dataStart, stringData); err != nil {
		return 0, err
	}

	return tableSize + len(stringData), nil
}
