package windows

import (
	"bytes"
	"encoding/binary"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/carbonblack/binee/pefile"
	"github.com/carbonblack/binee/util"
)

type StartupInfo struct {
	Cb          int32
	Reserved    uint32
	Desktop     uint32
	Title       uint32
	X           int32
	Y           int32
	XSize       int32
	YSize       int32
	XCountChars int32
	YCountChars int32
	Flags       int32
	ShowWindow  int16
	Reserved2   int16
	Reserved2a  uint32
	StdInput    uint32
	StdOutput   uint32
	StdError    uint32
}

//HANDLE CreateFileMappingW(
//  HANDLE                hFile,
//  LPSECURITY_ATTRIBUTES lpFileMappingAttributes,
//  DWORD                 flProtect,
//  DWORD                 dwMaximumSizeHigh,
//  DWORD                 dwMaximumSizeLow,
//  LPCWSTR               lpName
//);
func createFileMapping(emu *WinEmulator, in *Instruction, wide bool) func(emu *WinEmulator, in *Instruction) bool {
	fileHandle, ok := emu.Handles[in.Args[0]]
	if !ok {
		emu.setLastError(ERROR_INVALID_HANDLE)
		return SkipFunctionStdCall(true, 0)
	}
	file := fileHandle.File
	fileSize, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		emu.setLastError(ERROR_INVALID_HANDLE)
		return SkipFunctionStdCall(true, 0)
	}
	fileData := make([]byte, fileSize)
	_, err = file.Read(fileData)
	if err != nil {
		emu.setLastError(ERROR_INVALID_HANDLE)
		return SkipFunctionStdCall(true, 0)
	}
	addr := emu.Heap.Malloc(uint64(fileSize))
	err = emu.Uc.MemWrite(addr, fileData)
	if err != nil {
		return SkipFunctionStdCall(true, 0)
	}
	return SkipFunctionStdCall(true, addr)
}
func enumDeviceDrivers(emu *WinEmulator, in *Instruction) func(emu *WinEmulator, in *Instruction) bool {

	if emu.PtrSize == 4 {
		count := uint32(in.Args[1] / 4)
		numberOfDrivers := uint32(len(emu.Opts.Drivers))
		if count < numberOfDrivers {
			//Need more bytes
			util.PutPointer(emu.Uc, emu.PtrSize, in.Args[2], uint64(numberOfDrivers)*4)
			return SkipFunctionStdCall(true, 0) //Fail
		}
		index := in.Args[0]
		i := uint32(0)
		for val, _ := range emu.Opts.Drivers {
			if i == count {
				break
			}
			util.PutPointer(emu.Uc, emu.PtrSize, index, uint64(val))
			index += 4
		}

	}
	return SkipFunctionStdCall(true, 1)
}

func getDriveType(emu *WinEmulator, in *Instruction, wide bool) func(*WinEmulator, *Instruction) bool {
	/*the purpose of this function is give the binary the idea
	 the windows has multiple drives with different types, to
	be able to monitor whatever action is being done*/
	var driveName string
	if wide {
		driveName = util.ReadWideChar(emu.Uc, in.Args[0], 0)
	} else {
		driveName = util.ReadASCII(emu.Uc, in.Args[0], 0)
	}
	//Drives has the scheme of "A:\"

	returnCode := uint64(1) //DRIVE_NO_ROOT_DIR
	switch driveName[0] {
	case 'A':
		returnCode = 0 //DRIVE_UNKNOWN
		break
	case 'C':
		returnCode = 3 //DRIVE_FIXED
		break
	case 'D':
		returnCode = 2 //DRIVE_REMOVABLE
		break
	case 'E':
		returnCode = 4 //DRIVE_REMOTE
		break
	case 'F':
		returnCode = 5 //DRIVE_CDROM
		break
	case 'G':
		returnCode = 6 //DRIVE_RAMDISK
		break
	}
	return SkipFunctionStdCall(true, returnCode)
}

func getDeviceDriverBaseName(emu *WinEmulator, in *Instruction, wide bool) func(emu *WinEmulator, in *Instruction) bool {
	ret := 0
	if wide {
		address := in.Args[0]
		driverName := util.ASCIIToWinWChar(emu.Opts.Drivers[int(address)])
		maxSize := in.Args[2]
		ret = len(driverName)
		if int(maxSize+2) < ret {
			driverName = driverName[0 : maxSize-2]
			ret = int(maxSize - 2)
		}
		driverName = append(driverName, 0, 0)
		emu.Uc.MemWrite(in.Args[1], driverName)
	} else {
		address := in.Args[0]
		driverName := []byte(emu.Opts.Drivers[int(address)])
		maxSize := in.Args[2]
		ret = len(driverName)
		if int(maxSize-1) < ret {
			driverName = driverName[0 : maxSize-1]
			ret = int(maxSize)
		}
		driverName = append(driverName, 0)

		emu.Uc.MemWrite(in.Args[1], driverName)
	}
	return SkipFunctionStdCall(true, uint64(ret))
}

func lstrcmpi(emu *WinEmulator, in *Instruction, wide bool) func(emu *WinEmulator, in *Instruction) bool {
	var retVal int
	if wide {
		string1 := util.ReadWideChar(emu.Uc, in.Args[0], 0)
		string2 := util.ReadWideChar(emu.Uc, in.Args[1], 0)
		retVal = strings.Compare(strings.ToLower(string1), strings.ToLower(string2))

	} else {
		string1 := util.ReadASCII(emu.Uc, in.Args[0], 0)
		string2 := util.ReadASCII(emu.Uc, in.Args[1], 0)
		retVal = strings.Compare(strings.ToLower(string1), strings.ToLower(string2))
	}
	return SkipFunctionStdCall(true, uint64(retVal))
}
func getVolumeInformation(emu *WinEmulator, in *Instruction, wide bool) func(emu *WinEmulator, in *Instruction) bool {
	/*The function depends on the given parameters to know what is requested,
	  nulled input means its not required */
	volumeName := emu.Opts.VolumeName
	volumeSerial := emu.Opts.VolumeSerialNumber
	volumeSystemName := emu.Opts.VolumeSystemName
	if wide {
		if in.Args[0] != 0 {
			//This might be used later to assume we have many volumes.
			//rootPathName=util.ReadWideChar(emu.Uc,in.Args[0],0)
		}
		if in.Args[1] != 0 { // Volume name is required.
			if len(volumeName) < int(in.Args[2]) { //Check volume name size
				volumeNameW := util.ASCIIToWinWChar(volumeName)
				err := emu.Uc.MemWrite(in.Args[1], volumeNameW)
				if err != nil {
					return SkipFunctionStdCall(true, 0)
				}
			}
		}
		if in.Args[3] != 0 { //Volume serial is required.
			buf := make([]byte, 4)
			binary.LittleEndian.PutUint32(buf, uint32(volumeSerial))
			err := emu.Uc.MemWrite(in.Args[3], buf)
			if err != nil {
				return SkipFunctionStdCall(true, 0)
			}
		}

		if in.Args[6] != 0 {
			if len(volumeSystemName) < int(in.Args[7]) { //Check volume name size
				volumeSystemNameW := util.ASCIIToWinWChar(volumeSystemName)
				err := emu.Uc.MemWrite(in.Args[6], volumeSystemNameW)
				if err != nil {
					return SkipFunctionStdCall(true, 0)
				}
			}
		}

	} else {

	}
	return SkipFunctionStdCall(true, 0)
}
func GetModuleHandle(emu *WinEmulator, in *Instruction, wide bool) uint64 {
	hinstance := uint64(0)
	if in.Args[0] == 0x0 {
		hinstance = emu.MemRegions.ImageAddress
	} else {
		var s string
		if wide {
			s = strings.ToLower(util.ReadWideChar(emu.Uc, in.Args[0], 0))
		} else {
			s = strings.ToLower(util.ReadASCII(emu.Uc, in.Args[0], 0))
		}
		return emu.LoadedModules[s]
	}
	return hinstance
}

func getModuleHandleEx(emu *WinEmulator, in *Instruction, wide bool) uint64 {
	hinstance := uint64(0)
	if in.Args[1] == 0x0 {
		hinstance = emu.MemRegions.ImageAddress
	} else {
		var s string
		if wide == true {
			s = strings.ToLower(util.ReadWideChar(emu.Uc, in.Args[0], 0))
		} else {
			s = strings.ToLower(util.ReadASCII(emu.Uc, in.Args[1], 0))
		}
		hinstance = emu.LoadedModules[s]
		if hinstance == 0 {
			return emu.MemRegions.ImageAddress
		}
	}
	return hinstance
}

func getEnvironmentStrings(emu *WinEmulator, in *Instruction, wide bool) func(emu *WinEmulator, in *Instruction) bool {
	b := make([]byte, 0, 100)
	for _, entry := range emu.Opts.Env {
		if wide {
			s := util.ASCIIToWinWChar(entry.Key + "=" + entry.Value)
			b = append(b, s[:]...)
			b = append(b, 0x00)
		} else {
			s := []byte(entry.Key + "=" + entry.Value)
			b = append(b, s[:]...)
			b = append(b, 0x00)
		}
	}

	addr := emu.Heap.Malloc(uint64(len(b)))
	emu.Uc.MemWrite(addr, b)

	return SkipFunctionStdCall(true, addr)

}

//HANDLE OpenFileMappingA(
//DWORD  dwDesiredAccess,
//BOOL   bInheritHandle,
//LPCSTR lpName
//);
func openFileMapping(emu *WinEmulator, in *Instruction, wide bool) func(emu *WinEmulator, in *Instruction) bool {
	var path string
	if wide == false {
		path = util.ReadASCII(emu.Uc, in.Args[2], 0)
	} else {
		path = util.ReadWideChar(emu.Uc, in.Args[2], 0)
	}

	if handle, err := emu.OpenFile(path, int32(in.Args[0])); err == nil {
		addr := emu.Heap.Malloc(in.Args[2])
		emu.Handles[addr] = handle
		return SkipFunctionStdCall(true, addr)
	} else {
		return SkipFunctionStdCall(true, 0xffffffff)
	}
}

//LPVOID MapViewOfFile(
//HANDLE hFileMappingObject,
//DWORD  dwDesiredAccess,
//DWORD  dwFileOffsetHigh,
//DWORD  dwFileOffsetLow,
//SIZE_T dwNumberOfBytesToMap
//);

//This is not the identical implementation, we can just return the file handle since it won't know.

//func mapViewOfFile(emu *WinEmulator, in *Instruction, wide bool) func(emu *WinEmulator, in *Instruction) bool {
//	var path string
//
//	//Check if the handle exists
//	if _ , ok :=emu.Handles[in.Args[0]]; !ok{
//		return SkipFunctionStdCall(true, 0)
//	}
//
//	//find the address of this handle
//	util.FindKey()
//	if handle, err := emu.OpenFile(path, int32(in.Args[0])); err == nil {
//		addr := emu.Heap.Malloc(in.Args[2])
//		emu.Handles[addr] = handle
//		return SkipFunctionStdCall(true, addr)
//	} else {
//		return SkipFunctionStdCall(true, 0xffffffff)
//	}
//}

//Parameters: []string{"w:lpFileName", "dwDesiredAccess", "dwShareMode", "lpSecurityAttributes", "dwCreationDisposition", "dwFlagsAndAttributes", "hTemplateFile"},

//func FindResource(emu *WinEmulator, in *Instruction, wide bool) func(emu *WinEmulator, in *Instruction) bool {
//	//Check first if there was a name or an id.
//	//We will depend on the function, which is a hacky method.
//	//This function returns an empty string when no access occurs
//	//which will be the case  here in case of id (lower word).
//	handle:=emu.Handles[in.Args[0]]
//
//	var name string
//	if wide == false {
//		name = util.ReadASCII(emu.Uc, in.Args[1], 0)
//	} else {
//		name = util.ReadWideChar(emu.Uc, in.Args[1], 0)
//	}
//
//
//}

func createFile(emu *WinEmulator, in *Instruction, wide bool) func(emu *WinEmulator, in *Instruction) bool {
	var path string
	if wide == false {
		path = util.ReadASCII(emu.Uc, in.Args[0], 0)
	} else {
		path = util.ReadWideChar(emu.Uc, in.Args[0], 0)
	}

	if handle, err := emu.OpenFile(path, int32(in.Args[1])); err == nil {
		addr := emu.Heap.Malloc(in.Args[2])
		emu.Handles[addr] = handle
		return SkipFunctionStdCall(true, addr)
	} else {
		return SkipFunctionStdCall(true, 0xffffffff)
	}
}
func loadLibrary(emu *WinEmulator, in *Instruction, wide bool) func(emu *WinEmulator, in *Instruction) bool {
	var err error

	// read the dll that needs to be loaded
	var name string
	var orig string
	if wide {
		orig = util.ReadWideChar(emu.Uc, in.Args[0], 100)
	} else {
		orig = util.ReadASCII(emu.Uc, in.Args[0], 100)
	}
	name = strings.ToLower(orig)
	name = strings.Replace(name, "c:\\windows\\system32\\", "", -1)
	name = strings.Trim(name, "\x00")
	name = strings.Trim(name, "\u0000")

	if strings.Contains(name, ".dll") == false {
		name += ".dll"
	}

	// check if library is already loaded
	if val, ok := emu.LoadedModules[name]; ok {
		return SkipFunctionStdCall(true, val)
	}

	var realdll string
	// load Apisetschema dll for mapping to real dlls
	if apisetPath, err := util.SearchFile(emu.SearchPath, "apisetschema.dll"); err == nil {
		apiset, _ := pefile.LoadPeFile(apisetPath)
		realdll = apiset.ApiSetLookup(name)
	}

	var path string
	if path, err = util.SearchFile(emu.SearchPath, realdll); err != nil {
		if path, err = util.SearchFile(emu.SearchPath, orig); err != nil {
			return SkipFunctionStdCall(true, 0x0)
		}
	}

	if pe, err := pefile.LoadPeFile(path); err != nil {
		return SkipFunctionStdCall(true, 0x0)
	} else {
		pe.SetImageBase(emu.NextLibAddress)
		emu.LoadedModules[name] = emu.NextLibAddress
		//We have to set import address here
		err = emu.Uc.MemWrite(pe.ImageBase(), pe.RawHeaders)
		for i := 0; i < len(pe.Sections); i++ {
			err = emu.Uc.MemWrite(pe.ImageBase()+uint64(pe.Sections[i].VirtualAddress), pe.Sections[i].Raw)
		}

		// get total size of DLL in memory
		peSize := 0
		for i := 0; i < len(pe.Sections); i++ {
			peSize += int(pe.Sections[i].VirtualAddress + pe.Sections[i].Size)
		}

		for _, funcs := range pe.Exports {
			realAddr := uint64(funcs.Rva) + pe.ImageBase()
			if _, ok := emu.libFunctionAddress[name]; !ok {
				emu.libFunctionAddress[name] = make(map[string]uint64)
			}
			if _, ok := emu.libAddressFunction[name]; !ok {
				emu.libAddressFunction[name] = make(map[uint64]string)
			}
			if _, ok := emu.libOrdinalFunction[name]; !ok {
				emu.libOrdinalFunction[name] = make(map[uint16]string)
			}

			emu.libOrdinalFunction[name][funcs.Ordinal] = funcs.Name
			emu.libFunctionAddress[name][funcs.Name] = realAddr
			emu.libAddressFunction[name][realAddr] = funcs.Name
		}

		// set address for next DLL
		for i := 0; i <= peSize; i += 4096 {
			emu.NextLibAddress += 4096
		}

		return SkipFunctionStdCall(true, pe.ImageBase())
	}

}
func getProcAddress(emu *WinEmulator, baseAddress uint64, wantedFuncName string, wantedFuncOrdinal uint16) uint64 {

	raw, err := emu.Uc.MemRead(baseAddress, 4096)
	if err != nil {
		return 0
	}

	imageDosHeader := &pefile.DosHeader{}
	r := bytes.NewReader(raw)
	if err = binary.Read(r, binary.LittleEndian, imageDosHeader); err != nil {
		return 0
	}

	// move offset to CoffHeader
	if _, err = r.Seek(int64(imageDosHeader.AddressExeHeader)+4, io.SeekStart); err != nil {
		return 0
	}

	// advance reader to start of OptionalHeader(32|32+)
	if _, err = r.Seek(int64(imageDosHeader.AddressExeHeader)+4+int64(binary.Size(pefile.CoffHeader{})), io.SeekStart); err != nil {
		return 0
	}

	// check if pe or pe+, read 2 bytes to get Magic then seek backward two bytes
	var _magic uint16
	if err := binary.Read(r, binary.LittleEndian, &_magic); err != nil {
		return 0
	}
	var PeType uint16
	// check magic, must be a PE or PE+
	if _magic == 0x10b {
		PeType = 32
	} else if _magic == 0x20b {
		PeType = 64
	} else {
		return 0
	}

	if _, err = r.Seek(int64(imageDosHeader.AddressExeHeader)+4+int64(binary.Size(pefile.CoffHeader{})), io.SeekStart); err != nil {
		return 0
	}

	var peOptionalHeader interface{}
	// copy the optional headers into their respective structs
	if PeType == 32 {
		peOptionalHeader = &pefile.OptionalHeader32{}
		if err = binary.Read(r, binary.LittleEndian, peOptionalHeader); err != nil {
			return 0
		}
	} else {
		peOptionalHeader = &pefile.OptionalHeader32P{}
		if err = binary.Read(r, binary.LittleEndian, peOptionalHeader); err != nil {
			return 0
		}
	}

	var rawExportDirectory []byte
	var exportRva, size uint32
	var ordinal uint16
	if PeType == 32 {
		exportDirectory := peOptionalHeader.(*pefile.OptionalHeader32).DataDirectories[0]
		exportRva = exportDirectory.VirtualAddress
		size = exportDirectory.Size
		rawExportDirectory, _ = emu.Uc.MemRead(uint64(exportRva)+baseAddress, uint64(exportDirectory.Size))
		r = bytes.NewReader(rawExportDirectory)

	} else {
		exportDirectory := peOptionalHeader.(*pefile.OptionalHeader32P).DataDirectories[0]
		exportRva = exportDirectory.VirtualAddress
		size = exportDirectory.Size
		rawExportDirectory, _ = emu.Uc.MemRead(uint64(exportRva)+baseAddress, uint64(size))
		r = bytes.NewReader(rawExportDirectory)
	}
	exportDirectory := pefile.ExportDirectory{}
	if err := binary.Read(r, binary.LittleEndian, &exportDirectory); err != nil {
		return 0
	}
	namesTableRVA := exportDirectory.NamesRva - exportRva
	ordinalsTableRVA := exportDirectory.OrdinalsRva - exportRva
	for i := 0; i < int(exportDirectory.NumberOfNamePointers); i++ {
		// seek to index in names table
		if _, err := r.Seek(int64(namesTableRVA+uint32(i*4)), io.SeekStart); err != nil {
			return 0
		}

		exportAddressTable := pefile.ExportAddressTable{}
		if err := binary.Read(r, binary.LittleEndian, &exportAddressTable); err != nil {
			return 0
		}

		name := pefile.ReadString(rawExportDirectory[exportAddressTable.Rva-exportRva:])
		if name != wantedFuncName && wantedFuncOrdinal == 0 {
			continue //Another check to stop reads that are not helpful
		}

		// get first Name in array
		ordinal = binary.LittleEndian.Uint16(rawExportDirectory[ordinalsTableRVA+uint32(i*2) : ordinalsTableRVA+uint32(i*2)+2])

		// seek to ordinals table
		if _, err := r.Seek(int64(uint32(ordinal)*4+exportDirectory.FunctionsRva-exportRva), io.SeekStart); err != nil {
			return 0
		}

		// get ordinal address table
		exportOrdinalTable := pefile.ExportAddressTable{}
		if err := binary.Read(r, binary.LittleEndian, &exportOrdinalTable); err != nil {
			return 0
		}
		rva := exportOrdinalTable.Rva

		//Check whether its forwarded or not
		if rva < exportRva+size && rva > exportRva && (name == wantedFuncName || (uint32(i)+exportDirectory.OrdinalBase) == uint32(wantedFuncOrdinal)) {
			//Its in the range of exports, its forwarded.
			if _, err := r.Seek(int64(rva-exportRva), io.SeekStart); err != nil {
				return 0
			}
			forwardedExportRaw := pefile.ReadString(rawExportDirectory[rva-exportRva:])
			split := strings.Split(forwardedExportRaw, ".")
			dllName := strings.ToLower(split[0]) + ".dll"
			ordinalNum := 0
			funcName := ""
			var err error
			if split[1][0] == '#' {
				numStr := split[1][1:]
				if ordinalNum, err = strconv.Atoi(numStr); err != nil {
					return 0
				}
			} else {
				funcName = split[1]
			}
			libAddress := emu.LoadedModules[dllName]

			return getProcAddress(emu, libAddress, funcName, uint16(ordinalNum))
		}
		if name == wantedFuncName || uint32(i)+exportDirectory.OrdinalBase == uint32(wantedFuncOrdinal) {
			return uint64(rva) + baseAddress
		}

	}
	return 0

}
func getProcAddressWrapper(emu *WinEmulator, in *Instruction) func(emu *WinEmulator, in *Instruction) bool {
	baseAddr := in.Args[0]
	//The ordinal value might be given and not the function name.
	if in.Args[1] < 65535 { //USHRT_MAX
		ordinalValue := in.Args[1]
		rva := getProcAddress(emu, baseAddr, "", uint16(ordinalValue))
		return SkipFunctionStdCall(true, rva)
	} else {
		name := util.ReadASCII(emu.Uc, in.Args[1], 0)
		rva := getProcAddress(emu, baseAddr, name, 0)
		return SkipFunctionStdCall(true, rva)
	}
}

func KernelbaseHooks(emu *WinEmulator) {
	emu.AddHook("", "CloseHandle", &Hook{Parameters: []string{"hObject"}, Fn: SkipFunctionStdCall(true, 0x1)})
	emu.AddHook("", "CreateEventW", &Hook{
		Parameters: []string{"lpEventAttributes", "bManualReset", "bInitialState", "w:lpName"},
		Fn:         SkipFunctionStdCall(true, 0x1),
	})
	emu.AddHook("", "CreateFileA", &Hook{
		Parameters: []string{"a:lpFileName", "dwDesiredAccess", "dwShareMode", "lpSecurityAttributes", "dwCreationDisposition", "dwFlagsAndAttributes", "hTemplateFile"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			return createFile(emu, in, false)(emu, in)
		},
	})
	emu.AddHook("", "CreateFileW", &Hook{
		Parameters: []string{"w:lpFileName", "dwDesiredAccess", "dwShareMode", "lpSecurityAttributes", "dwCreationDisposition", "dwFlagsAndAttributes", "hTemplateFile"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			return createFile(emu, in, true)(emu, in)
		},
	})
	//HRSRC FindResourceA(
	//  HMODULE hModule,
	//  LPCSTR  lpName,
	//  LPCSTR  lpType
	//);
	//emu.AddHook("","FindResourceA",&Hook{
	//	Parameters:[]string{"hModule","a:lpName","a:lpType"},
	//	Fn: func(emu *WinEmulator,in *Instruction) bool{
	//		return FindResource(emu,in,false)(emu,in)
	//	},
	//})
	//emu.AddHook("","FindResourceW",&Hook{
	//	Parameters:[]string{"hModule","a:lpName","a:lpType"},
	//	Fn: func(emu *WinEmulator,in *Instruction) bool{
	//		return FindResource(emu,in,false)(emu,in)
	//	},
	//})

	emu.AddHook("", "CreateFileMappingA", &Hook{
		Parameters: []string{"hFile", "lpFileMappingAttributes", "flProtect", "dwMaximumSizeHigh", "dwMaximumSizeLow", "a:lpName"},
		Fn: func(emulator *WinEmulator, in *Instruction) bool {
			return createFileMapping(emu, in, true)(emu, in)
		},
	})
	emu.AddHook("", "CreateFileMappingW", &Hook{
		Parameters: []string{"hFile", "lpFileMappingAttributes", "flProtect", "dwMaximumSizeHigh", "dwMaximumSizeLow", "w:lpName"},
		Fn: func(emulator *WinEmulator, in *Instruction) bool {
			return createFileMapping(emu, in, true)(emu, in)
		},
	})

	emu.AddHook("", "EnumDeviceDrivers", &Hook{
		Parameters: []string{"lpImageBase", "cb", "lpcbNeeded"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			return enumDeviceDrivers(emu, in)(emu, in)
		},
	})
	emu.AddHook("", "GetDeviceDriverBaseNameW", &Hook{
		Parameters: []string{"ImageBase", "lpBaseName", "nSize"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			return getDeviceDriverBaseName(emu, in, true)(emu, in)
		},
	})

	emu.AddHook("", "GetDriveTypeA", &Hook{
		Parameters: []string{"a:lpRootPathName"},
		Fn: func(emulator *WinEmulator, in *Instruction) bool {
			return getDriveType(emu, in, false)(emu, in)
		},
	})
	emu.AddHook("", "GetDriveTypeW", &Hook{
		Parameters: []string{"w:lpRootPathName"},
		Fn: func(emulator *WinEmulator, in *Instruction) bool {
			return getDriveType(emu, in, true)(emu, in)
		},
	})
	emu.AddHook("", "DeleteCriticalSection", &Hook{
		Parameters: []string{"lpCriticalSection"},
		Fn:         SkipFunctionStdCall(false, 0),
	})

	emu.AddHook("", "DecodePointer", &Hook{
		Parameters: []string{"Ptr"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			return SkipFunctionStdCall(true, in.Args[0])(emu, in)
		},
	})
	emu.AddHook("", "ExitProcess", &Hook{
		Parameters: []string{"uExitCode"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			return false
		},
	})
	emu.AddHook("", "FlsAlloc", &Hook{
		Parameters: []string{"lpCallback"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			i := 0
			for i = 0; i < len(emu.Fls); i++ {
				if emu.Fls[i] == 0 {
					break
				}
			}
			return SkipFunctionStdCall(true, uint64(i))(emu, in)
		},
	})
	emu.AddHook("", "FormatMessageA", &Hook{
		Parameters: []string{"dwFlags", "lpSource", "dwMessageId", "dwLanguageId", "a:lpBuffer", "nSize", "..."},
		Fn:         SkipFunctionStdCall(true, 0x1),
	})
	emu.AddHook("", "FormatMessageW", &Hook{
		Parameters: []string{"dwFlags", "lpSource", "dwMessageId", "dwLanguageId", "w:lpBuffer", "nSize", "..."},
		Fn:         SkipFunctionStdCall(true, 0x1),
	})
	emu.AddHook("", "EncodePointer", &Hook{
		Parameters: []string{"Ptr"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			return SkipFunctionStdCall(true, in.Args[0])(emu, in)
		},
	})
	emu.AddHook("", "EnterCriticalSection", &Hook{
		Parameters: []string{"lpCriticalSection"},
		Fn:         SkipFunctionStdCall(false, 0),
	})
	emu.AddHook("", "RtlEnterCriticalSection", &Hook{
		Parameters: []string{"lpCriticalSection"},
		Fn:         SkipFunctionStdCall(false, 0),
	})
	emu.AddHook("", "FlsFree", &Hook{
		Parameters: []string{"dwFlsIndex"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			if in.Args[0] >= 0 && in.Args[0] < uint64(len(emu.Fls)) {
				emu.Fls[in.Args[0]] = 0
				return SkipFunctionStdCall(true, 0x1)(emu, in)
			} else {
				return SkipFunctionStdCall(true, 0x0)(emu, in)
			}
		},
	})
	emu.AddHook("", "FlsGetValue", &Hook{
		Parameters: []string{"dwFlsIndex"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			if in.Args[0] >= 0 && in.Args[0] < uint64(len(emu.Fls)) {
				return SkipFunctionStdCall(true, emu.Fls[in.Args[0]])(emu, in)
			} else {
				return SkipFunctionStdCall(true, 0x0)(emu, in)
			}
		},
	})
	emu.AddHook("", "FlsSetValue", &Hook{
		Parameters: []string{"dwFlsIndex", "lpFlsData"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			if in.Args[0] >= 0 && in.Args[0] < uint64(len(emu.Fls)) {
				emu.Fls[in.Args[0]] = in.Args[1]
				return SkipFunctionStdCall(true, 0x1)(emu, in)
			} else {
				return SkipFunctionStdCall(true, 0x0)(emu, in)
			}
		},
	})
	emu.AddHook("", "FreeEnvironmentStrings", &Hook{Parameters: []string{"lpszEnvironmentBlock"}, Fn: SkipFunctionStdCall(true, 0x1)})
	emu.AddHook("", "FreeEnvironmentStringsW", &Hook{Parameters: []string{"lpszEnvironmentBlock"}, Fn: SkipFunctionStdCall(true, 0x1)})
	emu.AddHook("", "GetACP", &Hook{
		Parameters: []string{},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			return SkipFunctionStdCall(true, uint64(emu.Opts.CodePageIdentifier))(emu, in)
		},
	})
	emu.AddHook("", "GetActiveWindow", &Hook{
		Parameters: []string{},
		Fn:         SkipFunctionStdCall(true, 0x1),
	})
	emu.AddHook("", "GetCommandLineW", &Hook{
		Parameters: []string{},
		Fn:         SkipFunctionStdCall(true, emu.Argv),
	})
	emu.AddHook("", "GetCommandLineA", &Hook{
		Parameters: []string{},
		Fn:         SkipFunctionStdCall(true, emu.Argv),
	})
	emu.AddHook("", "GetConsoleMode", &Hook{
		Parameters: []string{"hConsoleHandle", "lpMode"},
		Fn:         SkipFunctionStdCall(true, 0x1),
	})
	emu.AddHook("", "GetCPInfo", &Hook{
		Parameters: []string{"CodePage", "lpCPInfo"},
		Fn:         SkipFunctionStdCall(true, 0x1),
	})
	emu.AddHook("", "GetEnvironmentStrings", &Hook{
		Parameters: []string{},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			return getEnvironmentStrings(emu, in, true)(emu, in)
		},
	})
	emu.AddHook("", "GetEnvironmentStringsA", &Hook{
		Parameters: []string{},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			return getEnvironmentStrings(emu, in, false)(emu, in)
		},
	})
	emu.AddHook("", "GetEnvironmentStringsW", &Hook{
		Parameters: []string{},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			return getEnvironmentStrings(emu, in, true)(emu, in)
		},
	})
	emu.AddHook("", "GetCurrentThreadId", &Hook{Parameters: []string{}})
	emu.AddHook("", "GetCurrentProcess", &Hook{Parameters: []string{}})
	emu.AddHook("", "GetCurrentProcessId", &Hook{Parameters: []string{}})
	emu.AddHook("", "GetFileTime", &Hook{
		Parameters: []string{"hFile", "lpCreationTime", "lpLastAccessTime", "lpLastWriteTime"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			if handle := emu.Handles[in.Args[0]]; handle != nil {
				if handle.Info != nil {
					return SkipFunctionStdCall(true, 0xe)(emu, in)
				}
			}
			return SkipFunctionStdCall(true, uint64(172800031))(emu, in)
		},
	})
	emu.AddHook("", "GetFileType", &Hook{
		Parameters: []string{"hFile"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			return SkipFunctionStdCall(true, 0x2)(emu, in)
		},
	})
	emu.AddHook("", "GetLastError", &Hook{Parameters: []string{}})
	emu.AddHook("", "GetLastActivePopup", &Hook{
		Parameters: []string{"hWnd"},
		Fn:         SkipFunctionStdCall(true, 0x1),
	})
	emu.AddHook("", "GetModuleFileNameA", &Hook{
		Parameters: []string{"hModule", "a:lpFilename", "nSize"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			f := ""
			if in.Args[0] == 0x0 {
				f = "C:\\Users\\" + emu.Opts.User + "\\" + filepath.Base(emu.Binary)
				emu.Uc.MemWrite(in.Args[1], []byte(f))
			} else {
				f = "C:\\Windows\\System32\\" + filepath.Base(emu.Binary)
				emu.Uc.MemWrite(in.Args[1], []byte(f))
			}
			return SkipFunctionStdCall(true, uint64(len(f)+1))(emu, in)
		},
	})
	emu.AddHook("", "GetModuleFileNameW", &Hook{
		Parameters: []string{"hModule", "a:lpFilename", "nSize"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			f := ""
			if in.Args[0] == 0x0 {
				f = "C:\\Users\\" + emu.Opts.User + "\\" + filepath.Base(emu.Binary)
				emu.Uc.MemWrite(in.Args[1], util.ASCIIToWinWChar(f))
			} else {
				f = "C:\\Windows\\System32\\" + filepath.Base(emu.Binary)
				emu.Uc.MemWrite(in.Args[1], util.ASCIIToWinWChar(f))
			}
			return SkipFunctionStdCall(true, uint64(len(f)+2))(emu, in)
		},
	})
	emu.AddHook("", "GetModuleHandleA", &Hook{
		Parameters: []string{"a:lpModuleName"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			return SkipFunctionStdCall(true, GetModuleHandle(emu, in, false))(emu, in)
		},
	})
	emu.AddHook("", "GetModuleHandleExA", &Hook{
		Parameters: []string{"dwFlags", "a:lpModuleName", "phModule"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			return SkipFunctionStdCall(true, getModuleHandleEx(emu, in, false))(emu, in)
		},
	})
	emu.AddHook("", "GetModuleHandleExW", &Hook{
		Parameters: []string{"dwFlags", "a:lpModuleName", "phModule"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			return SkipFunctionStdCall(true, getModuleHandleEx(emu, in, true))(emu, in)
		},
	})
	emu.AddHook("", "GetModuleHandleW", &Hook{
		Parameters: []string{"w:lpModuleName"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			return SkipFunctionStdCall(true, GetModuleHandle(emu, in, true))(emu, in)
		},
	})

	emu.AddHook("", "GetProcessHeap", &Hook{
		Parameters: []string{},
		Fn:         SkipFunctionStdCall(true, 0x123456),
	})
	emu.AddHook("", "GetProcessIoCounters", &Hook{
		Parameters: []string{"hProcess", "lpIoCounters"},
		Fn:         SkipFunctionStdCall(true, 0x1),
	})
	emu.AddHook("", "GetProcessWindowStation", &Hook{
		Parameters: []string{},
		Fn:         SkipFunctionStdCall(true, 0x1),
	})
	emu.AddHook("", "GetProcAddress", &Hook{
		Parameters: []string{"hModule", "a:lpProcName"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			return getProcAddressWrapper(emu, in)(emu, in)
		},
	})
	emu.AddHook("", "GetStringTypeW", &Hook{
		Parameters: []string{"dwInfoType", "lpSrcStr", "cchSrc", "lpCharType"},
		Fn:         SkipFunctionStdCall(true, 0x1),
	})
	emu.AddHook("", "GetStartupInfoA", &Hook{
		Parameters: []string{"lpStartupInfo"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			startupInfo := StartupInfo{
				Cb:          0x44,
				Reserved:    0x0,
				Desktop:     0xc3c930,
				Title:       0x0,
				X:           0x0,
				Y:           0x0,
				XSize:       0x64,
				YSize:       0x64,
				XCountChars: 0x80,
				YCountChars: 0x80,
				Flags:       0x40,
				ShowWindow:  0x1,
				Reserved2:   0x0,
				Reserved2a:  0x0,
				StdInput:    0xffffffff,
				StdOutput:   0xffffffff,
				StdError:    0xffffffff,
			}
			buf := new(bytes.Buffer)
			binary.Write(buf, binary.LittleEndian, &startupInfo)
			emu.Uc.MemWrite(in.Args[0], buf.Bytes())
			return SkipFunctionStdCall(false, 0)(emu, in)
		},
	})
	emu.AddHook("", "GetStartupInfoW", &Hook{
		Parameters: []string{"lpStartupInfo"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			startupInfo := StartupInfo{
				Cb:          0x44,
				Reserved:    0x0,
				Desktop:     0xc3c930,
				Title:       0x0,
				X:           0x0,
				Y:           0x0,
				XSize:       0x64,
				YSize:       0x64,
				XCountChars: 0x80,
				YCountChars: 0x80,
				Flags:       0x40,
				ShowWindow:  0x1,
				Reserved2:   0x0,
				Reserved2a:  0x0,
				StdInput:    0xffffffff,
				StdOutput:   0xffffffff,
				StdError:    0xffffffff,
			}
			buf := new(bytes.Buffer)
			binary.Write(buf, binary.LittleEndian, &startupInfo)
			emu.Uc.MemWrite(in.Args[0], buf.Bytes())
			return SkipFunctionStdCall(false, 0)(emu, in)
		},
	})
	emu.AddHook("", "GetSystemDirectoryA", &Hook{
		Parameters: []string{"lpBuffer", "uSize"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			dir := []byte("c:\\windows\\system32")
			emu.Uc.MemWrite(in.Args[0], dir)
			return SkipFunctionStdCall(true, uint64(len(dir)))(emu, in)
		},
	})
	emu.AddHook("", "GetSystemDirectoryW", &Hook{
		Parameters: []string{"lpBuffer", "uSize"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			dir := util.ASCIIToWinWChar("c:\\windows\\system32")
			emu.Uc.MemWrite(in.Args[0], dir)
			return SkipFunctionStdCall(true, uint64(len(dir)))(emu, in)
		},
	})
	emu.AddHook("", "GetSystemTime", &Hook{
		Parameters: []string{"lpSystemTime"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			systemTime := struct {
				Year         uint16
				Month        uint16
				DayOfWeek    uint16
				Day          uint16
				Hour         uint16
				Minute       uint16
				Second       uint16
				Milliseconds uint16
			}{
				uint16(emu.Opts.SystemTime.Year),
				uint16(emu.Opts.SystemTime.Month),
				uint16(emu.Opts.SystemTime.DayOfWeek),
				uint16(emu.Opts.SystemTime.Day),
				uint16(emu.Opts.SystemTime.Hour),
				uint16(emu.Opts.SystemTime.Minute),
				uint16(emu.Opts.SystemTime.Second),
				uint16(emu.Opts.SystemTime.Millisecond),
			}
			buf := new(bytes.Buffer)
			binary.Write(buf, binary.LittleEndian, &systemTime)
			emu.Uc.MemWrite(in.Args[0], buf.Bytes())
			return SkipFunctionStdCall(false, 0)(emu, in)
		},
	})
	emu.AddHook("", "GetSystemTimeAsFileTime", &Hook{
		Parameters: []string{"lpSystemTimeAsFileTime"},
		Fn:         SkipFunctionStdCall(false, 0),
	})
	emu.AddHook("", "GetStdHandle", &Hook{
		Parameters: []string{"nStdHandle"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			handle := uint64(0x1)
			if in.Args[0] == 0xfffffff5 {
				handle = 0x2
			}
			if in.Args[0] == 0xfffffff4 {
				handle = 0x3
			}
			return SkipFunctionStdCall(true, handle)(emu, in)
		},
	})
	emu.AddHook("", "GetTickCount", &Hook{
		Parameters: []string{},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			t := uint64(emu.Timestamp) + emu.Ticks
			return SkipFunctionStdCall(true, t)(emu, in)
		},
	})
	emu.AddHook("", "GetTickCount64", &Hook{
		Parameters: []string{},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			t := uint64(emu.Timestamp) + emu.Ticks
			return SkipFunctionStdCall(true, t)(emu, in)
		},
	})
	emu.AddHook("", "GetTimeZoneInformation", &Hook{
		Parameters: []string{"lpTimeZoneInformation"},
		Fn:         SkipFunctionStdCall(true, 0x12345678),
	})
	emu.AddHook("", "GetUserObjectInformationA", &Hook{
		Parameters: []string{"hObj", "nIndex", "pvInfo", "nLength", "lpnLengthNeeded"},
		Fn:         SkipFunctionStdCall(true, 0x1),
	})
	emu.AddHook("", "GetUserObjectInformationW", &Hook{
		Parameters: []string{"hObj", "nIndex", "pvInfo", "nLength", "lpnLengthNeeded"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			if in.Args[1] == 0x1 {
				userObjectFlags := struct {
					Inherit  uint32
					Reserved uint32
					Flags    uint32
				}{
					0x1,
					0x1,
					0x0001,
				}
				buf := new(bytes.Buffer)
				binary.Write(buf, binary.LittleEndian, &userObjectFlags)
				emu.Uc.MemWrite(in.Args[2], buf.Bytes())

				return SkipFunctionStdCall(true, 0x11)(emu, in)
			}
			return SkipFunctionStdCall(true, 0x0)(emu, in)
		},
	})
	emu.AddHook("", "GetVersion", &Hook{
		Parameters: []string{},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			var ret = 0x0
			ret = ret | emu.Opts.OsMajorVersion
			ret = ret << 16
			ret = ret | emu.Opts.OsMinorVersion
			return SkipFunctionStdCall(true, uint64(ret))(emu, in)
		},
	})
	emu.AddHook("", "GetVersionExA", &Hook{
		Parameters: []string{"lpVersionInformation"},
		Fn:         SkipFunctionStdCall(true, 0x12),
	})
	emu.AddHook("", "GetVersionExW", &Hook{
		Parameters: []string{"lpVersionInformation"},
		Fn:         SkipFunctionStdCall(true, 0x12),
	})
	emu.AddHook("", "GetWindowsDirectoryA", &Hook{
		Parameters: []string{"lpBuffer", "uSize"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			d := []byte("c:\\windows")
			emu.Uc.MemWrite(in.Args[0], d)
			return SkipFunctionStdCall(true, uint64(len(d)))(emu, in)
		},
	})
	emu.AddHook("", "GetWindowsDirectoryW", &Hook{
		Parameters: []string{"lpBuffer", "uSize"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			d := util.ASCIIToWinWChar("c:\\windows")
			emu.Uc.MemWrite(in.Args[0], d)
			return SkipFunctionStdCall(true, uint64(len(d)))(emu, in)
		},
	})

	emu.AddHook("", "GetVolumeInformationW", &Hook{
		Parameters: []string{"w:lpRootPathName", "w:lpVolumeNameBuffer", "nVolumeNameSize", "lpVolumeSerialNumber", "lpMaximumComponentLength", "lpFileSystemFlags", "w:lpFileSystemNameBuffer", "nFileSystemNameSize"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			return getVolumeInformation(emu, in, true)(emu, in)
		},
	})
	emu.AddHook("", "InitializeCriticalSection", &Hook{
		Parameters: []string{"lpCriticalSection"},
		Fn:         SkipFunctionStdCall(false, 0x1),
	})
	emu.AddHook("", "InitializeCriticalSectionEx", &Hook{
		Parameters: []string{"lpCriticalSection", "dwSpinCount", "Flags"},
		Fn:         SkipFunctionStdCall(true, 0x1),
	})
	emu.AddHook("", "InitializeCriticalSectionAndSpinCount", &Hook{
		Parameters: []string{"lpCriticalSection", "dwSpinCount"},
		Fn:         SkipFunctionStdCall(true, 0x1),
	})
	emu.AddHook("", "InitializeSListHead", &Hook{Parameters: []string{"ListHead"}})
	emu.AddHook("", "IsDebuggerPresent", &Hook{Parameters: []string{}, Fn: SkipFunctionStdCall(true, 0x0)})
	emu.AddHook("", "IsValidCodePage", &Hook{
		Parameters: []string{"CodePage"},
		Fn:         SkipFunctionStdCall(true, 0x1),
	})
	emu.AddHook("", "IsProcessorFeaturePresent", &Hook{
		Parameters: []string{"ProcessorFeature"},
		Fn:         SkipFunctionStdCall(true, 1),
	})
	emu.AddHook("", "InterlockedIncrement", &Hook{Parameters: []string{"lpAddend"}})
	emu.AddHook("", "InterlockedDecrement", &Hook{Parameters: []string{"lpAddend"}})
	emu.AddHook("", "LeaveCriticalSection", &Hook{
		Parameters: []string{"lpCriticalSection"},
		Fn:         SkipFunctionStdCall(false, 0),
	})
	emu.AddHook("", "RtlLeaveCriticalSection", &Hook{
		Parameters: []string{"lpCriticalSection"},
		Fn:         SkipFunctionStdCall(false, 0),
	})
	emu.AddHook("", "LCMapStringA", &Hook{
		Parameters: []string{"Locale", "dwMapFlags", "lpSrcStr", "cchSrc", "lpDestStr", "cchDest"},
		Fn:         SkipFunctionStdCall(true, 0x1),
	})
	emu.AddHook("", "LCMapStringW", &Hook{
		Parameters: []string{"Locale", "dwMapFlags", "lpSrcStr", "cchSrc", "lpDestStr", "cchDest"},
		Fn:         SkipFunctionStdCall(true, 0x1),
	})
	emu.AddHook("", "LCMapStringEx", &Hook{
		Parameters: []string{"lpLocaleName", "dwMapFlags", "lpSrcStr", "cchSrc", "lpDestStr", "cchDest", "lpVersionInformation", "lpReserved", "sortHandle"},
		Fn:         SkipFunctionStdCall(true, 0x1),
	})
	emu.AddHook("", "LoadLibraryA", &Hook{
		Parameters: []string{"a:lpFileName"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			return loadLibrary(emu, in, false)(emu, in)
		},
	})
	emu.AddHook("", "LoadLibraryExA", &Hook{
		Parameters: []string{"a:lpFileName", "hFile", "dwFlags"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			return loadLibrary(emu, in, false)(emu, in)
		},
	})
	emu.AddHook("", "LoadLibraryExW", &Hook{
		Parameters: []string{"w:lpFileName", "hFile", "dwFlags"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			return loadLibrary(emu, in, true)(emu, in)
		},
	})
	emu.AddHook("", "LoadLibraryW", &Hook{
		Parameters: []string{"w:lpFileName"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			return loadLibrary(emu, in, true)(emu, in)
		},
	})
	emu.AddHook("", "lstrlenA", &Hook{Parameters: []string{"a:lpString"}})
	emu.AddHook("", "lstrlenW", &Hook{Parameters: []string{"w:lpString"}})
	emu.AddHook("", "MapPredefinedHandleInternal", &Hook{
		Parameters: []string{"unknown1", "unknown2", "unknown3", "unknown4"},
	})
	emu.AddHook("", "MessageBoxW", &Hook{
		Parameters: []string{"hWnd", "w:lpText", "w:lpCaption", "uType"},
		Fn:         SkipFunctionStdCall(true, 0x1),
	})
	emu.AddHook("", "OutputDebugStringA", &Hook{
		Parameters: []string{"a:lpOutputString"},
		Fn:         SkipFunctionStdCall(false, 0x1),
	})
	emu.AddHook("", "OutputDebugStringW", &Hook{
		Parameters: []string{"w:lpOutputString"},
		Fn:         SkipFunctionStdCall(false, 0x1),
	})
	emu.AddHook("", "QueryPerformanceCounter", &Hook{
		Parameters: []string{"lpPerformanceCount"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			buf := make([]byte, 8)
			binary.LittleEndian.PutUint64(buf, uint64(time.Now().Unix()))
			emu.Uc.MemWrite(in.Args[0], buf)
			return SkipFunctionStdCall(true, 0x1)(emu, in)
		},
	})
	emu.AddHook("", "SetHandleCount", &Hook{Parameters: []string{"uNumber"}})
	emu.AddHook("", "SetStdHandle", &Hook{
		Parameters: []string{"nStdHandle", "hHandle"},
		Fn:         SkipFunctionStdCall(true, 0x1),
	})
	emu.AddHook("", "SetUnhandledExceptionFilter", &Hook{
		Parameters: []string{"lpTopLevelExceptionFilter"},
		Fn:         SkipFunctionStdCall(true, 0x4),
	})
	emu.AddHook("", "SetErrorMode", &Hook{
		Parameters: []string{"uMode"},
		Fn:         SkipFunctionStdCall(true, 0x0),
	})
	emu.AddHook("", "SetLastError", &Hook{
		Parameters: []string{"dwErrCode"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			emu.setLastError(in.Args[0])
			return SkipFunctionStdCall(false, 0x1)(emu, in)
		},
	})
	emu.AddHook("", "SetThreadAffinityMask", &Hook{
		Parameters: []string{"hThread", "dwThreadAffinityMask"},
		Fn:         SkipFunctionStdCall(true, 0x1),
	})
	emu.AddHook("", "Sleep", &Hook{
		Parameters: []string{"dwMilliseconds"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			emu.Ticks += in.Args[0]
			return SkipFunctionStdCall(false, 0x0)(emu, in)
		},
	})
	emu.AddHook("", "TerminateProcess", &Hook{
		Parameters: []string{"hProcess", "uExitCode"},
		Fn: func(emu *WinEmulator, instr *Instruction) bool {
			return false
		},
	})
	emu.AddHook("", "TlsAlloc", &Hook{
		Parameters: []string{},
		Fn:         SkipFunctionStdCall(true, 0x1),
	})
	emu.AddHook("", "TlsGetValue", &Hook{Parameters: []string{"dwTlsIndex"}})
	emu.AddHook("", "TlsFree", &Hook{
		Parameters: []string{"dwTlsIndex"},
		Fn:         SkipFunctionStdCall(true, 0x1),
	})
	emu.AddHook("", "TlsSetValue", &Hook{Parameters: []string{"dwTlsIndex", "lpTlsValue"}})
	emu.AddHook("", "UnhandledExceptionFilter", &Hook{
		Parameters: []string{"ExceptionInfo"},
		Fn:         SkipFunctionStdCall(true, 0x1),
	})
	emu.AddHook("", "UnlockFileEx", &Hook{
		Parameters: []string{"hFile", "dwReserved", "nNumberOfBytesToUnlockLow", "nNumberOfBytesToUnlockHigh", "lpOverlapped"},
		Fn:         SkipFunctionStdCall(true, 0x1),
	})

	emu.AddHook("", "VirtualQuery", &Hook{
		Parameters: []string{"lpAddress", "lpBuffer", "dwLength"},
		Fn:         SkipFunctionStdCall(true, 0x1),
	})
	emu.AddHook("", "VerSetConditionMask", &Hook{
		Parameters: []string{},
		Fn:         SkipFunctionStdCall(false, 0x1),
	})
	emu.AddHook("", "WaitForSingleObject", &Hook{
		Parameters: []string{"hHandle", "dwMilliseconds"},
		Fn:         SkipFunctionStdCall(true, 0x1),
	})
	emu.AddHook("", "Wow64DisableWow64FsRedirection", &Hook{
		Parameters: []string{"OldValue"},
		Fn:         SkipFunctionStdCall(true, 0x1),
	})
	emu.AddHook("", "Wow64RevertWow64FsRedirection", &Hook{
		Parameters: []string{"OldValue"},
		Fn:         SkipFunctionStdCall(true, 0x1),
	})
	emu.AddHook("", "WriteFile", &Hook{
		Parameters: []string{"hFile", "lpBuffer", "nNumberOfBytesToWrite", "lpNumberOfBytesWritten", "lpOverlapped"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			// if the handle is a stdin/stdout/stderr treat the buffer as ascii
			if in.Args[0] == 0x1 || in.Args[0] == 0x2 || in.Args[0] == 0x3 {
				// TODO this could be problematic, need to do a deep copy of this Hook when it is initiated maybe
				in.Hook.Parameters[1] = "s:lpBuffer"
				s := util.ReadASCII(emu.Uc, in.Args[1], 0)
				in.Hook.Values[1] = s
				return SkipFunctionStdCall(true, uint64(len(s)))(emu, in)
			}

			if handle := emu.Handles[in.Args[0]]; handle != nil {
				if b, err := emu.Uc.MemRead(in.Args[1], in.Args[2]); err == nil {
					n, _ := handle.Write(b)
					return SkipFunctionStdCall(true, uint64(n))(emu, in)
				}
			}
			return SkipFunctionStdCall(true, 0x0)(emu, in)
		},
	})
	emu.AddHook("", "_CorExeMain", &Hook{Parameters: []string{}})
	emu.AddHook("", "GetCPHashNode", &Hook{Parameters: []string{}})
	emu.AddHook("", "GetCPFileNameFromRegistry", &Hook{Parameters: []string{"CodePage", "w:FileName", "FileNameSize"}})
	emu.AddHook("", "LocalFree", &Hook{
		Parameters: []string{"hMem"},
		Fn:         SkipFunctionStdCall(true, 0),
	})
	emu.AddHook("", "MultiByteToWideChar", &Hook{
		Parameters: []string{"CodePage", "dwFlags", "lpMultiByteStr", "cbMultiByte", "lpWideCharStr", "cchWideChar"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			mb := util.ReadASCII(emu.Uc, in.Args[2], 0)

			// check if multibyte function is only getting buffer size
			if in.Args[5] == 0x0 {
				return SkipFunctionStdCall(true, uint64(len(mb))*2+2)(emu, in)
			} else {
				wc := util.ASCIIToWinWChar(mb)
				emu.Uc.MemWrite(in.Args[4], wc)
				return SkipFunctionStdCall(true, uint64(len(wc))+2)(emu, in)
			}
		},
	})
	emu.AddHook("", "NlsValidateLocale", &Hook{Parameters: []string{"*Unknown*"}})
	emu.AddHook("", "PathCchRemoveFileSpec", &Hook{Parameters: []string{"pszPath", "cchPath"}})
	emu.AddHook("", "WideCharToMultiByte", &Hook{
		Parameters: []string{"CodePage", "dwFlags", "w:lpWideCharStr", "cchWideChar", "lpMultiByteStr", "cbMultiByte", "lpDefaultChar", "lpUsedDefaultChar"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			mb := util.ReadASCII(emu.Uc, in.Args[2], 0)
			// check if multibyte function is only getting buffer size
			if in.Args[5] == 0x0 {
				return SkipFunctionStdCall(true, uint64(len(mb))*2+2)(emu, in)
			} else {
				return SkipFunctionStdCall(true, 0x1)(emu, in)
			}
		},
	})
	emu.AddHook("", "OpenFileMappingA", &Hook{
		Parameters: []string{"dwDesiredAccess", "bInheritHandle", "w:lpName"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			return openFileMapping(emu, in, false)(emu, in)
		},
	})
	emu.AddHook("", "OpenFileMappingW", &Hook{
		Parameters: []string{"dwDesiredAccess", "bInheritHandle", "w:lpName"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			return openFileMapping(emu, in, true)(emu, in)
		},
	})

	emu.AddHook("", "lstrcmpiA", &Hook{
		Parameters: []string{"a:lpString1", "a:lpString2"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			return lstrcmpi(emu, in, false)(emu, in)
		},
	})
	emu.AddHook("", "lstrcmpiW", &Hook{
		Parameters: []string{"w:lpString1", "w:lpString2"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			return lstrcmpi(emu, in, true)(emu, in)
		},
	})
	emu.AddHook("", "MapViewOfFile", &Hook{
		Parameters: []string{"hFileMappingObject", "dwDesiredAccess", "dwFileOffsetHigh", "dwFileOffsetLow", "dwNumberOfBytesToMap"},
	})

	emu.AddHook("", "GetCurrentPackageId", &Hook{
		Parameters: []string{"bufferLength", "buffer"},
		//Fn:SkipFunctionStdCall(true,0),
	})
	//
	//BOOL GetDiskFreeSpaceA(
	//	LPCSTR  lpRootPathName,
	//	LPDWORD lpSectorsPerCluster,
	//	LPDWORD lpBytesPerSector,
	//	LPDWORD lpNumberOfFreeClusters,
	//	LPDWORD lpTotalNumberOfClusters
	//);
	emu.AddHook("", "GetDiskFreeSpaceA", &Hook{
		Parameters: []string{"a:RootPathName", "lpSectorsPerCluster", "lpBytesPerSector", "lpNumberOfFreeClusters", "lpTotalNumberOfClusters"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			return SkipFunctionStdCall(true, 0)(emu, in)
		},
	})
	emu.AddHook("", "GetDiskFreeSpaceW", &Hook{
		Parameters: []string{"a:RootPathName", "lpSectorsPerCluster", "lpBytesPerSector", "lpNumberOfFreeClusters", "lpTotalNumberOfClusters"},
		Fn: func(emu *WinEmulator, in *Instruction) bool {
			return SkipFunctionStdCall(true, 0)(emu, in)
		},
	})
	//BOOL CreatePipe(
	//  PHANDLE               hReadPipe,
	//  PHANDLE               hWritePipe,
	//  LPSECURITY_ATTRIBUTES lpPipeAttributes,
	//  DWORD                 nSize
	//);
	emu.AddHook("", "CreatePipe", &Hook{
		Parameters: []string{"hReadPipe", "hWritePipe", "lpPipeAttributes", "nSize"},
		Fn:         SkipFunctionStdCall(true, 0),
	})
}
