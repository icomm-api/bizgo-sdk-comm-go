package bizgo

// tr returns the transport of a service, or nil for a service of a zero Client (not made with
// NewClient); call then fails with a ConfigurationError instead of panicking.

func (s *SendService) tr() *transport {
	if s == nil {
		return nil
	}
	return s.t
}

func (s *FilesService) tr() *transport {
	if s == nil {
		return nil
	}
	return s.t
}

func (s *ReportsService) tr() *transport {
	if s == nil {
		return nil
	}
	return s.t
}

func (s *MessagesService) tr() *transport {
	if s == nil {
		return nil
	}
	return s.t
}

// nonNil returns an empty slice instead of nil (SDK-DESIGN.md §12.19).
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// errZeroClient is returned by the methods of a Client that was not made with NewClient.
var errZeroClient = &ConfigurationError{msg: "Client가 초기화되지 않았습니다. bizgo.NewClient로 만드세요"}
