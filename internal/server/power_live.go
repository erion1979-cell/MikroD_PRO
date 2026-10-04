package server

// The Power/UPS page's live updates: `power:state` for one unit after each of
// its polls, to every socket with the page open whose user may read that unit's
// site. Asked per send rather than at focus, so a grant revoked or a unit moved
// to another site takes effect on the next poll with nothing to invalidate -
// the rule alert delivery follows.

import "mikrodash/internal/power"

// powerWatch adds or removes a socket from the page's viewers.
func (s *Server) powerWatch(cn *conn, on bool) {
	s.power.mu.Lock()
	defer s.power.mu.Unlock()
	if !on {
		delete(s.power.watchers, cn)
		return
	}
	if s.power.watchers == nil {
		s.power.watchers = map[*conn]bool{}
	}
	s.power.watchers[cn] = true
}

// powerPush is the pollers' State hook.
func (s *Server) powerPush(st power.State) {
	s.power.mu.Lock()
	site, known := s.power.siteOf[st.UnitID]
	viewers := make([]*conn, 0, len(s.power.watchers))
	for cn := range s.power.watchers {
		viewers = append(viewers, cn)
	}
	s.power.mu.Unlock()
	if !known || len(viewers) == 0 {
		return
	}
	view := powerStateView(st)
	for _, cn := range viewers {
		if s.powerMay(cn.scope().sess, "read", site) {
			EvPowerState.Send(s.hub, cn.c, view)
		}
	}
}
