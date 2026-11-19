//go:build !windows

package migrate

import "fmt"

type unsupportedEMMC struct{}

func platformEMMC(*nativeBackend) emmcPlatform { return unsupportedEMMC{} }
func (unsupportedEMMC) List() ([]EMMCDisk, error) {
	return nil, fmt.Errorf("native Armbian eMMC writing currently requires Windows; use an external image writer on this platform")
}
func (p unsupportedEMMC) Inspect(string) (EMMCDisk, error) {
	_, err := p.List()
	return EMMCDisk{}, err
}
func (p unsupportedEMMC) Offline(EMMCDisk) error { _, err := p.List(); return err }
func (p unsupportedEMMC) Open(EMMCDisk, bool) (emmcHandle, error) {
	_, err := p.List()
	return nil, err
}
