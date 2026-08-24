package goroutineid

import "runtime"

// Current returns the runtime goroutine identifier used only to prevent
// lifecycle self-deadlocks. It is not a stable application identity.
func Current() uint64 {
	var buffer [64]byte
	length := runtime.Stack(buffer[:], false)
	const prefix = "goroutine "
	if length <= len(prefix) {
		return 0
	}
	var id uint64
	for index := len(prefix); index < length; index++ {
		value := buffer[index]
		if value < '0' || value > '9' {
			break
		}
		id = id*10 + uint64(value-'0')
	}
	return id
}
