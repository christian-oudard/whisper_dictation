// Package wayland speaks just enough of the Wayland wire protocol to ask a
// compositor what it supports, and to hand it a string to insert.
//
// Which mechanism can put text into a window is a property of the compositor,
// and every compositor answers differently: the virtual keyboard wtype needs
// and the input method that would replace it are both wlroots protocols, KWin
// implements an older input method instead, and GNOME implements neither. The
// registry is where a compositor says which of those it has, so asking it is
// the difference between knowing and inferring from a desktop name.
//
// Only the registry and the input method are implemented, and neither needs
// object lifetimes, surfaces or shared memory. Anything that did would want
// libwayland rather than this.
package wayland

import "errors"

// Global is one interface the compositor offers. Name is the compositor's
// numeric handle for it, which is what bind takes.
type Global struct {
	Name      uint32
	Interface string
	Version   uint32
}

// Object ids. 1 is wl_display, which the protocol fixes; the rest are ours to
// name, and every exchange here uses a fixed sequence of them rather than
// allocating.
const (
	displayID = iota + 1
	registryID
	firstFreeID
)

// wl_display requests.
const (
	displaySync = 0
	displayGetRegistry
)

// wl_registry.bind, and the events both wl_display and wl_registry send as
// their first opcode: an error, and a global.
const (
	registryBind = 0

	displayErrorEvent   = 0
	registryGlobalEvent = 0
	callbackDoneEvent   = 0
)

// Globals lists what the compositor at display advertises.
func Globals(display string) ([]Global, error) {
	c, err := dial(display)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	globals, err := c.globals(firstFreeID)
	if err != nil {
		return nil, err
	}
	return globals, nil
}

// globals asks for the registry and reads it to the end. sync is the id to
// spend on the callback that marks that end, since the globals themselves
// have no terminator.
func (c *conn) globals(sync uint32) ([]Global, error) {
	if err := c.send(displayID, displayGetRegistry, args{}.uint(registryID)); err != nil {
		return nil, err
	}
	if err := c.send(displayID, displaySync, args{}.uint(sync)); err != nil {
		return nil, err
	}

	var globals []Global
	for {
		m, err := c.next()
		if err != nil {
			return nil, err
		}
		switch {
		case m.object == displayID && m.opcode == displayErrorEvent:
			return nil, protocolError(m.body)
		case m.object == registryID && m.opcode == registryGlobalEvent:
			g, err := decodeGlobal(m.body)
			if err != nil {
				return nil, err
			}
			globals = append(globals, g)
		case m.object == sync && m.opcode == callbackDoneEvent:
			return globals, nil
		}
		// Everything else, wl_display.delete_id above all, is bookkeeping for
		// objects this never creates.
	}
}

// decodeGlobal reads wl_registry.global: name, interface, version.
func decodeGlobal(body []byte) (Global, error) {
	if len(body) < 4 {
		return Global{}, errors.New("wayland: global without a name")
	}
	name, rest, err := takeString(body[4:])
	if err != nil {
		return Global{}, err
	}
	if len(rest) < 4 {
		return Global{}, errors.New("wayland: global without a version")
	}
	return Global{Name: order.Uint32(body), Interface: name, Version: order.Uint32(rest)}, nil
}

// bind claims a global as object id, at the version given. The new_id
// argument of wl_registry.bind names no interface in the protocol, so unlike
// every other new_id it is sent as the interface name and version before the
// id itself.
func (c *conn) bind(g Global, version, id uint32) error {
	return c.send(registryID, registryBind,
		args{}.uint(g.Name).str(g.Interface).uint(version).uint(id))
}

// find returns the named global, and whether the compositor has it.
func find(globals []Global, name string) (Global, bool) {
	for _, g := range globals {
		if g.Interface == name {
			return g, true
		}
	}
	return Global{}, false
}

// Has reports whether the list carries an interface by name.
func Has(globals []Global, name string) bool {
	_, ok := find(globals, name)
	return ok
}
