//go:build !darwin && !windows && !linux

package platform

func inhibitSleep(_ string) (func(), error) {
	return func() {}, nil
}
