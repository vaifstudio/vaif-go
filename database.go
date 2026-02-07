package vaif

import (
	"context"
	"encoding/json"
	"fmt"
)

// Database provides type-safe database operations with a fluent query builder.
// All operations require a context.Context for cancellation and timeout support.
type Database struct {
	client *Client
}

// From starts a query on the given table. Chain methods to build the query,
// then call Execute, Single, or MaybeSingle to run it.
//
//	result, err := client.DB.From("users").Select("*").Eq("active", true).Execute(ctx)
func (d *Database) From(table string) *QueryBuilder {
	return &QueryBuilder{
		client:     d.client,
		table:      table,
		queryType:  "select",
		selectCols: "*",
		filters:    make([]map[string]any, 0),
		orderSpecs: make([]map[string]any, 0),
	}
}

// Raw executes a raw SQL query with parameterized values (admin only).
// Use $1, $2, etc. as placeholders in the SQL string.
//
//	result, err := client.DB.Raw(ctx, "SELECT * FROM users WHERE age > $1", 18)
func (d *Database) Raw(ctx context.Context, sql string, params ...any) (*QueryResult[[]map[string]any], error) {
	body := map[string]any{
		"sql":    sql,
		"params": params,
	}

	raw, err := d.client.request(ctx, "POST", "/db/query", body, nil)
	if err != nil {
		return nil, err
	}

	resp := parseResponse[map[string]any](raw)
	if resp.Error != nil {
		return &QueryResult[[]map[string]any]{
			Error:  &QueryError{Message: resp.Error.Message, Code: resp.Error.Code, Status: resp.Status, Operation: "Raw", sentinel: resp.Error.sentinel},
			Status: resp.Status,
		}, nil
	}

	rows, _ := resp.Data["rows"].([]any)
	result := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if m, ok := row.(map[string]any); ok {
			result = append(result, m)
		}
	}

	return &QueryResult[[]map[string]any]{Data: result, Status: resp.Status}, nil
}

// RPC executes a stored procedure/function with the given parameters.
//
//	result, err := client.DB.RPC(ctx, "get_user_stats", map[string]any{"user_id": "123"})
func (d *Database) RPC(ctx context.Context, functionName string, params map[string]any) (*QueryResult[any], error) {
	if params == nil {
		params = map[string]any{}
	}

	raw, err := d.client.request(ctx, "POST", "/db/rpc/"+functionName, params, nil)
	if err != nil {
		return nil, err
	}

	resp := parseResponse[any](raw)
	if resp.Error != nil {
		return &QueryResult[any]{
			Error:  &QueryError{Message: resp.Error.Message, Code: resp.Error.Code, Status: resp.Status, Operation: "RPC:" + functionName, sentinel: resp.Error.sentinel},
			Status: resp.Status,
		}, nil
	}

	return &QueryResult[any]{Data: resp.Data, Status: resp.Status}, nil
}

// QueryBuilder constructs database queries with fluent method chaining.
// Create one via Database.From and customize with filter, order, and limit
// methods before executing.
type QueryBuilder struct {
	client     *Client
	table      string
	queryType  string // select, insert, update, delete
	selectCols string
	insertData any
	updateData map[string]any
	filters    []map[string]any
	orderSpecs []map[string]any
	limitCount *int
	offsetCount *int
	returning  string
	countOption string
	negateNext bool

	onConflictColumns []string
	onConflictAction  string
	onConflictUpdate  map[string]any
}

// Select sets the columns to select. Pass "*" for all columns, or a
// comma-separated list like "id, name, email".
func (q *QueryBuilder) Select(columns string) *QueryBuilder {
	q.queryType = "select"
	q.selectCols = columns
	return q
}

// Insert sets data to insert. Accepts a map[string]any for a single row or
// []map[string]any for batch insert.
func (q *QueryBuilder) Insert(data any) *QueryBuilder {
	q.queryType = "insert"
	q.insertData = data
	return q
}

// Update sets data to update. Only the specified columns are modified.
func (q *QueryBuilder) Update(data map[string]any) *QueryBuilder {
	q.queryType = "update"
	q.updateData = data
	return q
}

// Delete marks the query as a DELETE operation. Combine with filters to
// delete specific rows.
func (q *QueryBuilder) Delete() *QueryBuilder {
	q.queryType = "delete"
	return q
}

// Upsert performs an insert-or-update. If a row with matching conflict columns
// exists, it is updated with the provided data; otherwise a new row is inserted.
//
//	client.DB.From("users").Upsert(data, "email").Execute(ctx)
func (q *QueryBuilder) Upsert(data any, conflictColumns ...string) *QueryBuilder {
	q.queryType = "insert"
	q.insertData = data
	q.onConflictColumns = conflictColumns
	q.onConflictAction = "update"
	return q
}

// Returning sets which columns to return after insert/update/delete.
// Pass "*" for all columns or a comma-separated list.
//
//	result, err := client.DB.From("users").Insert(data).Returning("id, name").Execute(ctx)
func (q *QueryBuilder) Returning(columns string) *QueryBuilder {
	q.returning = columns
	return q
}

// OnConflict sets conflict columns for upsert behavior on inserts.
func (q *QueryBuilder) OnConflict(columns ...string) *QueryBuilder {
	q.onConflictColumns = columns
	return q
}

// DoNothing handles a conflict by doing nothing (skip the insert).
func (q *QueryBuilder) DoNothing() *QueryBuilder {
	q.onConflictAction = "nothing"
	return q
}

// DoUpdate handles a conflict by updating the given fields.
func (q *QueryBuilder) DoUpdate(data map[string]any) *QueryBuilder {
	q.onConflictAction = "update"
	q.onConflictUpdate = data
	return q
}

// --- Filter methods ---

// Eq filters where column equals value.
func (q *QueryBuilder) Eq(column string, value any) *QueryBuilder {
	op := "eq"
	if q.negateNext {
		op = "neq"
		q.negateNext = false
	}
	q.addFilter(column, op, value)
	return q
}

// Neq filters where column does not equal value.
func (q *QueryBuilder) Neq(column string, value any) *QueryBuilder {
	op := "neq"
	if q.negateNext {
		op = "eq"
		q.negateNext = false
	}
	q.addFilter(column, op, value)
	return q
}

// Gt filters where column is greater than value.
func (q *QueryBuilder) Gt(column string, value any) *QueryBuilder {
	op := "gt"
	if q.negateNext {
		op = "lte"
		q.negateNext = false
	}
	q.addFilter(column, op, value)
	return q
}

// Gte filters where column is greater than or equal to value.
func (q *QueryBuilder) Gte(column string, value any) *QueryBuilder {
	op := "gte"
	if q.negateNext {
		op = "lt"
		q.negateNext = false
	}
	q.addFilter(column, op, value)
	return q
}

// Lt filters where column is less than value.
func (q *QueryBuilder) Lt(column string, value any) *QueryBuilder {
	op := "lt"
	if q.negateNext {
		op = "gte"
		q.negateNext = false
	}
	q.addFilter(column, op, value)
	return q
}

// Lte filters where column is less than or equal to value.
func (q *QueryBuilder) Lte(column string, value any) *QueryBuilder {
	op := "lte"
	if q.negateNext {
		op = "gt"
		q.negateNext = false
	}
	q.addFilter(column, op, value)
	return q
}

// Like filters where column matches the pattern (case-sensitive).
// Use % as wildcard: Like("name", "%john%").
func (q *QueryBuilder) Like(column, pattern string) *QueryBuilder {
	op := "like"
	if q.negateNext {
		op = "nlike"
		q.negateNext = false
	}
	q.addFilter(column, op, pattern)
	return q
}

// ILike filters where column matches the pattern (case-insensitive).
func (q *QueryBuilder) ILike(column, pattern string) *QueryBuilder {
	op := "ilike"
	if q.negateNext {
		op = "nilike"
		q.negateNext = false
	}
	q.addFilter(column, op, pattern)
	return q
}

// Is filters where column is null or a boolean value.
// Use Is("deleted_at", nil) to filter for NULL.
func (q *QueryBuilder) Is(column string, value any) *QueryBuilder {
	op := "is"
	if q.negateNext {
		op = "isnot"
		q.negateNext = false
	}
	q.addFilter(column, op, value)
	return q
}

// In filters where column is in the given values.
//
//	q.In("status", []any{"active", "pending"})
func (q *QueryBuilder) In(column string, values []any) *QueryBuilder {
	op := "in"
	if q.negateNext {
		op = "nin"
		q.negateNext = false
	}
	q.addFilter(column, op, values)
	return q
}

// Contains filters where array/jsonb column contains the given values.
func (q *QueryBuilder) Contains(column string, value []any) *QueryBuilder {
	q.addFilter(column, "cs", value)
	q.negateNext = false
	return q
}

// ContainedBy filters where array column is contained by values.
func (q *QueryBuilder) ContainedBy(column string, value []any) *QueryBuilder {
	q.addFilter(column, "cd", value)
	q.negateNext = false
	return q
}

// Overlaps filters where array column overlaps with values.
func (q *QueryBuilder) Overlaps(column string, value []any) *QueryBuilder {
	q.addFilter(column, "ov", value)
	q.negateNext = false
	return q
}

// TextSearch performs a full-text search on the column.
func (q *QueryBuilder) TextSearch(column, query string) *QueryBuilder {
	q.addFilter(column, "fts", query)
	q.negateNext = false
	return q
}

// Not negates the next filter method call.
//
//	q.Not().Eq("status", "deleted") // WHERE status != 'deleted'
func (q *QueryBuilder) Not() *QueryBuilder {
	q.negateNext = true
	return q
}

// Or combines filters with OR logic. Pass a PostgREST-style filter string.
//
//	q.Or("status.eq.active,status.eq.pending")
func (q *QueryBuilder) Or(filters string) *QueryBuilder {
	q.filters = append(q.filters, map[string]any{"type": "or", "value": filters})
	return q
}

// --- Ordering, limit, offset ---

// Order sorts results by the given column. Pass ascending=true for ASC,
// false for DESC.
func (q *QueryBuilder) Order(column string, ascending bool) *QueryBuilder {
	q.orderSpecs = append(q.orderSpecs, map[string]any{
		"column":    column,
		"ascending": ascending,
	})
	return q
}

// OrderNullsFirst sorts with explicit nulls-first/last control.
func (q *QueryBuilder) OrderNullsFirst(column string, ascending bool, nullsFirst bool) *QueryBuilder {
	q.orderSpecs = append(q.orderSpecs, map[string]any{
		"column":     column,
		"ascending":  ascending,
		"nullsFirst": nullsFirst,
	})
	return q
}

// Limit sets the maximum number of rows to return.
func (q *QueryBuilder) Limit(count int) *QueryBuilder {
	q.limitCount = &count
	return q
}

// Offset sets the number of rows to skip.
func (q *QueryBuilder) Offset(count int) *QueryBuilder {
	q.offsetCount = &count
	return q
}

// Range sets the result range (inclusive on both ends).
// Range(0, 9) returns the first 10 rows.
func (q *QueryBuilder) Range(start, end int) *QueryBuilder {
	q.offsetCount = &start
	limit := end - start + 1
	q.limitCount = &limit
	return q
}

// Count includes a row count in the result. Valid options are "exact",
// "planned", or "estimated".
func (q *QueryBuilder) Count(countType string) *QueryBuilder {
	q.countOption = countType
	return q
}

// Execute runs the query and returns the result as a slice of maps.
func (q *QueryBuilder) Execute(ctx context.Context) (*QueryResult[[]map[string]any], error) {
	body := q.buildQuery()

	raw, err := q.client.request(ctx, q.httpMethod(), "/db/"+q.table, body, nil)
	if err != nil {
		return nil, err
	}

	resp := parseResponse[map[string]any](raw)
	if resp.Error != nil {
		return &QueryResult[[]map[string]any]{
			Error:  &QueryError{Message: resp.Error.Message, Code: resp.Error.Code, Status: resp.Status, Operation: q.queryType + ":" + q.table, sentinel: resp.Error.sentinel},
			Status: resp.Status,
		}, nil
	}

	dataSlice, _ := resp.Data["data"].([]any)
	rows := make([]map[string]any, 0, len(dataSlice))
	for _, item := range dataSlice {
		if m, ok := item.(map[string]any); ok {
			rows = append(rows, m)
		}
	}

	var count *int
	if c, ok := resp.Data["count"].(float64); ok {
		ci := int(c)
		count = &ci
	}

	return &QueryResult[[]map[string]any]{
		Data:   rows,
		Status: resp.Status,
		Count:  count,
	}, nil
}

// ExecuteInto runs the query and unmarshals the result into the provided
// destination. The dest should be a pointer to a slice of structs:
//
//	var users []User
//	err := client.DB.From("users").Select("*").ExecuteInto(ctx, &users)
func (q *QueryBuilder) ExecuteInto(ctx context.Context, dest any) error {
	body := q.buildQuery()

	raw, err := q.client.request(ctx, q.httpMethod(), "/db/"+q.table, body, nil)
	if err != nil {
		return err
	}

	resp := parseResponse[map[string]any](raw)
	if resp.Error != nil {
		return &QueryError{Message: resp.Error.Message, Code: resp.Error.Code, Status: resp.Status, Operation: q.queryType + ":" + q.table, sentinel: resp.Error.sentinel}
	}

	dataSlice, _ := resp.Data["data"].([]any)
	jsonBytes, err := json.Marshal(dataSlice)
	if err != nil {
		return fmt.Errorf("marshal query data: %w", err)
	}

	if err := json.Unmarshal(jsonBytes, dest); err != nil {
		return fmt.Errorf("unmarshal into destination: %w", err)
	}

	return nil
}

// Single returns a single result, returning an error if no rows or multiple
// rows are found.
func (q *QueryBuilder) Single(ctx context.Context) (*QueryResult[map[string]any], error) {
	one := 1
	q.limitCount = &one

	result, err := q.Execute(ctx)
	if err != nil {
		return nil, err
	}

	if result.Error != nil {
		return &QueryResult[map[string]any]{Error: result.Error, Status: result.Status}, nil
	}

	if len(result.Data) == 0 {
		return &QueryResult[map[string]any]{
			Error:  &QueryError{Message: "No rows returned", Code: "PGRST116", Status: 406, Operation: q.queryType + ":" + q.table, sentinel: ErrNotFound},
			Status: 406,
		}, nil
	}

	return &QueryResult[map[string]any]{Data: result.Data[0], Status: result.Status}, nil
}

// MaybeSingle returns a single result or nil data if no rows found.
// Unlike Single, it does not error on zero results.
func (q *QueryBuilder) MaybeSingle(ctx context.Context) (*QueryResult[map[string]any], error) {
	one := 1
	q.limitCount = &one

	result, err := q.Execute(ctx)
	if err != nil {
		return nil, err
	}

	if result.Error != nil {
		return &QueryResult[map[string]any]{Error: result.Error, Status: result.Status}, nil
	}

	if len(result.Data) == 0 {
		return &QueryResult[map[string]any]{Status: result.Status}, nil
	}

	return &QueryResult[map[string]any]{Data: result.Data[0], Status: result.Status}, nil
}

// ExecuteJSON is a convenience that returns the raw JSON bytes of the response.
func (q *QueryBuilder) ExecuteJSON(ctx context.Context) (json.RawMessage, error) {
	body := q.buildQuery()

	raw, err := q.client.request(ctx, q.httpMethod(), "/db/"+q.table, body, nil)
	if err != nil {
		return nil, err
	}

	if raw.StatusCode >= 200 && raw.StatusCode < 300 {
		return raw.Body, nil
	}

	return nil, fmt.Errorf("query failed with status %d: %s", raw.StatusCode, string(raw.Body))
}

func (q *QueryBuilder) addFilter(column, operator string, value any) {
	q.filters = append(q.filters, map[string]any{
		"type":     "filter",
		"column":   column,
		"operator": operator,
		"value":    value,
	})
}

func (q *QueryBuilder) httpMethod() string {
	switch q.queryType {
	case "insert":
		return "POST"
	case "update":
		return "PATCH"
	case "delete":
		return "DELETE"
	default:
		return "GET"
	}
}

func (q *QueryBuilder) buildQuery() map[string]any {
	query := map[string]any{"type": q.queryType}

	if q.queryType == "select" {
		query["select"] = q.selectCols
	}

	if q.queryType == "insert" && q.insertData != nil {
		query["data"] = q.insertData
		if len(q.onConflictColumns) > 0 {
			conflict := map[string]any{
				"columns": q.onConflictColumns,
				"action":  q.onConflictAction,
			}
			if q.onConflictUpdate != nil {
				conflict["update"] = q.onConflictUpdate
			}
			query["onConflict"] = conflict
		}
	}

	if q.queryType == "update" && q.updateData != nil {
		query["data"] = q.updateData
	}

	if len(q.filters) > 0 {
		query["filters"] = q.filters
	}

	if len(q.orderSpecs) > 0 {
		query["order"] = q.orderSpecs
	}

	if q.limitCount != nil {
		query["limit"] = *q.limitCount
	}

	if q.offsetCount != nil {
		query["offset"] = *q.offsetCount
	}

	if q.countOption != "" {
		query["count"] = q.countOption
	}

	if q.returning != "" {
		query["returning"] = q.returning
	}

	return query
}
