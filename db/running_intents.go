package db

// RunningIntents 返回当前探索最新的运行中意图及同一查询快照中的总数。
func (s *ExplorationStore) RunningIntents(limit int) (nodes []*Node, total int, err error) {
	if limit <= 0 {
		limit = 300
	}
	rows, err := s.db.Query(`SELECT `+nodeCols+`, COUNT(*) OVER()
FROM exploration_nodes
WHERE exploration_id=$1 AND kind='intent' AND state='running'
ORDER BY id DESC LIMIT $2`, s.expID, limit)
	if err != nil {
		return nil, 0, err
	}
	defer func() {
		if closeErr := rows.Close(); err == nil && closeErr != nil {
			nodes, total, err = nil, 0, closeErr
		}
	}()
	for rows.Next() {
		var n Node
		var payload []byte
		if err := rows.Scan(&n.ID, &n.Kind, &payload, &n.Priority, &n.State, &n.Origin, &n.Owner, &n.BlockedReason, &n.DeleteReason, &n.CreatedAt, &total); err != nil {
			return nil, 0, err
		}
		n.Payload = payload
		nodes = append(nodes, &n)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return nodes, total, nil
}
