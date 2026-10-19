package hub

// NewInviteCode returns a short, human-transcribable one-time invite code.
func NewInviteCode() (string, error) {
	return newInviteCode()
}
