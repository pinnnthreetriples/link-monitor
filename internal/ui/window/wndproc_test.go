package window

import "testing"

func TestDecideProcActionRoutesEveryMessage(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		msg  uint32
		want procAction
	}{
		{name: "WM_CLOSE hides to the tray instead of quitting", msg: wmClose, want: actionHide},
		{name: "WM_GETMINMAXINFO sets the minimum size", msg: wmGetMinMaxInfo, want: actionMinSize},
		{
			name: "WM_DPICHANGED re-derives the size at the new display scale",
			msg:  wmDPIChanged, want: actionResize,
		},
		{name: "WM_DESTROY goes to the library", msg: 0x0002, want: actionDefault},
		{name: "WM_SIZE goes to the library, which resizes the browser", msg: 0x0005, want: actionDefault},
		{name: "WM_ACTIVATE goes to the library, which moves focus", msg: 0x0006, want: actionDefault},
		{name: "WM_PAINT goes to the library", msg: 0x000F, want: actionDefault},
		{name: "WM_MOVE goes to the library", msg: 0x0003, want: actionDefault},
		{name: "WM_QUIT is not a window message and falls through", msg: 0x0012, want: actionDefault},
		{name: "message zero falls through", msg: 0, want: actionDefault},
		{name: "the highest message falls through", msg: 0xFFFFFFFF, want: actionDefault},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := decideProcAction(tc.msg); got != tc.want {
				t.Errorf("decideProcAction(0x%04X) = %s, want %s", tc.msg, got, tc.want)
			}
		})
	}
}

// TestWmCloseIsNotTheDefaultAction guards the one mistake this package exists
// to prevent: if WM_CLOSE ever falls through to the library's procedure, it
// destroys the window, the message loop ends, and the program the user
// expected to stay in the tray exits.
func TestWmCloseIsNotTheDefaultAction(t *testing.T) {
	t.Parallel()

	if got := decideProcAction(wmClose); got == actionDefault {
		t.Fatal("WM_CLOSE reaches the default procedure, which would quit the program")
	}
}

// TestWmDPIChangedIsNotTheDefaultAction guards the same kind of mistake one
// message along. Left to the default procedure, WM_DPICHANGED does nothing:
// the window keeps its old size in physical pixels while the page inside it
// has already relaid itself out at the new devicePixelRatio, so the design
// ends up either cut off or swimming in dead space.
func TestWmDPIChangedIsNotTheDefaultAction(t *testing.T) {
	t.Parallel()

	if got := decideProcAction(wmDPIChanged); got != actionResize {
		t.Errorf("decideProcAction(WM_DPICHANGED) = %s, want %s", got, actionResize)
	}
}

func TestProcActionNamesItself(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		action procAction
		want   string
	}{
		{action: actionDefault, want: "default"},
		{action: actionHide, want: "hide"},
		{action: actionMinSize, want: "min-size"},
		{action: actionResize, want: "resize"},
		{action: procAction(99), want: "unknown"},
	} {
		if got := tc.action.String(); got != tc.want {
			t.Errorf("procAction(%d).String() = %q, want %q", tc.action, got, tc.want)
		}
	}
}
