package store

// StoredScopes lists every non-empty scope value the database holds, so the
// daemon can report the ones its scope table does not declare (rows that only
// the master can then see).
func (s *Store) StoredScopes() ([]string, error) {
	rows, err := s.db.Query(`SELECT scope FROM themes UNION SELECT scope FROM journal
		UNION SELECT scope FROM peer_sessions WHERE scope <> '' ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var sc string
		if err := rows.Scan(&sc); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}
