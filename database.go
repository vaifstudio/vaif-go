package vaif

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// MaxLimit is the API's cap on a list query's limit.
const MaxLimit = 1000

var identifierRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func isIdentifier(s string) bool { return identifierRE.MatchString(s) }

type orderKey struct {
	column string
	desc   bool
}

// Query is an immutable, chainable query against one table
// (/v1/data/:projectId/:table). Every builder method returns a new Query, so
// a base query can be shared. Argument errors are reported by the terminal
// call (List, Get, Insert, ...), never by panicking.
type Query struct {
	client  *Client
	table   string
	filters []map[string]any
	order   []orderKey
	columns []string
	limit   int
	offset  int
	err     error
}

func (q *Query) clone() *Query {
	c := *q
	c.filters = append([]map[string]any(nil), q.filters...)
	c.order = append([]orderKey(nil), q.order...)
	c.columns = append([]string(nil), q.columns...)
	return &c
}

func (q *Query) fail(err error) *Query {
	c := q.clone()
	if c.err == nil {
		c.err = err
	}
	return c
}

func (q *Query) clause(column, operator string, value any) *Query {
	if !isIdentifier(column) {
		return q.fail(invalidArg("column must be a SQL identifier, got %q", column))
	}
	c := q.clone()
	c.filters = append(c.filters, map[string]any{"field": column, "operator": operator, "value": value})
	return c
}

// Eq matches rows where column = value.
func (q *Query) Eq(column string, value any) *Query { return q.clause(column, "eq", value) }

// Neq matches rows where column != value.
func (q *Query) Neq(column string, value any) *Query { return q.clause(column, "neq", value) }

// Gt matches rows where column > value.
func (q *Query) Gt(column string, value any) *Query { return q.clause(column, "gt", value) }

// Gte matches rows where column >= value.
func (q *Query) Gte(column string, value any) *Query { return q.clause(column, "gte", value) }

// Lt matches rows where column < value.
func (q *Query) Lt(column string, value any) *Query { return q.clause(column, "lt", value) }

// Lte matches rows where column <= value.
func (q *Query) Lte(column string, value any) *Query { return q.clause(column, "lte", value) }

// Like matches a case-sensitive SQL LIKE pattern.
func (q *Query) Like(column, pattern string) *Query { return q.clause(column, "like", pattern) }

// ILike matches a case-insensitive SQL ILIKE pattern.
func (q *Query) ILike(column, pattern string) *Query { return q.clause(column, "ilike", pattern) }

// In matches rows where column is one of values.
func (q *Query) In(column string, values ...any) *Query {
	if len(values) == 0 {
		return q.fail(invalidArg("In(%q) needs at least one value", column))
	}
	return q.clause(column, "in", values)
}

// Where adds a raw clause, ANDed with the others, e.g. an OR group:
//
//	q.Where(map[string]any{"OR": []any{
//		map[string]any{"field": "status", "value": "open"},
//		map[string]any{"field": "priority", "operator": "gte", "value": 3},
//	}})
func (q *Query) Where(clause map[string]any) *Query {
	if len(clause) == 0 {
		return q.fail(invalidArg("Where needs a non-empty clause"))
	}
	c := q.clone()
	c.filters = append(c.filters, clause)
	return c
}

// Select returns only these columns (default: all).
func (q *Query) Select(columns ...string) *Query {
	for _, col := range columns {
		if !isIdentifier(col) {
			return q.fail(invalidArg("column must be a SQL identifier, got %q", col))
		}
	}
	c := q.clone()
	c.columns = append([]string(nil), columns...)
	return c
}

// Order sorts by column; call again to add secondary sort keys.
func (q *Query) Order(column string, descending bool) *Query {
	if !isIdentifier(column) {
		return q.fail(invalidArg("column must be a SQL identifier, got %q", column))
	}
	c := q.clone()
	c.order = append(c.order, orderKey{column: column, desc: descending})
	return c
}

// Limit caps the number of rows (1..1000).
func (q *Query) Limit(n int) *Query {
	if n < 1 || n > MaxLimit {
		return q.fail(invalidArg("limit must be between 1 and %d", MaxLimit))
	}
	c := q.clone()
	c.limit = n
	return c
}

// Offset skips n rows.
func (q *Query) Offset(n int) *Query {
	if n < 0 {
		return q.fail(invalidArg("offset must be >= 0"))
	}
	c := q.clone()
	c.offset = n
	return c
}

func (q *Query) params(limit, offset int) (url.Values, error) {
	v := url.Values{}
	switch len(q.filters) {
	case 0:
	case 1:
		raw, err := json.Marshal(q.filters[0])
		if err != nil {
			return nil, invalidArg("encoding filter: %v", err)
		}
		v.Set("where", string(raw))
	default:
		raw, err := json.Marshal(map[string]any{"AND": q.filters})
		if err != nil {
			return nil, invalidArg("encoding filter: %v", err)
		}
		v.Set("where", string(raw))
	}
	if len(q.columns) > 0 {
		v.Set("select", strings.Join(q.columns, ","))
	}
	if len(q.order) > 0 {
		cols := make([]string, len(q.order))
		dirs := make([]string, len(q.order))
		for i, o := range q.order {
			cols[i] = o.column
			dirs[i] = "asc"
			if o.desc {
				dirs[i] = "desc"
			}
		}
		v.Set("orderBy", strings.Join(cols, ","))
		v.Set("orderDir", strings.Join(dirs, ","))
	}
	if limit > 0 {
		v.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		v.Set("offset", strconv.Itoa(offset))
	}
	return v, nil
}

func (q *Query) base() string {
	return "/v1/data/" + url.PathEscape(q.client.projectID) + "/" + q.table
}

func (q *Query) rowPath(id string) (string, error) {
	if id == "" {
		return "", invalidArg("id must not be empty")
	}
	return q.base() + "/" + url.PathEscape(id), nil
}

type listBody struct {
	Data  []Row `json:"data"`
	Count *int  `json:"count"`
}

type rowBody struct {
	Data Row `json:"data"`
}

func (q *Query) list(ctx context.Context, limit, offset int) (*listBody, error) {
	if q.err != nil {
		return nil, q.err
	}
	params, err := q.params(limit, offset)
	if err != nil {
		return nil, err
	}
	resp, err := q.client.send(ctx, request{method: http.MethodGet, path: q.base(), query: params}, false)
	if err != nil {
		return nil, err
	}
	var body listBody
	if err := decodeJSON(resp, &body); err != nil {
		return nil, err
	}
	if body.Data == nil {
		body.Data = []Row{}
	}
	return &body, nil
}

// List fetches matching rows and the total match count.
func (q *Query) List(ctx context.Context) (*ListResult, error) {
	body, err := q.list(ctx, q.limit, q.offset)
	if err != nil {
		return nil, err
	}
	count := len(body.Data)
	if body.Count != nil {
		count = *body.Count
	}
	return &ListResult{Data: body.Data, Count: count}, nil
}

// ListInto decodes matching rows into dest (a pointer to a slice of structs)
// and returns the total match count.
func (q *Query) ListInto(ctx context.Context, dest any) (int, error) {
	res, err := q.List(ctx)
	if err != nil {
		return 0, err
	}
	raw, err := json.Marshal(res.Data)
	if err != nil {
		return 0, err
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		return 0, fmt.Errorf("vaif: decoding rows: %w", err)
	}
	return res.Count, nil
}

// First returns the first matching row, or nil when there is none.
func (q *Query) First(ctx context.Context) (Row, error) {
	body, err := q.list(ctx, 1, q.offset)
	if err != nil {
		return nil, err
	}
	if len(body.Data) == 0 {
		return nil, nil
	}
	return body.Data[0], nil
}

// Paginate fetches one page (page starts at 1).
func (q *Query) Paginate(ctx context.Context, page, pageSize int) (*Page, error) {
	if page < 1 {
		return nil, invalidArg("page must be >= 1")
	}
	if pageSize < 1 || pageSize > MaxLimit {
		return nil, invalidArg("pageSize must be between 1 and %d", MaxLimit)
	}
	body, err := q.list(ctx, pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, err
	}
	total := len(body.Data)
	if body.Count != nil {
		total = *body.Count
	}
	return &Page{Data: body.Data, Total: total, Page: page, PageSize: pageSize}, nil
}

func (q *Query) rowCall(ctx context.Context, method, path string, query url.Values, payload any) (Row, error) {
	resp, err := q.client.send(ctx, request{method: method, path: path, query: query, body: payload}, false)
	if err != nil {
		return nil, err
	}
	var body rowBody
	if err := decodeJSON(resp, &body); err != nil {
		return nil, err
	}
	if body.Data == nil {
		return nil, &Error{Kind: ErrBadResponse, Status: resp.status, Message: "response did not include a row"}
	}
	return body.Data, nil
}

// Get fetches one row by primary key; errors.Is(err, ErrNotFound) if missing.
func (q *Query) Get(ctx context.Context, id string) (Row, error) {
	if q.err != nil {
		return nil, q.err
	}
	path, err := q.rowPath(id)
	if err != nil {
		return nil, err
	}
	var query url.Values
	if len(q.columns) > 0 {
		query = url.Values{"select": {strings.Join(q.columns, ",")}}
	}
	return q.rowCall(ctx, http.MethodGet, path, query, nil)
}

// GetInto fetches one row and decodes it into dest.
func (q *Query) GetInto(ctx context.Context, id string, dest any) error {
	row, err := q.Get(ctx, id)
	if err != nil {
		return err
	}
	return DecodeRow(row, dest)
}

// Insert inserts one row (a map or a struct) and returns it.
func (q *Query) Insert(ctx context.Context, values any) (Row, error) {
	if q.err != nil {
		return nil, q.err
	}
	if values == nil {
		return nil, invalidArg("values must not be nil")
	}
	return q.rowCall(ctx, http.MethodPost, q.base(), nil, values)
}

// InsertMany inserts rows in one request; all rows must share the same keys.
func (q *Query) InsertMany(ctx context.Context, rows []Row) ([]Row, error) {
	if q.err != nil {
		return nil, q.err
	}
	if len(rows) == 0 {
		return []Row{}, nil
	}
	resp, err := q.client.send(ctx, request{method: http.MethodPost, path: q.base() + "/bulk", body: rows}, false)
	if err != nil {
		return nil, err
	}
	var body listBody
	if err := decodeJSON(resp, &body); err != nil {
		return nil, err
	}
	if body.Data == nil {
		body.Data = []Row{}
	}
	return body.Data, nil
}

// Upsert inserts values, or updates the row that conflicts on onConflict.
func (q *Query) Upsert(ctx context.Context, values Row, onConflict ...string) (Row, error) {
	if q.err != nil {
		return nil, q.err
	}
	if len(onConflict) == 0 {
		return nil, invalidArg("Upsert needs at least one conflict column")
	}
	for _, col := range onConflict {
		if !isIdentifier(col) {
			return nil, invalidArg("conflict column must be a SQL identifier, got %q", col)
		}
	}
	payload := make(Row, len(values)+2)
	for k, v := range values {
		payload[k] = v
	}
	payload["_upsert"] = true
	payload["_conflict_columns"] = onConflict
	return q.rowCall(ctx, http.MethodPost, q.base(), nil, payload)
}

// Update changes one row by primary key and returns it.
func (q *Query) Update(ctx context.Context, id string, values any) (Row, error) {
	if q.err != nil {
		return nil, q.err
	}
	path, err := q.rowPath(id)
	if err != nil {
		return nil, err
	}
	return q.rowCall(ctx, http.MethodPatch, path, nil, values)
}

// Delete removes one row by primary key.
func (q *Query) Delete(ctx context.Context, id string) error {
	if q.err != nil {
		return q.err
	}
	path, err := q.rowPath(id)
	if err != nil {
		return err
	}
	_, err = q.client.send(ctx, request{method: http.MethodDelete, path: path}, false)
	return err
}

// Range selects the rows from index from to index to, both included and
// counted from 0. The window holds at most MaxLimit rows.
func (q *Query) Range(from, to int) *Query {
	if from < 0 || to < from || to-from >= MaxLimit {
		return q.fail(invalidArg("range must be ordered, non-negative and at most 1000 rows"))
	}
	return q.Offset(from).Limit(to - from + 1)
}

// Count returns the total matching rows, independently of the selected window.
func (q *Query) Count(ctx context.Context) (int, error) {
	body, err := q.list(ctx, 1, 0)
	if err != nil {
		return 0, err
	}
	if body.Count == nil {
		return 0, &Error{Kind: ErrBadResponse, Message: "response did not include count"}
	}
	return *body.Count, nil
}
