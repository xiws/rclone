// Test WoCloud filesystem interface
package wocloud_test

import (
	"testing"

	"github.com/rclone/rclone/backend/wocloud"
	"github.com/rclone/rclone/fstest/fstests"
)

// TestIntegration runs integration tests against the remote
func TestIntegration(t *testing.T) {
	fstests.Run(t, &fstests.Opt{
		RemoteName: "TestWoCloud:",
		NilObject:  (*wocloud.Object)(nil),
	})
}
