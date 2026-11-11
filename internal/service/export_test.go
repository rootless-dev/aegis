package service

import "time"

func (s *SessionService) SetClock(now func() time.Time) { s.now = now }
