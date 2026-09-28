package core

import "errors"

// MaxUploadBytes caps one browser-originated file before peer transport.
const MaxUploadBytes int64 = 100 << 20

// ErrUploadTooLarge means the bytes were refused before being sent.
var ErrUploadTooLarge = errors.New("upload exceeds 100 MB")
