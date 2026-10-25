// Package events holds the messages screens use to talk to the app shell.
package events

import (
	"dbdeck/internal/config"
	"dbdeck/internal/driver"
)

// ConnectedMsg is sent by the connections screen when a connection is open.
type ConnectedMsg struct {
	Driver  driver.Driver
	Profile config.Profile
}

// DisconnectMsg asks the shell to close the browser and return to the connection list.
type DisconnectMsg struct{}

// QuitMsg asks the shell to exit.
type QuitMsg struct{}
