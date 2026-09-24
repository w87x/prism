// Package agent contains agent profiles, sessions, the runner loop and the
// orchestrator that delegates tasks between agents.
package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"prism/internal/textmatch"
)

const (
	RoleEntry  = "entry"
	RoleMaint  = "maint"
	RoleWorker = "worker"
)

// MaxDepth is the deepest delegation level: Atlas(0) → agent(1) → agent(2).
const MaxDepth = 2

type Profile struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	Group         string    `json:"group"`
	Description   string    `json:"description"`
	Soul          string    `json:"soul"`
	Traits        []string  `json:"traits"`
	Tools         []string  `json:"tools"`
	Skills        []string  `json:"skills"`
	Banks         []string  `json:"banks"`
	Model         string    `json:"model"`
	Role          string    `json:"role"`
	System        bool      `json:"system"`
	CanDelegate   bool      `json:"can_delegate"`
	AutoTools     bool      `json:"auto_tools"`
	MaxIterations int       `json:"max_iterations"`
	Enabled       bool      `json:"enabled"`
	SoulVersion   int       `json:"soul_version"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	Icon          string    `json:"icon"` // one emoji or CJK glyph shown in the UI
	// Probation marks an agent another agent hired: it works, but without exec tools and without delegating, until
	// the user confirms the hire.
	Probation bool `json:"probation"`
	// Team names the specialists this agent leads: it may delegate only to them (Atlas, the entry agent, leads
	// everyone), and its prompt makes it split work, delegate in one parallel call, wait and consolidate.
	Team []string `json:"team"`
}

type SoulVersion struct {
	Version   int       `json:"version"`
	Soul      string    `json:"soul"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
}

// Proposal kinds. A soul proposal carries the complete new soul; tools and traits carry one name per line,
// so every kind can be shown as a line diff against Base.
const (
	KindSoul   = "soul"
	KindTools  = "tools"
	KindTraits = "traits"
)

type Proposal struct {
	ID        int64  `json:"id"`
	ProfileID int64  `json:"profile_id"`
	Agent     string `json:"agent"`
	Kind      string `json:"kind"`
	Proposal  string `json:"proposal"`
	Rationale string `json:"rationale"`
	Status    string `json:"status"`
	// Base is the text the proposal changes: the soul as it was at BaseVersion (kept in soul history),
	// or the agent's current tools/traits, one per line.
	Base           string     `json:"base"`
	BaseVersion    int        `json:"base_version"`
	CurrentVersion int        `json:"current_version"` // the agent's soul version now; differs from BaseVersion when the agent changed since
	CreatedAt      time.Time  `json:"created_at"`
	DecidedAt      *time.Time `json:"decided_at,omitempty"`
}

type ProfileStore struct {
	db       *pgxpool.Pool
	OnChange func()
	// OnCreate runs after a new profile was stored (used to assign its icon).
	OnCreate func(id int64, icon string)
}

func NewProfileStore(db *pgxpool.Pool) *ProfileStore { return &ProfileStore{db: db} }

const pcols = `id,name,grp,description,soul,traits,tools,skills,banks,model,role,system,can_delegate,auto_tools,max_iterations,enabled,soul_version,created_at,updated_at,icon,probation,team`

func scanProfile(r pgx.Row) (Profile, error) {
	var p Profile
	err := r.Scan(&p.ID, &p.Name, &p.Group, &p.Description, &p.Soul, &p.Traits, &p.Tools, &p.Skills, &p.Banks, &p.Model, &p.Role,
		&p.System, &p.CanDelegate, &p.AutoTools, &p.MaxIterations, &p.Enabled, &p.SoulVersion, &p.CreatedAt, &p.UpdatedAt, &p.Icon, &p.Probation, &p.Team)
	return p, err
}

func (s *ProfileStore) List(ctx context.Context) ([]Profile, error) {
	rows, err := s.db.Query(ctx, `SELECT `+pcols+` FROM agent_profiles ORDER BY (role='entry') DESC, (role='maint') DESC, grp, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Profile
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *ProfileStore) Get(ctx context.Context, name string) (*Profile, error) {
	p, err := scanProfile(s.db.QueryRow(ctx, `SELECT `+pcols+` FROM agent_profiles WHERE lower(name)=lower($1)`, name))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("agent %q not found", name)
	}
	return &p, err
}

func (s *ProfileStore) GetID(ctx context.Context, id int64) (*Profile, error) {
	p, err := scanProfile(s.db.QueryRow(ctx, `SELECT `+pcols+` FROM agent_profiles WHERE id=$1`, id))
	return &p, err
}

func norm(s []string) []string {
	if s == nil {
		return []string{}
	}
	out := make([]string, 0, len(s))
	for _, x := range s {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// Save creates or updates a profile. A changed soul bumps the version and is archived.
func (s *ProfileStore) Save(ctx context.Context, p Profile, reason string) (*Profile, error) {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return nil, errors.New("agent name is required")
	}
	if p.Group == "" {
		p.Group = "General"
	}
	if p.Role == "" {
		p.Role = RoleWorker
	}
	if p.MaxIterations <= 0 {
		p.MaxIterations = 24
	}
	if p.MaxIterations > 80 {
		p.MaxIterations = 80
	}
	p.Traits, p.Tools, p.Skills, p.Banks = norm(p.Traits), norm(p.Tools), norm(p.Skills), norm(p.Banks)
	p.Team = norm(p.Team)
	if p.Team == nil {
		p.Team = []string{}
	}
	if len(p.Team) > 12 {
		return nil, errors.New("a team can have at most 12 members")
	}
	for _, m := range p.Team {
		if strings.EqualFold(m, p.Name) {
			return nil, errors.New("an agent cannot be on its own team")
		}
	}
	if len(p.Team) > 0 {
		p.CanDelegate = true // leading a team implies delegating
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var id int64
	creating := p.ID == 0
	if p.ID == 0 {
		err = tx.QueryRow(ctx, `INSERT INTO agent_profiles(name,grp,description,soul,traits,tools,skills,banks,model,role,system,can_delegate,auto_tools,max_iterations,enabled,icon,team)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17) RETURNING id`,
			p.Name, p.Group, p.Description, p.Soul, p.Traits, p.Tools, p.Skills, p.Banks, p.Model, p.Role, p.System, p.CanDelegate, p.AutoTools, p.MaxIterations, p.Enabled, normIcon(p.Icon), p.Team).Scan(&id)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO soul_history(profile_id,version,soul,reason) VALUES($1,1,$2,$3)`, id, p.Soul, firstNonEmpty(reason, "created")); err != nil {
			return nil, err
		}
	} else {
		id = p.ID
		var oldSoul, oldName string
		var isSystem bool
		var ver int
		if err := tx.QueryRow(ctx, `SELECT soul, soul_version, name, system FROM agent_profiles WHERE id=$1 FOR UPDATE`, id).Scan(&oldSoul, &ver, &oldName, &isSystem); err != nil {
			return nil, err
		}
		if isSystem {
			p.Name = oldName // well-known agents keep their names
			if oldName == "Atlas" {
				p.Enabled = true
			}
		}
		if oldSoul != p.Soul {
			ver++
			if _, err := tx.Exec(ctx, `INSERT INTO soul_history(profile_id,version,soul,reason) VALUES($1,$2,$3,$4)`, id, ver, p.Soul, firstNonEmpty(reason, "edited")); err != nil {
				return nil, err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE agent_profiles SET name=$2,grp=$3,description=$4,soul=$5,traits=$6,tools=$7,skills=$8,banks=$9,model=$10,
			can_delegate=$11,auto_tools=$12,max_iterations=$13,enabled=$14,soul_version=$15,icon=$16,team=$17,updated_at=now() WHERE id=$1`,
			id, p.Name, p.Group, p.Description, p.Soul, p.Traits, p.Tools, p.Skills, p.Banks, p.Model, p.CanDelegate, p.AutoTools, p.MaxIterations, p.Enabled, ver, normIcon(p.Icon), p.Team); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	if creating && s.OnCreate != nil {
		s.OnCreate(id, normIcon(p.Icon))
	}
	if s.OnChange != nil {
		s.OnChange()
	}
	return s.GetID(ctx, id)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (s *ProfileStore) Delete(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM agent_profiles WHERE id=$1 AND NOT system`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("well-known agents cannot be deleted (disable them instead)")
	}
	if s.OnChange != nil {
		s.OnChange()
	}
	return nil
}

func (s *ProfileStore) History(ctx context.Context, id int64) ([]SoulVersion, error) {
	rows, err := s.db.Query(ctx, `SELECT version,soul,reason,created_at FROM soul_history WHERE profile_id=$1 ORDER BY version DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SoulVersion
	for rows.Next() {
		var v SoulVersion
		if err := rows.Scan(&v.Version, &v.Soul, &v.Reason, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// Search ranks enabled profiles by group/name/description/traits.
func (s *ProfileStore) Search(ctx context.Context, query string, limit int) ([]Profile, error) {
	all, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	var cand []Profile
	var docs []string
	for _, p := range all {
		if !p.Enabled || p.Role == RoleEntry {
			continue
		}
		cand = append(cand, p)
		docs = append(docs, p.Name+" "+p.Group+" "+p.Description+" "+strings.Join(p.Traits, " ")+" "+strings.Join(p.Tools, " "))
	}
	hits := textmatch.Rank(query, docs, limit)
	out := make([]Profile, 0, len(hits))
	for _, h := range hits {
		out = append(out, cand[h.Index])
	}
	return out, nil
}

// SetProbation puts an agent on probation or confirms the hire.
func (s *ProfileStore) SetProbation(ctx context.Context, id int64, on bool) error {
	t, err := s.db.Exec(ctx, `UPDATE agent_profiles SET probation=$2, updated_at=now() WHERE id=$1`, id, on)
	if err == nil && t.RowsAffected() == 0 {
		err = errors.New("no such agent")
	}
	if err == nil && s.OnChange != nil {
		s.OnChange()
	}
	return err
}

// AgentHiresSince counts agents hired by other agents (not by the user) since t.
func (s *ProfileStore) AgentHiresSince(ctx context.Context, t time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `SELECT count(*) FROM soul_history WHERE version=1 AND reason LIKE 'hired by %' AND created_at >= $1`, t).Scan(&n)
	return n, err
}

// ── evolution proposals ────────────────────────────────────────────────────

func (s *ProfileStore) AddProposal(ctx context.Context, profileID int64, kind, proposal, rationale string) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx, `INSERT INTO evolution_proposals(profile_id,kind,proposal,rationale,base_version)
		VALUES($1,$2,$3,$4,(SELECT soul_version FROM agent_profiles WHERE id=$1)) RETURNING id`,
		profileID, kind, proposal, rationale).Scan(&id)
	return id, err
}

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func (s *ProfileStore) Proposals(ctx context.Context, status string) ([]Proposal, error) {
	rows, err := s.db.Query(ctx, `SELECT e.id,e.profile_id,p.name,e.kind,e.proposal,e.rationale,e.status,e.created_at,e.decided_at,
			e.base_version,p.soul_version,p.soul,h.soul,p.tools,p.traits
		FROM evolution_proposals e JOIN agent_profiles p ON p.id=e.profile_id
		LEFT JOIN soul_history h ON h.profile_id=e.profile_id AND h.version=e.base_version
		WHERE ($1='' OR e.status=$1) ORDER BY e.id DESC LIMIT 200`, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Proposal{}
	for rows.Next() {
		var p Proposal
		var curSoul string
		var baseSoul *string
		var tools, traits []string
		if err := rows.Scan(&p.ID, &p.ProfileID, &p.Agent, &p.Kind, &p.Proposal, &p.Rationale, &p.Status, &p.CreatedAt, &p.DecidedAt,
			&p.BaseVersion, &p.CurrentVersion, &curSoul, &baseSoul, &tools, &traits); err != nil {
			return nil, err
		}
		switch p.Kind {
		case KindTools:
			p.Base = strings.Join(tools, "\n")
		case KindTraits:
			p.Base = strings.Join(traits, "\n")
		default:
			p.Base = curSoul
			if baseSoul != nil {
				p.Base = *baseSoul // exactly what the proposal was written against
			}
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Decide applies (soul proposals replace the soul, archived in history) or rejects a proposal.
func (s *ProfileStore) Decide(ctx context.Context, id int64, apply bool) error {
	return s.DecideEdited(ctx, id, apply, "")
}

// DecideEdited is Decide with the reviewer's own wording: a non-empty edited text is applied instead of the
// proposed one (and recorded as what was applied).
func (s *ProfileStore) DecideEdited(ctx context.Context, id int64, apply bool, edited string) error {
	var pid int64
	var kind, proposal, status, rationale string
	if err := s.db.QueryRow(ctx, `SELECT profile_id,kind,proposal,status,rationale FROM evolution_proposals WHERE id=$1`, id).Scan(&pid, &kind, &proposal, &status, &rationale); err != nil {
		return err
	}
	if status != "pending" {
		return fmt.Errorf("proposal already %s", status)
	}
	newStatus := "rejected"
	if apply {
		newStatus = "applied"
		if strings.TrimSpace(edited) != "" {
			proposal = edited
		}
		p, err := s.GetID(ctx, pid)
		if err != nil {
			return err
		}
		switch kind {
		case KindTools:
			p.Tools = lines(proposal)
		case KindTraits:
			p.Traits = lines(proposal)
		case KindSoul, "":
			if strings.TrimSpace(proposal) == "" {
				return errors.New("the soul cannot be empty")
			}
			p.Soul = proposal
		default:
			return fmt.Errorf("unknown proposal kind %q", kind)
		}
		if _, err := s.Save(ctx, *p, "evolution: "+rationale); err != nil {
			return err
		}
	}
	_, err := s.db.Exec(ctx, `UPDATE evolution_proposals SET status=$2, decided_at=now(), proposal=$3 WHERE id=$1`, id, newStatus, proposal)
	return err
}
