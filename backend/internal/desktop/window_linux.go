//go:build linux

package desktop

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ebitengine/purego"
)

// RunWindow opens an in-process native GTK3 + WebKit2GTK desktop window (CGO_ENABLED=0)
// and falls back to an isolated chromeless browser window if WebKit2GTK is not installed.
func RunWindow(cfg WindowConfig) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	cfg = cfg.withDefaults()
	if cfg.DarkMode && strings.TrimSpace(os.Getenv("GTK_THEME")) == "" {
		_ = os.Setenv("GTK_THEME", "Adwaita:dark")
	}

	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return fmt.Errorf("no graphical display detected (DISPLAY / WAYLAND_DISPLAY are unset)")
	}

	if err := tryRunGTKWebKitWindow(cfg); err == nil {
		return nil
	}
	return runChromelessAppWindowFallback(cfg)
}

func tryRunGTKWebKitWindow(cfg WindowConfig) error {
	gtkLib, err := purego.Dlopen("libgtk-3.so.0", purego.RTLD_LAZY|purego.RTLD_GLOBAL)
	if err != nil {
		return err
	}

	var wkLib uintptr
	for _, candidate := range []string{"libwebkit2gtk-4.1.so.0", "libwebkit2gtk-4.0.so.37"} {
		if h, lErr := purego.Dlopen(candidate, purego.RTLD_LAZY|purego.RTLD_GLOBAL); lErr == nil {
			wkLib = h
			break
		}
	}
	if wkLib == 0 {
		return fmt.Errorf("libwebkit2gtk not available")
	}

	gobjLib, err := purego.Dlopen("libgobject-2.0.so.0", purego.RTLD_LAZY|purego.RTLD_GLOBAL)
	if err != nil {
		return err
	}

	var (
		gtkInitCheck          func(argc uintptr, argv uintptr) int32
		gtkWindowNew          func(winType int32) uintptr
		gtkWindowSetTitle     func(win uintptr, title string)
		gtkWindowSetDefSize   func(win uintptr, width int32, height int32)
		gtkWindowSetPosition  func(win uintptr, pos int32)
		gtkWidgetSetSizeReq   func(widget uintptr, width int32, height int32)
		gtkContainerAdd       func(container uintptr, widget uintptr)
		gtkWidgetShowAll      func(widget uintptr)
		gtkMain               func()
		gtkMainQuitFnAddr     uintptr
		webkitWebViewNew      func() uintptr
		webkitWebViewLoadURI  func(wv uintptr, uri string)
		gSignalConnectData    func(instance uintptr, detailedSignal string, cHandler uintptr, data uintptr, destroyData uintptr, connectFlags int32) uint64
	)

	purego.RegisterLibFunc(&gtkInitCheck, gtkLib, "gtk_init_check")
	purego.RegisterLibFunc(&gtkWindowNew, gtkLib, "gtk_window_new")
	purego.RegisterLibFunc(&gtkWindowSetTitle, gtkLib, "gtk_window_set_title")
	purego.RegisterLibFunc(&gtkWindowSetDefSize, gtkLib, "gtk_window_set_default_size")
	purego.RegisterLibFunc(&gtkWindowSetPosition, gtkLib, "gtk_window_set_position")
	purego.RegisterLibFunc(&gtkWidgetSetSizeReq, gtkLib, "gtk_widget_set_size_request")
	purego.RegisterLibFunc(&gtkContainerAdd, gtkLib, "gtk_container_add")
	purego.RegisterLibFunc(&gtkWidgetShowAll, gtkLib, "gtk_widget_show_all")
	purego.RegisterLibFunc(&gtkMain, gtkLib, "gtk_main")
	gtkMainQuitFnAddr, err = purego.Dlsym(gtkLib, "gtk_main_quit")
	if err != nil {
		return err
	}
	purego.RegisterLibFunc(&webkitWebViewNew, wkLib, "webkit_web_view_new")
	purego.RegisterLibFunc(&webkitWebViewLoadURI, wkLib, "webkit_web_view_load_uri")
	purego.RegisterLibFunc(&gSignalConnectData, gobjLib, "g_signal_connect_data")

	if gtkInitCheck(0, 0) == 0 {
		return fmt.Errorf("gtk_init_check failed")
	}

	const (
		gtkWindowToplevel = 0
		gtkWinPosCenter   = 1
	)
	win := gtkWindowNew(gtkWindowToplevel)
	if win == 0 {
		return fmt.Errorf("gtk_window_new returned NULL")
	}
	gtkWindowSetTitle(win, cfg.Title)
	gtkWindowSetDefSize(win, int32(cfg.Width), int32(cfg.Height))
	gtkWidgetSetSizeReq(win, 720, 480)
	gtkWindowSetPosition(win, gtkWinPosCenter)

	wv := webkitWebViewNew()
	if wv == 0 {
		return fmt.Errorf("webkit_web_view_new returned NULL")
	}
	gtkContainerAdd(win, wv)
	gSignalConnectData(win, "destroy", gtkMainQuitFnAddr, 0, 0, 0)
	webkitWebViewLoadURI(wv, cfg.BootURL)
	gtkWidgetShowAll(win)
	gtkMain()
	return nil
}

// SaveFileDialog opens a native Linux file save dialog via zenity/kdialog (falling back to ~/Downloads).
func SaveFileDialog(defaultFilename string) (string, bool, error) {
	home, _ := os.UserHomeDir()
	defaultPath := defaultFilename
	if home != "" {
		dl := filepath.Join(home, "Downloads")
		if _, err := os.Stat(dl); err == nil {
			defaultPath = filepath.Join(dl, defaultFilename)
		} else {
			defaultPath = filepath.Join(home, defaultFilename)
		}
	}

	if zenity, err := exec.LookPath("zenity"); err == nil {
		out, zErr := exec.Command(zenity, "--file-selection", "--save", "--confirm-overwrite", "--title=Export Database File - DBHarbor", "--filename="+defaultPath).Output()
		if zErr != nil {
			return "", true, nil
		}
		chosen := strings.TrimSpace(string(out))
		if chosen == "" {
			return "", true, nil
		}
		return chosen, false, nil
	}

	if kdialog, err := exec.LookPath("kdialog"); err == nil {
		out, kErr := exec.Command(kdialog, "--getsavefilename", defaultPath).Output()
		if kErr != nil {
			return "", true, nil
		}
		chosen := strings.TrimSpace(string(out))
		if chosen == "" {
			return "", true, nil
		}
		return chosen, false, nil
	}

	return uniqueFallbackSavePath(defaultPath), false, nil
}

func uniqueFallbackSavePath(candidate string) string {
	if _, err := os.Stat(candidate); os.IsNotExist(err) {
		return candidate
	}
	ext := filepath.Ext(candidate)
	stem := strings.TrimSuffix(candidate, ext)
	for i := 1; i <= 999; i++ {
		next := fmt.Sprintf("%s (%d)%s", stem, i, ext)
		if _, err := os.Stat(next); os.IsNotExist(err) {
			return next
		}
	}
	return candidate
}

// SetWindowDarkMode updates the GTK_THEME environment preference on Linux desktops.
func SetWindowDarkMode(dark bool) {
	if dark {
		_ = os.Setenv("GTK_THEME", "Adwaita:dark")
	} else if os.Getenv("GTK_THEME") == "Adwaita:dark" {
		_ = os.Unsetenv("GTK_THEME")
	}
}

// OpenFileDialog opens a native Linux file chooser dialog via zenity/kdialog.
func OpenFileDialog(kind string, currentPath string) (string, bool, error) {
	home, _ := os.UserHomeDir()
	startPath := strings.TrimSpace(currentPath)
	if startPath == "" || startPath == ":memory:" {
		startPath = home
	}
	filterArg := "--file-filter=Database Files | *.db *.sqlite *.sqlite3 *.duckdb *.ddb"
	if strings.EqualFold(strings.TrimSpace(kind), "duckdb") {
		filterArg = "--file-filter=DuckDB Files | *.duckdb *.ddb *.db *.parquet *.csv"
	}
	if zenity, err := exec.LookPath("zenity"); err == nil {
		args := []string{"--file-selection", "--title=Select Local Database File - DBHarbor", filterArg, "--file-filter=All Files | *"}
		if startPath != "" {
			args = append(args, "--filename="+startPath)
		}
		out, zErr := exec.Command(zenity, args...).Output()
		if zErr != nil {
			return "", true, nil
		}
		chosen := strings.TrimSpace(string(out))
		if chosen == "" {
			return "", true, nil
		}
		return chosen, false, nil
	}
	if kdialog, err := exec.LookPath("kdialog"); err == nil {
		out, kErr := exec.Command(kdialog, "--getopenfilename", startPath, "*.db *.sqlite *.sqlite3 *.duckdb *.ddb").Output()
		if kErr != nil {
			return "", true, nil
		}
		chosen := strings.TrimSpace(string(out))
		if chosen == "" {
			return "", true, nil
		}
		return chosen, false, nil
	}
	return "", true, nil
}

// ShowErrorDialog displays an error notification on Linux desktops.
func ShowErrorDialog(title, message string) {
	if zenity, err := exec.LookPath("zenity"); err == nil {
		_ = exec.Command(zenity, "--error", "--title="+title, "--text="+message).Run()
		return
	}
	_, _ = fmt.Fprintf(os.Stderr, "[%s] %s\n", title, message)
}
