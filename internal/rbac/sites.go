package rbac

import "mikrodash/internal/db"

// CanPageOnSites answers CanPage for something that belongs to SITES rather
// than to a router: a Power/UPS unit (internal/power), which sits at a site and
// has no router of its own.
//
// A global grant applies, and so does a grant on ANY of the sites, the same
// union a router in several sites gets (see `Router.SiteIDs`). A router-scoped
// grant never does: it names one router, and a unit is not that router. With no
// sites only a global grant can apply.
//
// The same refusals as CanPage, in the same order: no resolver, no user, an
// unknown page and an unknown access level all answer no.
func (r *Resolver) CanPageOnSites(userID, page, access string, siteIDs []string) (bool, error) {
	if !r.Available() || userID == "" || !r.pages[page] {
		return false, nil
	}
	need := accessRank[access]
	if need == 0 {
		return false, nil
	}
	grants, err := r.db.GrantsForUser(userID)
	if err != nil {
		return false, err
	}
	sets := []string{}
	for _, g := range grants {
		switch g.ScopeType {
		case "global":
			sets = append(sets, g.RoleID)
		case "site":
			for _, sid := range siteIDs {
				if sid != "" && g.ScopeID == sid {
					sets = append(sets, g.RoleID)
					break
				}
			}
		}
	}
	return r.setsConfer(sets, page, need, map[string]*db.Role{})
}
