package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/pinnnthreetriples/link-monitor/internal/adapters/sshx"
	"github.com/pinnnthreetriples/link-monitor/internal/adapters/syncfs"
	"github.com/pinnnthreetriples/link-monitor/internal/app"
	"github.com/pinnnthreetriples/link-monitor/internal/core/foldersync"
)

// The shared folder's own flags.
//
// They are registered here rather than in parseOptions so that this feature's
// wiring is one file: flag.StringVar in an init runs before flag.Parse, which
// parseOptions calls. Every description is Russian, like the rest of them.
//
// There is no default for -sync-folder, and that is the feature's off switch:
// rule 5 says the user picks the folder and nothing happens until they do.
var (
	syncFolder = flag.String("sync-folder", "",
		"общая папка на этой машине; без неё общая папка выключена")
	syncPeerFolder = flag.String("sync-peer-folder", "",
		`общая папка на второй машине; по умолчанию C:\Users\<пользователь>\LinkMonitor\shared`)
	syncInterval = flag.Duration("sync-interval", app.DefaultSyncInterval,
		"как часто сверять общие папки")
	syncMaxBytes = flag.Int64("sync-max-bytes", foldersync.DefaultMaxBytes,
		"наибольший размер файла, который переносится; больше — пропускается и называется в окне")
	syncExclude = flag.String("sync-exclude", "",
		"дополнительные шаблоны исключений, через запятую (например «*.iso,кэш»)")
	syncStatePath = flag.String("sync-state", "",
		`файл состояния синхронизации; по умолчанию в %LOCALAPPDATA%\LinkMonitor`)
)

// folderWiring is the shared folder's three pieces and the configuration that
// describes them. Every field is zero when no folder was chosen, which leaves
// the feature off rather than half-built.
type folderWiring struct {
	cfg   app.FolderConfig
	here  app.Tree
	peer  app.PeerTrees
	state app.SyncHistory
}

// wireFolder builds the shared folder over the SSH client the program already
// keeps to the peer. It contacts nothing: the first SFTP session is opened by
// the first pass.
//
// A folder that was not chosen, or a state file that has nowhere to live,
// leaves the feature switched off and says why in the log. Refusing to start
// the whole program over it would be the wrong trade: the monitor's job is to
// watch the link, and it can do that with no shared folder at all.
func wireFolder(o options, ssh *sshx.Lazy) folderWiring {
	root := strings.TrimSpace(*syncFolder)
	if root == "" {
		return folderWiring{}
	}
	statePath, err := folderStatePath()
	if err != nil {
		slog.Warn("the shared folder stays off: nowhere to keep its state", "err", err)
		return folderWiring{}
	}

	peerRoot := strings.TrimSpace(*syncPeerFolder)
	if peerRoot == "" {
		peerRoot = defaultPeerFolder(o.peer.User)
	}
	peer := syncfs.NewPeer(ssh, peerRoot, o.peer.TailnetName)

	return folderWiring{
		cfg: app.FolderConfig{
			Root:     filepath.Clean(root),
			PeerRoot: peerRoot,
			PeerName: o.peer.TailnetName,
			Interval: *syncInterval,
			Limits: foldersync.Limits{
				MaxBytes: *syncMaxBytes,
				Exclude:  splitPatterns(*syncExclude),
			},
		},
		here: syncfs.NewLocal(root, o.local.TailnetName),
		// The adapter returns its own concrete tree, so the function type is
		// what keeps internal/app from having to name it.
		peer: app.PeerTreeFunc(func(ctx context.Context) (app.Tree, io.Closer, error) {
			tree, closer, err := peer.Open(ctx)
			if err != nil {
				return nil, nil, fmt.Errorf("opening the shared folder on the peer: %w", err)
			}
			return tree, closer, nil
		}),
		state: syncfs.NewStore(statePath),
	}
}

// folderStatePath is where the sync state lives.
func folderStatePath() (string, error) {
	if chosen := strings.TrimSpace(*syncStatePath); chosen != "" {
		return chosen, nil
	}
	path, err := syncfs.DefaultStatePath()
	if err != nil {
		return "", fmt.Errorf("choosing where to keep the sync state: %w", err)
	}
	return path, nil
}

// defaultPeerFolder is the folder on the peer when the user named only this
// machine's. It sits under the directory this program's own install owns on
// that machine, so turning the feature on cannot point it at somebody's
// documents by accident.
func defaultPeerFolder(user string) string {
	return `C:\Users\` + user + `\LinkMonitor\shared`
}

// splitPatterns turns a comma-separated flag into an exclude list, dropping
// the empties a trailing comma leaves behind.
func splitPatterns(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
