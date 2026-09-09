package window

// The Win32 messages this window handles itself. They are written out here
// rather than taken from a library so that [decideProcAction] — the one part
// of the window procedure that makes a decision — stays a plain function that
// compiles and tests on any platform.
const (
	// wmClose is what the title bar's X sends.
	wmClose = 0x0010
	// wmGetMinMaxInfo asks the window for its size limits, just before the
	// user starts dragging an edge.
	wmGetMinMaxInfo = 0x0024
	// wmDPIChanged says the window has arrived on a display with a different
	// scale factor — dragged to a second monitor, or the slider moved in
	// Settings. Windows only sends it to a process that declared per-monitor
	// awareness; see [DeclarePerMonitorDPI].
	wmDPIChanged = 0x02E0
)

// procAction is what the window procedure does with one message.
type procAction int

const (
	// actionDefault hands the message to the procedure we subclassed, which is
	// the WebView2 library's own — it is what keeps the browser widget
	// resizing, focusing and repainting.
	actionDefault procAction = iota
	// actionHide hides the window to the tray and swallows the message.
	actionHide
	// actionMinSize fills in the caller's size limits and swallows the message.
	actionMinSize
	// actionResize re-derives the window's size from the design at the new
	// display scale and swallows the message.
	actionResize
)

// String names the action, for test failures and logs.
func (a procAction) String() string {
	switch a {
	case actionDefault:
		return "default"
	case actionHide:
		return "hide"
	case actionMinSize:
		return "min-size"
	case actionResize:
		return "resize"
	default:
		return "unknown"
	}
}

// decideProcAction reports what the window procedure should do with a message.
//
// WM_CLOSE is the whole reason this window is subclassed. The default
// procedure destroys the window, which would tear down the WebView2 host and
// end the message loop — and with it the program. Hiding instead is what makes
// the X button mean «свернуть в трей»: the tray keeps the process alive and
// re-opening costs a ShowWindow rather than a fresh browser boot.
//
// WM_DPICHANGED is the other one this window cannot leave to the default
// procedure. Dragging a window to a monitor with a different scale is an
// ordinary thing to do; the default procedure does nothing about it, so the
// window would keep its old size in physical pixels while the page inside it
// relaid itself out at the new devicePixelRatio — the design either cut off or
// swimming in dead space. Answering it means re-deriving the size from the
// design at the new DPI, which is what [layoutForDPIChange] does.
func decideProcAction(msg uint32) procAction {
	switch msg {
	case wmClose:
		return actionHide
	case wmGetMinMaxInfo:
		return actionMinSize
	case wmDPIChanged:
		return actionResize
	default:
		return actionDefault
	}
}
