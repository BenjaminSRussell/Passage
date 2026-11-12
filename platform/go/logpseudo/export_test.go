package logpseudo

import "log/slog"

// WrapsOnce reports whether h is a Handler whose next handler is not another Handler.
func WrapsOnce(h slog.Handler) bool {
	w, ok := h.(*Handler)
	if !ok {
		return false
	}
	_, twice := w.next.(*Handler)
	return !twice
}
