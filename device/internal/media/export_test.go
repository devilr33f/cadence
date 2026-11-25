package media

import "time"

// SetCommandTimeout shortens how long a command waits for audiod's answer.
func SetCommandTimeout(m *Media, d time.Duration) { m.commandTimeout = d }
