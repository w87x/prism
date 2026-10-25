package app

import "context"

// taskText is what a task asked for, and what the request it belongs to asked for: the web guard treats a site named in either as mandated.
func (a *App) taskText(ctx context.Context, taskID int64) string {
	var in, root string
	_ = a.DB.Pool.QueryRow(ctx, `SELECT t.input, COALESCE(r.input,'') FROM tasks t LEFT JOIN tasks r ON r.id=t.root_id AND r.id<>t.id WHERE t.id=$1`, taskID).Scan(&in, &root)
	return in + "\n" + root
}
