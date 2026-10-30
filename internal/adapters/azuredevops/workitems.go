package azuredevops

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// batchSize is the most work items one workitemsbatch call reads.
const batchSize = 200

// Link types (Azure DevOps docs, "Link types reference").
const (
	linkPredecessor = "System.LinkTypes.Dependency-Reverse"
	linkChild       = "System.LinkTypes.Hierarchy-Forward"
	linkParent      = "System.LinkTypes.Hierarchy-Reverse"
)

// Fields read beyond the System ones.
const fieldAcceptanceCriteria = "Microsoft.VSTS.Common.AcceptanceCriteria"

// notPlannedReasons are the System.Reason values that make a work item in
// a Completed state closed-not-planned rather than closed-completed, the
// way GitHub's "duplicate" and "not planned" are: work that was dropped
// must block its dependents instead of satisfying them (spec
// task-eligibility, "Dependency abandoned"). They are the reasons the
// Agile, Scrum and CMMI processes offer for closing without doing the
// work.
var notPlannedReasons = []string{"Duplicate", "Obsolete", "Won't Fix", "Cut", "Abandoned"}

// workItemJSON is a work item as the REST API returns it.
type workItemJSON struct {
	ID        int        `json:"id"`
	Rev       int        `json:"rev"`
	Fields    wiFields   `json:"fields"`
	Relations []relation `json:"relations"`
	// MultilineFieldsFormat names the rich-text fields stored as
	// Markdown instead of HTML.
	MultilineFieldsFormat map[string]string `json:"multilineFieldsFormat"`
}

type wiFields struct {
	TeamProject        string       `json:"System.TeamProject"`
	Type               string       `json:"System.WorkItemType"`
	State              string       `json:"System.State"`
	Reason             string       `json:"System.Reason"`
	Title              string       `json:"System.Title"`
	Description        string       `json:"System.Description"`
	AcceptanceCriteria string       `json:"Microsoft.VSTS.Common.AcceptanceCriteria"`
	Tags               string       `json:"System.Tags"`
	AssignedTo         *identityRef `json:"System.AssignedTo"`
	CreatedBy          *identityRef `json:"System.CreatedBy"`
	ChangedDate        time.Time    `json:"System.ChangedDate"`
	CommentCount       int          `json:"System.CommentCount"`
}

type relation struct {
	Rel string `json:"rel"`
	URL string `json:"url"`
}

// Items polls the work items of a project (organization/project) changed
// since cur (spec work-item-sources, "Change detection"). An empty cursor
// reads every item with the opt-in tag. A non-empty one reads every item
// changed since the cursor, with or without the tag, so that the removal
// of the tag or the closing of an item is seen; the ingestion service
// filters out items that are not opted in. Items re-read in the overlap
// whose System.Rev did not move are left out.
func (a *Adapter) Items(ctx context.Context, project domain.Repository, cur string) (ports.ItemPage, error) {
	project = Normalize(project)
	if err := a.checkProject(project); err != nil {
		return ports.ItemPage{}, err
	}
	c, err := parseCursor[itemCursor](cur)
	if err != nil {
		return ports.ItemPage{}, err
	}
	query := "SELECT [System.Id] FROM WorkItems WHERE [System.TeamProject] = @project"
	if c.Since.IsZero() {
		query += " AND [System.Tags] CONTAINS " + wiqlString(a.options(project).OptInLabel)
	} else {
		from := c.Since.Add(-overlap).UTC().Format("2006-01-02T15:04:05.000Z")
		query += " AND [System.ChangedDate] >= " + wiqlString(from)
	}
	query += " ORDER BY [System.Id]"
	var result struct {
		WorkItems []struct {
			ID int `json:"id"`
		} `json:"workItems"`
	}
	resp, err := a.send(ctx, call{
		op:     "query work items of " + project.String(),
		method: http.MethodPost,
		url:    a.api(url.Values{"timePrecision": {"true"}}, project.Org, project.Project, "_apis", "wit", "wiql"),
		body:   map[string]string{"query": query},
		out:    &result,
	})
	if err != nil {
		return ports.ItemPage{}, err
	}
	page := ports.ItemPage{ServerTime: a.serverTime(resp)}
	ids := make([]int, len(result.WorkItems))
	for i, w := range result.WorkItems {
		ids[i] = w.ID
	}
	raw, err := a.workItemsBatch(ctx, ids)
	if err != nil {
		return ports.ItemPage{}, err
	}

	next := itemCursor{Since: c.Since}
	for _, w := range raw {
		if w.Fields.ChangedDate.After(next.Since) {
			next.Since = w.Fields.ChangedDate
		}
	}
	var changed []workItemJSON
	for _, w := range raw {
		if !w.Fields.ChangedDate.Before(next.Since.Add(-overlap)) {
			if next.Revs == nil {
				next.Revs = map[int]int{}
			}
			next.Revs[w.ID] = w.Rev
		}
		if rev, seen := c.Revs[w.ID]; seen && rev == w.Rev {
			continue
		}
		changed = append(changed, w)
	}
	page.Cursor = cursorString(next)

	items, err := a.workItems(ctx, changed)
	if err != nil {
		return ports.ItemPage{}, err
	}
	page.Items = items
	return page, nil
}

// wiqlString quotes a WIQL string literal.
func wiqlString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// workItemsBatch reads work items with their relations, 200 per call.
// Items Anthill cannot read are left out.
func (a *Adapter) workItemsBatch(ctx context.Context, ids []int) ([]workItemJSON, error) {
	var out []workItemJSON
	for start := 0; start < len(ids); start += batchSize {
		chunk := ids[start:min(start+batchSize, len(ids))]
		var list listOf[*workItemJSON]
		if _, err := a.send(ctx, call{
			op:     "read work items",
			method: http.MethodPost,
			url:    a.api(nil, a.org, "_apis", "wit", "workitemsbatch"),
			body:   map[string]any{"ids": chunk, "$expand": "relations", "errorPolicy": "omit"},
			out:    &list,
		}); err != nil {
			return nil, err
		}
		for _, w := range list.Value {
			// errorPolicy omit answers null for an item that cannot be
			// read.
			if w != nil {
				out = append(out, *w)
			}
		}
	}
	return out, nil
}

// Item reads one work item fresh, with Azure DevOps' time (spec
// task-eligibility, "Re-check at acceptance"). An item that lives in
// another project than ref says is not found.
func (a *Adapter) Item(ctx context.Context, ref domain.Ref) (domain.WorkItem, time.Time, error) {
	ref = normalizeRef(ref)
	if err := a.checkRef(ref); err != nil {
		return domain.WorkItem{}, time.Time{}, err
	}
	if ref.Kind != domain.KindItem {
		return domain.WorkItem{}, time.Time{}, fmt.Errorf("azure devops: %s is not a work item", ref.Full())
	}
	op := "read work item " + ref.Full()
	w, resp, err := a.readWorkItem(ctx, ref, url.Values{"$expand": {"relations"}}, op)
	if err != nil {
		return domain.WorkItem{}, time.Time{}, err
	}
	items, err := a.workItems(ctx, []workItemJSON{w})
	if err != nil {
		return domain.WorkItem{}, time.Time{}, err
	}
	return items[0], a.serverTime(resp), nil
}

// readWorkItem reads one work item of ref's project.
func (a *Adapter) readWorkItem(ctx context.Context, ref domain.Ref, query url.Values, op string) (workItemJSON, *http.Response, error) {
	var w workItemJSON
	resp, err := a.send(ctx, call{
		op:     op,
		method: http.MethodGet,
		url:    a.api(query, ref.Repo.Org, ref.Repo.Project, "_apis", "wit", "workitems", strconv.Itoa(ref.Number)),
		out:    &w,
	})
	if err != nil {
		return workItemJSON{}, resp, err
	}
	if w.Fields.TeamProject != "" && !strings.EqualFold(w.Fields.TeamProject, ref.Repo.Project) {
		return workItemJSON{}, resp, fmt.Errorf("azure devops: %s: it belongs to project %s: %w", op, w.Fields.TeamProject, ports.ErrNotFound)
	}
	return w, resp, nil
}

// workItems maps work items and reads what the item itself only counts or
// names: the projects of linked items, the state categories and the
// comments. The result is sorted by ID.
func (a *Adapter) workItems(ctx context.Context, raw []workItemJSON) ([]domain.WorkItem, error) {
	projects, err := a.linkProjects(ctx, raw)
	if err != nil {
		return nil, err
	}
	items := make([]domain.WorkItem, 0, len(raw))
	for _, w := range raw {
		item := a.mapWorkItem(w, projects)
		category, err := a.stateCategory(ctx, item.Ref.Repo, w.Fields.Type, w.Fields.State)
		if err != nil {
			return nil, err
		}
		item.State = itemState(category, w.Fields.State, w.Fields.Reason)
		if w.Fields.CommentCount > 0 {
			comments, _, err := a.workItemComments(ctx, item.Ref)
			if err != nil {
				return nil, err
			}
			item.Comments = comments
		}
		items = append(items, item)
	}
	slices.SortFunc(items, func(x, y domain.WorkItem) int { return x.Ref.Number - y.Ref.Number })
	return items, nil
}

// mapWorkItem maps the fields and links of a work item. projects gives
// the project of each linked item that could be read.
func (a *Adapter) mapWorkItem(w workItemJSON, projects map[int]string) domain.WorkItem {
	f := w.Fields
	project := domain.Repository{Provider: domain.AzureDevOps, Org: a.org, Project: strings.ToLower(f.TeamProject)}
	ref := domain.Ref{Repo: project, Kind: domain.KindItem, Number: w.ID}
	item := domain.WorkItem{
		Ref:                ref,
		URL:                a.webURL(project.Org, project.Project, "_workitems", "edit", strconv.Itoa(w.ID)),
		Author:             f.CreatedBy.login(),
		Title:              f.Title,
		Body:               richText(w, "System.Description", f.Description),
		AcceptanceCriteria: richText(w, fieldAcceptanceCriteria, f.AcceptanceCriteria),
		Type:               itemType(f.Type),
		Labels:             splitTags(f.Tags),
		Revision:           strconv.Itoa(w.Rev),
		UpdatedAt:          f.ChangedDate,
	}
	if login := f.AssignedTo.login(); login != "" {
		item.Assignees = []string{login}
	}
	link := func(id int) domain.Ref {
		repo := project
		if p, ok := projects[id]; ok {
			repo.Project = p
		}
		return domain.Ref{Repo: repo, Kind: domain.KindItem, Number: id}
	}
	for _, r := range w.Relations {
		id, ok := linkedID(r)
		if !ok {
			continue
		}
		switch r.Rel {
		case linkPredecessor:
			item.Dependencies = append(item.Dependencies, link(id))
		case linkChild:
			item.Children = append(item.Children, link(id))
		case linkParent:
			parent := link(id)
			item.Parent = &parent
		}
	}
	return item
}

// richText returns a rich-text field as text: Markdown fields as they are,
// HTML fields through htmlToText.
func richText(w workItemJSON, field, value string) string {
	if strings.EqualFold(w.MultilineFieldsFormat[field], "markdown") {
		return strings.TrimSpace(value)
	}
	return htmlToText(value)
}

// splitTags splits System.Tags, which Azure DevOps writes as "a; b".
func splitTags(tags string) []string {
	var out []string
	for t := range strings.SplitSeq(tags, ";") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// joinTags writes tags the way System.Tags holds them.
func joinTags(tags []string) string {
	return strings.Join(tags, "; ")
}

// linkedID reads the work item ID at the end of a link's URL, such as
// https://dev.azure.com/contoso/_apis/wit/workItems/10.
func linkedID(r relation) (int, bool) {
	if r.Rel != linkPredecessor && r.Rel != linkChild && r.Rel != linkParent {
		return 0, false
	}
	i := strings.LastIndex(strings.ToLower(r.URL), "/workitems/")
	if i < 0 {
		return 0, false
	}
	id, err := strconv.Atoi(r.URL[i+len("/workitems/"):])
	return id, err == nil && id > 0
}

// linkProjects reads the project of every item linked from raw. A link's
// URL names only the item's ID, which is unique in the organization, and
// a link may cross projects. Items that cannot be read are missing from
// the result and keep their linking item's project.
func (a *Adapter) linkProjects(ctx context.Context, raw []workItemJSON) (map[int]string, error) {
	seen := map[int]bool{}
	var ids []int
	for _, w := range raw {
		for _, r := range w.Relations {
			if id, ok := linkedID(r); ok && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	projects := map[int]string{}
	for start := 0; start < len(ids); start += batchSize {
		chunk := ids[start:min(start+batchSize, len(ids))]
		var list listOf[*workItemJSON]
		if _, err := a.send(ctx, call{
			op:     "read the projects of linked work items",
			method: http.MethodPost,
			url:    a.api(nil, a.org, "_apis", "wit", "workitemsbatch"),
			body:   map[string]any{"ids": chunk, "fields": []string{"System.Id", "System.TeamProject"}, "errorPolicy": "omit"},
			out:    &list,
		}); err != nil {
			return nil, err
		}
		for _, w := range list.Value {
			if w != nil && w.Fields.TeamProject != "" {
				projects[w.ID] = strings.ToLower(w.Fields.TeamProject)
			}
		}
	}
	return projects, nil
}

// itemType maps a work item type of the Agile, Scrum, CMMI and Basic
// processes.
func itemType(t string) domain.ItemType {
	switch strings.ToLower(t) {
	case "bug":
		return domain.TypeBug
	case "task":
		return domain.TypeTask
	case "user story", "product backlog item", "requirement", "feature", "epic", "issue":
		return domain.TypeFeature
	}
	return domain.TypeOther
}

// The state categories of Azure DevOps (docs, "Workflow states and state
// categories").
const (
	categoryCompleted = "Completed"
	categoryRemoved   = "Removed"
)

// itemState maps a state through its category: Completed is
// closed-completed, unless the reason says the work was dropped; Removed
// is closed-not-planned; Proposed, InProgress and Resolved are open.
func itemState(category, state, reason string) domain.ItemState {
	switch category {
	case categoryCompleted:
		if slices.ContainsFunc(notPlannedReasons, func(r string) bool { return strings.EqualFold(r, reason) }) {
			return domain.StateClosedNotPlanned
		}
		return domain.StateClosedCompleted
	case categoryRemoved:
		return domain.StateClosedNotPlanned
	case "":
		// A state the type does not list, such as one renamed since:
		// fall back on the names the processes use.
		switch strings.ToLower(state) {
		case "closed", "done":
			return itemState(categoryCompleted, state, reason)
		case "removed":
			return domain.StateClosedNotPlanned
		}
	}
	return domain.StateOpen
}

type stateKey struct {
	project  domain.Repository
	itemType string
}

// stateCategory returns the category of a state of a work item type. The
// states of each type are read once per project and kept for the life of
// the adapter: a process change is rare and needs a restart.
func (a *Adapter) stateCategory(ctx context.Context, project domain.Repository, itemType, state string) (string, error) {
	key := stateKey{project: project, itemType: strings.ToLower(itemType)}
	a.mu.Lock()
	categories, ok := a.states[key]
	a.mu.Unlock()
	if !ok {
		var list listOf[struct {
			Name     string `json:"name"`
			Category string `json:"category"`
		}]
		if _, err := a.send(ctx, call{
			op:     fmt.Sprintf("read the states of %s in %s", itemType, project),
			method: http.MethodGet,
			url:    a.api(nil, project.Org, project.Project, "_apis", "wit", "workitemtypes", itemType, "states"),
			out:    &list,
		}); err != nil {
			return "", err
		}
		categories = map[string]string{}
		for _, s := range list.Value {
			categories[strings.ToLower(s.Name)] = s.Category
		}
		a.mu.Lock()
		a.states[key] = categories
		a.mu.Unlock()
	}
	return categories[strings.ToLower(state)], nil
}

// commentJSON is a work item comment.
type commentJSON struct {
	ID           int          `json:"id"`
	Text         string       `json:"text"`
	CreatedBy    *identityRef `json:"createdBy"`
	CreatedDate  time.Time    `json:"createdDate"`
	ModifiedDate time.Time    `json:"modifiedDate"`
	IsDeleted    bool         `json:"isDeleted"`
}

func (a *Adapter) comment(ref domain.Ref, c commentJSON) domain.Comment {
	return domain.Comment{
		ID:        strconv.Itoa(c.ID),
		Author:    c.CreatedBy.login(),
		URL:       a.webURL(ref.Repo.Org, ref.Repo.Project, "_workitems", "edit", strconv.Itoa(ref.Number)),
		Body:      c.Text,
		CreatedAt: c.CreatedDate,
		UpdatedAt: c.ModifiedDate,
	}
}

// workItemComments reads every comment of a work item, oldest first,
// with the first page's response.
func (a *Adapter) workItemComments(ctx context.Context, ref domain.Ref) ([]domain.Comment, *http.Response, error) {
	var comments []domain.Comment
	var first *http.Response
	token := ""
	for {
		query := url.Values{"api-version": {commentsAPIVersion}, "$top": {"200"}, "order": {"asc"}}
		if token != "" {
			query.Set("continuationToken", token)
		}
		var page struct {
			Comments          []commentJSON `json:"comments"`
			ContinuationToken string        `json:"continuationToken"`
		}
		resp, err := a.send(ctx, call{
			op:     "list comments of " + ref.Full(),
			method: http.MethodGet,
			url:    a.api(query, ref.Repo.Org, ref.Repo.Project, "_apis", "wit", "workItems", strconv.Itoa(ref.Number), "comments"),
			out:    &page,
		})
		if err != nil {
			return nil, nil, err
		}
		if first == nil {
			first = resp
		}
		for _, c := range page.Comments {
			if !c.IsDeleted {
				comments = append(comments, a.comment(ref, c))
			}
		}
		if page.ContinuationToken == "" {
			return comments, first, nil
		}
		token = page.ContinuationToken
	}
}
