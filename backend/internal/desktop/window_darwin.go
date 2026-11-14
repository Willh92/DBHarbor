//go:build darwin

package desktop

import (
	"os/exec"
	"runtime"
	"strings"
	"sync/atomic"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

var (
	activeMacWindow   atomic.Uintptr
	activeMacDelegate atomic.Uintptr
	pendingDarkMode   atomic.Bool
)

type cgPoint struct {
	x float64
	y float64
}

type cgSize struct {
	width  float64
	height float64
}

type cgRect struct {
	origin cgPoint
	size   cgSize
}

const (
	nsApplicationActivationPolicyRegular = 0
	nsBackingStoreBuffered               = 2

	nsWindowStyleMaskTitled         uintptr = 1 << 0
	nsWindowStyleMaskClosable       uintptr = 1 << 1
	nsWindowStyleMaskMiniaturizable uintptr = 1 << 2
	nsWindowStyleMaskResizable      uintptr = 1 << 3

	nsEventModifierFlagCommand uintptr = 1 << 20
)

func nsString(s string) objc.ID {
	cstr := append([]byte(s), 0)
	return objc.ID(objc.GetClass("NSString")).Send(
		objc.RegisterName("stringWithUTF8String:"),
		uintptr(unsafe.Pointer(&cstr[0])),
	)
}

func applyMacWindowAppearance(win objc.ID, dark bool) {
	if win == 0 {
		return
	}
	appCls := objc.GetClass("NSAppearance")
	if appCls == 0 {
		return
	}
	name := "NSAppearanceNameAqua"
	if dark {
		name = "NSAppearanceNameDarkAqua"
	}
	appearance := objc.ID(appCls).Send(objc.RegisterName("appearanceNamed:"), nsString(name))
	if appearance != 0 {
		win.Send(objc.RegisterName("setAppearance:"), appearance)
	}
}

// RunWindow opens an in-process native macOS Cocoa NSWindow + WKWebView (CGO_ENABLED=0) and blocks until closed.
func RunWindow(cfg WindowConfig) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	cfg = cfg.withDefaults()
	pendingDarkMode.Store(cfg.DarkMode)

	if _, err := purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_LAZY|purego.RTLD_GLOBAL); err != nil {
		return runChromelessAppWindowFallback(cfg)
	}
	if _, err := purego.Dlopen("/System/Library/Frameworks/WebKit.framework/WebKit", purego.RTLD_LAZY|purego.RTLD_GLOBAL); err != nil {
		return runChromelessAppWindowFallback(cfg)
	}

	nsApp := objc.ID(objc.GetClass("NSApplication")).Send(objc.RegisterName("sharedApplication"))
	if nsApp == 0 {
		return runChromelessAppWindowFallback(cfg)
	}
	nsApp.Send(objc.RegisterName("setActivationPolicy:"), uintptr(nsApplicationActivationPolicyRegular))

	// Register delegate so closing the main window cleanly terminates NSApp.run and supports main-thread theme updates
	if delClass, err := objc.RegisterClass(
		"DBHarborAppDelegate",
		objc.GetClass("NSObject"),
		nil,
		nil,
		[]objc.MethodDef{
			{
				Cmd: objc.RegisterName("applicationShouldTerminateAfterLastWindowClosed:"),
				Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID) bool {
					return true
				},
			},
			{
				Cmd: objc.RegisterName("syncWindowTheme:"),
				Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID) {
					if w := objc.ID(activeMacWindow.Load()); w != 0 {
						applyMacWindowAppearance(w, pendingDarkMode.Load())
					}
				},
			},
		},
	); err == nil && delClass != 0 {
		delegate := objc.ID(delClass).Send(objc.RegisterName("alloc")).Send(objc.RegisterName("init"))
		nsApp.Send(objc.RegisterName("setDelegate:"), delegate)
		activeMacDelegate.Store(uintptr(delegate))
		defer activeMacDelegate.Store(0)
	}

	setupMacMenuBar(nsApp, cfg.Title)

	frame := cgRect{
		origin: cgPoint{x: 120, y: 120},
		size:   cgSize{width: float64(cfg.Width), height: float64(cfg.Height)},
	}
	styleMask := nsWindowStyleMaskTitled | nsWindowStyleMaskClosable | nsWindowStyleMaskMiniaturizable | nsWindowStyleMaskResizable

	win := objc.Send[objc.ID](
		objc.ID(objc.GetClass("NSWindow")).Send(objc.RegisterName("alloc")),
		objc.RegisterName("initWithContentRect:styleMask:backing:defer:"),
		frame,
		styleMask,
		uintptr(nsBackingStoreBuffered),
		false,
	)
	if win == 0 {
		return runChromelessAppWindowFallback(cfg)
	}
	activeMacWindow.Store(uintptr(win))
	defer activeMacWindow.Store(0)

	win.Send(objc.RegisterName("setTitle:"), nsString(cfg.Title))
	objc.Send[struct{}](win, objc.RegisterName("setMinSize:"), cgSize{width: 720, height: 480})
	win.Send(objc.RegisterName("center"))
	applyMacWindowAppearance(win, pendingDarkMode.Load())

	wkConfig := objc.ID(objc.GetClass("WKWebViewConfiguration")).Send(objc.RegisterName("alloc")).Send(objc.RegisterName("init"))
	webView := objc.Send[objc.ID](
		objc.ID(objc.GetClass("WKWebView")).Send(objc.RegisterName("alloc")),
		objc.RegisterName("initWithFrame:configuration:"),
		frame,
		wkConfig,
	)
	if webView == 0 {
		return runChromelessAppWindowFallback(cfg)
	}

	nsURL := objc.ID(objc.GetClass("NSURL")).Send(objc.RegisterName("URLWithString:"), nsString(cfg.BootURL))
	nsReq := objc.ID(objc.GetClass("NSURLRequest")).Send(objc.RegisterName("requestWithURL:"), nsURL)
	webView.Send(objc.RegisterName("loadRequest:"), nsReq)

	win.Send(objc.RegisterName("setContentView:"), webView)
	win.Send(objc.RegisterName("makeKeyAndOrderFront:"), objc.ID(0))
	nsApp.Send(objc.RegisterName("activateIgnoringOtherApps:"), true)
	nsApp.Send(objc.RegisterName("run"))
	return nil
}

func setupMacMenuBar(nsApp objc.ID, appTitle string) {
	menuCls := objc.GetClass("NSMenu")
	itemCls := objc.GetClass("NSMenuItem")
	if menuCls == 0 || itemCls == 0 {
		return
	}

	mainMenu := objc.ID(menuCls).Send(objc.RegisterName("alloc")).Send(objc.RegisterName("init"))

	// Application Menu (Quit Cmd+Q)
	appMenuItem := objc.ID(itemCls).Send(objc.RegisterName("alloc")).Send(objc.RegisterName("init"))
	appMenu := objc.ID(menuCls).Send(objc.RegisterName("alloc")).Send(objc.RegisterName("initWithTitle:"), nsString(appTitle))
	quitItem := objc.ID(itemCls).Send(objc.RegisterName("alloc")).Send(
		objc.RegisterName("initWithTitle:action:keyEquivalent:"),
		nsString("Quit "+appTitle),
		objc.RegisterName("terminate:"),
		nsString("q"),
	)
	appMenu.Send(objc.RegisterName("addItem:"), quitItem)
	appMenuItem.Send(objc.RegisterName("setSubmenu:"), appMenu)
	mainMenu.Send(objc.RegisterName("addItem:"), appMenuItem)

	// Edit Menu (essential so Cmd+Z/X/C/V/A work inside WKWebView & Monaco Editor)
	editMenuItem := objc.ID(itemCls).Send(objc.RegisterName("alloc")).Send(objc.RegisterName("init"))
	editMenu := objc.ID(menuCls).Send(objc.RegisterName("alloc")).Send(objc.RegisterName("initWithTitle:"), nsString("Edit"))
	editEntries := []struct {
		title string
		sel   string
		key   string
	}{
		{"Undo", "undo:", "z"},
		{"Redo", "redo:", "Z"},
		{"Cut", "cut:", "x"},
		{"Copy", "copy:", "c"},
		{"Paste", "paste:", "v"},
		{"Select All", "selectAll:", "a"},
	}
	for _, e := range editEntries {
		it := objc.ID(itemCls).Send(objc.RegisterName("alloc")).Send(
			objc.RegisterName("initWithTitle:action:keyEquivalent:"),
			nsString(e.title),
			objc.RegisterName(e.sel),
			nsString(e.key),
		)
		it.Send(objc.RegisterName("setKeyEquivalentModifierMask:"), nsEventModifierFlagCommand)
		editMenu.Send(objc.RegisterName("addItem:"), it)
	}
	editMenuItem.Send(objc.RegisterName("setSubmenu:"), editMenu)
	mainMenu.Send(objc.RegisterName("addItem:"), editMenuItem)

	nsApp.Send(objc.RegisterName("setMainMenu:"), mainMenu)
}

// SetWindowDarkMode dynamically updates the native macOS NSWindow appearance on the Cocoa main thread.
func SetWindowDarkMode(dark bool) {
	pendingDarkMode.Store(dark)
	if del := objc.ID(activeMacDelegate.Load()); del != 0 {
		del.Send(
			objc.RegisterName("performSelectorOnMainThread:withObject:waitUntilDone:"),
			objc.RegisterName("syncWindowTheme:"),
			objc.ID(0),
			false,
		)
	}
}

// SaveFileDialog opens the native macOS Finder Save panel safely from any goroutine.
func SaveFileDialog(defaultFilename string) (string, bool, error) {
	const script = `on run argv
	return POSIX path of (choose file name with prompt "Export Database File - DBHarbor" default name (item 1 of argv))
end run`
	out, err := exec.Command("osascript", "-e", script, defaultFilename).Output()
	if err != nil {
		return "", true, nil
	}
	chosen := strings.TrimSpace(string(out))
	if chosen == "" {
		return "", true, nil
	}
	return chosen, false, nil
}

// OpenFileDialog opens the native macOS Finder dialog for choosing an existing database file.
func OpenFileDialog(kind string, _ string) (string, bool, error) {
	prompt := "Select Local SQLite Database File - DBHarbor"
	if strings.EqualFold(strings.TrimSpace(kind), "duckdb") {
		prompt = "Select Local DuckDB Database File - DBHarbor"
	}
	const script = `on run argv
	return POSIX path of (choose file with prompt (item 1 of argv))
end run`
	out, err := exec.Command("osascript", "-e", script, prompt).Output()
	if err != nil {
		return "", true, nil
	}
	chosen := strings.TrimSpace(string(out))
	if chosen == "" {
		return "", true, nil
	}
	return chosen, false, nil
}

// ShowErrorDialog displays a native macOS alert box when startup fails.
func ShowErrorDialog(title, message string) {
	const script = `on run argv
	display alert (item 1 of argv) message (item 2 of argv) as critical
end run`
	_ = exec.Command("osascript", "-e", script, title, message).Run()
}
