package permissions

// shareLevel maps share role names to numeric precedence for comparison.
// Share roles use a different vocabulary from platform roles (admin/member/viewer)
// because they describe access on a specific asset, not tenant-level authority.
//
// Mapping onto resolver layer decisions:
//   viewer  → read-only
//   member  → read + write (required for write_file / edit_file / delete_file / cron)
//   editor  → full file CRUD (superset of member for file ops; same threshold here)
//   owner   → full control; treated as admin bypass in resolver
var shareLevel = map[string]int{
	ShareNone:   0,
	ShareViewer: 1,
	ShareMember: 2,
	ShareEditor: 3,
	ShareOwner:  4,
}

// requiredLevel returns the minimum numeric role level for an action.
// read → viewer (1); write/edit/delete/cron → member (2); admin → owner (4).
func requiredLevel(action Action) int {
	switch action {
	case ActionRead:
		return shareLevel[ShareViewer]
	case ActionWriteFile, ActionEditFile, ActionDeleteFile, ActionCron:
		return shareLevel[ShareMember]
	case ActionAdmin:
		return shareLevel[ShareOwner]
	default:
		return shareLevel[ShareOwner] // unknown action → highest bar
	}
}

// roleAtLeast reports whether role r meets or exceeds the minimum level for action.
func roleAtLeast(role string, action Action) bool {
	lvl, ok := shareLevel[role]
	if !ok {
		return false
	}
	return lvl >= requiredLevel(action)
}

// minShareRole returns the lower of two share roles (by numeric precedence).
// When either is empty (no grant found), returns ShareNone.
func minShareRole(a, b string) string {
	la, oka := shareLevel[a]
	lb, okb := shareLevel[b]
	if !oka || !okb {
		return ShareNone
	}
	if la <= lb {
		return a
	}
	return b
}

// isAdminShare reports whether a share role carries admin-level authority
// (owner or above — used for admin bypass in resolver).
func isAdminShare(role string) bool {
	return shareLevel[role] >= shareLevel[ShareOwner]
}
