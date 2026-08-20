package store

import (
	"database/sql"
	"time"
)

// Subscriber groups. A group says once what a class of subscriber gets, and a
// subscriber belongs to at most one — see 0029_subscriber_groups.sql for why
// membership is single rather than a set.
//
// Everything here is about ONE question: given a subscriber, what do they
// actually hold? The answer is resolved in three layers — their own row, then
// their group's, then the default — and the functions the rest of the panel
// already calls (UserProfileIDs, UserNodeDenies, and their kind) return the
// resolved answer. That is deliberate: subscription assembly, credential
// minting and the console must not each be able to resolve it differently.

type SubscriberGroup struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Note      string `json:"note"`
	RulesetID string `json:"ruleset_id"`
	Members   int    `json:"members"`
	CreatedAt int64  `json:"created_at"`
}

const groupCols = `id, name, note, COALESCE(ruleset_id, ''), created_at`

// CreateSubscriberGroup adds a group that grants nothing and withholds
// everything that exists.
//
// The denials are the point. A group created open would hand every current
// node to everyone put into it, which is the failure this feature exists to
// prevent — the operator's next action is "put people in it", not "audit what
// it already implies".
func (s *Store) CreateSubscriberGroup(name, note string) (SubscriberGroup, error) {
	g := SubscriberGroup{ID: NewID(), Name: name, Note: note, CreatedAt: time.Now().Unix()}
	tx, err := s.db.Begin()
	if err != nil {
		return SubscriberGroup{}, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO subscriber_groups (id, name, note, created_at) VALUES (?, ?, ?, ?)`,
		g.ID, g.Name, g.Note, g.CreatedAt); err != nil {
		return SubscriberGroup{}, err
	}
	for _, q := range []string{
		`INSERT INTO group_node_denies (group_id, node_id) SELECT ?, id FROM nodes`,
		`INSERT INTO group_relay_denies (group_id, relay_id) SELECT ?, id FROM node_relays`,
		`INSERT INTO group_external_denies (group_id, proxy_id) SELECT ?, id FROM external_proxies`,
	} {
		if _, err := tx.Exec(q, g.ID); err != nil {
			return SubscriberGroup{}, err
		}
	}
	return g, tx.Commit()
}

func (s *Store) ListSubscriberGroups() ([]SubscriberGroup, error) {
	rows, err := s.db.Query(`SELECT ` + groupCols + `,
		(SELECT COUNT(*) FROM users WHERE users.group_id = subscriber_groups.id)
		FROM subscriber_groups ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SubscriberGroup
	for rows.Next() {
		var g SubscriberGroup
		if err := rows.Scan(&g.ID, &g.Name, &g.Note, &g.RulesetID, &g.CreatedAt, &g.Members); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) GetSubscriberGroup(id string) (SubscriberGroup, error) {
	var g SubscriberGroup
	err := s.db.QueryRow(`SELECT `+groupCols+`,
		(SELECT COUNT(*) FROM users WHERE users.group_id = subscriber_groups.id)
		FROM subscriber_groups WHERE id = ?`, id).
		Scan(&g.ID, &g.Name, &g.Note, &g.RulesetID, &g.CreatedAt, &g.Members)
	return g, err
}

func (s *Store) UpdateSubscriberGroup(id, name, note string) error {
	res, err := s.db.Exec(`UPDATE subscriber_groups SET name = ?, note = ? WHERE id = ?`, name, note, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// SetGroupRuleset points a group at a ruleset, or at none when id is empty.
func (s *Store) SetGroupRuleset(id, rulesetID string) error {
	res, err := s.db.Exec(`UPDATE subscriber_groups SET ruleset_id = ? WHERE id = ?`,
		nullIfEmpty(rulesetID), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteSubscriberGroup removes a group, writing what it decided into each
// member's own rows first.
//
// Without that step, deleting a group would hand every member everything:
// their personal tables hold only exceptions after a join, and a subscriber
// with no denials is a subscriber who may use the whole fleet. A deletion is
// meant to dissolve the group, not to publish it.
func (s *Store) DeleteSubscriberGroup(id string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	members, err := s.idListTx(tx, `SELECT id FROM users WHERE group_id = ?`, id)
	if err != nil {
		return err
	}
	for _, userID := range members {
		if err := detachUser(tx, userID, id); err != nil {
			return err
		}
	}
	res, err := tx.Exec(`DELETE FROM subscriber_groups WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

// detachUser copies a group's decisions into one member's own rows, so that
// leaving the group changes nothing about what they hold. Their exceptions are
// folded in on the way — an allow against a group denial simply becomes the
// absence of a denial — and then dropped, because an exception to a group they
// are no longer in has nothing to be an exception to.
func detachUser(tx *sql.Tx, userID, groupID string) error {
	if _, err := tx.Exec(`INSERT OR IGNORE INTO user_profiles (user_id, profile_id)
		SELECT ?, gp.profile_id FROM group_profiles gp
		WHERE gp.group_id = ? AND gp.profile_id NOT IN (
			SELECT profile_id FROM user_profile_denies WHERE user_id = ?)`,
		userID, groupID, userID); err != nil {
		return err
	}
	for _, kind := range []string{"node", "relay", "external"} {
		col, denyTable, allowTable, groupTable, _, _ := accessTables(kind)
		if _, err := tx.Exec(`INSERT OR IGNORE INTO `+denyTable+` (user_id, `+col+`)
			SELECT ?, g.`+col+` FROM `+groupTable+` g
			WHERE g.group_id = ? AND g.`+col+` NOT IN (
				SELECT `+col+` FROM `+allowTable+` WHERE user_id = ?)`,
			userID, groupID, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM `+allowTable+` WHERE user_id = ?`, userID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM user_profile_denies WHERE user_id = ?`, userID); err != nil {
		return err
	}
	// The group's rule set, if the member had not chosen one of their own.
	_, err := tx.Exec(`UPDATE users SET ruleset_id = COALESCE(ruleset_id,
			(SELECT ruleset_id FROM subscriber_groups WHERE id = ?))
		WHERE id = ? AND ruleset_none = 0`, groupID, userID)
	return err
}

// SetUserGroup moves a subscriber into a group (or out of one, with an empty
// id) and DISCARDS their personal rows.
//
// Every (subscriber, node) pair carries a materialised row today — a node is
// denied to everyone the moment it is created — so a join that kept those rows
// would leave the member entirely covered by exceptions, and their group would
// decide nothing at all. Clearing them is what makes membership mean anything.
// It is also the destructive half of this feature, so the console states what
// will change before calling it.
func (s *Store) SetUserGroup(userID, groupID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var previous string
	if err := tx.QueryRow(`SELECT COALESCE(group_id, '') FROM users WHERE id = ?`, userID).
		Scan(&previous); err != nil {
		return err
	}
	// Leaving one keeps what it decided; joining one discards what the
	// subscriber had. Both directions preserve the safe reading: nobody gains
	// access because an operator moved them.
	if previous != "" && groupID == "" {
		if err := detachUser(tx, userID, previous); err != nil {
			return err
		}
	}
	res, err := tx.Exec(`UPDATE users SET group_id = ?, updated_at = ? WHERE id = ?`,
		nullIfEmpty(groupID), time.Now().Unix(), userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	if groupID != "" {
		for _, q := range []string{
			`DELETE FROM user_profiles WHERE user_id = ?`,
			`DELETE FROM user_profile_denies WHERE user_id = ?`,
			`DELETE FROM user_node_denies WHERE user_id = ?`,
			`DELETE FROM user_node_allows WHERE user_id = ?`,
			`DELETE FROM user_relay_denies WHERE user_id = ?`,
			`DELETE FROM user_relay_allows WHERE user_id = ?`,
			`DELETE FROM user_external_denies WHERE user_id = ?`,
			`DELETE FROM user_external_allows WHERE user_id = ?`,
		} {
			if _, err := tx.Exec(q, userID); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// UserGroupID returns the group this subscriber belongs to, empty if none.
func (s *Store) UserGroupID(userID string) (string, error) {
	var id string
	err := s.db.QueryRow(`SELECT COALESCE(group_id, '') FROM users WHERE id = ?`, userID).Scan(&id)
	return id, err
}

// GroupMemberIDs lists the subscribers in a group.
func (s *Store) GroupMemberIDs(groupID string) ([]string, error) {
	return s.idList(`SELECT id FROM users WHERE group_id = ? ORDER BY id`, groupID)
}

// --- what a group grants and withholds ---

func (s *Store) GroupProfileIDs(groupID string) ([]string, error) {
	return s.idList(`SELECT profile_id FROM group_profiles WHERE group_id = ? ORDER BY profile_id`, groupID)
}

func (s *Store) BindGroupProfile(groupID, profileID string) error {
	_, err := s.db.Exec(
		`INSERT INTO group_profiles (group_id, profile_id) VALUES (?, ?) ON CONFLICT DO NOTHING`,
		groupID, profileID)
	return err
}

func (s *Store) UnbindGroupProfile(groupID, profileID string) error {
	_, err := s.db.Exec(`DELETE FROM group_profiles WHERE group_id = ? AND profile_id = ?`,
		groupID, profileID)
	return err
}

func (s *Store) GroupNodeDenies(groupID string) (map[string]struct{}, error) {
	return s.denySet(`SELECT node_id FROM group_node_denies WHERE group_id = ?`, groupID)
}

func (s *Store) GroupRelayDenies(groupID string) (map[string]struct{}, error) {
	return s.denySet(`SELECT relay_id FROM group_relay_denies WHERE group_id = ?`, groupID)
}

func (s *Store) GroupExternalDenies(groupID string) (map[string]struct{}, error) {
	return s.denySet(`SELECT proxy_id FROM group_external_denies WHERE group_id = ?`, groupID)
}

// SetGroupNodeAccess replaces a group's denials in one go, for the same reason
// SetUserNodeAccess does: the console shows the whole list and an operator
// ticking boxes is describing an end state.
func (s *Store) SetGroupNodeAccess(groupID string, deniedNodes, deniedProxies, deniedRelays []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM group_node_denies WHERE group_id = ?`,
		`DELETE FROM group_external_denies WHERE group_id = ?`,
		`DELETE FROM group_relay_denies WHERE group_id = ?`,
	} {
		if _, err := tx.Exec(q, groupID); err != nil {
			return err
		}
	}
	for _, r := range []struct {
		query string
		ids   []string
	}{
		{`INSERT OR IGNORE INTO group_node_denies (group_id, node_id) VALUES (?, ?)`, deniedNodes},
		{`INSERT OR IGNORE INTO group_external_denies (group_id, proxy_id) VALUES (?, ?)`, deniedProxies},
		{`INSERT OR IGNORE INTO group_relay_denies (group_id, relay_id) VALUES (?, ?)`, deniedRelays},
	} {
		for _, id := range r.ids {
			if _, err := tx.Exec(r.query, groupID, id); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// --- a subscriber's own rows, unresolved ---

// UserOwnAccess is what the subscriber's own tables say, with no group applied.
// The console needs it to show WHY a node is or is not theirs; nothing that
// decides access should read it.
type UserOwnAccess struct {
	GroupID        string
	Profiles       map[string]struct{}
	ProfileDenies  map[string]struct{}
	NodeDenies     map[string]struct{}
	NodeAllows     map[string]struct{}
	RelayDenies    map[string]struct{}
	RelayAllows    map[string]struct{}
	ExternalDenies map[string]struct{}
	ExternalAllows map[string]struct{}
}

func (s *Store) UserOwnAccess(userID string) (UserOwnAccess, error) {
	out := UserOwnAccess{}
	var err error
	if out.GroupID, err = s.UserGroupID(userID); err != nil {
		return out, err
	}
	for _, r := range []struct {
		query string
		into  *map[string]struct{}
	}{
		{`SELECT profile_id FROM user_profiles WHERE user_id = ?`, &out.Profiles},
		{`SELECT profile_id FROM user_profile_denies WHERE user_id = ?`, &out.ProfileDenies},
		{`SELECT node_id FROM user_node_denies WHERE user_id = ?`, &out.NodeDenies},
		{`SELECT node_id FROM user_node_allows WHERE user_id = ?`, &out.NodeAllows},
		{`SELECT relay_id FROM user_relay_denies WHERE user_id = ?`, &out.RelayDenies},
		{`SELECT relay_id FROM user_relay_allows WHERE user_id = ?`, &out.RelayAllows},
		{`SELECT proxy_id FROM user_external_denies WHERE user_id = ?`, &out.ExternalDenies},
		{`SELECT proxy_id FROM user_external_allows WHERE user_id = ?`, &out.ExternalAllows},
	} {
		set, err := s.denySet(r.query, userID)
		if err != nil {
			return out, err
		}
		*r.into = set
	}
	return out, nil
}

// SetUserObjectAccess records one subscriber's exception for one object, or
// clears it. allow=true grants against a group that withholds; allow=false
// withholds; clearing leaves the group (or the default) to decide.
//
// Exactly one of the two rows may exist for a pair, so both are deleted before
// either is written: a subscriber who is somehow both granted and denied is a
// question with no defensible answer.
func (s *Store) SetUserObjectAccess(userID, kind, objectID string, allow, clear bool) error {
	denyTable, allowTable, col := "", "", ""
	switch kind {
	case "node":
		denyTable, allowTable, col = "user_node_denies", "user_node_allows", "node_id"
	case "relay":
		denyTable, allowTable, col = "user_relay_denies", "user_relay_allows", "relay_id"
	case "external":
		denyTable, allowTable, col = "user_external_denies", "user_external_allows", "proxy_id"
	default:
		return sql.ErrNoRows
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range []string{denyTable, allowTable} {
		if _, err := tx.Exec(`DELETE FROM `+t+` WHERE user_id = ? AND `+col+` = ?`,
			userID, objectID); err != nil {
			return err
		}
	}
	if !clear {
		t := denyTable
		if allow {
			t = allowTable
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO `+t+` (user_id, `+col+`) VALUES (?, ?)`,
			userID, objectID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetUserProfileAccess is the same three-state control for a profile: granted
// to this subscriber, withheld from them despite their group, or neither.
func (s *Store) SetUserProfileAccess(userID, profileID string, grant, clear bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range []string{"user_profiles", "user_profile_denies"} {
		if _, err := tx.Exec(`DELETE FROM `+t+` WHERE user_id = ? AND profile_id = ?`,
			userID, profileID); err != nil {
			return err
		}
	}
	if !clear {
		t := "user_profile_denies"
		if grant {
			t = "user_profiles"
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO `+t+` (user_id, profile_id) VALUES (?, ?)`,
			userID, profileID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// --- resolved answers ---

// EffectiveRulesetID is the routing rule set this subscriber's subscription
// carries: their own choice, else their group's, else none. ruleset_none is
// how a member says "none" against a group that states one — without it, NULL
// would have to mean both "inherit" and "no rules".
func (s *Store) EffectiveRulesetID(userID string) (string, error) {
	var own, groupRuleset string
	var none int
	err := s.db.QueryRow(`SELECT COALESCE(u.ruleset_id, ''), u.ruleset_none,
		COALESCE((SELECT g.ruleset_id FROM subscriber_groups g WHERE g.id = u.group_id), '')
		FROM users u WHERE u.id = ?`, userID).Scan(&own, &none, &groupRuleset)
	if err != nil {
		return "", err
	}
	switch {
	case own != "":
		return own, nil
	case none != 0:
		return "", nil
	default:
		return groupRuleset, nil
	}
}

// SetUserRulesetNone records that this subscriber carries no routing rules
// whatever their group says.
func (s *Store) SetUserRulesetNone(userID string, none bool) error {
	v := 0
	if none {
		v = 1
	}
	res, err := s.db.Exec(`UPDATE users SET ruleset_none = ?, ruleset_id = NULL WHERE id = ?`, v, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// --- reconciling whole-set editors against a group ---

// accessTables names the three tables one dimension of access lives in.
func accessTables(kind string) (col, denyTable, allowTable, groupTable, universe string, ok bool) {
	switch kind {
	case "node":
		return "node_id", "user_node_denies", "user_node_allows", "group_node_denies", "nodes", true
	case "relay":
		return "relay_id", "user_relay_denies", "user_relay_allows", "group_relay_denies", "node_relays", true
	case "external":
		return "proxy_id", "user_external_denies", "user_external_allows", "group_external_denies", "external_proxies", true
	}
	return "", "", "", "", "", false
}

// setUserDimension replaces one subscriber's decisions for one dimension,
// writing ONLY what differs from their group.
//
// The console hands over an end state — "these are the nodes they may not
// use" — and for a subscriber in a group most of that end state is already
// what the group says. Writing it out per object anyway would bury the group
// under a full set of exceptions on the first save, and the group would stop
// deciding anything without anyone touching it.
func (s *Store) setUserDimension(tx *sql.Tx, userID, groupID, kind string, denied []string) error {
	col, denyTable, allowTable, groupTable, universe, ok := accessTables(kind)
	if !ok {
		return sql.ErrNoRows
	}
	for _, t := range []string{denyTable, allowTable} {
		if _, err := tx.Exec(`DELETE FROM `+t+` WHERE user_id = ?`, userID); err != nil {
			return err
		}
	}
	want := map[string]bool{}
	for _, id := range denied {
		want[id] = true
	}
	byGroup := map[string]bool{}
	if groupID != "" {
		rows, err := tx.Query(`SELECT `+col+` FROM `+groupTable+` WHERE group_id = ?`, groupID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			byGroup[id] = true
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	rows, err := tx.Query(`SELECT id FROM ` + universe)
	if err != nil {
		return err
	}
	var all []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		all = append(all, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range all {
		switch {
		case want[id] && !byGroup[id]:
			if _, err := tx.Exec(`INSERT OR IGNORE INTO `+denyTable+` (user_id, `+col+`) VALUES (?, ?)`,
				userID, id); err != nil {
				return err
			}
		case !want[id] && byGroup[id]:
			if _, err := tx.Exec(`INSERT OR IGNORE INTO `+allowTable+` (user_id, `+col+`) VALUES (?, ?)`,
				userID, id); err != nil {
				return err
			}
		}
	}
	return nil
}

// setObjectAccess is the same reconciliation read from the object's end: one
// line or external node, and the subscribers refused it.
func (s *Store) setObjectAccess(kind, objectID string, deniedUsers []string) error {
	col, denyTable, allowTable, groupTable, _, ok := accessTables(kind)
	if !ok {
		return sql.ErrNoRows
	}
	want := map[string]bool{}
	for _, id := range deniedUsers {
		want[id] = true
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, t := range []string{denyTable, allowTable} {
		if _, err := tx.Exec(`DELETE FROM `+t+` WHERE `+col+` = ?`, objectID); err != nil {
			return err
		}
	}
	rows, err := tx.Query(`SELECT u.id, COALESCE(u.group_id, ''),
		EXISTS (SELECT 1 FROM `+groupTable+` g WHERE g.group_id = u.group_id AND g.`+col+` = ?)
		FROM users u`, objectID)
	if err != nil {
		return err
	}
	type decision struct {
		userID   string
		byGroup  bool
		hasGroup bool
	}
	var users []decision
	for rows.Next() {
		var d decision
		var groupID string
		if err := rows.Scan(&d.userID, &groupID, &d.byGroup); err != nil {
			rows.Close()
			return err
		}
		d.hasGroup = groupID != ""
		users = append(users, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, d := range users {
		byGroup := d.hasGroup && d.byGroup
		switch {
		case want[d.userID] && !byGroup:
			if _, err := tx.Exec(`INSERT OR IGNORE INTO `+denyTable+` (user_id, `+col+`) VALUES (?, ?)`,
				d.userID, objectID); err != nil {
				return err
			}
		case !want[d.userID] && byGroup:
			if _, err := tx.Exec(`INSERT OR IGNORE INTO `+allowTable+` (user_id, `+col+`) VALUES (?, ?)`,
				d.userID, objectID); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// objectDenies lists the subscribers effectively refused one object, so an
// editor that works from the object's end shows the same answer the
// subscription will act on.
func (s *Store) objectDenies(kind, objectID string) (map[string]struct{}, error) {
	col, denyTable, allowTable, groupTable, _, ok := accessTables(kind)
	if !ok {
		return nil, sql.ErrNoRows
	}
	return s.denySet(`SELECT user_id FROM `+denyTable+` WHERE `+col+` = ?
		UNION
		SELECT u.id FROM users u
			JOIN `+groupTable+` g ON g.group_id = u.group_id AND g.`+col+` = ?
			WHERE u.id NOT IN (SELECT user_id FROM `+allowTable+` WHERE `+col+` = ?)`,
		objectID, objectID, objectID)
}
