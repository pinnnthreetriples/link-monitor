//go:build windows

package clipboard

import (
	"fmt"
	"sync"

	"github.com/pinnnthreetriples/link-monitor/internal/core/clipshare"
)

const cfDIB = 8

var pngFormat = sync.OnceValues(func() (uint32, error) { return registerFormat("PNG") })

func imagePNGAvailable() bool {
	id, err := pngFormat()
	return err == nil && formatAvailable(id)
}

// readImage copies only bounded data and gives the watcher canonical PNG bytes.
func readImage() (clipshare.Snapshot, error) {
	if imagePNGAvailable() {
		id, _ := pngFormat() // availability proves registration succeeded.
		data, size, err := readImageBlock(id, clipshare.MaxImageBytes)
		if err != nil || size > clipshare.MaxImageBytes {
			return imageTooBig(size), err
		}
		defer clipshare.Zero(data)
		canonical, err := clipshare.NormalizePNG(data)
		if err != nil {
			return clipshare.Snapshot{}, fmt.Errorf("decoding clipboard PNG: %w", err)
		}
		return imageSnapshot(canonical), nil
	}
	// A decoded NRGBA can take 64 MiB at the pixel limit. DIB adds a small header.
	data, size, err := readImageBlock(cfDIB, 4*clipshare.MaxImagePixels+124)
	if err != nil || size > 4*clipshare.MaxImagePixels+124 {
		return imageTooBig(size), err
	}
	defer clipshare.Zero(data)
	im, err := decodeDIB(data)
	if err != nil {
		return clipshare.Snapshot{}, err
	}
	defer clipshare.Zero(im.Pix)
	canonical, err := clipshare.EncodePNG(im)
	if err != nil {
		return clipshare.Snapshot{}, err
	}
	return imageSnapshot(canonical), nil
}

func readImageBlock(id uint32, limit int) ([]byte, int, error) {
	h, err := clipboardData(id)
	if err != nil {
		return nil, 0, err
	}
	size, err := globalSize(h)
	if err != nil {
		return nil, 0, err
	}
	if size > uintptr(limit) {
		return nil, limit + 1, nil
	}
	addr, err := globalLock(h)
	if err != nil {
		return nil, 0, err
	}
	defer globalUnlock(h)
	data := make([]byte, int(size))
	copyOutBytes(data, addr)
	return data, int(size), nil
}

func imageTooBig(size int) clipshare.Snapshot {
	return clipshare.Snapshot{Format: "png", Recordable: true, Bytes: size}
}

func imageSnapshot(data []byte) clipshare.Snapshot {
	return clipshare.Snapshot{Format: "png", Recordable: true, Bytes: len(data), Text: data}
}

// PutImage makes an incoming screenshot pasteable both by modern PNG-aware apps
// and by software that reads the standard CF_DIB format.
func (c *Clipboard) PutImage(png []byte) error {
	im, err := clipshare.DecodePNG(png)
	if err != nil {
		return err
	}
	defer clipshare.Zero(im.Pix)
	dib := encodeDIB(im)
	defer clipshare.Zero(dib)
	id, err := pngFormat()
	if err != nil {
		return err
	}
	if err := openClipboard(); err != nil {
		return err
	}
	defer closeClipboard()
	if ok, callErr := call(procEmptyClipboard); ok == 0 {
		return fmt.Errorf("emptying clipboard for image: %w", callErr)
	}
	if err := handOverBytes(cfDIB, dib); err != nil {
		return err
	}
	if err := handOverBytes(id, png); err != nil {
		return err
	}
	return nil
}

func handOverBytes(id uint32, data []byte) error {
	block, err := globalAlloc(uintptr(len(data)))
	if err != nil {
		return err
	}
	addr, err := globalLock(block)
	if err != nil {
		globalFree(block)
		return err
	}
	copyIntoBytes(addr, data)
	globalUnlock(block)
	if ok, callErr := call(procSetClipboardData, uintptr(id), block); ok == 0 {
		globalFree(block)
		return fmt.Errorf("putting image on clipboard: %w", callErr)
	}
	return nil
}
