package v1alpha1

import "time"

const (
	// stopPollEach is how often Stop checks whether the child has exited.
	stopPollEach = 100 * time.Millisecond

	// defaultGroupID is the cobra group ID for the lifecycle subcommands;
	// defaultGroupName is its title (a ":" is appended on render).
	defaultGroupID   = "daemonize"
	defaultGroupName = "Daemon Commands"
)
