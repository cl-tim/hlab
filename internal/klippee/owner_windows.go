package klippee

import "os"

// Windows access is controlled by the user's profile ACL, not POSIX mode bits.
func ownedByCurrentUser(os.FileInfo) bool { return true }
