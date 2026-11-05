package common

import (
	"net/netip"
	"slices"

	"github.com/sagernet/sing/common/control"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/x/list"
)

var errInterfaceExcluded = E.New("network interface is excluded")

// ExcludingInterfaceFinder hides named interfaces from an InterfaceFinder.
//
// UDP NAT used to take an explicit excluded-interface list; from sing-tun v0.9
// it discovers its egress interfaces through the finder alone. Endpoints that
// must not send tunnel traffic out of their own interface wrap the finder in
// this adapter and keep their egress table limited to the other interfaces.
type ExcludingInterfaceFinder struct {
	finder  control.InterfaceFinder
	exclude []string
}

func NewExcludingInterfaceFinder(finder control.InterfaceFinder, exclude []string) control.InterfaceFinder {
	if len(exclude) == 0 {
		return finder
	}
	return &ExcludingInterfaceFinder{finder: finder, exclude: exclude}
}

func (f *ExcludingInterfaceFinder) isExcluded(name string) bool {
	return slices.Contains(f.exclude, name)
}

func (f *ExcludingInterfaceFinder) filtered(interfaces []control.Interface) []control.Interface {
	if len(interfaces) == 0 {
		return interfaces
	}
	result := make([]control.Interface, 0, len(interfaces))
	for _, networkInterface := range interfaces {
		if f.isExcluded(networkInterface.Name) {
			continue
		}
		result = append(result, networkInterface)
	}
	return result
}

func (f *ExcludingInterfaceFinder) Update() error {
	return f.finder.Update()
}

func (f *ExcludingInterfaceFinder) Interfaces() []control.Interface {
	return f.filtered(f.finder.Interfaces())
}

func (f *ExcludingInterfaceFinder) ByName(name string) (*control.Interface, error) {
	if f.isExcluded(name) {
		return nil, errInterfaceExcluded
	}
	return f.finder.ByName(name)
}

func (f *ExcludingInterfaceFinder) ByIndex(index int) (*control.Interface, error) {
	networkInterface, err := f.finder.ByIndex(index)
	if err != nil {
		return nil, err
	}
	if f.isExcluded(networkInterface.Name) {
		return nil, errInterfaceExcluded
	}
	return networkInterface, nil
}

func (f *ExcludingInterfaceFinder) ByAddr(addr netip.Addr) (*control.Interface, error) {
	networkInterface, err := f.finder.ByAddr(addr)
	if err != nil {
		return nil, err
	}
	if f.isExcluded(networkInterface.Name) {
		return nil, errInterfaceExcluded
	}
	return networkInterface, nil
}

func (f *ExcludingInterfaceFinder) RegisterInterfaceUpdateCallback(callback control.InterfaceUpdateCallback) *list.Element[control.InterfaceUpdateCallback] {
	return f.finder.RegisterInterfaceUpdateCallback(func(interfaces []control.Interface) {
		callback(f.filtered(interfaces))
	})
}

func (f *ExcludingInterfaceFinder) UnregisterInterfaceUpdateCallback(element *list.Element[control.InterfaceUpdateCallback]) {
	f.finder.UnregisterInterfaceUpdateCallback(element)
}
