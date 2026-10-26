//go:build windows

// Native Win32 GUI for metascrub (images + videos), standard library only.
// The scan runs on a background goroutine; a UI-thread timer polls shared state
// and updates a progress bar + status line, so the window stays responsive even
// for very large files/folders. No cross-thread GUI calls are made.
package main

import (
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

const (
	wsOVERLAPPEDWINDOW = 0x00CF0000
	wsCHILD            = 0x40000000
	wsVISIBLE          = 0x10000000
	wsBORDER           = 0x00800000
	wsVSCROLL          = 0x00200000
	wsTABSTOP          = 0x00010000

	esAUTOHSCROLL = 0x0080
	esMULTILINE   = 0x0004
	esAUTOVSCROLL = 0x0040
	esREADONLY    = 0x0800

	bsDEFPUSHBUTTON = 0x0001
	bsAUTOCHECKBOX  = 0x0003

	ssLEFTNOWORDWRAP = 0x000C

	swSHOW = 5

	csHREDRAW = 0x0002
	csVREDRAW = 0x0001

	wmCREATE        = 0x0001
	wmDESTROY       = 0x0002
	wmSIZE          = 0x0005
	wmCOMMAND       = 0x0111
	wmTIMER         = 0x0113
	wmSETFONT       = 0x0030
	wmGETMINMAXINFO = 0x0024
	emSETSEL        = 0x00B1
	emREPLACESEL    = 0x00C2
	emSCROLLCARET   = 0x00B7
	bmGETCHECK      = 0x00F0
	bmSETCHECK      = 0x00F1
	bstCHECKED      = 1
	pmREMOVE        = 1
	pbmSETRANGE32   = 0x0406
	pbmSETPOS       = 0x0402
	mbOK            = 0
	mbICONINFO      = 0x40
	mbICONWARNING   = 0x30
	defaultGUIFont  = 17
	colorBtnFace    = 15
	idcARROW        = 32512
	bifRETURNFSDIRS = 0x0001
	bifNEWDIALOG    = 0x0040
	iccPROGRESS     = 0x20
	maxPath         = 260
	cwUseDefault    = 0x80000000

	idBrowse  = 102
	idClean   = 103
	idRecurse = 105
	idTimer   = 1
)

type wndClassEx struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type point struct{ x, y int32 }

type msgT struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      point
}

type rect struct{ left, top, right, bottom int32 }

type minMaxInfo struct {
	ptReserved     point
	ptMaxSize      point
	ptMaxPosition  point
	ptMinTrackSize point
	ptMaxTrackSize point
}

type browseInfo struct {
	hwndOwner      uintptr
	pidlRoot       uintptr
	pszDisplayName *uint16
	lpszTitle      *uint16
	ulFlags        uint32
	lpfn           uintptr
	lParam         uintptr
	iImage         int32
}

type initCommonControlsEx struct{ dwSize, dwICC uint32 }

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	ole32    = syscall.NewLazyDLL("ole32.dll")
	comctl32 = syscall.NewLazyDLL("comctl32.dll")

	pRegisterClassEx     = user32.NewProc("RegisterClassExW")
	pCreateWindowEx      = user32.NewProc("CreateWindowExW")
	pDefWindowProc       = user32.NewProc("DefWindowProcW")
	pPostQuitMessage     = user32.NewProc("PostQuitMessage")
	pGetMessage          = user32.NewProc("GetMessageW")
	pTranslateMessage    = user32.NewProc("TranslateMessage")
	pDispatchMessage     = user32.NewProc("DispatchMessageW")
	pShowWindow          = user32.NewProc("ShowWindow")
	pUpdateWindow        = user32.NewProc("UpdateWindow")
	pSendMessage         = user32.NewProc("SendMessageW")
	pSetWindowText       = user32.NewProc("SetWindowTextW")
	pGetWindowText       = user32.NewProc("GetWindowTextW")
	pGetWindowTextLength = user32.NewProc("GetWindowTextLengthW")
	pEnableWindow        = user32.NewProc("EnableWindow")
	pMessageBox          = user32.NewProc("MessageBoxW")
	pLoadCursor          = user32.NewProc("LoadCursorW")
	pGetClientRect       = user32.NewProc("GetClientRect")
	pMoveWindow          = user32.NewProc("MoveWindow")
	pSetTimer            = user32.NewProc("SetTimer")
	pKillTimer           = user32.NewProc("KillTimer")

	pGetModuleHandle = kernel32.NewProc("GetModuleHandleW")
	pGetStockObject  = gdi32.NewProc("GetStockObject")

	pSHBrowseForFolder   = shell32.NewProc("SHBrowseForFolderW")
	pSHGetPathFromIDList = shell32.NewProc("SHGetPathFromIDListW")

	pOleInitialize = ole32.NewProc("OleInitialize")
	pCoTaskMemFree = ole32.NewProc("CoTaskMemFree")

	pInitCommonControlsEx = comctl32.NewProc("InitCommonControlsEx")
)

var (
	hInst                                  uintptr
	hFont                                  uintptr
	hPath, hBrowse, hClean, hLog, hRecurse uintptr
	hStatic, hStatus, hProg                uintptr
	busy                                   bool
)

// shared state between the worker goroutine and the UI timer
var (
	mu      sync.Mutex
	stLog   []string
	stFrac  float64
	stIdx   int
	stTotal int
	stName  string
	stDone  bool
	stRes   Result
	stErr   error
)

func strptr(s string) *uint16 {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		panic(err)
	}
	return p
}

func setText(h uintptr, s string) {
	pSetWindowText.Call(h, uintptr(unsafe.Pointer(strptr(s))))
}

func getText(h uintptr) string {
	n, _, _ := pGetWindowTextLength.Call(h)
	if n == 0 {
		return ""
	}
	buf := make([]uint16, int(n)+1)
	pGetWindowText.Call(h, uintptr(unsafe.Pointer(&buf[0])), n+1)
	return syscall.UTF16ToString(buf)
}

func logln(s string) {
	pSendMessage.Call(hLog, emSETSEL, ^uintptr(0), ^uintptr(0))
	pSendMessage.Call(hLog, emREPLACESEL, 0, uintptr(unsafe.Pointer(strptr(s+"\r\n"))))
	pSendMessage.Call(hLog, emSCROLLCARET, 0, 0)
}

func setProgress(perMille int) {
	pSendMessage.Call(hProg, pbmSETPOS, uintptr(perMille), 0)
}

func msgBox(hwnd uintptr, text, title string, flags uintptr) {
	pMessageBox.Call(hwnd, uintptr(unsafe.Pointer(strptr(text))),
		uintptr(unsafe.Pointer(strptr(title))), flags)
}

func createControls(hwnd uintptr) {
	mk := func(class string, style uintptr, id int) uintptr {
		cn := strptr(class)
		h, _, _ := pCreateWindowEx.Call(0, uintptr(unsafe.Pointer(cn)), 0, style,
			0, 0, 0, 0, hwnd, uintptr(id), hInst, 0)
		pSendMessage.Call(h, wmSETFONT, hFont, 1)
		return h
	}
	hStatic = mk("STATIC", wsCHILD|wsVISIBLE, 0)
	setText(hStatic, "Image/video file or folder:")
	hPath = mk("EDIT", wsCHILD|wsVISIBLE|wsBORDER|esAUTOHSCROLL|wsTABSTOP, 101)
	hBrowse = mk("BUTTON", wsCHILD|wsVISIBLE|wsTABSTOP, idBrowse)
	setText(hBrowse, "Browse…")
	hRecurse = mk("BUTTON", wsCHILD|wsVISIBLE|bsAUTOCHECKBOX|wsTABSTOP, idRecurse)
	setText(hRecurse, "Include subfolders")
	pSendMessage.Call(hRecurse, bmSETCHECK, bstCHECKED, 0)
	hClean = mk("BUTTON", wsCHILD|wsVISIBLE|bsDEFPUSHBUTTON|wsTABSTOP, idClean)
	setText(hClean, "Remove metadata → copies")
	hStatus = mk("STATIC", wsCHILD|wsVISIBLE|ssLEFTNOWORDWRAP, 0)
	setText(hStatus, "Idle.")
	hProg = mk("msctls_progress32", wsCHILD|wsVISIBLE, 0)
	pSendMessage.Call(hProg, pbmSETRANGE32, 0, 1000)
	hLog = mk("EDIT", wsCHILD|wsVISIBLE|wsBORDER|wsVSCROLL|esMULTILINE|esAUTOVSCROLL|esREADONLY, 104)
	logln("Pick a file or folder, then click \"Remove metadata → copies\".")
	logln("Images (jpg/png) need nothing. Videos use ffmpeg if installed.")
	logln("Folder → <folder>-cleaned; single file → <name>-nometa.<ext>. Lossless: no re-encoding.")
}

func layout(hwnd uintptr) {
	var rc rect
	pGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
	w, h := rc.right-rc.left, rc.bottom-rc.top
	m := int32(12)
	btnW := int32(96)
	mv := func(hh uintptr, x, y, ww, hh2 int32) {
		pMoveWindow.Call(hh, uintptr(x), uintptr(y), uintptr(ww), uintptr(hh2), 1)
	}
	mv(hStatic, m, m, 220, 18)
	py := m + 22
	mv(hPath, m, py, w-2*m-btnW-8, 24)
	mv(hBrowse, w-m-btnW, py-1, btnW, 26)
	ry := py + 34
	mv(hRecurse, m, ry, 220, 20)
	cy := ry + 28
	mv(hClean, m, cy, 220, 28)
	sy := cy + 38
	mv(hStatus, m, sy, w-2*m, 18)
	gy := sy + 20
	mv(hProg, m, gy, w-2*m, 18)
	ly := gy + 28
	mv(hLog, m, ly, w-2*m, h-ly-m)
}

func onBrowse(hwnd uintptr) {
	var display [maxPath]uint16
	title := strptr("Select a folder of images/videos")
	bi := browseInfo{
		hwndOwner:      hwnd,
		pszDisplayName: &display[0],
		lpszTitle:      title,
		ulFlags:        bifRETURNFSDIRS | bifNEWDIALOG,
	}
	pidl, _, _ := pSHBrowseForFolder.Call(uintptr(unsafe.Pointer(&bi)))
	if pidl == 0 {
		return
	}
	var path [maxPath]uint16
	ok, _, _ := pSHGetPathFromIDList.Call(pidl, uintptr(unsafe.Pointer(&path[0])))
	pCoTaskMemFree.Call(pidl)
	if ok != 0 {
		setText(hPath, syscall.UTF16ToString(path[:]))
	}
}

func checked() bool {
	r, _, _ := pSendMessage.Call(hRecurse, bmGETCHECK, 0, 0)
	return r == bstCHECKED
}

func onClean(hwnd uintptr) {
	if busy {
		return
	}
	in := getText(hPath)
	if in == "" {
		msgBox(hwnd, "Choose a file or folder first.", "metascrub", mbOK|mbICONWARNING)
		return
	}
	ff := FindFFmpeg()
	fp := FindFFprobe(ff)
	recursive := checked()

	busy = true
	pEnableWindow.Call(hClean, 0)
	pEnableWindow.Call(hBrowse, 0)
	setText(hLog, "")
	setProgress(0)
	setText(hStatus, "Scanning…")
	if ff != "" {
		logln("ffmpeg: " + ff)
	} else {
		logln("ffmpeg: not found — images will still be processed, videos skipped")
	}
	logln("Processing: " + in)

	// reset shared state
	mu.Lock()
	stLog = nil
	stFrac, stIdx, stTotal, stName = 0, 0, 0, ""
	stDone, stRes, stErr = false, Result{}, nil
	mu.Unlock()

	go func() {
		res, err := ScrubWithProgress(in, recursive, ff, fp, Progress{
			Log: func(s string) { mu.Lock(); stLog = append(stLog, s); mu.Unlock() },
			Status: func(frac float64, idx, total int, name string) {
				mu.Lock()
				stFrac, stIdx, stTotal, stName = frac, idx, total, name
				mu.Unlock()
			},
		})
		mu.Lock()
		stRes, stErr, stDone = res, err, true
		mu.Unlock()
	}()

	pSetTimer.Call(hwnd, idTimer, 100, 0)
}

func onTimer(hwnd uintptr) {
	mu.Lock()
	logs := stLog
	stLog = nil
	frac, idx, total, name := stFrac, stIdx, stTotal, stName
	done, res, perr := stDone, stRes, stErr
	mu.Unlock()

	for _, l := range logs {
		logln(l)
	}
	setProgress(int(frac * 1000))
	if total > 0 {
		setText(hStatus, fmt.Sprintf("File %d of %d  —  %d%%   %s", idx, total, int(frac*100), name))
	}

	if !done {
		return
	}
	pKillTimer.Call(hwnd, idTimer)
	setProgress(1000)
	setText(hStatus, "Done.")
	logln(fmt.Sprintf("Done. cleaned=%d  failed=%d  skipped=%d", res.Processed, res.Failed, res.SkippedNoFFmpeg))
	if res.OutLoc != "" {
		logln("Output: " + res.OutLoc)
	}
	if res.SkippedNoFFmpeg > 0 {
		logln("Some videos were skipped (ffmpeg not installed).")
		logln("Install it (free):  winget install Gyan.FFmpeg  — then run again.")
	}

	icon := uintptr(mbICONINFO)
	if res.Failed > 0 || res.SkippedNoFFmpeg > 0 || perr != nil {
		icon = mbICONWARNING
	}
	summary := fmt.Sprintf("Cleaned copies: %d\nFailures: %d", res.Processed, res.Failed)
	if res.SkippedNoFFmpeg > 0 {
		summary += fmt.Sprintf("\nVideos skipped (need ffmpeg): %d", res.SkippedNoFFmpeg)
	}
	if res.OutLoc != "" {
		summary += "\n\nOutput:\n" + res.OutLoc
	}
	if perr != nil {
		summary += "\n\n" + perr.Error()
	}
	msgBox(hwnd, summary, "metascrub", mbOK|icon)

	pEnableWindow.Call(hClean, 1)
	pEnableWindow.Call(hBrowse, 1)
	busy = false
}

func wndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmCREATE:
		createControls(hwnd)
		layout(hwnd)
		return 0
	case wmSIZE:
		layout(hwnd)
		return 0
	case wmCOMMAND:
		switch wParam & 0xFFFF {
		case idBrowse:
			onBrowse(hwnd)
		case idClean:
			onClean(hwnd)
		}
		return 0
	case wmTIMER:
		onTimer(hwnd)
		return 0
	case wmGETMINMAXINFO:
		mmi := (*minMaxInfo)(unsafe.Pointer(lParam))
		mmi.ptMinTrackSize = point{560, 420}
		return 0
	case wmDESTROY:
		pPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := pDefWindowProc.Call(hwnd, msg, wParam, lParam)
	return r
}

func main() {
	runtime.LockOSThread()

	hInst, _, _ = pGetModuleHandle.Call(0)
	pOleInitialize.Call(0)
	icc := initCommonControlsEx{dwSize: uint32(unsafe.Sizeof(initCommonControlsEx{})), dwICC: iccPROGRESS}
	pInitCommonControlsEx.Call(uintptr(unsafe.Pointer(&icc)))
	hFont, _, _ = pGetStockObject.Call(defaultGUIFont)
	cursor, _, _ := pLoadCursor.Call(0, idcARROW)

	className := strptr("metascrubWindow")
	wc := wndClassEx{
		cbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		style:         csHREDRAW | csVREDRAW,
		lpfnWndProc:   syscall.NewCallback(wndProc),
		hInstance:     hInst,
		hCursor:       cursor,
		hbrBackground: colorBtnFace + 1,
		lpszClassName: className,
	}
	pRegisterClassEx.Call(uintptr(unsafe.Pointer(&wc)))

	hwnd, _, _ := pCreateWindowEx.Call(0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(strptr("metascrub — remove image & video metadata"))),
		wsOVERLAPPEDWINDOW,
		cwUseDefault, cwUseDefault, 720, 560,
		0, 0, hInst, 0)

	pShowWindow.Call(hwnd, swSHOW)
	pUpdateWindow.Call(hwnd)

	var m msgT
	for {
		r, _, _ := pGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if r == 0 || r == ^uintptr(0) {
			break
		}
		pTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		pDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
}
