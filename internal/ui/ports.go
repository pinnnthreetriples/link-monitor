// Package ui is the tray shell: the icon whose colour tells the user whether
// the link is up, the menu behind it, and the Windows notification that fires
// when the link goes bad while the window is closed.
//
// «Открыть» raises the program's own window — see internal/ui/window — and
// only reaches for a browser when there is no window to raise, which means a
// machine without the Edge WebView2 runtime or a deliberate `-browser`.
//
// It knows nothing about polling or probing. Everything it needs from the rest
// of the program arrives through the interfaces declared here, and
// cmd/linkmon adapts the app layer to them.
package ui

import (
	"context"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// Status is the slice of a core.Snapshot the tray actually draws: a state for
// the colour, and the two Russian lines for the tooltip and the notification.
type Status struct {
	Overall core.State
	// Summary is the headline, e.g. "Связь установлена".
	Summary string
	// Detail is the sentence under it, naming the cause when there is one.
	Detail string
}

// StatusOf narrows a snapshot to what the tray needs. It exists so the wiring
// in cmd/linkmon is one call rather than three field copies.
func StatusOf(s core.Snapshot) Status {
	return Status{Overall: s.Overall, Summary: s.Summary, Detail: s.Detail}
}

// Source is the tray's view of the app layer's status fan-out.
//
// Subscribe must return a channel that carries every status the subscriber
// should see and that stops on its own when ctx is done — either closed or
// simply left alone; the tray handles both. A slow tray must never wedge the
// poller, so the implementation is expected to buffer or drop, not block.
type Source interface {
	Subscribe(ctx context.Context) <-chan Status
}

// SourceFunc adapts a plain function to [Source].
type SourceFunc func(ctx context.Context) <-chan Status

// Subscribe implements [Source].
func (f SourceFunc) Subscribe(ctx context.Context) <-chan Status { return f(ctx) }

// Actions are the things the menu asks the rest of the program to do.
type Actions interface {
	// CheckNow triggers an immediate probe. It should return as soon as the
	// probe is scheduled rather than waiting for its result: the menu blocks
	// while it runs.
	CheckNow(ctx context.Context) error
}

// ActionsFunc adapts a plain function to [Actions].
type ActionsFunc func(ctx context.Context) error

// CheckNow implements [Actions].
func (f ActionsFunc) CheckNow(ctx context.Context) error { return f(ctx) }

// Opener hands a URL to whatever the user reads URLs with. It is an interface
// so tests can watch what would have been opened without a browser appearing.
type Opener interface {
	Open(ctx context.Context, rawURL string) error
}

// Windower is the program's own window, as the tray uses it: something that
// can be brought to the front.
//
// It is one method because that is all the tray ever wants of it. Building the
// window, pumping its message loop and tearing it down are cmd/linkmon's job;
// the tray only ever asks for it to be shown. Being an interface is also what
// lets the tray be tested — and run at all — on a machine that has no window.
type Windower interface {
	Show()
}

// WindowerFunc adapts a plain function to [Windower].
type WindowerFunc func()

// Show implements [Windower].
func (f WindowerFunc) Show() { f() }

// ShellMenu is Explorer's right-click item, as the tray switches it on and
// off. internal/adapters/shellmenu implements it.
//
// No method takes a context, unlike the rest of this file. The three of them
// are registry writes under HKCU — a local hive, no network, no lock to wait
// on, microseconds each — so a context here would be a cancellation nothing
// could honour, offered to a caller who would then believe in it.
//
// Installed is asked rather than remembered. The registry is the state, the
// user can edit it behind the program's back, and two installs of this program
// would each hold their own opinion; a tick mark that disagrees with Explorer
// is worse than no tick mark at all.
type ShellMenu interface {
	// Installed reports whether the item is in Explorer's menu now.
	Installed() (bool, error)
	// Install puts it there. Installing over an existing item is a repair, not
	// an error.
	Install() error
	// Remove takes it out, leaving no key behind.
	Remove() error
}

// Notification is one desktop notification. Both fields are shown to the user
// and are therefore in Russian.
type Notification struct {
	Title string
	Body  string
}

// Notifier shows a desktop notification. It is an interface so tests can
// assert when the tray decides to notify without anything appearing on screen.
type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}

// NotifierFunc adapts a plain function to [Notifier].
type NotifierFunc func(ctx context.Context, n Notification) error

// Notify implements [Notifier].
func (f NotifierFunc) Notify(ctx context.Context, n Notification) error { return f(ctx, n) }
