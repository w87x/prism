package memory

import "strconv"

// ── transitive invalidation of conclusions (memo: stale views through chains of inference) ─────────────────
//
// Facts are level 0; reflection conclusions rest on facts (level 1); syntheses rest on conclusions (2);
// principles on syntheses (3). A conclusion is STALE when any premise beneath it — at any depth — has been
// retired (corrected, retracted, expired, or archived). Staleness is derived on every read from the premise
// links, never stored, so a delayed maintenance job can never leave a conclusion looking established after the
// ground under it moved. Premises of a conclusion are its evidence-linked neighbours of a LOWER level; that is how
// the undirected evidence links get a direction.

const maxInferenceDepth = 4

// staleExpr is a SQL boolean: "some premise of the conclusion aliased a, at any depth, is retired".
func staleExpr(a string) string {
	return `EXISTS (WITH RECURSIVE prem(id, lvl, depth) AS (
			SELECT x.id, ` + levelExpr("x") + `, 1
			FROM memory_links e JOIN memory_facts x ON x.id=CASE WHEN e.a=` + a + `.id THEN e.b ELSE e.a END
			WHERE (e.a=` + a + `.id OR e.b=` + a + `.id) AND e.kind='evidence' AND ` + levelExpr("x") + ` < ` + levelExpr(a) + `
			UNION ALL
			SELECT y.id, ` + levelExpr("y") + `, p.depth+1
			FROM prem p JOIN memory_links e2 ON (e2.a=p.id OR e2.b=p.id) AND e2.kind='evidence'
			JOIN memory_facts y ON y.id=CASE WHEN e2.a=p.id THEN e2.b ELSE e2.a END
			WHERE ` + levelExpr("y") + ` < p.lvl AND p.depth < ` + strconv.Itoa(maxInferenceDepth) + `
		) SELECT 1 FROM prem p JOIN memory_facts z ON z.id=p.id WHERE z.valid_to IS NOT NULL)`
}

// staleConclusionExpr is staleExpr guarded to conclusions (a plain fact is never "stale").
func staleConclusionExpr(a string) string {
	return `(CASE WHEN ` + a + `.kind='conclusion' THEN ` + staleExpr(a) + ` ELSE false END)`
}

// staleWeight discounts a stale conclusion in retrieval: it may still be worth showing (flagged), but it must not
// outrank a conclusion whose premises all still hold.
const staleWeight = 0.5
