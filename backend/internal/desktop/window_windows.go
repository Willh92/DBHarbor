//go:build windows

package desktop

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"unsafe"

	"github.com/jchv/go-webview2"
)

var (
	user32DLL                     = syscall.NewLazyDLL("user32.dll")
	comdlg32DLL                   = syscall.NewLazyDLL("comdlg32.dll")
	dwmapiDLL                     = syscall.NewLazyDLL("dwmapi.dll")
	procMessageBoxW               = user32DLL.NewProc("MessageBoxW")
	procSetProcessDpiAwarenessCtx = user32DLL.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDPIAware        = user32DLL.NewProc("SetProcessDPIAware")
	procGetDpiForSystem           = user32DLL.NewProc("GetDpiForSystem")
	procSystemParametersInfoW     = user32DLL.NewProc("SystemParametersInfoW")
	procSetWindowPos              = user32DLL.NewProc("SetWindowPos")
	procGetWindowLongW            = user32DLL.NewProc("GetWindowLongW")
	procSetWindowLongW            = user32DLL.NewProc("SetWindowLongW")
	procGetMessageW               = user32DLL.NewProc("GetMessageW")
	procTranslateMessage          = user32DLL.NewProc("TranslateMessage")
	procDispatchMessageW          = user32DLL.NewProc("DispatchMessageW")
	procGetSaveFileNameW          = comdlg32DLL.NewProc("GetSaveFileNameW")
	procGetOpenFileNameW          = comdlg32DLL.NewProc("GetOpenFileNameW")
	procDwmSetWindowAttribute     = dwmapiDLL.NewProc("DwmSetWindowAttribute")

	activeMainHWND  atomic.Uintptr
	pendingDarkMode atomic.Bool
)

const (
	dpiAwarenessPerMonitorAwareV2 = ^uintptr(3) // (DPI_AWARENESS_CONTEXT)-4
	spiGetWorkArea                = 0x0030
	swpNoZOrder                   = 0x0004
	swpNoActivate                 = 0x0010
	swpFrameChanged               = 0x0020
	swpNoMove                     = 0x0002
	swpNoSize                     = 0x0001
	gwlStyle                      = ^uintptr(15) // -16 (GWL_STYLE)
	wsClipChildren                = 0x02000000
	wmQuit                        = 0x0012
	mbIconError                   = 0x00000010
	mbOK                          = 0x00000000
	ofnOverwritePrompt            = 0x00000002
	ofnPathMustExist              = 0x00000800
	ofnNoChangeDir                = 0x00000008
	ofnExplorer                   = 0x00080000

	dwmwaUseImmersiveDarkModeWin10 = 19
	dwmwaUseImmersiveDarkMode      = 20
	dwmwaCaptionColor              = 35
	dwmColorDarkHeader             = uint32(0x00141414) // BGR for #141414
	dwmColorDefault                = uint32(0xFFFFFFFF)
)

type winPoint struct {
	X int32
	Y int32
}

type winMsg struct {
	Hwnd     syscall.Handle
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       winPoint
	LPrivate uint32
}

type winRect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

type openFileNameW struct {
	lStructSize       uint32
	hwndOwner          uintptr
	hInstance         uintptr
	lpstrFilter       *uint16
	lpstrCustomFilter *uint16
	nMaxCustFilter    uint32
	nFilterIndex      uint32
	lpstrFile         *uint16
	nMaxFile          uint32
	lpstrFileTitle    *uint16
	nMaxFileTitle     uint32
	lpstrInitialDir   *uint16
	lpstrTitle        *uint16
	flags             uint32
	nFileOffset       uint16
	nFileExtension    uint16
	lpstrDefExt       *uint16
	lCustData         uintptr
	lpfnHook          uintptr
	lpTemplateName    *uint16
	pvReserved        uintptr
	dwReserved        uint32
	flagsEx           uint32
}

func enableWindowsHiDPI() {
	if err := procSetProcessDpiAwarenessCtx.Find(); err == nil {
		_, _, _ = procSetProcessDpiAwarenessCtx.Call(dpiAwarenessPerMonitorAwareV2)
		return
	}
	if err := procSetProcessDPIAware.Find(); err == nil {
		_, _, _ = procSetProcessDPIAware.Call()
	}
}

func resolveHiDPIWindowGeometry(logicalW, logicalH int) (posX, posY, physW, physH, minW, minH int) {
	scale := 1.0
	if err := procGetDpiForSystem.Find(); err == nil {
		if dpi, _, _ := procGetDpiForSystem.Call(); dpi >= 96 && dpi <= 480 {
			scale = float64(dpi) / 96.0
		}
	}

	physW = int(float64(logicalW) * scale)
	physH = int(float64(logicalH) * scale)
	minW = int(720.0 * scale)
	minH = int(480.0 * scale)

	var work winRect
	if err := procSystemParametersInfoW.Find(); err == nil {
		ret, _, _ := procSystemParametersInfoW.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&work)), 0)
		workW := int(work.Right - work.Left)
		workH := int(work.Bottom - work.Top)
		if ret != 0 && workW > 400 && workH > 300 {
			maxW := workW * 90 / 100
			maxH := workH * 90 / 100
			if physW > maxW {
				physW = maxW
			}
			if physH > maxH {
				physH = maxH
			}
			if minW > workW*75/100 {
				minW = workW * 75 / 100
			}
			if minH > workH*75/100 {
				minH = workH * 75 / 100
			}
			posX = int(work.Left) + (workW-physW)/2
			posY = int(work.Top) + (workH-physH)/2
		}
	}
	return
}

// configureWebView2PerformanceFlags ensures the embedded Edge WebView2 runtime runs with
// full hardware GPU rasterization, zero-copy compositing, and unlocked VSync frame pacing
// (preventing Windows native child-HWND occlusion checks from throttling interactive drag frames).
func configureWebView2PerformanceFlags() {
	if strings.TrimSpace(os.Getenv("WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS")) != "" {
		return
	}
	_ = os.Setenv(
		"WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS",
		"--disable-features=CalculateNativeWinOcclusion --disable-renderer-backgrounding --disable-backgrounding-occluded-windows --enable-gpu-rasterization --enable-zero-copy",
	)
}

// runNativeMessagePump executes a lean Win32 UI message loop without go-webview2's per-message
// GetAncestor + IsDialogMessageW child-HWND tree traversal, eliminating high-rate mouse/pointer
// dispatch overhead and keeping Tab/Escape key handling intact inside WebView2.
func runNativeMessagePump() {
	var msg winMsg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(ret) <= 0 || msg.Message == wmQuit {
			return
		}
		_, _, _ = procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		_, _, _ = procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
}

// RunWindow opens a native Win32 + Microsoft Edge WebView2 desktop window and blocks until closed.
func RunWindow(cfg WindowConfig) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	enableWindowsHiDPI()
	configureWebView2PerformanceFlags()
	cfg = cfg.withDefaults()
	posX, posY, physW, physH, minW, minH := resolveHiDPIWindowGeometry(1380, 860)

	wvDataDir := filepath.Join(filepath.Dir(cfg.DataDir), "webview2")
	_ = os.MkdirAll(wvDataDir, 0o755)

	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     false,
		AutoFocus: true,
		DataPath:  wvDataDir,
		WindowOptions: webview2.WindowOptions{
			Title:  cfg.Title,
			Width:  uint(physW),
			Height: uint(physH),
			IconId: 1,
			Center: true,
		},
	})
	if w == nil {
		return runChromelessAppWindowFallback(cfg)
	}
	defer w.Destroy()

	w.SetSize(minW, minH, webview2.HintMin)
	pendingDarkMode.Store(cfg.DarkMode)
	if hwnd := uintptr(w.Window()); hwnd != 0 {
		activeMainHWND.Store(hwnd)
		defer activeMainHWND.Store(0)
		// Prevent Win32 GDI parent overdraw behind the DirectComposition WebView2 child surface
		if style, _, _ := procGetWindowLongW.Call(hwnd, gwlStyle); style != 0 {
			_, _, _ = procSetWindowLongW.Call(hwnd, gwlStyle, style|wsClipChildren)
			_, _, _ = procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoZOrder|swpNoActivate|swpFrameChanged)
		}
		if posX > 0 || posY > 0 {
			_, _, _ = procSetWindowPos.Call(hwnd, 0, uintptr(posX), uintptr(posY), uintptr(physW), uintptr(physH), swpNoZOrder|swpNoActivate)
		}
		applyWindowsWindowDarkMode(hwnd, pendingDarkMode.Load())
	}
	w.Navigate(cfg.BootURL)
	runNativeMessagePump()
	return nil
}

func applyWindowsWindowDarkMode(hwnd uintptr, dark bool) {
	if hwnd == 0 {
		return
	}
	if err := procDwmSetWindowAttribute.Find(); err != nil {
		return
	}
	var useDark uint32
	if dark {
		useDark = 1
	}
	hr, _, _ := procDwmSetWindowAttribute.Call(hwnd, dwmwaUseImmersiveDarkMode, uintptr(unsafe.Pointer(&useDark)), unsafe.Sizeof(useDark))
	if hr != 0 {
		_, _, _ = procDwmSetWindowAttribute.Call(hwnd, dwmwaUseImmersiveDarkModeWin10, uintptr(unsafe.Pointer(&useDark)), unsafe.Sizeof(useDark))
	}
	captionColor := dwmColorDefault
	if dark {
		captionColor = dwmColorDarkHeader
	}
	_, _, _ = procDwmSetWindowAttribute.Call(hwnd, dwmwaCaptionColor, uintptr(unsafe.Pointer(&captionColor)), unsafe.Sizeof(captionColor))
	_, _, _ = procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0, swpNoMove|swpNoSize|swpNoZOrder|swpNoActivate|swpFrameChanged)
}

// SetWindowDarkMode dynamically updates the native Windows 10/11 DWM title bar appearance.
func SetWindowDarkMode(dark bool) {
	pendingDarkMode.Store(dark)
	if hwnd := activeMainHWND.Load(); hwnd != 0 {
		applyWindowsWindowDarkMode(hwnd, dark)
	}
}

// SaveFileDialog opens the native Windows Explorer Save As dialog.
func SaveFileDialog(defaultFilename string) (string, bool, error) {
	buf := make([]uint16, 4096)
	defUTF16, _ := syscall.UTF16FromString(defaultFilename)
	copy(buf, defUTF16)

	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(defaultFilename)), ".")
	filterPairs := []string{"All Files (*.*)", "*.*"}
	switch ext {
	case "sql":
		filterPairs = []string{"SQL Script (*.sql)", "*.sql", "All Files (*.*)", "*.*"}
	case "csv":
		filterPairs = []string{"CSV File (*.csv)", "*.csv", "All Files (*.*)", "*.*"}
	case "json":
		filterPairs = []string{"JSON File (*.json)", "*.json", "All Files (*.*)", "*.*"}
	case "jsonl":
		filterPairs = []string{"JSON Lines (*.jsonl)", "*.jsonl", "All Files (*.*)", "*.*"}
	case "md":
		filterPairs = []string{"Markdown (*.md)", "*.md", "All Files (*.*)", "*.*"}
	}
	var filterUTF16 []uint16
	for _, s := range filterPairs {
		u, _ := syscall.UTF16FromString(s)
		filterUTF16 = append(filterUTF16, u...)
	}
	filterUTF16 = append(filterUTF16, 0)

	var initDirPtr *uint16
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dl := filepath.Join(home, "Downloads")
		if _, sErr := os.Stat(dl); sErr == nil {
			initDirPtr, _ = syscall.UTF16PtrFromString(dl)
		}
	}
	titlePtr, _ := syscall.UTF16PtrFromString("Export Database File - DBHarbor")
	defExtPtr, _ := syscall.UTF16PtrFromString(ext)

	ofn := openFileNameW{
		lStructSize:     uint32(unsafe.Sizeof(openFileNameW{})),
		hwndOwner:       activeMainHWND.Load(),
		lpstrFilter:     &filterUTF16[0],
		nFilterIndex:    1,
		lpstrFile:       &buf[0],
		nMaxFile:        uint32(len(buf)),
		lpstrInitialDir: initDirPtr,
		lpstrTitle:      titlePtr,
		lpstrDefExt:     defExtPtr,
		flags:           ofnExplorer | ofnOverwritePrompt | ofnPathMustExist | ofnNoChangeDir,
	}

	ret, _, _ := procGetSaveFileNameW.Call(uintptr(unsafe.Pointer(&ofn)))
	if ret == 0 {
		return "", true, nil
	}
	chosen := syscall.UTF16ToString(buf)
	if strings.TrimSpace(chosen) == "" {
		return "", true, nil
	}
	return chosen, false, nil
}

// OpenFileDialog opens the native Windows Explorer file picker for selecting or creating a local SQLite / DuckDB file.
func OpenFileDialog(kind string, currentPath string) (string, bool, error) {
	buf := make([]uint16, 4096)
	defExt := "sqlite"
	filterPairs := []string{
		"SQLite Database (*.db;*.sqlite;*.sqlite3)", "*.db;*.sqlite;*.sqlite3",
		"DuckDB Database (*.duckdb;*.ddb)", "*.duckdb;*.ddb",
		"All Files (*.*)", "*.*",
	}
	if strings.EqualFold(strings.TrimSpace(kind), "duckdb") {
		defExt = "duckdb"
		filterPairs = []string{
			"DuckDB Database (*.duckdb;*.ddb;*.db)", "*.duckdb;*.ddb;*.db",
			"Parquet / CSV Data (*.parquet;*.csv)", "*.parquet;*.csv",
			"All Files (*.*)", "*.*",
		}
	}

	var initDirPtr *uint16
	trimmed := strings.TrimSpace(currentPath)
	if trimmed != "" && trimmed != ":memory:" {
		if abs, err := filepath.Abs(trimmed); err == nil {
			dir := filepath.Dir(abs)
			if st, sErr := os.Stat(dir); sErr == nil && st.IsDir() {
				initDirPtr, _ = syscall.UTF16PtrFromString(dir)
				base := filepath.Base(abs)
				if base != "." && base != string(filepath.Separator) {
					defUTF16, _ := syscall.UTF16FromString(base)
					copy(buf, defUTF16)
				}
			}
		}
	}
	if initDirPtr == nil {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			initDirPtr, _ = syscall.UTF16PtrFromString(home)
		}
	}

	var filterUTF16 []uint16
	for _, s := range filterPairs {
		u, _ := syscall.UTF16FromString(s)
		filterUTF16 = append(filterUTF16, u...)
	}
	filterUTF16 = append(filterUTF16, 0)

	titlePtr, _ := syscall.UTF16PtrFromString("Select or Enter New Local Database File - DBHarbor")
	defExtPtr, _ := syscall.UTF16PtrFromString(defExt)

	ofn := openFileNameW{
		lStructSize:     uint32(unsafe.Sizeof(openFileNameW{})),
		hwndOwner:       activeMainHWND.Load(),
		lpstrFilter:     &filterUTF16[0],
		nFilterIndex:    1,
		lpstrFile:       &buf[0],
		nMaxFile:        uint32(len(buf)),
		lpstrInitialDir: initDirPtr,
		lpstrTitle:      titlePtr,
		lpstrDefExt:     defExtPtr,
		flags:           ofnExplorer | ofnPathMustExist | ofnNoChangeDir,
	}

	ret, _, _ := procGetOpenFileNameW.Call(uintptr(unsafe.Pointer(&ofn)))
	if ret == 0 {
		return "", true, nil
	}
	chosen := syscall.UTF16ToString(buf)
	if strings.TrimSpace(chosen) == "" {
		return "", true, nil
	}
	return chosen, false, nil
}

// ShowErrorDialog displays a native Win32 error box when startup fails in GUI mode.
func ShowErrorDialog(title, message string) {
	tPtr, _ := syscall.UTF16PtrFromString(title)
	mPtr, _ := syscall.UTF16PtrFromString(message)
	_, _, _ = procMessageBoxW.Call(0, uintptr(unsafe.Pointer(mPtr)), uintptr(unsafe.Pointer(tPtr)), mbOK|mbIconError)
}
