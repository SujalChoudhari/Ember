//go:build !linux

package ember

func confinementAvailable(root string) bool { return false }
