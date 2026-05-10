package gateway

import (
	"encoding/json"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/permissions"
	"github.com/nextlevelbuilder/goclaw/pkg/protocol"
)

// NewTestClient returns a minimally-wired Client for unit tests in other
// packages. Role is set directly because the underlying fields are unexported.
// SendResponse is safe because the send channel is nil — the writer hits the
// default branch of the select and drops the frame silently.
//
// Not for production use. Any non-test caller should use NewClient instead.
func NewTestClient(role permissions.Role, userID string) *Client {
	return &Client{
		id:            uuid.NewString(),
		authenticated: true,
		role:          role,
		userID:        userID,
	}
}

// NewTestClientWithCapture returns a Client whose responses can be read back
// via the returned function. Each call to the function returns the next
// response frame; nil if none queued. Buffered to 16 frames — handlers that
// emit more in a single test will overflow and surface that as a stuck send.
func NewTestClientWithCapture(role permissions.Role, userID string) (*Client, func() *protocol.ResponseFrame) {
	c := &Client{
		id:            uuid.NewString(),
		authenticated: true,
		role:          role,
		userID:        userID,
		send:          make(chan []byte, 16),
	}
	read := func() *protocol.ResponseFrame {
		select {
		case raw := <-c.send:
			var resp protocol.ResponseFrame
			if err := json.Unmarshal(raw, &resp); err != nil {
				return nil
			}
			return &resp
		default:
			return nil
		}
	}
	return c, read
}
