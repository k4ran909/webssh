package gateway

import "github.com/k4ran909/webssh/internal/sshcore"

// The terminal protocol is defined in sshcore, which the in-browser client
// for end-to-end sessions shares.
type (
	clientMsg = sshcore.ClientMsg
	serverMsg = sshcore.ServerMsg
	hop       = sshcore.Hop
	authInfo  = sshcore.Auth
	gwError   = sshcore.Error
)

const (
	codeBadRequest      = sshcore.CodeBadRequest
	codeBusy            = sshcore.CodeBusy
	codeAuthFailed      = sshcore.CodeAuthFailed
	codeUnreachable     = sshcore.CodeUnreachable
	codeHostKeyRejected = sshcore.CodeHostKeyRejected
	codeHandshake       = sshcore.CodeHandshake
	codeSession         = sshcore.CodeSession
	codeResumeGone      = sshcore.CodeResumeGone
)

var (
	decodeMsg = sshcore.DecodeMsg
	validSize = sshcore.ValidSize
)
