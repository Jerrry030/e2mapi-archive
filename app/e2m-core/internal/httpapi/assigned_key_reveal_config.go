package httpapi

// EnableAssignedKeyReveal opts a deployment into exporting plaintext managed
// upstream keys. It is intentionally disabled by default because export can
// bypass the controlled Connector path, trusted metering, and balance gates.
func (s *Server) EnableAssignedKeyReveal() { s.allowAssignedKeyReveal = true }
