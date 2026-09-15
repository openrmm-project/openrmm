package main

// shortcutKeys keeps press/release ordering atomic on the platform input worker.
func shortcutKeys(codes []int, send func(code int, down bool)) {
	for _, code := range codes {
		send(code, true)
	}
	for i := len(codes) - 1; i >= 0; i-- {
		send(codes[i], false)
	}
}
